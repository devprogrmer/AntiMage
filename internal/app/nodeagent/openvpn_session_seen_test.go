package nodeagent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenVPNManagementClientKillTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	command := make(chan string, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)

		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		command <- line

		_, _ = io.WriteString(
			conn,
			"SUCCESS: client-kill command succeeded\n",
		)
	}()

	err = openVPNManagementClientKill(
		"tcp",
		listener.Addr().String(),
		"7",
	)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-command:
		if strings.TrimSpace(got) != "client-kill 7" {
			t.Fatalf(
				"unexpected management command: %q",
				got,
			)
		}

	case <-time.After(2 * time.Second):
		t.Fatal("management command not received")
	}
}

func TestOpenVPNManagementStatusReadsENDBeforeClosing(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		reader := bufio.NewReader(conn)
		command, readErr := reader.ReadString('\n')
		if readErr != nil {
			serverErr <- readErr
			return
		}
		if strings.TrimSpace(command) != "status 3" {
			serverErr <- fmt.Errorf("first management command = %q, want status 3", strings.TrimSpace(command))
			return
		}
		if _, writeErr := io.WriteString(conn, "TITLE\tOpenVPN 2.6\nHEADER\tCLIENT_LIST\tCommon Name\tReal Address\tBytes Received\tBytes Sent\tConnected Since (time_t)\tUsername\tClient ID\tPeer ID\nEND\n"); writeErr != nil {
			serverErr <- writeErr
			return
		}
		if extra, extraErr := reader.ReadString('\n'); extraErr == nil {
			serverErr <- fmt.Errorf("unexpected management command after status: %q", strings.TrimSpace(extra))
			return
		} else if !errors.Is(extraErr, io.EOF) {
			serverErr <- extraErr
			return
		}
		serverErr <- nil
	}()

	clients, err := openVPNManagementStatus("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 0 {
		t.Fatalf("empty native status returned clients: %+v", clients)
	}
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("management test server did not observe client close")
	}
}

func TestRefreshOpenVPNSessionsKillsOnDeviceLimit(t *testing.T) {
	root := t.TempDir()

	stateDir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	killed := make(chan string, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)

		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		killed <- line

		_, _ = io.WriteString(
			conn,
			"SUCCESS: client-kill command succeeded\n",
		)
	}()

	var received nativeSessionEvent

	controller := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()

				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Errorf("decode callback: %v", err)
				}

				http.Error(
					w,
					"device limit reached",
					http.StatusConflict,
				)
			},
		),
	)
	defer controller.Close()

	cfg := nativeSessionHelperConfig{
		Callback: nativeRuntimeSessionCallback{
			URL:    controller.URL,
			Token:  "test-token",
			NodeID: 9,
		},
		InboundTag: "openvpn-main",
		Users: map[string]int64{
			"alice": 42,
		},
		StateDir:          stateDir,
		ManagementNetwork: "tcp",
		ManagementAddress: listener.Addr().String(),
	}

	stateKey := nativeSessionStateKey(
		cfg.InboundTag,
		42,
		"10.66.0.10",
		"203.0.113.10",
		"54321",
	)

	if err := os.WriteFile(
		filepath.Join(stateDir, stateKey+".session"),
		[]byte("ov-test-session\n"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	status := "" +
		"HEADER\tCLIENT_LIST\tCommon Name\tReal Address\tVirtual Address\tVirtual IPv6 Address\tBytes Received\tBytes Sent\tConnected Since\tConnected Since (time_t)\tUsername\tClient ID\n" +
		"CLIENT_LIST\talice\t203.0.113.10:54321\t10.66.0.10\t\t100\t200\tnow\t123\talice\t7\n"

	if err := os.WriteFile(
		filepath.Join(root, "status.tsv"),
		[]byte(status),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	server := &Server{}

	if err := server.refreshOpenVPNSessions(
		"openvpn-main",
		root,
		cfg,
	); err != nil {
		t.Fatal(err)
	}

	if received.Event != "seen" {
		t.Fatalf(
			"expected seen event, got %q",
			received.Event,
		)
	}

	if received.UserID != 42 {
		t.Fatalf(
			"unexpected user id: %d",
			received.UserID,
		)
	}

	if received.SessionID != "ov-test-session" {
		t.Fatalf(
			"unexpected session id: %q",
			received.SessionID,
		)
	}

	if received.AssignedIP != "10.66.0.10" {
		t.Fatalf(
			"unexpected assigned ip: %q",
			received.AssignedIP,
		)
	}

	if received.ClientIP != "203.0.113.10" {
		t.Fatalf(
			"unexpected client ip: %q",
			received.ClientIP,
		)
	}

	select {
	case command := <-killed:
		if strings.TrimSpace(command) != "client-kill 7" {
			t.Fatalf(
				"unexpected kill command: %q",
				command,
			)
		}

	case <-time.After(2 * time.Second):
		t.Fatal("client-kill was not sent")
	}
}

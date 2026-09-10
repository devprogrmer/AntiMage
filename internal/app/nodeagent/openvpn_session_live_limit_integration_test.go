package nodeagent

import (
	"bufio"
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

func TestRefreshOpenVPNSessionsKillsAtLiveDataLimit(
	t *testing.T,
) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "sessions")

	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen(
		"tcp",
		"127.0.0.1:0",
	)
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

		line, err :=
			bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			return
		}

		killed <- strings.TrimSpace(line)

		_, _ = io.WriteString(
			conn,
			"SUCCESS: client-kill command succeeded\n",
		)
	}()

	callbackCalled := make(chan struct{}, 1)

	controller := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				callbackCalled <- struct{}{}
				w.WriteHeader(http.StatusOK)
			},
		),
	)
	defer controller.Close()

	limit := int64(1000)

	policies := buildNativeSessionUserPolicies(
		[]openVPNRuntimeUser{
			{
				UserID:      42,
				VPNUsername: "alice",
				Status:      "active",
				UsedTraffic: 900,
				DataLimit:   &limit,
			},
		},
	)

	cfg := nativeSessionHelperConfig{
		Callback: nativeRuntimeSessionCallback{
			URL:    controller.URL,
			NodeID: 9,
		},
		InboundTag: "openvpn-main",
		Users: map[string]int64{
			"alice": 42,
		},
		Policies:          policies,
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
		filepath.Join(
			stateDir,
			stateKey+".session",
		),
		[]byte("ov-live-limit-test\n"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	// 60 received + 40 sent = 100 live bytes.
	// 900 snapshot + 100 live = 1000 data limit.
	status := "" +
		"HEADER\tCLIENT_LIST\tCommon Name\tReal Address\tVirtual Address\tVirtual IPv6 Address\tBytes Received\tBytes Sent\tConnected Since\tConnected Since (time_t)\tUsername\tClient ID\n" +
		"CLIENT_LIST\talice\t203.0.113.10:54321\t10.66.0.10\t\t60\t40\tnow\t123\talice\t7\n"

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

	select {
	case command := <-killed:
		if command != "client-kill 7" {
			t.Fatalf(
				"unexpected command: %q",
				command,
			)
		}

	case <-time.After(2 * time.Second):
		t.Fatal(
			"live data limit did not disconnect client",
		)
	}

	select {
	case <-callbackCalled:
		t.Fatal(
			"quota-denied client unexpectedly sent seen callback",
		)
	default:
	}
}

package nodeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenVPNOfflineQuotaAggregatesSessionsAndRetainedUsage(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	tag := "offline-quota"
	root := filepath.Join(s.cfg.DataDir, "openvpn", openVPNRuntimeDirName(tag))
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan string, 4)
	durableBeforeKill := make(chan bool, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			line, err := bufio.NewReader(conn).ReadString('\n')
			if err == nil {
				raw, readErr := os.ReadFile(s.openVPNUsageStatePath())
				var state openVPNUsageDiskState
				valid := readErr == nil && json.Unmarshal(raw, &state) == nil && state.Baseline[offlineAccountingTotalKey(42, tag)] == 250
				durableBeforeKill <- valid
				commands <- line
				_, _ = io.WriteString(conn, "SUCCESS: client-kill command succeeded\n")
			}
			_ = conn.Close()
		}
	}()
	defer func() { listener.Close(); <-done }()
	if err := offlineDurableJSON(filepath.Join(root, "usage-helper.json"), openVPNUsageRuntimeConfig{InboundTag: tag, Users: map[string]int64{"alice": 42}}); err != nil {
		t.Fatal(err)
	}
	cfg := nativeSessionHelperConfig{Protocol: "ov", InboundTag: tag, Users: map[string]int64{"alice": 42}, Policies: map[string]nativeSessionUserPolicy{"alice": {Status: "active", DataLimit: 250}}, ManagementNetwork: "tcp", ManagementAddress: listener.Addr().String()}
	if err := offlineDurableJSON(filepath.Join(root, "session-helper.json"), cfg); err != nil {
		t.Fatal(err)
	}
	status := "HEADER\tCLIENT_LIST\tCommon Name\tUsername\tClient ID\tConnected Since (time_t)\tBytes Received\tBytes Sent\n" +
		"CLIENT_LIST\talice\talice\t7\t100\t25\t0\n" +
		"CLIENT_LIST\talice\talice\t8\t101\t25\t0\n"
	if err := os.WriteFile(filepath.Join(root, "status.tsv"), []byte(status), 0600); err != nil {
		t.Fatal(err)
	}
	// Prior ended sessions are part of local quota even with empty runtime maps.
	s.openVPNUsageLoaded = true
	s.openVPNUsageBaseline = map[string]uint64{offlineAccountingTotalKey(42, tag): 200}
	if err := s.persistOpenVPNUsageStateLocked(); err != nil {
		t.Fatal(err)
	}
	if err := s.quotaCheckOpenVPNOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-commands:
		case <-time.After(time.Second):
			t.Fatal("aggregate quota failed to disconnect both sessions")
		}
		if !<-durableBeforeKill {
			t.Fatal("disconnect happened before durable aggregate snapshot")
		}
	}
	if err := s.quotaCheckOpenVPNOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case command := <-commands:
		t.Fatalf("repeated kill storm: %s", command)
	default:
	}
	if s.openVPNUsagePending != nil {
		t.Fatal("quota check created delivery batch")
	}
}

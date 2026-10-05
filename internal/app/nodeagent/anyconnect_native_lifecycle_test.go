package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func anyConnectNativePID() (string, error) {
	if pid := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_PID")); pid != "" {
		return pid, nil
	}
	path := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_PID_FILE"))
	if path == "" {
		return "", fmt.Errorf("ocserv PID and PID file are missing")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read ocserv PID file: %w", err)
	}
	pid := strings.TrimSpace(string(raw))
	if pid == "" {
		return "", fmt.Errorf("ocserv PID file is empty")
	}
	return pid, nil
}

func TestAnyConnectNativePIDFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ocserv.pid")
	if err := os.WriteFile(path, []byte("4321\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTIMAGE_ANYCONNECT_NATIVE_PID", "")
	t.Setenv("ANTIMAGE_ANYCONNECT_NATIVE_PID_FILE", path)
	if pid, err := anyConnectNativePID(); err != nil || pid != "4321" {
		t.Fatalf("ocserv PID from file = %q, %v", pid, err)
	}
}

// TestAnyConnectNativeAccountingStage reads live sessions from the actual
// ocserv control socket and exercises the durable collector across reload.
func TestAnyConnectNativeAccountingStage(t *testing.T) {
	root := os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_ROOT")
	if root == "" {
		t.Skip("requires isolated native ocserv harness")
	}
	pid, err := anyConnectNativePID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strconv.Atoi(pid); err != nil {
		t.Fatalf("invalid ocserv PID: %v", err)
	}
	if os.Getenv("ANTIMAGE_ANYCONNECT_ACTION") == "admission" {
		configPath := filepath.Join(os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_STATE"), "anyconnect", "native", "session-helper.json")
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			return
		}
		if err := RunNativeSessionEventHelper([]string{configPath, "start"}); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := filepath.Join(os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_STATE"), "anyconnect", "native")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ocserv.pid"), []byte(pid), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := anyConnectUsageRuntimeConfig{InboundTag: "native", SocketPath: filepath.Join(root, "ocserv.sock"), Users: map[string]int64{"native-user": 7}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage-helper.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ANTIMAGE_ANYCONNECT_ACTION") == "quota" {
		policy := nativeSessionUserPolicy{Status: "active", DataLimit: 1}
		helper, err := json.Marshal(nativeSessionHelperConfig{
			InboundTag: "native", Protocol: "anyconnect", Users: cfg.Users,
			Policies: map[string]nativeSessionUserPolicy{"native-user": policy},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session-helper.json"), helper, 0600); err != nil {
			t.Fatal(err)
		}
		s := New(Config{DataDir: os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_STATE")})
		if err := s.quotaCheckAnyConnectOffline(context.Background()); err != nil {
			t.Fatal(err)
		}
		snapshots, err := s.anyConnectOfflineSnapshots(context.Background())
		if err != nil || len(snapshots) != 1 || len(snapshots[0].Sessions) != 0 {
			t.Fatalf("production offline quota did not disconnect over-limit native session: snapshots=%v err=%v", snapshots, err)
		}
		t.Log("production AnyConnect offline quota checkpointed and disconnected the over-limit native session")
		return
	}
	s := New(Config{DataDir: os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_STATE")})
	batch, err := s.collectAnyConnectUserUsage(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.anyConnectUsagePending == nil || len(s.anyConnectUsagePending.Samples) != 1 || s.anyConnectUsagePending.Samples[0].UserID != 7 || s.anyConnectUsagePending.Samples[0].Value == 0 {
		t.Fatalf("production collector did not persist positive usage from live ocserv: batch=%v pending=%#v", batch, s.anyConnectUsagePending)
	}
	first := batch.GetBatchId()
	s = New(Config{DataDir: os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_STATE")})
	replay, err := s.collectAnyConnectUserUsage(context.Background(), nil)
	if err != nil || replay.GetBatchId() != first {
		t.Fatalf("pending batch changed across reload: batch=%v err=%v", replay, err)
	}
	t.Logf("production ocserv collector durably persisted batch %s with %d bytes", first, s.anyConnectUsagePending.Samples[0].Value)
}

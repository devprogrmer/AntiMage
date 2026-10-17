//go:build linux

package nodeagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOfflineProcessIdentityRetainsZombieForStopAndExcludesItFromLiveSessions(t *testing.T) {
	child := exec.Command("sh", "-c", "exit 0")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Wait() }()
	pid := strconv.Itoa(child.Process.Pid)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile("/proc/" + pid + "/stat")
		if errors.Is(err, os.ErrNotExist) {
			t.Skip("kernel reaped the short-lived child before it became observable as a zombie")
		}
		if err != nil {
			t.Fatal(err)
		}
		end := strings.LastIndexByte(string(raw), ')')
		if end < 0 {
			t.Fatalf("invalid child process stat: %q", raw)
		}
		fields := strings.Fields(string(raw)[end+1:])
		if len(fields) > 0 && fields[0] == "Z" {
			identity, identityErr := offlineProcessIdentity(pid)
			if identityErr != nil || !strings.HasSuffix(identity, ":"+pid+":"+fields[19]) {
				t.Fatalf("zombie process identity must remain available to its stop hook: identity=%q err=%v", identity, identityErr)
			}
			if liveIdentity, liveErr := offlineLiveProcessIdentity(pid); !errors.Is(liveErr, os.ErrNotExist) || liveIdentity != "" {
				t.Fatalf("zombie process must not count as live: identity=%q err=%v", liveIdentity, liveErr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process %s did not become a zombie", pid)
}

func TestL2TPStopHookPersistsFinalRecordWhenPPPDIsZombie(t *testing.T) {
	testL2TPExitedStopHook(t, false)
}

func TestL2TPStopHookPersistsFinalRecordWhenPPPDIsReaped(t *testing.T) {
	testL2TPExitedStopHook(t, true)
}

func testL2TPExitedStopHook(t *testing.T, reap bool) {
	child := exec.Command("sh", "-c", "exit 0")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Wait() }()
	pid := strconv.Itoa(child.Process.Pid)

	var identity string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile("/proc/" + pid + "/stat")
		if errors.Is(err, os.ErrNotExist) {
			t.Skip("kernel reaped the short-lived child before its zombie state could be tested")
		}
		if err != nil {
			t.Fatal(err)
		}
		end := strings.LastIndexByte(string(raw), ')')
		if end < 0 {
			t.Fatalf("invalid child process stat: %q", raw)
		}
		fields := strings.Fields(string(raw)[end+1:])
		if len(fields) > 0 && fields[0] == "Z" {
			identity, err = offlineProcessIdentity(pid)
			if err != nil {
				t.Fatalf("resolve zombie pppd identity for stop callback: %v", err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if identity == "" {
		t.Fatalf("child process %s did not become a zombie", pid)
	}

	if reap {
		if err := child.Wait(); err != nil {
			t.Fatal(err)
		}
		if _, err := offlineProcessIdentity(pid); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reaped child must no longer have a process identity: %v", err)
		}
	}

	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "l2tp", "native-l2tp")
	stateDir := filepath.Join(root, "sessions")
	configPath := filepath.Join(root, "session-helper.json")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	callbackEvents := make(chan nativeSessionEvent, 1)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event nativeSessionEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			http.Error(w, "invalid session event", http.StatusBadRequest)
			return
		}
		callbackEvents <- event
		w.WriteHeader(http.StatusOK)
	}))
	defer callback.Close()
	config, err := json.Marshal(nativeSessionHelperConfig{
		Callback:   nativeRuntimeSessionCallback{URL: callback.URL, NodeID: 7},
		Protocol:   "l2tp",
		InboundTag: "native-l2tp",
		Users:      map[string]int64{"alice": 7},
		Policies:   map[string]nativeSessionUserPolicy{"alice": {Status: "active"}},
		StateDir:   stateDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	active := pppOfflineSession{ID: "session-zombie-stop", UserID: 7, InboundTag: "native-l2tp", Interface: "ppp0", PeerIP: "10.67.0.2", ClientIP: "10.251.0.2", Process: identity}
	if err := pppOfflineSessionEvent(root, "start", active); err != nil {
		t.Fatal(err)
	}
	previousInterfaceIdentity := pppOfflineReadIdentity
	pppOfflineReadIdentity = func(iface string) (string, error) {
		if iface != active.Interface {
			return "", os.ErrNotExist
		}
		return "identity-ppp0", nil
	}
	t.Cleanup(func() { pppOfflineReadIdentity = previousInterfaceIdentity })
	if denied, reason, err := pppOfflineAdmissionLimit(dataDir, active.UserID, "10.252.0.2", "", nativeSessionUserPolicy{DeviceLimit: 1}); err != nil || denied {
		t.Fatalf("zombie PPP record must not consume DeviceLimit: denied=%t reason=%q err=%v", denied, reason, err)
	}
	sum := sha256.Sum256([]byte("l2tp\x00native-l2tp\x00" + identity))
	statePath := filepath.Join(stateDir, hex.EncodeToString(sum[:16])+".session")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("panel-session-zombie\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEERNAME", "alice")
	t.Setenv("IFNAME", "ppp0")
	t.Setenv("PPPD_PID", pid)
	t.Setenv("BYTES_SENT", "70")
	t.Setenv("BYTES_RCVD", "80")
	if err := RunNativeSessionEventHelper([]string{configPath, "stop"}); err != nil {
		t.Fatalf("stop hook must persist and deliver final state after pppd becomes zombie: %v", err)
	}

	finalRaw, err := os.ReadFile(filepath.Join(root, "ppp-accounting", "final", active.ID+".json"))
	if err != nil {
		t.Fatalf("final PPP accounting record missing: %v", err)
	}
	var final pppOfflineSession
	if err := json.Unmarshal(finalRaw, &final); err != nil {
		t.Fatal(err)
	}
	if !final.Final || final.Total != 150 || final.Process != identity {
		t.Fatalf("unexpected final PPP record after zombie stop: %+v", final)
	}
	select {
	case event := <-callbackEvents:
		if event.Event != "stop" || event.SessionID != "panel-session-zombie" || event.UserID != 7 || event.AssignedIP != "10.67.0.2" || event.ClientIP != "10.251.0.2" {
			t.Fatalf("zombie stop callback did not preserve the session identity: %+v", event)
		}
	default:
		t.Fatal("zombie stop callback was not delivered")
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Panel stop callback state should be removed after delivery: err=%v", err)
	}
}

func TestPPPReapedStopIdentitySafety(t *testing.T) {
	bootRaw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	boot := strings.TrimSpace(string(bootRaw))
	for _, tc := range []struct {
		name    string
		records []pppOfflineSession
		want    string
		reject  bool
	}{
		{name: "unique", records: []pppOfflineSession{{Process: boot + ":123:10", Interface: "ppp0"}}, want: boot + ":123:10"},
		{name: "duplicate retained identity", records: []pppOfflineSession{{Process: boot + ":123:10", Interface: "ppp0"}, {Process: boot + ":123:10", Interface: "ppp0"}}, want: boot + ":123:10"},
		{name: "foreign boot", records: []pppOfflineSession{{Process: "other:123:10", Interface: "ppp0"}}, reject: true},
		{name: "wrong interface", records: []pppOfflineSession{{Process: boot + ":123:10", Interface: "ppp1"}}, reject: true},
		{name: "reused PID same interface", records: []pppOfflineSession{{Process: boot + ":123:10", Interface: "ppp0"}, {Process: boot + ":123:20", Interface: "ppp0"}}, reject: true},
		{name: "reused PID other interface", records: []pppOfflineSession{{Process: boot + ":123:10", Interface: "ppp0"}, {Process: boot + ":123:20", Interface: "ppp1"}}, reject: true},
		{name: "invalid start time", records: []pppOfflineSession{{Process: boot + ":123:bad", Interface: "ppp0"}}, reject: true},
		{name: "missing", reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "ppp-accounting", "active")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for i, record := range tc.records {
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(i)+".json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := pppOfflineFindReapedProcess(root, "ppp0", "123", boot)
			if tc.reject {
				if err == nil {
					t.Fatalf("unsafe identity accepted: %q", got)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("identity=%q err=%v", got, err)
			}
		})
	}
}

func TestPPPReapedStopIdentityRejectsPIDReuseDuringLookup(t *testing.T) {
	bootRaw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	boot := strings.TrimSpace(string(bootRaw))
	root := t.TempDir()
	record := pppOfflineSession{ID: "reaped", Process: boot + ":123:10", Interface: "ppp0"}
	if err := pppOfflineSessionEvent(root, "start", record); err != nil {
		t.Fatal(err)
	}
	previous := pppOfflineReadProcess
	t.Cleanup(func() { pppOfflineReadProcess = previous })
	calls := 0
	pppOfflineReadProcess = func(string) (string, error) {
		calls++
		if calls == 1 {
			return "", os.ErrNotExist
		}
		return boot + ":123:20", nil
	}
	if identity, err := pppOfflineStopProcessIdentity(root, "ppp0", "123"); err == nil || identity != "" {
		t.Fatalf("PID reuse accepted: %q %v", identity, err)
	}
	calls = 0
	pppOfflineReadProcess = func(string) (string, error) { calls++; return "", os.ErrPermission }
	if _, err := pppOfflineStopProcessIdentity(root, "ppp0", "123"); !errors.Is(err, os.ErrPermission) || calls != 1 {
		t.Fatalf("non-missing process error must not use fallback: calls=%d err=%v", calls, err)
	}
}

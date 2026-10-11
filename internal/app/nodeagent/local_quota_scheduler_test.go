package nodeagent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestLocalQuotaInterval(t *testing.T) {
	mockWireGuardGenerationIdentity(t)
	for input, expected := range map[string]time.Duration{"": 100 * time.Millisecond, "50ms": 50 * time.Millisecond, "2s": 2 * time.Second, "0": 0, "off": 0, "disabled": 0, "false": 0, " OFF ": 0} {
		actual, err := localQuotaInterval(input)
		if err != nil || actual != expected {
			t.Fatalf("interval %q = %v, %v", input, actual, err)
		}
	}
	for _, input := range []string{"49ms", "2001ms", "garbage", "-1s"} {
		if _, err := localQuotaInterval(input); err == nil {
			t.Fatalf("invalid interval accepted: %s", input)
		}
	}
}

func offlineWireGuardFixture(t *testing.T, dir string, limit *int64) *Server {
	t.Helper()
	s := New(Config{DataDir: dir})
	if err := s.syncWireGuardUsageConfigs([]wireGuardRuntimeInbound{{
		Tag: "wg-offline", ListenPort: 51820,
		Settings: map[string]any{"accounting_enabled": true, "interface_name": "wg-offline0"},
		Peers:    []wireGuardRuntimePeer{{UserID: 10, PublicKey: "peer-offline", Status: "active", DataLimit: limit}},
	}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func mockOfflineWireGuardCounters(t *testing.T, counter *uint64) *int {
	t.Helper()
	previousAll, previousInterface := wireGuardDumpAll, wireGuardDumpInterface
	reads := new(int)
	peer := func() string {
		return fmt.Sprintf("peer-offline\t(none)\t198.51.100.2:1000\t10.69.0.2/32\t0\t%d\t0\t0\n", *counter)
	}
	wireGuardDumpAll = func(context.Context) ([]byte, error) {
		*reads++
		return []byte("wg-offline0\tprivate\tpublic\t51820\toff\n" + "wg-offline0\t" + peer()), nil
	}
	wireGuardDumpInterface = func(context.Context, string) ([]byte, error) {
		return []byte("private\tpublic\t51820\toff\n" + peer()), nil
	}
	t.Cleanup(func() {
		wireGuardDumpAll, wireGuardDumpInterface = previousAll, previousInterface
	})
	return reads
}

func TestWireGuardOfflineCheckpointsContinueWithPendingAndNodeRestart(t *testing.T) {
	mockWireGuardGenerationIdentity(t)
	ctx := context.Background()
	const mb = uint64(1024 * 1024)
	dir := t.TempDir()
	s := offlineWireGuardFixture(t, dir, nil)
	counter := 500 * mb
	mockOfflineWireGuardCounters(t, &counter)
	if err := s.wireGuardOfflineTick(ctx, false, true); err != nil {
		t.Fatal(err)
	}
	first, err := s.CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
	if err != nil || first.GetStats()[0].GetValue() != 500*mb {
		t.Fatalf("first pending = %v, %v", first, err)
	}
	counter = 600 * mb
	if err := s.wireGuardOfflineTick(ctx, false, true); err != nil {
		t.Fatal(err)
	}
	counter = 100 * mb
	if err := s.wireGuardOfflineTick(ctx, false, true); err != nil {
		t.Fatal(err)
	}
	s = New(Config{DataDir: dir})
	if err := s.wireGuardOfflineTick(ctx, false, true); err != nil {
		t.Fatal(err)
	}
	retry, err := s.CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
	if err != nil || retry.GetBatchId() != first.GetBatchId() || retry.GetStats()[0].GetValue() != 500*mb {
		t.Fatalf("offline restart changed pending = %v, %v", retry, err)
	}
	ack, err := s.AckUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: retry.GetBatchId()})
	if err != nil || !ack.GetAcknowledged() {
		t.Fatalf("ACK = %v, %v", ack, err)
	}
	remainder, err := s.CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
	if err != nil || remainder.GetStats()[0].GetValue() != 200*mb {
		t.Fatalf("durable offline remainder = %v, %v", remainder, err)
	}
}

func TestWireGuardOfflineQuota50MBCutsOffUsingPersistedPolicy(t *testing.T) {
	mockWireGuardGenerationIdentity(t)
	ctx := context.Background()
	const mb = uint64(1024 * 1024)
	limit := int64(50 * mb)
	dir := t.TempDir()
	_ = offlineWireGuardFixture(t, dir, &limit)
	// A fresh agent has no runtime/process map and no panel connection.
	s := New(Config{DataDir: dir})
	var counter uint64
	reads := mockOfflineWireGuardCounters(t, &counter)
	previousLookPath, previousRun := wireGuardRuntimeLookPath, wireGuardRuntimeRun
	removed := false
	wireGuardRuntimeLookPath = func(name string) (string, error) { return name, nil }
	wireGuardRuntimeRun = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "set wg-offline0 peer peer-offline remove" {
			removed = true
		}
		return nil, nil
	}
	t.Cleanup(func() { wireGuardRuntimeLookPath, wireGuardRuntimeRun = previousLookPath, previousRun })
	for i := 0; i < 20 && !removed; i++ {
		counter += 7 * mb
		if err := s.wireGuardOfflineTick(ctx, true, i == 0); err != nil {
			t.Fatal(err)
		}
	}
	if !removed || counter != 56*mb || *reads != 8 {
		t.Fatalf("quota fixture cutoff=%dMB removed=%v aggregate reads=%d", counter/mb, removed, *reads)
	}
	carry := s.wireGuardUsageCarry[wireGuardUsageBaselineKey("wg-offline", "wg-offline0", "peer-offline")]
	if carry.Value != counter {
		t.Fatalf("quota removal lost final traffic: %+v", carry)
	}
	t.Log("synthetic 7MB/tick workload: 50MB quota, 56MB cutoff, 6MB overshoot; no live timing claim")
}

func TestWireGuardNormalQuotaTicksDoNotWriteAccountingState(t *testing.T) {
	mockWireGuardGenerationIdentity(t)
	s := offlineWireGuardFixture(t, t.TempDir(), nil)
	counter := uint64(100)
	mockOfflineWireGuardCounters(t, &counter)
	if err := s.wireGuardOfflineTick(context.Background(), false, true); err != nil {
		t.Fatal(err)
	}
	original := wireGuardAccountingWrite
	writes := 0
	wireGuardAccountingWrite = func(path string, raw []byte) error { writes++; return original(path, raw) }
	t.Cleanup(func() { wireGuardAccountingWrite = original })
	for i := 0; i < 10; i++ {
		counter += 100
		if err := s.wireGuardOfflineTick(context.Background(), true, false); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 0 {
		t.Fatalf("normal quota ticks wrote disk %d times", writes)
	}
	if err := s.wireGuardOfflineTick(context.Background(), false, true); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("periodic checkpoint writes=%d", writes)
	}
}

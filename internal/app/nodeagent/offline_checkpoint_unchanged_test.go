package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// The durable usage-state file is the only evidence the collector can see for a
// protocol's accounting activity. Rewriting it when nothing changed produces
// steady, visible churn for protocols that have no users at all, which is what
// the AntiMage-s3 baseline reported for openvpn/l2tp/pptp.
func offlineStateModTime(tb testing.TB, path string) int64 {
	tb.Helper()
	info, err := os.Stat(path)
	if err != nil {
		tb.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime().UnixNano()
}

func TestL2TPCheckpointUnchangedDoesNotRewriteState(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	t.Cleanup(s.resetAccountingStateDedup)
	path := s.l2TPUsageStatePath()
	if err := s.checkpointPPPOffline(offlineCheckpointOnlyContext()); err != nil {
		t.Fatalf("first checkpoint: %v", err)
	}
	first := offlineStateModTime(t, path)
	if err := s.checkpointPPPOffline(offlineCheckpointOnlyContext()); err != nil {
		t.Fatalf("second checkpoint: %v", err)
	}
	if got := offlineStateModTime(t, path); got != first {
		t.Fatalf("unchanged L2TP checkpoint rewrote durable state: %d -> %d", first, got)
	}
}

func TestOpenVPNCheckpointUnchangedDoesNotRewriteState(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	t.Cleanup(s.resetAccountingStateDedup)
	path := s.openVPNUsageStatePath()
	if err := s.checkpointOpenVPNOffline(offlineCheckpointOnlyContext()); err != nil {
		t.Fatalf("first checkpoint: %v", err)
	}
	first := offlineStateModTime(t, path)
	if err := s.checkpointOpenVPNOffline(offlineCheckpointOnlyContext()); err != nil {
		t.Fatalf("second checkpoint: %v", err)
	}
	if got := offlineStateModTime(t, path); got != first {
		t.Fatalf("unchanged OpenVPN checkpoint rewrote durable state: %d -> %d", first, got)
	}
}

func offlineCheckpointOnlyContext() context.Context {
	return context.WithValue(context.Background(), offlineCheckpointContextKey{}, true)
}

// BenchmarkNativeUnchangedCheckpointTick measures one checkpoint tick over the
// native protocols that had zero users in the production baseline.
func BenchmarkNativeUnchangedCheckpointTick(b *testing.B) {
	s := New(Config{DataDir: b.TempDir()})
	ctx := offlineCheckpointOnlyContext()
	if err := s.checkpointPPPOffline(ctx); err != nil {
		b.Fatal(err)
	}
	if err := s.checkpointOpenVPNOffline(ctx); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.checkpointPPPOffline(ctx); err != nil {
			b.Fatal(err)
		}
		if err := s.checkpointOpenVPNOffline(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// A recorded byte-level change must still be persisted: the optimization may
// only skip identical writes, and must never drop a real delta.
func TestL2TPCheckpointPersistsRecordedDelta(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	t.Cleanup(s.resetAccountingStateDedup)
	path := s.l2TPUsageStatePath()
	if err := s.checkpointPPPOffline(offlineCheckpointOnlyContext()); err != nil {
		t.Fatalf("first checkpoint: %v", err)
	}
	first := offlineStateModTime(t, path)
	s.l2TPUsageBaseline = map[string]uint64{"seed": 1}
	s.l2TPUsageLoaded = true
	if err := s.checkpointPPPOffline(offlineCheckpointOnlyContext()); err != nil {
		t.Fatalf("second checkpoint: %v", err)
	}
	if got := offlineStateModTime(t, path); got == first {
		t.Fatal("changed L2TP baseline was not persisted")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineStatePathLayout(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	if got := filepath.Base(s.l2TPUsageStatePath()); got != "usage-state.json" {
		t.Fatalf("unexpected l2tp state file %q", got)
	}
}

var _ = nodev1.UserUsageBatch{}

// A second Server sharing the same data directory must never be told its write
// was already persisted by another instance's hint map. The dedup cache is
// per-instance state, so distinct dirs (and distinct instances) cannot mask each
// other's writes.
func TestAccountingStateDedupIsPerServerInstance(t *testing.T) {
	dir := t.TempDir()
	first := New(Config{DataDir: dir})
	second := New(Config{DataDir: dir})
	t.Cleanup(first.resetAccountingStateDedup)
	t.Cleanup(second.resetAccountingStateDedup)

	path := first.l2TPUsageStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := first.writeAccountingStateDeduped(path, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := first.writeAccountingStateDeduped(path, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}

	// A different instance writing different bytes must reach disk.
	if err := second.writeAccountingStateDeduped(path, []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"v":2}` {
		t.Fatalf("second instance write was masked by another instance: %q", raw)
	}
}

// If another process replaces the file behind the writer, the hint must not
// claim the bytes are already durable.
func TestAccountingStateDedupDetectsExternalReplacement(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	t.Cleanup(s.resetAccountingStateDedup)

	path := s.l2TPUsageStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":1}`)
	if err := s.writeAccountingStateDeduped(path, raw); err != nil {
		t.Fatal(err)
	}
	// Replace the file with different bytes of the same length.
	if err := os.WriteFile(path, []byte(`{"v":9}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.writeAccountingStateDeduped(path, raw); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("external replacement not detected: %q", got)
	}
}

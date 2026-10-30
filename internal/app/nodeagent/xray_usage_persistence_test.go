package nodeagent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestXrayChangedCheckpointsUseDurableJournalWithoutFullSnapshotRewrite(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	s.xrayAccountingGeneration = "generation-1"
	s.xrayAccountingCounters = map[string]accountingCounter{"42.alice:uplink": {Native: 100, Total: 100}}
	oldSnapshotWrite := xrayAccountingWrite
	oldJournalAppend := xrayAccountingJournalAppend
	oldAtomicSync, oldWALSync, oldDirectorySync := accountingAtomicFileSync, accountingWALFileSync, accountingDirectorySync
	snapshotWrites, journalAppends := 0, 0
	atomicSyncs, walSyncs, parentDirectorySyncs := 0, 0, 0
	xrayAccountingWrite = func(path string, raw []byte) error {
		snapshotWrites++
		return oldSnapshotWrite(path, raw)
	}
	xrayAccountingJournalAppend = func(path string, expectedOffset int64, raw []byte) (int64, error) {
		journalAppends++
		return oldJournalAppend(path, expectedOffset, raw)
	}
	accountingAtomicFileSync = func(file *os.File) error {
		atomicSyncs++
		return oldAtomicSync(file)
	}
	accountingWALFileSync = func(file *os.File) error {
		walSyncs++
		return oldWALSync(file)
	}
	parent := filepath.Dir(s.xrayUsageJournalPath())
	accountingDirectorySync = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(parent) {
			parentDirectorySyncs++
		}
		return oldDirectorySync(path)
	}
	t.Cleanup(func() {
		xrayAccountingWrite = oldSnapshotWrite
		xrayAccountingJournalAppend = oldJournalAppend
		accountingAtomicFileSync, accountingWALFileSync, accountingDirectorySync = oldAtomicSync, oldWALSync, oldDirectorySync
	})
	for _, value := range []int64{110, 120, 130} {
		if err := s.checkpointXrayStatsLocked([]xrayStat{{Name: "user>>>42.alice>>>traffic>>>uplink", Value: value}}); err != nil {
			t.Fatal(err)
		}
	}
	if snapshotWrites != 0 || journalAppends != 3 {
		t.Fatalf("writes: full snapshots=%d journal appends=%d, want 0 and 3", snapshotWrites, journalAppends)
	}
	wantParentDirectorySyncs := 2
	if runtime.GOOS == "windows" {
		wantParentDirectorySyncs = 1 // Directory fsync is not supported by this Windows path.
	}
	if atomicSyncs != 0 || walSyncs != 3 || parentDirectorySyncs != wantParentDirectorySyncs {
		t.Fatalf("durability syncs: atomic-file=%d wal-file=%d parent-directory=%d, want 0, 3, %d", atomicSyncs, walSyncs, parentDirectorySyncs, wantParentDirectorySyncs)
	}
	restarted := New(Config{DataDir: s.cfg.DataDir})
	if err := restarted.ensureXrayUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	if got := restarted.xrayAccountingCounters["42.alice:uplink"].Total; got != 130 {
		t.Fatalf("recovered total=%d, want 130", got)
	}
}

func TestXrayJournalCompactionSnapshotRestartsWithoutDoubleCounting(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	s.xrayAccountingGeneration = "generation-1"
	s.xrayAccountingCounters = map[string]accountingCounter{"42.alice:uplink": {Native: 100, Total: 100}}
	for value := int64(101); value <= 228; value++ {
		if err := s.checkpointXrayStatsLocked([]xrayStat{{Name: "user>>>42.alice>>>traffic>>>uplink", Value: value}}); err != nil {
			t.Fatal(err)
		}
	}
	if s.xrayAccountingSnapshotSequence != xrayAccountingJournalCompactRecords || s.xrayAccountingJournalOffset != 0 {
		t.Fatalf("compaction state: snapshot sequence=%d journal offset=%d", s.xrayAccountingSnapshotSequence, s.xrayAccountingJournalOffset)
	}
	info, err := os.Stat(s.xrayUsageJournalPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("compacted journal size=%d, want 0", info.Size())
	}
	restarted := New(Config{DataDir: s.cfg.DataDir})
	if err := restarted.ensureXrayUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	if got := restarted.xrayAccountingCounters["42.alice:uplink"].Total; got != 228 {
		t.Fatalf("recovered total=%d, want 228", got)
	}
}

func TestXrayFailedJournalAppendDoesNotCommitCountersInMemory(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	s.xrayAccountingGeneration = "generation-1"
	s.xrayAccountingCounters = map[string]accountingCounter{"42.alice:uplink": {Native: 100, Total: 100}}
	oldAppend := xrayAccountingJournalAppend
	xrayAccountingJournalAppend = func(string, int64, []byte) (int64, error) {
		return 0, os.ErrPermission
	}
	t.Cleanup(func() { xrayAccountingJournalAppend = oldAppend })
	if err := s.checkpointXrayStatsLocked([]xrayStat{{Name: "user>>>42.alice>>>traffic>>>uplink", Value: 120}}); err == nil {
		t.Fatal("expected journal append failure")
	}
	if got := s.xrayAccountingCounters["42.alice:uplink"].Total; got != 100 {
		t.Fatalf("failed checkpoint committed total=%d, want 100", got)
	}
	if s.xrayAccountingJournalSequence != 0 {
		t.Fatalf("failed checkpoint advanced sequence to %d", s.xrayAccountingJournalSequence)
	}
}

func TestXrayUnchangedCheckpointSkipsAtomicRewrite(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	s.xrayAccountingGeneration = "generation-1"
	s.xrayAccountingCounters = map[string]accountingCounter{"42.alice:uplink": {Native: 100, Total: 100}}
	oldWrite := xrayAccountingWrite
	oldJournalAppend := xrayAccountingJournalAppend
	writes, journalAppends := 0, 0
	xrayAccountingWrite = func(path string, raw []byte) error {
		writes++
		return oldWrite(path, raw)
	}
	xrayAccountingJournalAppend = func(path string, expectedOffset int64, raw []byte) (int64, error) {
		journalAppends++
		return oldJournalAppend(path, expectedOffset, raw)
	}
	t.Cleanup(func() {
		xrayAccountingWrite = oldWrite
		xrayAccountingJournalAppend = oldJournalAppend
	})
	stats := []xrayStat{{Name: "user>>>42.alice>>>traffic>>>uplink", Value: 100}}
	for range 8 {
		if err := s.checkpointXrayStatsLocked(stats); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 0 {
		t.Fatalf("unchanged checkpoints issued %d atomic writes, want 0", writes)
	}
	stats[0].Value = 120
	if err := s.checkpointXrayStatsLocked(stats); err != nil {
		t.Fatal(err)
	}
	if writes != 0 || journalAppends != 1 {
		t.Fatalf("changed checkpoint writes: snapshots=%d journal=%d, want 0 and 1", writes, journalAppends)
	}
	restarted := New(Config{DataDir: s.cfg.DataDir})
	if err := restarted.ensureXrayUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	if restarted.xrayAccountingCounters["42.alice:uplink"].Total != 120 {
		t.Fatal("changed counter was not durable across restart")
	}
}

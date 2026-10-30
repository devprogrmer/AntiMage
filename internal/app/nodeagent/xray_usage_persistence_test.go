package nodeagent

import "testing"

func TestXrayUnchangedCheckpointSkipsAtomicRewrite(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	s.xrayAccountingGeneration = "generation-1"
	s.xrayAccountingCounters = map[string]accountingCounter{"42.alice:uplink": {Native: 100, Total: 100}}
	oldWrite := xrayAccountingWrite
	writes := 0
	xrayAccountingWrite = func(path string, raw []byte) error {
		writes++
		return oldWrite(path, raw)
	}
	t.Cleanup(func() { xrayAccountingWrite = oldWrite })
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
	if writes != 1 {
		t.Fatalf("changed checkpoint writes=%d, want 1", writes)
	}
	restarted := New(Config{DataDir: s.cfg.DataDir})
	if err := restarted.ensureXrayUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	if restarted.xrayAccountingCounters["42.alice:uplink"].Total != 120 {
		t.Fatal("changed counter was not durable across restart")
	}
}

package nodeagent

import "testing"

func TestXrayOutboundUsageGenerationResetsBaseline(t *testing.T) {
	stats := []xrayStat{
		{Name: "outbound>>>direct>>>traffic>>>uplink", Value: 11 * 1024 * 1024 * 1024},
		{Name: "outbound>>>direct>>>traffic>>>downlink", Value: 2 * 1024 * 1024 * 1024},
	}
	baseline := map[string]uint64{
		"direct:uplink":   10 * 1024 * 1024 * 1024,
		"direct:downlink": 1 * 1024 * 1024 * 1024,
	}
	got, next := xrayOutboundUsageDeltas(stats, baseline, nil, "runtime-a", "runtime-b")
	if got["direct"].Up != 11*1024*1024*1024 || got["direct"].Down != 2*1024*1024*1024 {
		t.Fatalf("new generation was treated as a delta: %#v", got["direct"])
	}
	if next["direct:uplink"] != 11*1024*1024*1024 || next["direct:downlink"] != 2*1024*1024*1024 {
		t.Fatalf("next baseline = %#v", next)
	}
}

func TestXrayOutboundUsageSameGenerationUsesDelta(t *testing.T) {
	stats := []xrayStat{{Name: "outbound>>>direct>>>traffic>>>uplink", Value: 125}}
	got, _ := xrayOutboundUsageDeltas(stats, map[string]uint64{"direct:uplink": 100}, nil, "runtime-a", "runtime-a")
	if got["direct"].Up != 25 {
		t.Fatalf("same-generation delta = %d, want 25", got["direct"].Up)
	}
}

func TestXrayOutboundUsagePendingGenerationSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{DataDir: dir})
	s.xrayOutboundUsageGeneration = "runtime-b"
	s.xrayOutboundUsageBaseline = map[string]uint64{"direct:uplink": 11}
	s.xrayOutboundUsagePending = &xrayOutboundUsagePendingBatch{
		BatchID:      "outbound-lost-ack",
		Generation:   "runtime-b",
		NextBaseline: map[string]uint64{"direct:uplink": 12},
	}
	if err := s.persistXrayOutboundUsageStateLocked(); err != nil {
		t.Fatal(err)
	}
	reloaded := New(Config{DataDir: dir})
	if err := reloaded.ensureXrayOutboundUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	if reloaded.xrayOutboundUsagePending == nil || reloaded.xrayOutboundUsagePending.Generation != "runtime-b" {
		t.Fatalf("pending generation lost after reload: %#v", reloaded.xrayOutboundUsagePending)
	}
}

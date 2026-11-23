package nodeagent

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func reflectionCPUFixture() *Server {
	s := &Server{}
	for batch := 0; batch < 128; batch++ {
		rows := make([]wireGuardUsageSample, 64)
		for user := range rows {
			rows[user] = wireGuardUsageSample{UserID: int64(user + 1), InboundTag: "wg", Value: 3}
		}
		s.wireGuardUsageAwaitingReflection = append(s.wireGuardUsageAwaitingReflection,
			wireGuardUsageAwaitingReflectionBatch{BatchID: fmt.Sprintf("wg-%d", batch), Samples: rows})
	}
	return s
}

func TestWireGuardReflectionReadDoesNotAllocate(t *testing.T) {
	s := reflectionCPUFixture()
	for _, marker := range []string{"", "missing"} {
		allocs := testing.AllocsPerRun(20, func() {
			got, err := s.wireGuardAwaitingReflectionUsageLocked("wg", 1, marker)
			if err != nil || got != 384 {
				t.Fatalf("usage=%d err=%v", got, err)
			}
		})
		if allocs != 0 {
			t.Fatalf("unchanged quota read allocated %.0f objects per user", allocs)
		}
	}
}

func TestWireGuardReflectionReconcilePersistsOnlyOwner(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	s.wireGuardUsageLoaded = true
	s.wireGuardUsageAwaitingReflection = []wireGuardUsageAwaitingReflectionBatch{
		{BatchID: "a", Samples: []wireGuardUsageSample{{UserID: 1, InboundTag: "wg", Value: 5}, {UserID: 2, InboundTag: "wg", Value: 7}}},
		{BatchID: "b", Samples: []wireGuardUsageSample{{UserID: 1, InboundTag: "other", Value: 11}, {UserID: 1, InboundTag: "wg", Value: 13}}},
		{BatchID: "c", Samples: []wireGuardUsageSample{{UserID: 1, InboundTag: "wg", Value: 17}}},
	}
	got, err := s.wireGuardAwaitingReflectionUsageLocked("wg", 1, "b")
	if err != nil || got != 17 {
		t.Fatalf("usage=%d err=%v", got, err)
	}
	want := []wireGuardUsageAwaitingReflectionBatch{
		{BatchID: "a", Samples: []wireGuardUsageSample{{UserID: 2, InboundTag: "wg", Value: 7}}},
		{BatchID: "c", Samples: []wireGuardUsageSample{{UserID: 1, InboundTag: "wg", Value: 17}}},
	}
	reloaded := New(Config{DataDir: s.cfg.DataDir})
	if err := reloaded.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.wireGuardUsageAwaitingReflection, want) {
		t.Fatalf("persisted reconciliation=%+v", reloaded.wireGuardUsageAwaitingReflection)
	}
}

func BenchmarkWireGuardReflectionQuotaRead(b *testing.B) {
	s := reflectionCPUFixture()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for user := int64(1); user <= 64; user++ {
			if _, err := s.wireGuardAwaitingReflectionUsageLocked("wg", user, ""); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func TestWireGuardReflectionPendingMarkerAndFailureSafety(t *testing.T) {
	t.Run("pending marker covers preceding batches", func(t *testing.T) {
		s := New(Config{DataDir: t.TempDir()})
		s.wireGuardUsageLoaded = true
		s.wireGuardUsagePending = &wireGuardUsagePendingBatch{BatchID: "pending"}
		s.wireGuardUsageAwaitingReflection = []wireGuardUsageAwaitingReflectionBatch{
			{BatchID: "old", Samples: []wireGuardUsageSample{{UserID: 1, InboundTag: "wg", Value: 9}, {UserID: 2, InboundTag: "wg", Value: 5}}},
		}
		got, err := s.wireGuardAwaitingReflectionUsageLocked("wg", 1, "pending")
		if err != nil || got != 0 || len(s.wireGuardUsageAwaitingReflection[0].Samples) != 1 || s.wireGuardUsageAwaitingReflection[0].Samples[0].UserID != 2 {
			t.Fatalf("usage=%d err=%v state=%+v", got, err, s.wireGuardUsageAwaitingReflection)
		}
	})
	for _, scenario := range []string{"write failure", "overflow"} {
		t.Run(scenario, func(t *testing.T) {
			s := New(Config{DataDir: t.TempDir()})
			s.wireGuardUsageLoaded = true
			s.wireGuardUsageAwaitingReflection = []wireGuardUsageAwaitingReflectionBatch{
				{BatchID: "a", Samples: []wireGuardUsageSample{{UserID: 1, InboundTag: "wg", Value: 3}, {UserID: 2, InboundTag: "wg", Value: 4}}},
				{BatchID: "b", Samples: []wireGuardUsageSample{{UserID: 1, InboundTag: "wg", Value: ^uint64(0)}, {UserID: 1, InboundTag: "wg", Value: 1}}},
			}
			marker := ""
			if scenario == "write failure" {
				marker = "b"
				if err := os.WriteFile(filepath.Join(s.cfg.DataDir, "wireguard"), []byte("block directory creation"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Independent rows detect accidental in-place edits during rollback.
			before := make([]wireGuardUsageAwaitingReflectionBatch, len(s.wireGuardUsageAwaitingReflection))
			for i, batch := range s.wireGuardUsageAwaitingReflection {
				before[i] = wireGuardUsageAwaitingReflectionBatch{BatchID: batch.BatchID, Samples: append([]wireGuardUsageSample(nil), batch.Samples...)}
			}
			if _, err := s.wireGuardAwaitingReflectionUsageLocked("wg", 1, marker); err == nil {
				t.Fatal("expected error")
			}
			if !reflect.DeepEqual(before, s.wireGuardUsageAwaitingReflection) {
				t.Fatal("failed reconciliation changed original accounting rows")
			}
		})
	}
}

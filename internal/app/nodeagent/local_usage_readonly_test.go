package nodeagent

import (
	"sync"
	"testing"
)

func usageReadOnlyFixture() *Server {
	rows := make([]localUsageDeliveryRow, 650)
	for i := range rows {
		rows[i] = localUsageDeliveryRow{Protocol: "xray", ChildID: "xray-old", UserID: int64(i + 1), InboundTag: "in", Raw: 100}
	}
	return &Server{localUsageLoaded: true, localUsageDeliveries: []localUsageDelivery{{BatchID: "combined-old", Rows: rows}}}
}

func TestLocalUsageReadOnlyDoesNotAllocateOrMutate(t *testing.T) {
	s := usageReadOnlyFixture()
	before := &s.localUsageDeliveries[0].Rows[0]
	allocs := testing.AllocsPerRun(100, func() {
		got, err := s.localAwaitingReflectionUsage("xray", 1, "in", "", "")
		if err != nil || got != 100 {
			t.Fatalf("usage=%d err=%v", got, err)
		}
	})
	if allocs != 0 || before != &s.localUsageDeliveries[0].Rows[0] || before.Raw != 100 {
		t.Fatalf("read mutated the ledger or allocated: allocations=%v", allocs)
	}
}

func TestLocalUsageConcurrentReadsAndLockedWrites(t *testing.T) {
	s := usageReadOnlyFixture()
	var workers sync.WaitGroup
	for n := 0; n < 4; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 100; i++ {
				got, err := s.localAwaitingReflectionUsage("xray", 1, "in", "", "")
				if err != nil || (got != 100 && got != 200) {
					t.Errorf("usage=%d err=%v", got, err)
					return
				}
			}
		}()
	}
	for i := 0; i < 100; i++ {
		s.localUsageMu.Lock()
		s.localUsageDeliveries[0].Rows[0].Raw = uint64(100 + 100*(i%2))
		s.localUsageMu.Unlock()
	}
	workers.Wait()
}

func BenchmarkLocalUsageReadOnly650Users(b *testing.B) {
	s := usageReadOnlyFixture()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := s.localAwaitingReflectionUsage("xray", int64(i%291+1), "in", "", "")
		if err != nil || got != 100 {
			b.Fatalf("usage=%d err=%v", got, err)
		}
	}
}

package nodeagent

import (
	"encoding/json"
	"fmt"
	"testing"
)

// This isolates the CPU and heap cost of replacing each complete JSON snapshot
// at the sizes observed in production. Durable writes are separately counted
// by the no-op checkpoint tests.
func BenchmarkAccountingSnapshotMarshal(b *testing.B) {
	b.Run("xray-1778-series", func(b *testing.B) {
		counters := make(map[string]accountingCounter, 1778)
		for i := range 1778 {
			counters[fmt.Sprintf("%d.user@example.test:up", i+1)] = accountingCounter{Native: 100, Total: 1000}
		}
		state := xrayUsageDiskState{Counters: counters, Baseline: map[string]uint64{}, Generation: "generation-1"}
		benchmarkJSONMarshal(b, state)
	})
	b.Run("wireguard-516-carry-782-generations", func(b *testing.B) {
		carry := make(map[string]wireGuardUsageCarry, 516)
		generations := make(map[string]wireGuardOfflineGeneration, 782)
		for i := range 516 {
			carry[fmt.Sprintf("in\x00wg\x00peer-%d", i)] = wireGuardUsageCarry{Value: 10_000, NextBaseline: 20_000, UserID: int64(i + 1), InboundTag: "in"}
		}
		for i := range 782 {
			generations[fmt.Sprintf("in\x00wg\x00peer-%d", i)] = wireGuardOfflineGeneration{Identity: "boot:3", UserID: int64(i + 1), Total: 20_000, Reference: 10_000}
		}
		state := wireGuardUsageDiskState{Generations: generations, Carry: carry, Baseline: map[string]uint64{}}
		benchmarkJSONMarshal(b, state)
	})
	b.Run("amneziawg-1000-counters", func(b *testing.B) {
		counters := make(map[string]amneziaWGOfflineCounter, 1000)
		for i := range 1000 {
			counters[fmt.Sprintf("in\x00awg\x00peer-%d", i)] = amneziaWGOfflineCounter{UserID: int64(i + 1), InboundTag: "in", Generation: "g", Received: 10_000, Sent: 5_000, Total: 15_000}
		}
		benchmarkJSONMarshal(b, amneziaWGOfflineState{Counters: counters})
	})
}

func benchmarkJSONMarshal(b *testing.B, state any) {
	b.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := json.Marshal(state); err != nil {
			b.Fatal(err)
		}
	}
}

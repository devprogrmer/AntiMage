package nodeagent

import (
	"strconv"
	"testing"
)

var xrayPendingReflectionBenchTotal uint64

func xrayPendingReflectionFixture() ([]xrayLocalUserPolicy, []xrayUsageSample) {
	policies := make([]xrayLocalUserPolicy, 1000)
	samples := make([]xrayUsageSample, 1000)
	for i := range policies {
		policies[i] = xrayLocalUserPolicy{UserID: int64(i + 1), InboundTag: "inbound-" + strconv.Itoa(i%4)}
		samples[i] = xrayUsageSample{UserID: policies[i].UserID, InboundTag: policies[i].InboundTag, Value: 1}
	}
	return policies, samples
}

func BenchmarkXrayPendingReflectionRepeatedScan(b *testing.B) {
	policies, samples := xrayPendingReflectionFixture()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var total uint64
		for _, policy := range policies {
			raw := uint64(2)
			for _, sample := range samples {
				if sample.UserID == policy.UserID && sample.InboundTag == policy.InboundTag {
					if sample.Value > raw {
						b.Fatal("invalid fixture")
					}
					raw -= sample.Value
				}
			}
			total += raw
		}
		xrayPendingReflectionBenchTotal = total
	}
}

func BenchmarkXrayPendingReflectionIndexed(b *testing.B) {
	policies, samples := xrayPendingReflectionFixture()
	scratch := xrayPendingReflectionTotalsInto(samples, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scratch = xrayPendingReflectionTotalsInto(samples, scratch)
		var total uint64
		for _, policy := range policies {
			raw := uint64(2)
			entry := scratch[xrayPendingReflectionKey{UserID: policy.UserID, InboundTag: policy.InboundTag}]
			if entry.Overflow || entry.Value > raw {
				b.Fatal("invalid fixture")
			}
			total += raw - entry.Value
		}
		xrayPendingReflectionBenchTotal = total
	}
}

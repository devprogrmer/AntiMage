package nodeagent

import (
	"fmt"
	"strings"
	"testing"
)

func benchmarkQuotaFixture(users, seriesPerUser int) ([]xrayLocalUserPolicy, map[string]accountingCounter, map[string]uint64) {
	policies := make([]xrayLocalUserPolicy, users)
	counters := make(map[string]accountingCounter, users*seriesPerUser)
	baselines := make(map[string]uint64, users*seriesPerUser)
	for user := range users {
		email := fmt.Sprintf("%d.user@example.test", user+1)
		policies[user] = xrayLocalUserPolicy{Email: email}
		for series := range seriesPerUser {
			key := fmt.Sprintf("%s:%d", email, series)
			baselines[key] = 100
			counters[key] = accountingCounter{Total: 200}
		}
	}
	return policies, counters, baselines
}

func BenchmarkXrayQuotaAggregationLegacy(b *testing.B) {
	policies, counters, baselines := benchmarkQuotaFixture(1000, 4)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var total uint64
		for _, policy := range policies {
			for key, counter := range counters {
				if !strings.HasPrefix(key, policy.Email+":") {
					continue
				}
				baseline := baselines[key]
				if counter.Total >= baseline {
					total += counter.Total - baseline
				}
			}
		}
		if total == 0 {
			b.Fatal("empty benchmark result")
		}
	}
}

func BenchmarkXrayQuotaAggregationIndexed(b *testing.B) {
	policies, counters, baselines := benchmarkQuotaFixture(1000, 4)
	index := &xrayQuotaPolicyIndex{EmailIndex: make(map[string]struct{}, len(policies)), UsageTotal: make(map[string]uint64, len(policies))}
	for _, policy := range policies {
		index.EmailIndex[policy.Email] = struct{}{}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := xrayQuotaUsageTotalsInto(counters, baselines, index); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLocalUsageReflection(b *testing.B) {
	const users, deliveries, rowsPerDelivery = 250, 64, 250
	s := &Server{}
	s.localUsageLoaded = true
	s.localUsageDeliveries = make([]localUsageDelivery, deliveries)
	for batch := range deliveries {
		rows := make([]localUsageDeliveryRow, rowsPerDelivery)
		for row := range rows {
			uid := int64((batch*rowsPerDelivery+row)%users + 1)
			rows[row] = localUsageDeliveryRow{Protocol: "xray", ChildID: fmt.Sprintf("child-%d", batch), UserID: uid, InboundTag: "in", Raw: 64}
		}
		s.localUsageDeliveries[batch] = localUsageDelivery{BatchID: fmt.Sprintf("batch-%d", batch), Rows: rows}
	}
	s.localUsageIndexLocked()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for user := range users {
			if _, err := s.localAwaitingReflectionUsage("xray", int64(user+1), "in", "pending", ""); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkXrayStatKeyResolution(b *testing.B) {
	name := "user>>>42.rb1_aW4.username>>>traffic>>>downlink"
	resolved := resolveXrayStatNativeKey(name)
	cache := map[string]xrayStatNativeKey{name: resolved}
	b.Run("parse-every-sample", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if !resolveXrayStatNativeKey(name).OK {
				b.Fatal("invalid test stat")
			}
		}
	})
	b.Run("cached-name", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if !cache[name].OK {
				b.Fatal("invalid cached test stat")
			}
		}
	})
}

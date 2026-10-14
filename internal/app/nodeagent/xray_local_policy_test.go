package nodeagent

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"testing"
)

var benchmarkXrayQuotaTotal uint64

func TestXrayPoliciesNeedLocalQuotaCheck(t *testing.T) {
	for name, testCase := range map[string]struct {
		Policies []xrayLocalUserPolicy
		Want     bool
	}{
		"no users":             {},
		"unlimited active":     {Policies: []xrayLocalUserPolicy{{nativeSessionUserPolicy: nativeSessionUserPolicy{Status: "active"}}}},
		"normalized unlimited": {Policies: []xrayLocalUserPolicy{{nativeSessionUserPolicy: nativeSessionUserPolicy{Status: " ACTIVE "}}}},
		"active data limit":    {Policies: []xrayLocalUserPolicy{{nativeSessionUserPolicy: nativeSessionUserPolicy{Status: "active", DataLimit: 10}}}, Want: true},
		"active expiry":        {Policies: []xrayLocalUserPolicy{{nativeSessionUserPolicy: nativeSessionUserPolicy{Status: "active", Expire: 10}}}, Want: true},
		"held user":            {Policies: []xrayLocalUserPolicy{{nativeSessionUserPolicy: nativeSessionUserPolicy{Status: "on_hold", DataLimit: 10, Expire: 10}}}},
		"disabled user":        {Policies: []xrayLocalUserPolicy{{nativeSessionUserPolicy: nativeSessionUserPolicy{Status: "disabled"}}}, Want: true},
		"missing status":       {Policies: []xrayLocalUserPolicy{{}}, Want: true},
		"mixed finite and unlimited": {Policies: []xrayLocalUserPolicy{
			{nativeSessionUserPolicy: nativeSessionUserPolicy{Status: "active"}},
			{nativeSessionUserPolicy: nativeSessionUserPolicy{Status: "active", DataLimit: 10}},
		}, Want: true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := xrayPoliciesNeedLocalQuotaCheck(testCase.Policies); got != testCase.Want {
				t.Fatalf("needs quota check=%v, want %v", got, testCase.Want)
			}
		})
	}
}

func TestXrayQuotaUsageTotalsGroupsCountersInSinglePass(t *testing.T) {
	policies := []xrayLocalUserPolicy{{Email: "42.alice"}, {Email: "43.bob"}}
	counters := map[string]accountingCounter{
		"42.alice:uplink":   {Total: 130},
		"42.alice:downlink": {Total: 240},
		"43.bob:uplink":     {Total: 70},
		"unrelated:uplink":  {Total: 1 << 40},
	}
	baselines := map[string]uint64{
		"42.alice:uplink":   100,
		"42.alice:downlink": 200,
		"43.bob:uplink":     20,
	}

	got, err := xrayQuotaUsageTotals(counters, baselines, policies)
	if err != nil {
		t.Fatal(err)
	}
	if got["42.alice"] != 70 || got["43.bob"] != 50 {
		t.Fatalf("quota totals=%v, want alice=70 bob=50", got)
	}
}

func TestXrayQuotaUsageTotalsRejectsCounterRegressionAndOverflow(t *testing.T) {
	policy := []xrayLocalUserPolicy{{Email: "alice"}}
	if _, err := xrayQuotaUsageTotals(
		map[string]accountingCounter{"alice:uplink": {Total: 4}},
		map[string]uint64{"alice:uplink": 5}, policy,
	); err == nil {
		t.Fatal("counter below baseline was accepted")
	}
	if _, err := xrayQuotaUsageTotals(
		map[string]accountingCounter{"alice:uplink": {Total: math.MaxUint64}, "alice:downlink": {Total: 1}},
		map[string]uint64{}, policy,
	); err == nil {
		t.Fatal("overflowing quota total was accepted")
	}
}

func TestXrayQuotaUsageTotalsIntoReusesIndexedScratchAndChecksOverflow(t *testing.T) {
	index := &xrayQuotaPolicyIndex{EmailIndex: map[string]int{"alice": 0, "bob": 1}, UsageTotal: []uint64{99, 99}}
	counters := map[string]accountingCounter{"alice:uplink": {Total: 120}, "bob:downlink": {Total: 35}}
	if err := xrayQuotaUsageTotalsInto(counters, map[string]uint64{"alice:uplink": 100}, index); err != nil {
		t.Fatal(err)
	}
	if index.UsageTotal[0] != 20 || index.UsageTotal[1] != 35 {
		t.Fatalf("indexed totals=%v, want [20 35]", index.UsageTotal)
	}
	if err := xrayQuotaUsageTotalsInto(
		map[string]accountingCounter{"alice:uplink": {Total: math.MaxUint64}, "alice:downlink": {Total: 1}},
		map[string]uint64{}, index,
	); err == nil {
		t.Fatal("overflowing indexed total was accepted")
	}
}

func TestXrayQuotaPolicyIndexRefreshesWhenRevisionChanges(t *testing.T) {
	s := New(Config{})
	s.runtimePolicy = &runtimePolicySnapshot{Revision: 7, Checksum: "revision-seven", NativeJSON: `{"xray_policies":[{"email":"alice","status":"active"}]}`}
	first, err := s.xrayQuotaPolicyIndexFor(s.runtimePolicy)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.xrayQuotaPolicyIndexFor(s.runtimePolicy)
	if err != nil || again != first {
		t.Fatalf("same policy revision did not reuse index: same=%v err=%v", again == first, err)
	}
	s.mu.Lock()
	s.runtimePolicy = &runtimePolicySnapshot{Revision: 8, Checksum: "revision-eight", NativeJSON: `{"xray_policies":[{"email":"bob","status":"active"}]}`}
	nextSnapshot := s.runtimePolicy
	s.mu.Unlock()
	next, err := s.xrayQuotaPolicyIndexFor(nextSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if next == first || len(next.Policies) != 1 || next.Policies[0].Email != "bob" {
		t.Fatalf("policy index was not refreshed: %+v", next)
	}
}

func TestXrayQuotaPolicyIndexExcludesUnlimitedAndOnHoldUsers(t *testing.T) {
	s := New(Config{})
	snapshot := &runtimePolicySnapshot{Revision: 9, Checksum: "revision-nine", NativeJSON: `{"xray_policies":[{"user_id":1,"email":"alice","status":"active"},{"user_id":2,"email":"bob","status":"active","data_limit":100},{"user_id":3,"email":"carol","status":"on_hold","data_limit":100}]}`}
	s.runtimePolicy = snapshot
	index, err := s.xrayQuotaPolicyIndexFor(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Policies) != 3 || len(index.EnforcedPositions) != 1 || index.EnforcedPositions[0] != 1 {
		t.Fatalf("enforcement index includes non-enforced policies: positions=%v policies=%d", index.EnforcedPositions, len(index.Policies))
	}
	if len(index.EmailIndex) != 1 || len(index.ReflectionQueries) != 1 || index.ReflectionQueries[0].UserID != 2 {
		t.Fatalf("polling index includes unlimited/on_hold users: emails=%v queries=%+v", index.EmailIndex, index.ReflectionQueries)
	}
}

func BenchmarkXrayQuotaAggregation(b *testing.B) {
	const userCount = 512
	policies := make([]xrayLocalUserPolicy, 0, userCount)
	counters := make(map[string]accountingCounter, userCount*2)
	baselines := make(map[string]uint64, userCount*2)
	for index := 0; index < userCount; index++ {
		email := fmt.Sprintf("user-%03d", index)
		policies = append(policies, xrayLocalUserPolicy{Email: email})
		for _, direction := range []string{"uplink", "downlink"} {
			key := email + ":" + direction
			counters[key] = accountingCounter{Total: 1000}
			baselines[key] = 500
		}
	}
	b.Run("per-policy-scan", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			var total uint64
			for _, policy := range policies {
				prefix := policy.Email + ":"
				for key, counter := range counters {
					if strings.HasPrefix(key, prefix) {
						total += counter.Total - baselines[key]
					}
				}
			}
			benchmarkXrayQuotaTotal = total
		}
	})
	index := &xrayQuotaPolicyIndex{Policies: policies, EmailIndex: make(map[string]int, userCount), UsageTotal: make([]uint64, userCount)}
	for _, policy := range policies {
		index.EmailIndex[policy.Email] = len(index.EmailIndex)
	}
	b.Run("cached-single-pass-index", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if err := xrayQuotaUsageTotalsInto(counters, baselines, index); err != nil {
				b.Fatal(err)
			}
			var total uint64
			for _, policy := range policies {
				total += index.UsageTotal[index.EmailIndex[policy.Email]]
			}
			benchmarkXrayQuotaTotal = total
		}
	})
}

func TestXrayQuotaWorkerSkipsStatsForUnlimitedActiveUsers(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	s.runtimePolicy = &runtimePolicySnapshot{NativeJSON: `{"xray_policies":[{"user_id":1,"email":"alice","inbound_tag":"vless","status":"active"}]}`}
	s.lastRuntime = &exec.Cmd{Process: &os.Process{Pid: 1}}
	if err := s.quotaCheckXrayOffline(context.Background()); err != nil {
		t.Fatalf("unlimited policy should not query stats: %v", err)
	}
	if s.xrayStatsClient != nil {
		t.Fatal("unlimited policy initialized the Xray stats client")
	}
}

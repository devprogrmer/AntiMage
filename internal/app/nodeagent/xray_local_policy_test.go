package nodeagent

import (
	"context"
	"math"
	"os"
	"os/exec"
	"testing"
)

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

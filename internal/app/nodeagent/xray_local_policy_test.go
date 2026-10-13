package nodeagent

import (
	"context"
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

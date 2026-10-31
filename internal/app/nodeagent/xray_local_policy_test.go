package nodeagent

import (
	"encoding/json"
	"testing"
)

func TestRemoveXrayConfigClientsPreservesOtherUsersAndConfig(t *testing.T) {
	input := `{"log":{"loglevel":"warning"},"inbounds":[{"tag":"vless","protocol":"vless","settings":{"clients":[{"email":"quota-user","id":"one"},{"email":"other-user","id":"two"}],"decryption":"none"}}]}`
	got, removed, err := removeXrayConfigClients(input, map[string]struct{}{"quota-user": {}})
	if err != nil || !removed {
		t.Fatalf("remove quota user: removed=%v err=%v", removed, err)
	}
	var config struct {
		Log struct {
			LogLevel string `json:"loglevel"`
		} `json:"log"`
		Inbounds []struct {
			Protocol string `json:"protocol"`
			Settings struct {
				Clients []struct {
					Email string `json:"email"`
				} `json:"clients"`
				Decryption string `json:"decryption"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(got), &config); err != nil {
		t.Fatal(err)
	}
	if config.Log.LogLevel != "warning" || len(config.Inbounds) != 1 || config.Inbounds[0].Protocol != "vless" || config.Inbounds[0].Settings.Decryption != "none" {
		t.Fatalf("unrelated Xray config was not preserved: %s", got)
	}
	if len(config.Inbounds[0].Settings.Clients) != 1 || config.Inbounds[0].Settings.Clients[0].Email != "other-user" {
		t.Fatalf("unexpected surviving clients: %#v", config.Inbounds[0].Settings.Clients)
	}
	unchanged, removed, err := removeXrayConfigClients(got, map[string]struct{}{"absent": {}})
	if err != nil || removed || unchanged != got {
		t.Fatalf("absent user removal changed config: removed=%v err=%v", removed, err)
	}
}

func TestXrayQuotaUsageTotalsIntoAggregatesEachEmailOncePerSeries(t *testing.T) {
	index := &xrayQuotaPolicyIndex{
		EmailIndex: map[string]struct{}{"alice@example.test": {}, "bob@example.test": {}},
		UsageTotal: map[string]uint64{"alice@example.test": 999, "bob@example.test": 999},
	}
	counters := map[string]accountingCounter{
		"alice@example.test:uplink":   {Total: 90},
		"alice@example.test:downlink": {Total: 150},
		"bob@example.test:uplink":     {Total: 55},
	}
	baselines := map[string]uint64{
		"alice@example.test:uplink":   10,
		"alice@example.test:downlink": 50,
		"bob@example.test:uplink":     5,
	}
	if err := xrayQuotaUsageTotalsInto(counters, baselines, index); err != nil {
		t.Fatal(err)
	}
	if got := index.UsageTotal["alice@example.test"]; got != 180 {
		t.Fatalf("alice usage = %d, want 180", got)
	}
	if got := index.UsageTotal["bob@example.test"]; got != 50 {
		t.Fatalf("bob usage = %d, want 50", got)
	}
}

func TestXrayQuotaUsageTotalsIntoRejectsInvalidBaselineAndOverflow(t *testing.T) {
	index := &xrayQuotaPolicyIndex{EmailIndex: map[string]struct{}{"alice": {}}, UsageTotal: map[string]uint64{"alice": 7}}
	if err := xrayQuotaUsageTotalsInto(map[string]accountingCounter{"alice:up": {Total: 1}}, map[string]uint64{"alice:up": 2}, index); err == nil {
		t.Fatal("expected baseline validation error")
	}
	if err := xrayQuotaUsageTotalsInto(map[string]accountingCounter{"alice:up": {Total: ^uint64(0)}, "alice:down": {Total: 1}}, nil, index); err == nil {
		t.Fatal("expected usage overflow error")
	}
}

func TestXrayQuotaPolicyIndexCacheUsesRevisionAndChecksum(t *testing.T) {
	s := &Server{}
	first := &runtimePolicySnapshot{Revision: 1, Checksum: "one", NativeJSON: `{"xray_policies":[{"email":"alice"}]}`}
	s.runtimePolicy = first
	index, err := s.xrayQuotaPolicyIndexFor(first)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := s.xrayQuotaPolicyIndexFor(first)
	if err != nil || cached != index {
		t.Fatalf("same revision/checksum did not reuse cache: %p %p err=%v", index, cached, err)
	}
	next := &runtimePolicySnapshot{Revision: 2, Checksum: "two", NativeJSON: `{"xray_policies":[{"email":"bob"}]}`}
	s.runtimePolicy = next
	updated, err := s.xrayQuotaPolicyIndexFor(next)
	if err != nil {
		t.Fatal(err)
	}
	if updated == index || len(updated.Policies) != 1 || updated.Policies[0].Email != "bob" {
		t.Fatalf("revision change did not invalidate cache: %#v", updated)
	}
}

package nodecontroller

import "testing"

func TestAccountingAlertsUseOnlyLoadedSampledHealth(t *testing.T) {
	runtime := RuntimeResult{NodeID: 7, Name: "node-7", Accounting: &AccountingHealth{
		ReflectionOverdue: true,
		Protocols: []AccountingProtocolStatus{
			{Protocol: "xray", StateLoaded: true, CheckpointOverdue: true, PendingACKOverdue: true},
			{Protocol: "wireguard", StateLoaded: true, Failure: "private diagnostic"},
			{Protocol: "openvpn", CheckpointOverdue: true},
		},
	}}
	alerts := accountingAlerts(runtime)
	want := []struct{ protocol, kind string }{
		{"all", "reflection_overdue"},
		{"xray", "checkpoint_overdue"},
		{"xray", "pending_ack_overdue"},
		{"wireguard", "collector_failure"},
	}
	if len(alerts) != len(want) {
		t.Fatalf("got %d alerts, want %d: %#v", len(alerts), len(want), alerts)
	}
	for i, expected := range want {
		if alerts[i].NodeID != 7 || alerts[i].NodeName != "node-7" || alerts[i].Protocol != expected.protocol || alerts[i].Kind != expected.kind {
			t.Fatalf("alert %d = %#v, want %#v", i, alerts[i], expected)
		}
	}
	if len(accountingAlerts(RuntimeResult{})) != 0 {
		t.Fatal("missing accounting sample must not generate alerts")
	}
}

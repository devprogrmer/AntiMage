package networkintelligence

import (
	"testing"
	"time"
)

func TestInfrastructureDiagnosticsRequireRecentCounterDeltas(t *testing.T) {
	now := time.Now()
	rates := map[string]float64{"softnet.dropped": 1.5, "ip.ReasmFails": 0, "ip.FragFails": 3, "interface.eth0.rx_bytes": 1000}
	if got := DiagnoseInfrastructure(now.Add(-2*time.Minute), now, 5, rates); len(got) != 0 {
		t.Fatal(got)
	}
	if got := DiagnoseInfrastructure(now, now, 0, rates); len(got) != 0 {
		t.Fatal(got)
	}
	got := DiagnoseInfrastructure(now, now, 5, rates)
	if len(got) != 2 || got[0].Code != "node_softnet_drops" || got[1].Code != "node_fragmentation_failures" {
		t.Fatal(got)
	}
}

func TestDiagnosticsUnavailableAndObservedEvidence(t *testing.T) {
	now := time.Now()
	if got := Diagnose(DiagnosticInput{}, now); len(got) != 1 || got[0].Code != "stale_telemetry" {
		t.Fatal(got)
	}
	if got := Diagnose(DiagnosticInput{ObservedAt: now}, now); len(got) != 0 {
		t.Fatal(got)
	}
	rtt, jitter := time.Second, 40*time.Millisecond
	got := Diagnose(DiagnosticInput{ObservedAt: now, ACKRTT: &rtt, Jitter: &jitter, Retransmissions: 1, CollectorDrops: 1}, now)
	if len(got) != 4 {
		t.Fatal(got)
	}
	for _, finding := range got {
		if finding.Confidence != "observed" || finding.Evidence == "" || finding.Recommendation == "" {
			t.Fatal(finding)
		}
	}
}

package networkintelligence

import (
	"testing"
	"time"
)

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

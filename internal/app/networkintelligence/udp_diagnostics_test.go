package networkintelligence

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestUDPDiagnosticsRequireFreshMeasuredTransactions(t *testing.T) {
	now := time.Now().UTC()
	mean, jitter := 250.0, 45.0
	q := UDPFlowQuality{Method: "udp_request_response_observation", Protocol: "stun_binding", ObservedAt: now, MeasurementAt: &now, UploadExchange: LatencyMilliseconds{Samples: 2, Mean: &mean, Jitter: &jitter}}
	findings := DiagnoseUDP(&q, now)
	if len(findings) != 2 || findings[0].Code != "high_udp_transaction_rtt" || findings[1].Code != "variable_udp_transaction_rtt" {
		t.Fatalf("findings=%+v", findings)
	}
	for _, f := range findings {
		if f.Confidence != "observed" || !strings.Contains(f.Evidence, "upload") || !strings.Contains(f.Recommendation, "processing") {
			t.Fatal(f)
		}
	}
	for _, change := range []func(*UDPFlowQuality){
		func(q *UDPFlowQuality) { q.Method = "unknown" }, func(q *UDPFlowQuality) { q.Protocol = "unknown" },
		func(q *UDPFlowQuality) { q.ObservedAt = now.Add(-2 * time.Minute) }, func(q *UDPFlowQuality) { q.MeasurementAt = nil },
		func(q *UDPFlowQuality) { future := now.Add(time.Second); q.MeasurementAt = &future },
		func(q *UDPFlowQuality) { old := now.Add(-2 * time.Minute); q.MeasurementAt = &old },
		func(q *UDPFlowQuality) { q.ObservedAt = now.Add(-time.Second) },
		func(q *UDPFlowQuality) { q.UploadExchange.Samples = 0 }, func(q *UDPFlowQuality) { v := math.NaN(); q.UploadExchange.Mean = &v; q.UploadExchange.Jitter = &v },
		func(q *UDPFlowQuality) { q.Protocol = "source_server_query" },
	} {
		copy := q
		change(&copy)
		if got := DiagnoseUDP(&copy, now); len(got) != 0 {
			t.Fatalf("invalid timing diagnosed: %+v", got)
		}
	}
	if got := DiagnoseUDP(nil, now); len(got) != 0 {
		t.Fatal(got)
	}
	q.UploadExchange, q.DownloadExchange = LatencyMilliseconds{}, q.UploadExchange
	for _, finding := range DiagnoseUDP(&q, now) {
		if !strings.Contains(finding.Evidence, "download") {
			t.Fatal("wrong measurement leg", finding)
		}
	}
}

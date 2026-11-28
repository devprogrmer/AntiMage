package networkintelligence

import (
	"fmt"
	"math"
	"time"
)

// DiagnoseUDP reports transaction observations, never in-game ping, packet loss
// or a proven root cause. Unknown protocols and ambiguous/stale timing fail closed.
func DiagnoseUDP(q *UDPFlowQuality, now time.Time) []Finding {
	if q == nil || q.Protocol == "source_server_query" || UDPApplicationDetection(q, now).Application == "Unknown" || q.MeasurementAt == nil || q.MeasurementAt.IsZero() || q.MeasurementAt.After(now) || q.MeasurementAt.After(q.ObservedAt) || now.Sub(*q.MeasurementAt) >= udpEvidenceWindow {
		return nil
	}
	var findings []Finding
	for _, leg := range []struct {
		name    string
		quality LatencyMilliseconds
	}{{"upload", q.UploadExchange}, {"download", q.DownloadExchange}} {
		quality := leg.quality
		valid := func(v *float64) bool {
			return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v > 0 && *v <= float64(udpExchangeTimeout/time.Millisecond)
		}
		if quality.Samples == 0 || quality.Samples > 32 {
			continue
		}
		if valid(quality.Mean) && *quality.Mean > 200 {
			findings = append(findings, Finding{Code: "high_udp_transaction_rtt", Severity: "warning", Confidence: "observed", Evidence: fmt.Sprintf("%s %s transaction RTT averaged %.1f ms across %d matched samples; not in-game ping", q.Protocol, leg.name, *quality.Mean, quality.Samples), Recommendation: "Compare both path legs and remote processing time; this observation alone does not establish the cause of lag"})
		}
		if quality.Samples >= 2 && valid(quality.Jitter) && *quality.Jitter > 30 {
			findings = append(findings, Finding{Code: "variable_udp_transaction_rtt", Severity: "warning", Confidence: "observed", Evidence: fmt.Sprintf("%s %s successive transaction RTT variation was %.1f ms across %d samples", q.Protocol, leg.name, *quality.Jitter, quality.Samples), Recommendation: "Compare collection completeness, tunnel evidence and remote processing over the same interval"})
		}
	}
	return findings
}

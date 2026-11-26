package networkintelligence

import "time"

// DiagnosticInput contains observations, never cumulative host error counters.
type DiagnosticInput struct {
	ObservedAt      time.Time
	ACKRTT          *time.Duration
	Jitter          *time.Duration
	Retransmissions uint64
	CollectorDrops  uint64
}

type Finding struct {
	Code           string `json:"code"`
	Severity       string `json:"severity"`
	Confidence     string `json:"confidence"`
	Evidence       string `json:"evidence"`
	Recommendation string `json:"recommendation"`
}

// Diagnose reports evidence-backed observations. It never changes network state.
func Diagnose(in DiagnosticInput, now time.Time) []Finding {
	findings := []Finding{}
	if in.ObservedAt.IsZero() || in.ObservedAt.After(now) || now.Sub(in.ObservedAt) > time.Minute {
		return append(findings, Finding{"stale_telemetry", "warning", "observed", "No recent observation is available", "Check node connectivity and collector status"})
	}
	if in.CollectorDrops > 0 {
		findings = append(findings, Finding{"collector_observation_loss", "warning", "observed", "The capture socket reported dropped observations", "Reduce collection load before interpreting traffic quality"})
	}
	if in.ACKRTT != nil && *in.ACKRTT > 200*time.Millisecond {
		findings = append(findings, Finding{"high_tcp_ack_rtt", "warning", "observed", "TCP ACK RTT exceeded 200 ms at the observation point", "Compare both path legs and destination response timing"})
	}
	if in.Jitter != nil && *in.Jitter > 30*time.Millisecond {
		findings = append(findings, Finding{"variable_tcp_ack_rtt", "warning", "observed", "Successive TCP ACK RTT variation exceeded 30 ms", "Compare resource and tunnel measurements in the same interval"})
	}
	if in.Retransmissions > 0 {
		findings = append(findings, Finding{"observed_tcp_retransmission", "warning", "observed", "Repeated TCP sequence spans were observed", "Inspect capture completeness and both network directions"})
	}
	return findings
}

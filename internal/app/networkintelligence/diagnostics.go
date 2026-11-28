package networkintelligence

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// DiagnoseInfrastructure reports recent interval counter evidence at node scope.
func DiagnoseInfrastructure(sampledAt, now time.Time, interval float64, rates map[string]float64) []Finding {
	if sampledAt.IsZero() || sampledAt.After(now) || now.Sub(sampledAt) > time.Minute || interval <= 0 || interval > 60 || math.IsNaN(interval) {
		return nil
	}
	var findings []Finding
	for _, metric := range []struct{ key, code, label string }{
		{"softnet.dropped", "node_softnet_drops", "Kernel receive backlog drops"},
		{"ip.ReasmFails", "node_reassembly_failures", "IPv4 reassembly failures"},
		{"ip.FragFails", "node_fragmentation_failures", "IPv4 fragmentation failures"},
	} {
		rate := rates[metric.key]
		if rate > 0 && !math.IsInf(rate, 0) {
			findings = append(findings, Finding{
				Code: metric.code, Severity: "warning", Confidence: "observed",
				Evidence:       fmt.Sprintf("%s averaged %.3g events/s over %.3g s on this node", metric.label, rate, interval),
				Recommendation: "Compare node counters and tunnel observations over the same interval",
			})
		}
	}
	for _, metric := range []struct{ suffix, code, label string }{
		{".rx_dropped", "interface_rx_drops", "receive drops"},
		{".tx_dropped", "interface_tx_drops", "transmit drops"},
		{".rx_errors", "interface_rx_errors", "receive errors"},
		{".tx_errors", "interface_tx_errors", "transmit errors"},
	} {
		var iface string
		var highest float64
		for key, rate := range rates {
			name, ok := strings.CutPrefix(key, "interface.")
			if !ok {
				continue
			}
			name, ok = strings.CutSuffix(name, metric.suffix)
			if !ok || name == "" || !(rate > 0) || math.IsInf(rate, 0) {
				continue
			}
			if rate > highest || (rate == highest && name < iface) {
				iface, highest = name, rate
			}
		}
		if iface != "" {
			findings = append(findings, Finding{
				Code: metric.code, Severity: "warning", Confidence: "observed",
				Evidence:       fmt.Sprintf("Interface %s averaged %.3g %s/s over %.3g s", iface, highest, metric.label, interval),
				Recommendation: "Compare interface counters with tunnel and traffic-control measurements over the same interval",
			})
		}
	}
	return findings
}

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

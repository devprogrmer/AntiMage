package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	ni "github.com/antimage/antimage/internal/app/networkintelligence"
)

func (s *Server) handleNetworkIntelligenceNode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := r.Context().Value(adminContextKey).(adminPrincipal)
	if !ok || p.Role != string(adminapp.RoleFullAccess) {
		writeError(w, http.StatusForbidden, "You're not allowed")
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("node_id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "valid node_id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	snapshot, err := s.nodeController.NetworkIntelligence(ctx, id)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Network telemetry unavailable")
		return
	}
	if r.URL.Query().Get("diagnose") == "true" {
		var telemetry struct {
			ObservedAt     time.Time `json:"observed_at"`
			Infrastructure *struct {
				SampledAt time.Time          `json:"sampled_at"`
				Interval  float64            `json:"interval_seconds"`
				Rates     map[string]float64 `json:"counter_rates_per_second"`
			} `json:"infrastructure"`
			Flows []struct {
				UserID      int64              `json:"user_id"`
				Destination string             `json:"destination"`
				Interface   string             `json:"interface"`
				Source      string             `json:"source"`
				ObservedAt  time.Time          `json:"observed_at"`
				Quality     *ni.TCPFlowQuality `json:"tcp_quality"`
				Protocol    string             `json:"protocol"`
				UDPQuality  *ni.UDPFlowQuality `json:"udp_quality"`
			} `json:"flows"`
		}
		if err := json.Unmarshal(snapshot, &telemetry); err != nil {
			writeError(w, http.StatusBadGateway, "Invalid network telemetry")
			return
		}
		results := make([]map[string]any, 0, len(telemetry.Flows))
		now := time.Now().UTC()
		nodeFindings := ni.Diagnose(ni.DiagnosticInput{ObservedAt: telemetry.ObservedAt}, now)
		if infra := telemetry.Infrastructure; infra != nil {
			nodeFindings = append(nodeFindings, ni.DiagnoseInfrastructure(infra.SampledAt, now, infra.Interval, infra.Rates)...)
		}
		if len(nodeFindings) != 0 {
			results = append(results, map[string]any{"scope": "node", "findings": nodeFindings})
		}
		for _, flow := range telemetry.Flows {
			input := ni.DiagnosticInput{ObservedAt: flow.ObservedAt}
			if flow.Quality != nil {
				input.Retransmissions = flow.Quality.Retransmissions
				input.ACKRTT = millisecondsDuration(flow.Quality.UploadACK.Mean)
				input.Jitter = millisecondsDuration(flow.Quality.UploadACK.Jitter)
			}
			findings := ni.Diagnose(input, now)
			if flow.Protocol == "udp" && !flow.ObservedAt.IsZero() && !flow.ObservedAt.After(now) && now.Sub(flow.ObservedAt) <= time.Minute {
				findings = append(findings, ni.DiagnoseUDP(flow.UDPQuality, now)...)
			}
			results = append(results, map[string]any{"user_id": flow.UserID, "destination": flow.Destination, "interface": flow.Interface, "source": flow.Source, "protocol": flow.Protocol, "findings": findings})
		}
		writeJSON(w, http.StatusOK, map[string]any{"node_id": id, "observed_at": telemetry.ObservedAt, "results": results})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func millisecondsDuration(value *float64) *time.Duration {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value <= 0 || *value > 600000 {
		return nil
	}
	duration := time.Duration(*value * float64(time.Millisecond))
	return &duration
}

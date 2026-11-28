package nodeagent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
)

// Monitoring is absent by default. Its lifecycle barrier joins old capture
// before a kernel ownership mutation, while preserving the runtime result.
func (s *Server) withNetworkIntelligenceMutation(interfaceName string, mutate func() error) error {
	if s.cfg.NetworkOptimizationEnabled {
		s.networkOptimizationMu.Lock()
		err := resetNetworkOptimizationsForInterface(s.cfg.DataDir, interfaceName)
		s.networkOptimizationMu.Unlock()
		if err != nil {
			return err
		}
	}
	if s.networkIntelligenceLifecycle == nil {
		return mutate()
	}
	s.networkIntelligenceMu.Lock()
	defer s.networkIntelligenceMu.Unlock()
	return s.networkIntelligenceLifecycle.Mutate(interfaceName, mutate)
}

func (s *Server) initializeNetworkIntelligence() {
	if !s.cfg.NetworkIntelligenceEnabled {
		return
	}
	tracker, err := ni.New(4096, time.Minute)
	if err != nil {
		return
	}
	lifecycle, err := ni.NewLifecycle(tracker, 32)
	if err != nil {
		return
	}
	s.networkIntelligenceTracker = tracker
	// Invalid/expired catalogues leave game titles Unknown; protocol evidence
	// is independently bounded and never invents a title from a shared transport.
	if strings.TrimSpace(s.cfg.NetworkIntelligenceSignatures) == "" {
		s.networkIntelligenceDetector, _ = ni.LoadDefaultApplicationDetector(time.Now().UTC())
	} else {
		s.networkIntelligenceDetector, _ = loadNetworkApplicationDetector(s.cfg.NetworkIntelligenceSignatures)
	}
	s.networkIntelligenceLifecycle = lifecycle
	lifecycle.SetEnabled(true)
}

func (s *Server) stopNetworkIntelligence() {
	if s.networkIntelligenceLifecycle == nil {
		return
	}
	s.networkIntelligenceMu.Lock()
	defer s.networkIntelligenceMu.Unlock()
	s.networkIntelligenceLifecycle.SetEnabled(false)
}

// Caller holds networkIntelligenceMu across apply, verification and publication.
func (s *Server) publishNetworkIntelligence(prepared preparedWireGuardRuntime) error {
	policy, err := s.currentRuntimePolicy()
	if err != nil || policy == nil {
		return errors.New("monitoring policy unavailable")
	}
	payload, err := policy.nativePayload()
	if err != nil || payload.SessionCallback.NodeID <= 0 {
		return errors.New("monitoring node identity unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	owned, err := wireGuardInterfaceHasOwnershipAlias(prepared.Tag, prepared.InterfaceName)
	if err != nil || !owned {
		return errors.New("monitoring interface ownership unavailable")
	}
	identity, err := wireGuardInterfaceIdentity(prepared.InterfaceName)
	if err != nil || identity == "" {
		return errors.New("monitoring interface identity unavailable")
	}
	wg, err := wireGuardRuntimeLookPath("wg")
	if err != nil {
		return err
	}
	raw, err := wireGuardRuntimeRun(ctx, wg, "show", prepared.InterfaceName, "allowed-ips")
	if err != nil {
		return errors.New("monitoring kernel peers unavailable")
	}
	after, err := wireGuardInterfaceIdentity(prepared.InterfaceName)
	if err != nil || after != identity {
		return errors.New("monitoring interface replaced")
	}
	owned, err = wireGuardInterfaceHasOwnershipAlias(prepared.Tag, prepared.InterfaceName)
	if err != nil || !owned {
		return errors.New("monitoring interface ownership changed")
	}
	if s.networkIntelligenceGeneration == ^uint64(0) {
		return errors.New("monitoring generation exhausted")
	}
	s.networkIntelligenceGeneration++
	bindings, err := verifiedWireGuardBindings(payload.SessionCallback.NodeID, prepared, identity, s.networkIntelligenceGeneration, time.Now().UTC(), string(raw))
	if err != nil {
		return err
	}
	// Provisional per-interface observation budget, pending forwarding benchmarks.
	return s.networkIntelligenceLifecycle.Publish(context.Background(), ni.CaptureConfig{Interface: prepared.InterfaceName, MaxPacketsPerSecond: 10000}, bindings, func() (ni.CaptureSession, error) {
		return ni.NewSessionCapture(s.networkIntelligenceTracker, bindings, 4096)
	})
}

func (s *Server) networkIntelligenceSnapshot() string {
	if s.networkIntelligenceTracker == nil || s.networkIntelligenceLifecycle == nil || !s.networkIntelligenceLifecycle.Enabled() {
		return ""
	}
	now := time.Now().UTC()
	flows := s.networkIntelligenceTracker.Snapshot(now)
	history := s.networkIntelligenceHistory.Snapshot(flows, s.networkIntelligenceTracker.Epoch(), now)
	slices.SortFunc(flows, func(a, b ni.Flow) int {
		if order := b.At.Compare(a.At); order != 0 {
			return order
		}
		if order := a.Tuple.Source.Compare(b.Tuple.Source); order != 0 {
			return order
		}
		if order := a.Tuple.Destination.Compare(b.Tuple.Destination); order != 0 {
			return order
		}
		if order := cmp.Compare(a.Binding.Interface, b.Binding.Interface); order != 0 {
			return order
		}
		return cmp.Compare(a.Tuple.Protocol, b.Tuple.Protocol)
	})
	truncated := len(flows) > 250
	if truncated {
		flows = flows[:250]
	}
	// Binding session identities remain local to the collector.
	rows := make([]map[string]any, 0, len(flows))
	for _, flow := range flows {
		udp := flow.UDPQuality
		if udp != nil && (udp.ObservedAt.IsZero() || udp.ObservedAt.After(now) || now.Sub(udp.ObservedAt) >= time.Minute) {
			udp = nil
		}
		rows = append(rows, map[string]any{"user_id": flow.Binding.UserID, "node_id": flow.Binding.NodeID, "interface": flow.Binding.Interface, "source": flow.Tuple.Source.String(), "destination": flow.Tuple.Destination.String(), "protocol": flow.Tuple.Protocol, "upload_packets": flow.UploadPackets, "download_packets": flow.DownloadPackets, "upload_bytes": flow.UploadBytes, "download_bytes": flow.DownloadBytes, "observed_at": flow.At, "tcp_quality": flow.TCPQuality, "udp_quality": udp, "protocol_detection": ni.UDPApplicationDetection(udp, now), "application": s.networkIntelligenceDetector.DetectObserved(flow.Tuple, udp, now)})
	}
	snapshot := map[string]any{"version": 1, "observed_at": now, "truncated": truncated, "flows": rows}
	snapshot["history"] = history
	if infrastructure, err := s.networkIntelligenceInfrastructure.Sample(time.Now().UTC()); err == nil {
		snapshot["infrastructure"] = infrastructure
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	return string(raw)
}

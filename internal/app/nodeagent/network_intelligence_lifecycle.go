package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	"time"
)

// Monitoring is absent by default. Its lifecycle barrier joins old capture
// before a kernel ownership mutation, while preserving the runtime result.
func (s *Server) withNetworkIntelligenceMutation(interfaceName string, mutate func() error) error {
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
	// Invalid or unavailable signatures leave classification Unknown.
	s.networkIntelligenceDetector, _ = loadNetworkApplicationDetector(s.cfg.NetworkIntelligenceSignatures)
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
	return s.networkIntelligenceLifecycle.Publish(context.Background(), ni.CaptureConfig{Interface: prepared.InterfaceName}, bindings, func() (ni.CaptureSession, error) {
		return ni.NewSessionCapture(s.networkIntelligenceTracker, bindings, 4096)
	})
}

func (s *Server) networkIntelligenceSnapshot() string {
	if s.networkIntelligenceTracker == nil || s.networkIntelligenceLifecycle == nil || !s.networkIntelligenceLifecycle.Enabled() {
		return ""
	}
	flows := s.networkIntelligenceTracker.Snapshot(time.Now().UTC())
	truncated := len(flows) > 250
	if truncated {
		flows = flows[:250]
	}
	// Binding session identities remain local to the collector.
	rows := make([]map[string]any, 0, len(flows))
	for _, flow := range flows {
		rows = append(rows, map[string]any{"user_id": flow.Binding.UserID, "node_id": flow.Binding.NodeID, "interface": flow.Binding.Interface, "source": flow.Tuple.Source.String(), "destination": flow.Tuple.Destination.String(), "protocol": flow.Tuple.Protocol, "upload_packets": flow.UploadPackets, "download_packets": flow.DownloadPackets, "upload_bytes": flow.UploadBytes, "download_bytes": flow.DownloadBytes, "observed_at": flow.At, "tcp_quality": flow.TCPQuality, "application": s.networkIntelligenceDetector.Detect(flow.Tuple, time.Now().UTC())})
	}
	raw, err := json.Marshal(map[string]any{"version": 1, "observed_at": time.Now().UTC(), "truncated": truncated, "flows": rows})
	if err != nil {
		return ""
	}
	return string(raw)
}

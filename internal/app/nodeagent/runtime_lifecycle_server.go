package nodeagent

import (
	"encoding/json"
	"fmt"
)

func (s *Server) runtimeLifecycleCoordinator() *runtimeLifecycle {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtimeLifecycle == nil {
		s.runtimeLifecycle = newRuntimeLifecycle()
	}
	return s.runtimeLifecycle
}

func (s *Server) lifecycleTagsForConfig(configJSON, nativeJSON string) ([]string, error) {
	tags, err := runtimeLifecycleTags(configJSON, nativeJSON)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	for tag := range s.openVPNRuntimes {
		tags = append(tags, tag)
	}
	for tag := range s.anyConnectRuntimes {
		tags = append(tags, tag)
	}
	for tag := range s.l2TPRuntimes {
		tags = append(tags, tag)
	}
	for tag := range s.pptpRuntimes {
		tags = append(tags, tag)
	}
	for tag := range s.ikev2Runtimes {
		tags = append(tags, tag)
	}
	for tag := range s.wireGuardRuntimes {
		tags = append(tags, tag)
	}
	for tag := range s.amneziaWGRuntimes {
		tags = append(tags, tag)
	}
	s.mu.Unlock()
	return normalizeLifecycleTags(tags), nil
}

func runtimeLifecycleTags(configJSON, nativeJSON string) ([]string, error) {
	tags := make([]string, 0)
	var config struct {
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
	}
	if configJSON != "" {
		if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
			return nil, fmt.Errorf("parse lifecycle config tags: %w", err)
		}
		for _, inbound := range config.Inbounds {
			tags = append(tags, inbound.Tag)
		}
	}
	payload, err := parseNativeRuntimePayload(nativeJSON)
	if err != nil {
		return nil, err
	}
	for _, inbound := range payload.OpenVPNInbounds {
		tags = append(tags, inbound.Tag)
	}
	for _, inbound := range payload.AnyConnectInbounds {
		tags = append(tags, inbound.Tag)
	}
	for _, inbound := range payload.L2TPInbounds {
		tags = append(tags, inbound.Tag)
	}
	for _, inbound := range payload.PPTPInbounds {
		tags = append(tags, inbound.Tag)
	}
	for _, inbound := range payload.WireGuardInbounds {
		tags = append(tags, inbound.Tag)
	}
	for _, inbound := range payload.AmneziaWGInbounds {
		tags = append(tags, inbound.Tag)
	}
	for _, inbound := range payload.IKEv2Inbounds {
		tags = append(tags, inbound.Tag)
	}
	for _, policy := range payload.XrayPolicies {
		tags = append(tags, policy.InboundTag)
	}
	return normalizeLifecycleTags(tags), nil
}

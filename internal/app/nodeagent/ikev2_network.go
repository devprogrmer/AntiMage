package nodeagent

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const (
	ikev2TProxyMark         uint32 = 0xA1750000
	ikev2TProxyMask         uint32 = 0xFFFF0000
	ikev2TProxyTable               = 205
	ikev2TProxyRulePriority        = 10050

	ikev2TProxyChain = "ANTIMAGE_IKEV2_TPROXY"
	ikev2NATChain    = "ANTIMAGE_IKEV2_NAT"
)

type ikev2TProxySpec struct {
	Enabled    bool
	SourceCIDR string
	TunnelPort int
}

func buildIKEv2TProxySpec(
	inbound ikev2RuntimeInbound,
) (ikev2TProxySpec, error) {
	spec := ikev2TProxySpec{
		Enabled: openVPNBoolSetting(
			inbound.Settings,
			"tproxy_enabled",
			true,
		),
		TunnelPort: inbound.TunnelPort,
	}

	if !spec.Enabled {
		return spec, nil
	}

	if spec.TunnelPort <= 0 || spec.TunnelPort > 65535 {
		return spec, fmt.Errorf(
			"ikev2 %q: invalid tunnel port %d",
			inbound.Tag,
			spec.TunnelPort,
		)
	}

	prefix, err := ikev2PoolPrefix(inbound)
	if err != nil {
		return spec, err
	}

	spec.SourceCIDR = prefix.String()
	return spec, nil
}

func buildIKEv2NATSpec(
	inbound ikev2RuntimeInbound,
) (openVPNNATSpec, error) {
	prefix, err := ikev2PoolPrefix(inbound)
	if err != nil {
		return openVPNNATSpec{}, err
	}

	return openVPNNATSpec{
		Enabled:    true,
		SourceCIDR: prefix.String(),
	}, nil
}

func (s *Server) applyIKEv2TProxy(
	tag string,
	spec ikev2TProxySpec,
) error {
	s.mu.Lock()
	old, exists := s.ikev2TProxySpecs[tag]
	s.mu.Unlock()

	if exists && old != spec {
		if err := s.removeIKEv2TProxy(old); err != nil {
			return err
		}
		s.mu.Lock()
		delete(s.ikev2TProxySpecs, tag)
		s.mu.Unlock()
	}

	if !spec.Enabled {
		if exists {
			if err := s.removeIKEv2TProxy(old); err != nil {
				return err
			}
			s.mu.Lock()
			delete(s.ikev2TProxySpecs, tag)
			s.mu.Unlock()
		}
		return s.cleanupIKEv2TProxyIfUnused()
	}

	if openVPNNetworkGOOS != "linux" {
		return fmt.Errorf("ikev2 tproxy is supported only on linux")
	}

	ipPath, err := openVPNNetworkLookPath("ip")
	if err != nil {
		return fmt.Errorf("ikev2 tproxy: ip command not installed")
	}
	iptablesPath, err := openVPNNetworkLookPath("iptables")
	if err != nil {
		return fmt.Errorf("ikev2 tproxy: iptables command not installed")
	}
	sysctlPath, err := openVPNNetworkLookPath("sysctl")
	if err != nil {
		return fmt.Errorf("ikev2 tproxy: sysctl command not installed")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	if err := runOpenVPNNetworkRequired(
		ctx,
		sysctlPath,
		"-w",
		"net.ipv4.ip_forward=1",
	); err != nil {
		return err
	}

	markMask := fmt.Sprintf(
		"0x%x/0x%x",
		ikev2TProxyMark,
		ikev2TProxyMask,
	)

	rules, err := openVPNNetworkRun(ctx, ipPath, "rule", "show")
	if err != nil {
		return err
	}

	ruleNeedle := fmt.Sprintf(
		"fwmark 0x%x/0x%x lookup %d",
		ikev2TProxyMark,
		ikev2TProxyMask,
		ikev2TProxyTable,
	)

	if !strings.Contains(string(rules), ruleNeedle) {
		if err := runOpenVPNNetworkRequired(
			ctx,
			ipPath,
			"rule",
			"add",
			"priority",
			strconv.Itoa(ikev2TProxyRulePriority),
			"fwmark",
			markMask,
			"table",
			strconv.Itoa(ikev2TProxyTable),
		); err != nil {
			return err
		}
	}

	if err := runOpenVPNNetworkRequired(
		ctx,
		ipPath,
		"route",
		"replace",
		"local",
		"0.0.0.0/0",
		"dev",
		"lo",
		"table",
		strconv.Itoa(ikev2TProxyTable),
	); err != nil {
		return err
	}

	if err := ensureTaggedTProxyChain(
		ctx,
		iptablesPath,
		ikev2TProxyChain,
	); err != nil {
		return err
	}

	for _, protocol := range []string{"tcp", "udp"} {
		args := []string{
			"-w", "5",
			"-t", "mangle",
			"-C", ikev2TProxyChain,
			"-s", spec.SourceCIDR,
			"-p", protocol,
			"-j", "TPROXY",
			"--on-ip", "127.0.0.1",
			"--on-port", strconv.Itoa(spec.TunnelPort),
			"--tproxy-mark", markMask,
		}

		if _, err := openVPNNetworkRun(
			ctx,
			iptablesPath,
			args...,
		); err == nil {
			continue
		}

		args[4] = "-A"

		if err := runOpenVPNNetworkRequired(
			ctx,
			iptablesPath,
			args...,
		); err != nil {
			return err
		}
	}

	s.mu.Lock()
	s.ikev2TProxySpecs[tag] = spec
	s.mu.Unlock()

	return nil
}

func (s *Server) removeIKEv2TProxy(
	spec ikev2TProxySpec,
) error {
	if !spec.Enabled || openVPNNetworkGOOS != "linux" {
		return nil
	}

	iptablesPath, err := openVPNNetworkLookPath("iptables")
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	markMask := fmt.Sprintf(
		"0x%x/0x%x",
		ikev2TProxyMark,
		ikev2TProxyMask,
	)

	for _, protocol := range []string{"tcp", "udp"} {
		args := []string{
			"-w", "5",
			"-t", "mangle",
			"-D", ikev2TProxyChain,
			"-s", spec.SourceCIDR,
			"-p", protocol,
			"-j", "TPROXY",
			"--on-ip", "127.0.0.1",
			"--on-port", strconv.Itoa(spec.TunnelPort),
			"--tproxy-mark", markMask,
		}

		_, _ = openVPNNetworkRun(
			ctx,
			iptablesPath,
			args...,
		)
	}

	return nil
}

func (s *Server) stopRemovedIKEv2TProxySpecs(
	desired map[string]struct{},
) {
	s.mu.Lock()
	removed := make([]ikev2TProxySpec, 0)

	for tag, spec := range s.ikev2TProxySpecs {
		if _, ok := desired[tag]; ok {
			continue
		}
		removed = append(removed, spec)
		delete(s.ikev2TProxySpecs, tag)
	}

	s.mu.Unlock()

	for _, spec := range removed {
		_ = s.removeIKEv2TProxy(spec)
	}

	_ = s.cleanupIKEv2TProxyIfUnused()
}

func (s *Server) stopAllIKEv2TProxySpecs() {
	s.mu.Lock()
	specs := s.ikev2TProxySpecs
	s.ikev2TProxySpecs = make(map[string]ikev2TProxySpec)
	s.mu.Unlock()

	for _, spec := range specs {
		_ = s.removeIKEv2TProxy(spec)
	}

	_ = s.cleanupIKEv2TProxyIfUnused()
}

func (s *Server) cleanupIKEv2TProxyIfUnused() error {
	s.mu.Lock()
	unused := len(s.ikev2TProxySpecs) == 0
	s.mu.Unlock()

	if !unused || openVPNNetworkGOOS != "linux" {
		return nil
	}

	ipPath, _ := openVPNNetworkLookPath("ip")
	iptablesPath, _ := openVPNNetworkLookPath("iptables")

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	if iptablesPath != "" {
		cleanupTaggedTProxyChain(
			ctx,
			iptablesPath,
			ikev2TProxyChain,
		)
	}

	if ipPath != "" {
		markMask := fmt.Sprintf(
			"0x%x/0x%x",
			ikev2TProxyMark,
			ikev2TProxyMask,
		)

		_, _ = openVPNNetworkRun(
			ctx,
			ipPath,
			"route",
			"del",
			"local",
			"0.0.0.0/0",
			"dev",
			"lo",
			"table",
			strconv.Itoa(ikev2TProxyTable),
		)

		_, _ = openVPNNetworkRun(
			ctx,
			ipPath,
			"rule",
			"del",
			"priority",
			strconv.Itoa(ikev2TProxyRulePriority),
			"fwmark",
			markMask,
			"table",
			strconv.Itoa(ikev2TProxyTable),
		)
	}

	return nil
}

func (s *Server) applyIKEv2NAT(
	tag string,
	spec openVPNNATSpec,
) error {
	return s.applyTaggedNAT(
		tag,
		spec,
		&s.ikev2NATSpecs,
		ikev2NATChain,
		"ikev2",
	)
}

func (s *Server) removeIKEv2NATForTag(tag string) error {
	return s.removeTaggedNATForTag(
		tag,
		&s.ikev2NATSpecs,
		ikev2NATChain,
	)
}

func (s *Server) stopRemovedIKEv2NATSpecs(
	desired map[string]struct{},
) {
	s.stopRemovedTaggedNATSpecs(
		desired,
		&s.ikev2NATSpecs,
		ikev2NATChain,
		"ikev2",
	)
}

func (s *Server) stopAllIKEv2NATSpecs() {
	s.stopAllTaggedNATSpecs(
		&s.ikev2NATSpecs,
		ikev2NATChain,
		"ikev2",
	)
}

func ikev2ValidPool(raw string) bool {
	_, err := netip.ParsePrefix(strings.TrimSpace(raw))
	return err == nil
}

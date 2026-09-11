package nodeagent

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	wireGuardTProxyMark         uint32 = 0xA17F0000
	wireGuardTProxyMask         uint32 = 0xFFFF0000
	wireGuardTProxyTable               = 202
	wireGuardTProxyRulePriority        = 10020
	wireGuardTProxyChain               = "ANTIMAGE_WG_TPROXY"
	wireGuardNATChain                  = "ANTIMAGE_WG_NAT"
	wireGuardForwardChain              = "ANTIMAGE_WG_FORWARD"
)

type wireGuardRoutingMode string

const (
	wireGuardRoutingNone   wireGuardRoutingMode = ""
	wireGuardRoutingTProxy wireGuardRoutingMode = "tproxy"
	wireGuardRoutingNAT    wireGuardRoutingMode = "nat"
)

type wireGuardRoutingSpec struct {
	Mode       wireGuardRoutingMode
	Interface  string
	SourceCIDR string
	TunnelPort int
	Mark       uint32
	Mask       uint32
	Table      int
	Priority   int
}

func buildWireGuardRoutingSpec(
	inbound wireGuardRuntimeInbound,
	interfaceName string,
	sourceCIDR string,
) (wireGuardRoutingSpec, error) {
	spec := wireGuardRoutingSpec{
		Interface:  strings.TrimSpace(interfaceName),
		SourceCIDR: strings.TrimSpace(sourceCIDR),
		Mark:       wireGuardTProxyMark,
		Mask:       wireGuardTProxyMask,
		Table:      wireGuardTProxyTable,
		Priority:   wireGuardTProxyRulePriority,
	}

	if spec.Interface == "" || spec.SourceCIDR == "" {
		return spec, fmt.Errorf(
			"wireguard %q: incomplete routing specification",
			inbound.Tag,
		)
	}

	tproxyEnabled := wireGuardBoolSetting(
		inbound.Settings,
		"tproxy_enabled",
		true,
	)
	natEnabled := wireGuardBoolSetting(
		inbound.Settings,
		"nat_enabled",
		false,
	)

	if tproxyEnabled {
		if inbound.TunnelPort < 1 || inbound.TunnelPort > 65535 {
			return spec, fmt.Errorf(
				"wireguard %q: invalid tunnel port %d",
				inbound.Tag,
				inbound.TunnelPort,
			)
		}
		spec.Mode = wireGuardRoutingTProxy
		spec.TunnelPort = inbound.TunnelPort
		return spec, nil
	}

	if natEnabled {
		spec.Mode = wireGuardRoutingNAT
		return spec, nil
	}

	spec.Mode = wireGuardRoutingNone
	return spec, nil
}

func (s wireGuardRoutingSpec) markMask() string {
	return fmt.Sprintf("0x%x/0x%x", s.Mark, s.Mask)
}

func (s wireGuardRoutingSpec) tproxyArgs(
	action string,
	protocol string,
) ([]string, error) {
	if s.Mode != wireGuardRoutingTProxy {
		return nil, nil
	}

	switch action {
	case "-A", "-C", "-D":
	default:
		return nil, fmt.Errorf("invalid iptables action %q", action)
	}

	switch protocol {
	case "tcp", "udp":
	default:
		return nil, fmt.Errorf("invalid tproxy protocol %q", protocol)
	}

	if s.Interface == "" ||
		s.SourceCIDR == "" ||
		s.TunnelPort < 1 ||
		s.Mark == 0 ||
		s.Mask == 0 ||
		s.Table <= 0 ||
		s.Priority <= 0 {
		return nil, fmt.Errorf("incomplete wireguard tproxy specification")
	}

	return []string{
		"-t", "mangle",
		action, wireGuardTProxyChain,
		"-i", s.Interface,
		"-s", s.SourceCIDR,
		"-p", protocol,
		"-j", "TPROXY",
		"--on-ip", "127.0.0.1",
		"--on-port", strconv.Itoa(s.TunnelPort),
		"--tproxy-mark", s.markMask(),
	}, nil
}

func (s wireGuardRoutingSpec) natArgs(action string) ([]string, error) {
	if s.Mode != wireGuardRoutingNAT {
		return nil, nil
	}
	switch action {
	case "-A", "-C", "-D":
	default:
		return nil, fmt.Errorf("invalid iptables action %q", action)
	}
	if s.SourceCIDR == "" {
		return nil, fmt.Errorf("incomplete wireguard NAT specification")
	}
	return []string{
		"-t", "nat",
		action, wireGuardNATChain,
		"-s", s.SourceCIDR,
		"-j", "MASQUERADE",
	}, nil
}

func (s wireGuardRoutingSpec) forwardOutArgs(action string) ([]string, error) {
	if s.Mode != wireGuardRoutingNAT {
		return nil, nil
	}
	switch action {
	case "-A", "-C", "-D":
	default:
		return nil, fmt.Errorf("invalid iptables action %q", action)
	}
	return []string{
		action, wireGuardForwardChain,
		"-i", s.Interface,
		"-s", s.SourceCIDR,
		"-j", "ACCEPT",
	}, nil
}

func (s wireGuardRoutingSpec) forwardReturnArgs(action string) ([]string, error) {
	if s.Mode != wireGuardRoutingNAT {
		return nil, nil
	}
	switch action {
	case "-A", "-C", "-D":
	default:
		return nil, fmt.Errorf("invalid iptables action %q", action)
	}
	return []string{
		action, wireGuardForwardChain,
		"-o", s.Interface,
		"-d", s.SourceCIDR,
		"-m", "conntrack",
		"--ctstate", "RELATED,ESTABLISHED",
		"-j", "ACCEPT",
	}, nil
}

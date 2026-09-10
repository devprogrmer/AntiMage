package nodeagent

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

const (
	openVPNTProxyMark         uint32 = 0xA17E0000
	openVPNTProxyMask         uint32 = 0xFFFF0000
	openVPNTProxyTable               = 201
	openVPNTProxyRulePriority        = 10010
	openVPNTProxyChain               = "ANTIMAGE_OV_TPROXY"
)

type openVPNTProxySpec struct {
	Enabled    bool
	Interface  string
	SourceCIDR string
	TunnelPort int
	Mark       uint32
	Mask       uint32
	Table      int
	Priority   int
}

func buildOpenVPNTProxySpec(
	inbound openVPNRuntimeInbound,
) (openVPNTProxySpec, error) {
	spec := openVPNTProxySpec{
		Enabled: openVPNBoolSetting(
			inbound.Settings,
			"tproxy_enabled",
			true,
		),
		Mark:     openVPNTProxyMark,
		Mask:     openVPNTProxyMask,
		Table:    openVPNTProxyTable,
		Priority: openVPNTProxyRulePriority,
	}

	if !spec.Enabled {
		return spec, nil
	}

	if strings.TrimSpace(inbound.Tag) == "" {
		return spec, fmt.Errorf(
			"openvpn tproxy: inbound tag is required",
		)
	}

	if inbound.TunnelPort <= 0 ||
		inbound.TunnelPort > 65535 {
		return spec, fmt.Errorf(
			"openvpn %q: invalid tunnel port %d",
			inbound.Tag,
			inbound.TunnelPort,
		)
	}

	rawPool := openVPNStringSetting(
		inbound.Settings,
		"ipv4_pool_cidr",
		"10.66.0.0/16",
	)

	prefix, err := netip.ParsePrefix(
		strings.TrimSpace(rawPool),
	)
	if err != nil || !prefix.Addr().Is4() {
		return spec, fmt.Errorf(
			"openvpn %q: invalid IPv4 pool CIDR %q",
			inbound.Tag,
			rawPool,
		)
	}

	spec.Interface = openVPNTunName(inbound.Tag)
	spec.SourceCIDR = prefix.Masked().String()
	spec.TunnelPort = inbound.TunnelPort

	return spec, nil
}

func (s openVPNTProxySpec) markMask() string {
	return fmt.Sprintf(
		"0x%x/0x%x",
		s.Mark,
		s.Mask,
	)
}

func (s openVPNTProxySpec) iptablesArgs(
	action,
	protocol string,
) ([]string, error) {
	if !s.Enabled {
		return nil, nil
	}

	switch action {
	case "-A", "-C", "-D":
	default:
		return nil, fmt.Errorf(
			"invalid iptables action %q",
			action,
		)
	}

	switch protocol {
	case "tcp", "udp":
	default:
		return nil, fmt.Errorf(
			"invalid tproxy protocol %q",
			protocol,
		)
	}

	if s.Interface == "" ||
		s.SourceCIDR == "" ||
		s.TunnelPort <= 0 ||
		s.Mark == 0 ||
		s.Mask == 0 ||
		s.Table <= 0 ||
		s.Priority <= 0 {
		return nil, fmt.Errorf(
			"incomplete openvpn tproxy specification",
		)
	}

	return []string{
		"-t", "mangle",
		action, openVPNTProxyChain,
		"-i", s.Interface,
		"-s", s.SourceCIDR,
		"-p", protocol,
		"-j", "TPROXY",
		"--on-ip", "127.0.0.1",
		"--on-port", strconv.Itoa(s.TunnelPort),
		"--tproxy-mark", s.markMask(),
	}, nil
}

func (s openVPNTProxySpec) policyRuleArgs() []string {
	return []string{
		"rule", "add",
		"priority", strconv.Itoa(s.Priority),
		"fwmark", s.markMask(),
		"table", strconv.Itoa(s.Table),
	}
}

func (s openVPNTProxySpec) policyRuleDeleteArgs() []string {
	return []string{
		"rule", "del",
		"priority", strconv.Itoa(s.Priority),
		"fwmark", s.markMask(),
		"table", strconv.Itoa(s.Table),
	}
}

func (s openVPNTProxySpec) localRouteArgs() []string {
	return []string{
		"route", "replace",
		"local", "0.0.0.0/0",
		"dev", "lo",
		"table", strconv.Itoa(s.Table),
	}
}

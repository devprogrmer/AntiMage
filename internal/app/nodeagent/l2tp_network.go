package nodeagent

import (
	"fmt"
	"strings"
)

const (
	l2TPTProxyMark         uint32 = 0xA1720000
	l2TPTProxyMask         uint32 = 0xFFFF0000
	l2TPTProxyTable               = 203
	l2TPTProxyRulePriority        = 10030
	l2TPTProxyChain               = "ANTIMAGE_L2TP_TPROXY"
	l2TPNATChain                  = "ANTIMAGE_L2TP_NAT"
)

func buildL2TPTProxySpec(inbound l2TPRuntimeInbound) (openVPNTProxySpec, error) {
	spec := openVPNTProxySpec{
		Enabled:  l2TPBoolSetting(inbound.Settings, "tproxy_enabled", true),
		Mark:     l2TPTProxyMark,
		Mask:     l2TPTProxyMask,
		Table:    l2TPTProxyTable,
		Priority: l2TPTProxyRulePriority,
	}
	if !spec.Enabled {
		return spec, nil
	}
	if strings.TrimSpace(inbound.Tag) == "" {
		return spec, fmt.Errorf("l2tp tproxy: inbound tag is required")
	}
	if inbound.TunnelPort <= 0 {
		inbound.TunnelPort = defaultL2TPTunnel
	}
	if inbound.TunnelPort != defaultL2TPTunnel {
		return spec, fmt.Errorf("l2tp %q: tunnel port must be %d", inbound.Tag, defaultL2TPTunnel)
	}
	prefix, err := l2TPPoolPrefix(inbound)
	if err != nil {
		return spec, err
	}
	spec.Interface = "ppp+"
	spec.SourceCIDR = prefix.String()
	spec.TunnelPort = inbound.TunnelPort
	return spec, nil
}

func buildL2TPNATSpec(inbound l2TPRuntimeInbound) (openVPNNATSpec, error) {
	prefix, err := l2TPPoolPrefix(inbound)
	if err != nil {
		return openVPNNATSpec{}, fmt.Errorf("l2tp %q NAT: %w", inbound.Tag, err)
	}
	return openVPNNATSpec{Enabled: true, SourceCIDR: prefix.String()}, nil
}

func (s *Server) applyL2TPTProxy(tag string, spec openVPNTProxySpec) error {
	return s.applyTaggedTProxy(tag, spec, &s.l2TPTProxySpecs, l2TPTProxyChain, "l2tp")
}

func (s *Server) removeL2TPTProxyForTag(tag string) error {
	return s.removeTaggedTProxyForTag(tag, &s.l2TPTProxySpecs, l2TPTProxyChain)
}

func (s *Server) stopRemovedL2TPTProxySpecs(desired map[string]struct{}) {
	s.stopRemovedTaggedTProxySpecs(desired, &s.l2TPTProxySpecs, l2TPTProxyChain, "l2tp")
}

func (s *Server) stopAllL2TPTProxySpecs() {
	s.stopAllTaggedTProxySpecs(&s.l2TPTProxySpecs, l2TPTProxyChain, "l2tp")
}

func (s *Server) applyL2TPNAT(tag string, spec openVPNNATSpec) error {
	return s.applyTaggedNAT(tag, spec, &s.l2TPNATSpecs, l2TPNATChain, "l2tp")
}

func (s *Server) removeL2TPNATForTag(tag string) error {
	return s.removeTaggedNATForTag(tag, &s.l2TPNATSpecs, l2TPNATChain)
}

func (s *Server) stopRemovedL2TPNATSpecs(desired map[string]struct{}) {
	s.stopRemovedTaggedNATSpecs(desired, &s.l2TPNATSpecs, l2TPNATChain, "l2tp")
}

func (s *Server) stopAllL2TPNATSpecs() {
	s.stopAllTaggedNATSpecs(&s.l2TPNATSpecs, l2TPNATChain, "l2tp")
}

func l2TPBoolSetting(settings map[string]any, key string, fallback bool) bool {
	return openVPNBoolSetting(settings, key, fallback)
}

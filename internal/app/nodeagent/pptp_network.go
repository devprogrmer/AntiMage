package nodeagent

import (
	"fmt"
	"strings"
)

const (
	pptpTProxyMark         uint32 = 0xA1710000
	pptpTProxyMask         uint32 = 0xFFFF0000
	pptpTProxyTable               = 204
	pptpTProxyRulePriority        = 10040
	pptpTProxyChain               = "ANTIMAGE_PPTP_TPROXY"
	pptpNATChain                  = "ANTIMAGE_PPTP_NAT"
)

func buildPPTPTProxySpec(inbound pptpRuntimeInbound) (openVPNTProxySpec, error) {
	spec := openVPNTProxySpec{
		Enabled:  openVPNBoolSetting(inbound.Settings, "tproxy_enabled", true),
		Mark:     pptpTProxyMark,
		Mask:     pptpTProxyMask,
		Table:    pptpTProxyTable,
		Priority: pptpTProxyRulePriority,
	}
	if !spec.Enabled {
		return spec, nil
	}
	if strings.TrimSpace(inbound.Tag) == "" {
		return spec, fmt.Errorf("pptp tproxy: inbound tag is required")
	}
	if inbound.TunnelPort <= 0 {
		inbound.TunnelPort = defaultPPTPTunnel
	}
	if inbound.TunnelPort != defaultPPTPTunnel {
		return spec, fmt.Errorf("pptp %q: tunnel port must be %d", inbound.Tag, defaultPPTPTunnel)
	}
	prefix, err := pptpPoolPrefix(inbound)
	if err != nil {
		return spec, err
	}
	spec.Interface = "ppp+"
	spec.SourceCIDR = prefix.String()
	spec.TunnelPort = inbound.TunnelPort
	return spec, nil
}

func buildPPTPNATSpec(inbound pptpRuntimeInbound) (openVPNNATSpec, error) {
	prefix, err := pptpPoolPrefix(inbound)
	if err != nil {
		return openVPNNATSpec{}, fmt.Errorf("pptp %q NAT: %w", inbound.Tag, err)
	}
	return openVPNNATSpec{Enabled: true, SourceCIDR: prefix.String()}, nil
}

func (s *Server) applyPPTPTProxy(tag string, spec openVPNTProxySpec) error {
	return s.applyTaggedTProxy(tag, spec, &s.pptpTProxySpecs, pptpTProxyChain, "pptp")
}

func (s *Server) removePPTPTProxyForTag(tag string) error {
	return s.removeTaggedTProxyForTag(tag, &s.pptpTProxySpecs, pptpTProxyChain)
}

func (s *Server) stopRemovedPPTPTProxySpecs(desired map[string]struct{}) {
	s.stopRemovedTaggedTProxySpecs(desired, &s.pptpTProxySpecs, pptpTProxyChain, "pptp")
}

func (s *Server) stopAllPPTPTProxySpecs() {
	s.stopAllTaggedTProxySpecs(&s.pptpTProxySpecs, pptpTProxyChain, "pptp")
}

func (s *Server) applyPPTPNAT(tag string, spec openVPNNATSpec) error {
	return s.applyTaggedNAT(tag, spec, &s.pptpNATSpecs, pptpNATChain, "pptp")
}

func (s *Server) removePPTPNATForTag(tag string) error {
	return s.removeTaggedNATForTag(tag, &s.pptpNATSpecs, pptpNATChain)
}

func (s *Server) stopRemovedPPTPNATSpecs(desired map[string]struct{}) {
	s.stopRemovedTaggedNATSpecs(desired, &s.pptpNATSpecs, pptpNATChain, "pptp")
}

func (s *Server) stopAllPPTPNATSpecs() {
	s.stopAllTaggedNATSpecs(&s.pptpNATSpecs, pptpNATChain, "pptp")
}

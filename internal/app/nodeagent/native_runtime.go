package nodeagent

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

type nativeRuntimeSessionCallback struct {
	URL    string `json:"url,omitempty"`
	Token  string `json:"token,omitempty"`
	NodeID int64  `json:"node_id,omitempty"`
}

type nativeRuntimePayload struct {
	GeneratedAt     string                       `json:"generated_at"`
	Target          string                       `json:"target"`
	SessionCallback nativeRuntimeSessionCallback `json:"session_callback,omitempty"`

	OpenVPNInbounds    []openVPNRuntimeInbound   `json:"inbounds"`
	L2TPInbounds       []l2TPRuntimeInbound      `json:"l2tp_inbounds"`
	PPTPInbounds       []pptpRuntimeInbound      `json:"pptp_inbounds"`
	WireGuardInbounds  []wireGuardRuntimeInbound `json:"wg_inbounds"`
	AmneziaWGInbounds  []amneziaWGRuntimeInbound `json:"awg_inbounds"`
	IKEv2Inbounds      []json.RawMessage         `json:"ikev2_inbounds"`
	AnyConnectInbounds []json.RawMessage         `json:"anyconnect_inbounds"`

	HAProxy json.RawMessage `json:"haproxy"`
}

type openVPNRuntimeInbound struct {
	Tag        string               `json:"tag"`
	TunnelTag  string               `json:"tunnel_tag"`
	Port       int                  `json:"port"`
	Transport  string               `json:"transport"`
	TunnelPort int                  `json:"tunnel_port"`
	Settings   map[string]any       `json:"settings"`
	Users      []openVPNRuntimeUser `json:"users"`
}

type openVPNRuntimeUser struct {
	UserID      int64  `json:"user_id"`
	Username    string `json:"username"`
	VPNUsername string `json:"vpn_username"`
	Password    string `json:"password"`
	IPv4Address string `json:"ipv4_address"`
	Status      string `json:"status"`
	UsedTraffic int64  `json:"used_traffic"`
	DataLimit   *int64 `json:"data_limit,omitempty"`
	Expire      *int64 `json:"expire,omitempty"`
	DeviceLimit int64  `json:"device_limit,omitempty"`
}

type preparedOpenVPNRuntime struct {
	Tag        string
	ConfigPath string
	TProxy     openVPNTProxySpec
	NAT        openVPNNATSpec
}

type preparedL2TPRuntime struct {
	Tag          string
	IPSecConfig  string
	IPSecSecrets string
	XL2TPConfig  string
	CHAPSecrets  string
	TProxy       openVPNTProxySpec
	NAT          openVPNNATSpec
}

type preparedPPTPRuntime struct {
	Tag         string
	ConfigPath  string
	CHAPSecrets string
	TProxy      openVPNTProxySpec
	NAT         openVPNNATSpec
}

func parseNativeRuntimePayload(raw string) (nativeRuntimePayload, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nativeRuntimePayload{}, nil
	}
	var payload nativeRuntimePayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nativeRuntimePayload{}, fmt.Errorf(
			"parse native runtime payload: %w",
			err,
		)
	}
	return payload, nil
}

func (s *Server) applyNativeRuntime(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	payload, err := parseNativeRuntimePayload(raw)
	if err != nil {
		return err
	}

	wgDesired := make(
		map[string]preparedWireGuardRuntime,
		len(payload.WireGuardInbounds),
	)
	wgPrepared := make(
		[]preparedWireGuardRuntime,
		0,
		len(payload.WireGuardInbounds),
	)
	wgUsageInbounds := make(
		[]wireGuardRuntimeInbound,
		0,
		len(payload.WireGuardInbounds),
	)
	usedWGInterfaces := make(map[string]string)
	usedWGPools := make(map[string]string)

	for _, inbound := range payload.WireGuardInbounds {
		tag := strings.TrimSpace(inbound.Tag)
		if tag == "" {
			return fmt.Errorf("wireguard inbound tag is required")
		}
		if _, exists := wgDesired[tag]; exists {
			return fmt.Errorf("duplicate wireguard runtime tag %q", tag)
		}

		runtimeInbound, suppressedPeers := filterWireGuardRuntimeInboundByStaticPolicy(inbound)
		runtimeInbound = s.filterWireGuardRuntimeInboundByDynamicSuppression(
			runtimeInbound,
		)
		prepared, err := s.prepareWireGuardInbound(runtimeInbound)
		if err != nil {
			return err
		}
		prepared.SuppressedPeers = suppressedPeers
		if owner, exists := usedWGInterfaces[prepared.InterfaceName]; exists {
			return fmt.Errorf(
				"wireguard interface %q is assigned to both %q and %q",
				prepared.InterfaceName,
				owner,
				tag,
			)
		}
		usedWGInterfaces[prepared.InterfaceName] = tag

		pool, err := netip.ParsePrefix(prepared.SourceCIDR)
		if err != nil {
			return fmt.Errorf(
				"wireguard %q: invalid prepared source pool %q: %w",
				tag,
				prepared.SourceCIDR,
				err,
			)
		}
		pool = pool.Masked()
		for rawOtherPool, owner := range usedWGPools {
			otherPool, parseErr := netip.ParsePrefix(rawOtherPool)
			if parseErr != nil {
				return fmt.Errorf(
					"wireguard %q: invalid existing source pool %q: %w",
					owner,
					rawOtherPool,
					parseErr,
				)
			}
			otherPool = otherPool.Masked()
			if pool.Contains(otherPool.Addr()) ||
				otherPool.Contains(pool.Addr()) {
				return fmt.Errorf(
					"wireguard address pool %q for %q overlaps pool %q used by %q",
					pool,
					tag,
					otherPool,
					owner,
				)
			}
		}
		usedWGPools[pool.String()] = tag
		wgDesired[tag] = prepared
		wgPrepared = append(wgPrepared, prepared)
		wgUsageInbounds = append(
			wgUsageInbounds,
			cloneWireGuardRuntimeInboundWithInterface(
				inbound,
				prepared.InterfaceName,
			),
		)
	}

	awgDesired := make(map[string]preparedAmneziaWGRuntime, len(payload.AmneziaWGInbounds))
	awgPrepared := make([]preparedAmneziaWGRuntime, 0, len(payload.AmneziaWGInbounds))
	usedListenPorts := make(map[int]string, len(payload.WireGuardInbounds)+len(payload.AmneziaWGInbounds))
	for _, inbound := range payload.WireGuardInbounds {
		usedListenPorts[inbound.ListenPort] = "wireguard:" + strings.TrimSpace(inbound.Tag)
	}
	for _, inbound := range payload.AmneziaWGInbounds {
		inbound = filterAmneziaWGRuntimeInboundByPolicy(inbound, time.Now())
		tag := strings.TrimSpace(inbound.Tag)
		if _, exists := awgDesired[tag]; exists {
			return fmt.Errorf("duplicate amneziawg runtime tag %q", tag)
		}
		if owner, exists := usedListenPorts[inbound.ListenPort]; exists {
			return fmt.Errorf("amneziawg listen port %d for %q conflicts with %s", inbound.ListenPort, tag, owner)
		}
		prepared, err := s.prepareAmneziaWGInbound(inbound, payload.SessionCallback)
		if err != nil {
			return err
		}
		if owner, exists := usedWGInterfaces[prepared.InterfaceName]; exists {
			return fmt.Errorf("amneziawg interface %q for %q conflicts with wireguard %q", prepared.InterfaceName, tag, owner)
		}
		pool, _ := netip.ParsePrefix(prepared.SourceCIDR)
		for rawOtherPool, owner := range usedWGPools {
			otherPool, _ := netip.ParsePrefix(rawOtherPool)
			if pool.Contains(otherPool.Addr()) || otherPool.Contains(pool.Addr()) {
				return fmt.Errorf("amneziawg address pool %q for %q overlaps wireguard pool %q used by %q", pool, tag, otherPool, owner)
			}
		}
		for _, other := range awgPrepared {
			otherPool, _ := netip.ParsePrefix(other.SourceCIDR)
			if pool.Contains(otherPool.Addr()) || otherPool.Contains(pool.Addr()) {
				return fmt.Errorf("amneziawg address pool %q for %q overlaps pool %q used by %q", pool, tag, otherPool, other.Tag)
			}
		}
		usedListenPorts[inbound.ListenPort] = "amneziawg:" + tag
		awgDesired[tag] = prepared
		awgPrepared = append(awgPrepared, prepared)
	}

	ovDesired := make(
		map[string]struct{},
		len(payload.OpenVPNInbounds),
	)
	ovPrepared := make(
		[]preparedOpenVPNRuntime,
		0,
		len(payload.OpenVPNInbounds),
	)

	for _, inbound := range payload.OpenVPNInbounds {
		tag := strings.TrimSpace(inbound.Tag)
		if tag == "" {
			return fmt.Errorf("openvpn inbound tag is required")
		}
		if _, exists := ovDesired[tag]; exists {
			return fmt.Errorf("duplicate openvpn runtime tag %q", tag)
		}

		configPath, err := s.prepareOpenVPNInbound(
			inbound,
			payload.SessionCallback,
		)
		if err != nil {
			return err
		}
		tproxy, err := buildOpenVPNTProxySpec(inbound)
		if err != nil {
			return err
		}
		nat, err := buildOpenVPNNATSpec(inbound)
		if err != nil {
			return err
		}

		ovDesired[tag] = struct{}{}
		ovPrepared = append(
			ovPrepared,
			preparedOpenVPNRuntime{
				Tag:        tag,
				ConfigPath: configPath,
				TProxy:     tproxy,
				NAT:        nat,
			},
		)
	}

	l2tpDesired := make(
		map[string]struct{},
		len(payload.L2TPInbounds),
	)
	l2tpPrepared := make(
		[]preparedL2TPRuntime,
		0,
		len(payload.L2TPInbounds),
	)

	for _, inbound := range payload.L2TPInbounds {
		tag := strings.TrimSpace(inbound.Tag)
		if tag == "" {
			return fmt.Errorf("l2tp inbound tag is required")
		}
		if _, exists := l2tpDesired[tag]; exists {
			return fmt.Errorf("duplicate l2tp runtime tag %q", tag)
		}
		if len(l2tpDesired) > 0 {
			return fmt.Errorf("only one l2tp runtime inbound is supported")
		}

		files, err := s.prepareL2TPInbound(inbound, payload.SessionCallback)
		if err != nil {
			return err
		}
		tproxy, err := buildL2TPTProxySpec(inbound)
		if err != nil {
			return err
		}
		nat, err := buildL2TPNATSpec(inbound)
		if err != nil {
			return err
		}

		l2tpDesired[tag] = struct{}{}
		l2tpPrepared = append(
			l2tpPrepared,
			preparedL2TPRuntime{
				Tag:          tag,
				IPSecConfig:  files.IPSecConfig,
				IPSecSecrets: files.IPSecSecrets,
				XL2TPConfig:  files.XL2TPConfig,
				CHAPSecrets:  files.CHAPSecrets,
				TProxy:       tproxy,
				NAT:          nat,
			},
		)
	}

	pptpDesired := make(
		map[string]struct{},
		len(payload.PPTPInbounds),
	)
	pptpPrepared := make(
		[]preparedPPTPRuntime,
		0,
		len(payload.PPTPInbounds),
	)

	for _, inbound := range payload.PPTPInbounds {
		tag := strings.TrimSpace(inbound.Tag)
		if tag == "" {
			return fmt.Errorf("pptp inbound tag is required")
		}
		if _, exists := pptpDesired[tag]; exists {
			return fmt.Errorf("duplicate pptp runtime tag %q", tag)
		}
		if len(pptpDesired) > 0 {
			return fmt.Errorf("only one pptp runtime inbound is supported")
		}

		configPath, err := s.preparePPTPInbound(inbound, payload.SessionCallback)
		if err != nil {
			return err
		}
		tproxy, err := buildPPTPTProxySpec(inbound)
		if err != nil {
			return err
		}
		nat, err := buildPPTPNATSpec(inbound)
		if err != nil {
			return err
		}

		pptpDesired[tag] = struct{}{}
		pptpPrepared = append(
			pptpPrepared,
			preparedPPTPRuntime{
				Tag:         tag,
				ConfigPath:  configPath,
				CHAPSecrets: renderPPTPCHAPSecrets(inbound.Users),
				TProxy:      tproxy,
				NAT:         nat,
			},
		)
	}

	if err := s.preflightWireGuardRuntimes(wgPrepared); err != nil {
		return err
	}
	if err := s.preflightAmneziaWGRuntimes(awgPrepared); err != nil {
		return err
	}
	if err := preflightOpenVPNRuntimes(ovPrepared); err != nil {
		return err
	}
	if err := preflightL2TPRuntimes(l2tpPrepared); err != nil {
		return err
	}
	if err := preflightPPTPRuntimes(pptpPrepared); err != nil {
		return err
	}
	if len(payload.IKEv2Inbounds) > 0 ||
		len(payload.AnyConnectInbounds) > 0 {
		return fmt.Errorf(
			"native runtime contains unsupported daemon inbounds: ikev2=%d anyconnect=%d",
			len(payload.IKEv2Inbounds),
			len(payload.AnyConnectInbounds),
		)
	}

	if err := s.syncWireGuardUsageConfigs(
		wgUsageInbounds,
		payload.SessionCallback,
	); err != nil {
		return err
	}

	s.stopRemovedWireGuardRuntimes(wgDesired)
	s.stopRemovedAmneziaWGRuntimes(awgDesired)
	s.stopRemovedOpenVPNRuntimes(ovDesired)
	s.stopRemovedOpenVPNTProxySpecs(ovDesired)
	s.stopRemovedOpenVPNNATSpecs(ovDesired)
	s.stopRemovedL2TPRuntimes(l2tpDesired)
	s.stopRemovedL2TPTProxySpecs(l2tpDesired)
	s.stopRemovedL2TPNATSpecs(l2tpDesired)
	s.stopRemovedPPTPRuntimes(pptpDesired)
	s.stopRemovedPPTPTProxySpecs(pptpDesired)
	s.stopRemovedPPTPNATSpecs(pptpDesired)
	if len(pptpDesired) == 0 {
		if err := clearPPTPSystemCHAPSecrets(); err != nil {
			s.appendLog("clear PPTP chap secrets failed: " + err.Error())
		}
	}

	for _, runtime := range wgPrepared {
		if err := s.applyWireGuardRuntime(runtime); err != nil {
			return err
		}
	}
	for _, runtime := range awgPrepared {
		if err := s.applyAmneziaWGRuntime(runtime); err != nil {
			return err
		}
	}

	routingPrepared := append([]preparedWireGuardRuntime(nil), wgPrepared...)
	for _, runtime := range awgPrepared {
		routingPrepared = append(routingPrepared, preparedWireGuardRuntime{Tag: "amneziawg:" + runtime.Tag, InterfaceName: runtime.InterfaceName, SourceCIDR: runtime.SourceCIDR, Routing: runtime.Routing})
	}
	if err := s.reconcileWireGuardRouting(routingPrepared); err != nil {
		return err
	}

	for _, runtime := range ovPrepared {
		if err := s.applyOpenVPNTProxy(runtime.Tag, runtime.TProxy); err != nil {
			return err
		}
		if err := s.applyOpenVPNNAT(runtime.Tag, runtime.NAT); err != nil {
			_ = s.removeOpenVPNTProxyForTag(runtime.Tag)
			return err
		}
		if err := s.startOpenVPNInbound(
			runtime.Tag,
			runtime.ConfigPath,
		); err != nil {
			_ = s.removeOpenVPNTProxyForTag(runtime.Tag)
			_ = s.removeOpenVPNNATForTag(runtime.Tag)
			return err
		}
	}

	for _, runtime := range l2tpPrepared {
		if err := s.applyL2TPTProxy(runtime.Tag, runtime.TProxy); err != nil {
			return err
		}
		if err := s.applyL2TPNAT(runtime.Tag, runtime.NAT); err != nil {
			_ = s.removeL2TPTProxyForTag(runtime.Tag)
			return err
		}
		if err := s.startL2TPInbound(
			runtime.Tag,
			runtime.IPSecConfig,
			runtime.IPSecSecrets,
			runtime.XL2TPConfig,
			runtime.CHAPSecrets,
		); err != nil {
			_ = s.removeL2TPTProxyForTag(runtime.Tag)
			_ = s.removeL2TPNATForTag(runtime.Tag)
			return err
		}
	}

	pptpCHAPSecrets := renderPPTPSystemCHAPSecrets(pptpPrepared)
	for _, runtime := range pptpPrepared {
		if err := s.applyPPTPTProxy(runtime.Tag, runtime.TProxy); err != nil {
			return err
		}
		if err := s.applyPPTPNAT(runtime.Tag, runtime.NAT); err != nil {
			_ = s.removePPTPTProxyForTag(runtime.Tag)
			return err
		}
		if err := s.startPPTPInbound(runtime.Tag, runtime.ConfigPath, pptpCHAPSecrets); err != nil {
			_ = s.removePPTPTProxyForTag(runtime.Tag)
			_ = s.removePPTPNATForTag(runtime.Tag)
			return err
		}
	}

	if len(ovPrepared) > 0 || len(wgPrepared) > 0 || len(awgPrepared) > 0 || len(l2tpPrepared) > 0 || len(pptpPrepared) > 0 {
		s.appendLog(fmt.Sprintf(
			"native runtime applied: openvpn=%d wireguard=%d amneziawg=%d l2tp=%d pptp=%d",
			len(ovPrepared),
			len(wgPrepared),
			len(awgPrepared),
			len(l2tpPrepared),
			len(pptpPrepared),
		))
	}
	return nil
}

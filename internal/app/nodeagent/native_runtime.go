package nodeagent

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
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
	L2TPInbounds       []json.RawMessage         `json:"l2tp_inbounds"`
	PPTPInbounds       []json.RawMessage         `json:"pptp_inbounds"`
	WireGuardInbounds  []wireGuardRuntimeInbound `json:"wg_inbounds"`
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

		prepared, err := s.prepareWireGuardInbound(inbound)
		if err != nil {
			return err
		}
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

		ovDesired[tag] = struct{}{}
		ovPrepared = append(
			ovPrepared,
			preparedOpenVPNRuntime{
				Tag:        tag,
				ConfigPath: configPath,
				TProxy:     tproxy,
			},
		)
	}

	if err := s.preflightWireGuardRuntimes(wgPrepared); err != nil {
		return err
	}

	if err := s.syncWireGuardUsageConfigs(wgUsageInbounds); err != nil {
		return err
	}

	s.stopRemovedWireGuardRuntimes(wgDesired)
	s.stopRemovedOpenVPNRuntimes(ovDesired)
	s.stopRemovedOpenVPNTProxySpecs(ovDesired)

	for _, runtime := range wgPrepared {
		if err := s.applyWireGuardRuntime(runtime); err != nil {
			return err
		}
	}

	if err := s.reconcileWireGuardRouting(wgPrepared); err != nil {
		return err
	}

	for _, runtime := range ovPrepared {
		if err := s.applyOpenVPNTProxy(runtime.Tag, runtime.TProxy); err != nil {
			return err
		}
		if err := s.startOpenVPNInbound(
			runtime.Tag,
			runtime.ConfigPath,
		); err != nil {
			_ = s.removeOpenVPNTProxyForTag(runtime.Tag)
			return err
		}
	}

	if len(ovPrepared) > 0 || len(wgPrepared) > 0 {
		s.appendLog(fmt.Sprintf(
			"native runtime applied: openvpn=%d wireguard=%d",
			len(ovPrepared),
			len(wgPrepared),
		))
	}
	return nil
}

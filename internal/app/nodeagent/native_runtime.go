package nodeagent

import (
	"encoding/json"
	"fmt"
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

	OpenVPNInbounds    []openVPNRuntimeInbound `json:"inbounds"`
	L2TPInbounds       []json.RawMessage       `json:"l2tp_inbounds"`
	PPTPInbounds       []json.RawMessage       `json:"pptp_inbounds"`
	WireGuardInbounds  []json.RawMessage       `json:"wg_inbounds"`
	IKEv2Inbounds      []json.RawMessage       `json:"ikev2_inbounds"`
	AnyConnectInbounds []json.RawMessage       `json:"anyconnect_inbounds"`

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

	// Older controllers may not send native runtime state.
	if raw == "" {
		return nil
	}

	payload, err := parseNativeRuntimePayload(raw)
	if err != nil {
		return err
	}

	desired := make(map[string]struct{}, len(payload.OpenVPNInbounds))
	prepared := make([]preparedOpenVPNRuntime, 0, len(payload.OpenVPNInbounds))

	// Validate and prepare all files before changing running state.
	for _, inbound := range payload.OpenVPNInbounds {
		tag := strings.TrimSpace(inbound.Tag)
		if tag == "" {
			return fmt.Errorf("openvpn inbound tag is required")
		}

		if _, exists := desired[tag]; exists {
			return fmt.Errorf("duplicate openvpn runtime tag %q", tag)
		}

		configPath, err := s.prepareOpenVPNInbound(inbound, payload.SessionCallback)
		if err != nil {
			return err
		}

		tproxy, err := buildOpenVPNTProxySpec(inbound)
		if err != nil {
			return err
		}

		desired[tag] = struct{}{}

		prepared = append(prepared, preparedOpenVPNRuntime{
			Tag:        tag,
			ConfigPath: configPath,
			TProxy:     tproxy,
		})
	}

	// Remove services and rules no longer present in desired state.
	s.stopRemovedOpenVPNRuntimes(desired)
	s.stopRemovedOpenVPNTProxySpecs(desired)

	for _, runtime := range prepared {
		if err := s.applyOpenVPNTProxy(runtime.Tag, runtime.TProxy); err != nil {
			return err
		}

		if err := s.startOpenVPNInbound(runtime.Tag, runtime.ConfigPath); err != nil {
			_ = s.removeOpenVPNTProxyForTag(runtime.Tag)
			return err
		}
	}

	if len(prepared) > 0 {
		s.appendLog(fmt.Sprintf(
			"native runtime applied: openvpn=%d",
			len(prepared),
		))
	}

	return nil
}

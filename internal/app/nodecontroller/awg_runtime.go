package nodecontroller

import (
	"context"
	"strings"
	"time"

	"github.com/antimage/antimage/internal/app/xrayconfig"
)

type AWGRuntime struct {
	GeneratedAt     string                 `json:"generated_at"`
	Target          string                 `json:"target,omitempty"`
	SessionCallback RuntimeSessionCallback `json:"session_callback,omitempty"`
	Inbounds        []AWGRuntimeInbound    `json:"inbounds"`
}

type AWGRuntimeInbound struct {
	Tag        string           `json:"tag"`
	TunnelTag  string           `json:"tunnel_tag"`
	ListenPort int              `json:"listen_port"`
	TunnelPort int              `json:"tunnel_port"`
	Settings   map[string]any   `json:"settings"`
	Peers      []AWGRuntimePeer `json:"peers"`
}

type AWGRuntimePeer struct {
	UserID       int64  `json:"user_id"`
	Username     string `json:"username"`
	DeviceIndex  int    `json:"device_index"`
	PublicKey    string `json:"public_key"`
	PresharedKey string `json:"preshared_key,omitempty"`
	Address      string `json:"address"`
}

func (r Repository) AWGRuntime(ctx context.Context, nodeID int64) (AWGRuntime, error) {
	configRepo := xrayconfig.NewRepository(r.db, r.dialect, xrayconfig.Options{})
	inbounds, err := configRepo.FullInbounds(ctx)
	if err != nil {
		return AWGRuntime{}, err
	}
	return r.awgRuntime(ctx, nodeID, inbounds)
}

func (r Repository) awgRuntime(ctx context.Context, nodeID int64, inbounds []map[string]any) (AWGRuntime, error) {
	target := xrayconfig.NodeTargetID(nodeID)
	usedPorts := map[int]struct{}{}
	for _, inbound := range inbounds {
		if port := OVIntValue(inbound["port"]); port > 0 {
			usedPorts[port] = struct{}{}
		}
	}
	result := AWGRuntime{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Target: target, Inbounds: []AWGRuntimeInbound{}}
	if callback, err := r.RuntimeSessionCallback(ctx, NodeRow{ID: nodeID}); err != nil {
		return AWGRuntime{}, err
	} else {
		result.SessionCallback = callback
	}
	for _, inbound := range inbounds {
		if strings.ToLower(OVStringValue(inbound["protocol"])) != xrayconfig.AWGProtocol || !OVInboundMatchesTarget(inbound, target) {
			continue
		}
		tag := strings.TrimSpace(OVStringValue(inbound["tag"]))
		if tag == "" {
			continue
		}
		settings := AWGRuntimeSettings(inbound)
		tunnelPort := xrayconfig.RuntimeTunnelPortForInbound(inbound, usedPorts)
		if tunnelPort > 0 {
			usedPorts[tunnelPort] = struct{}{}
		}
		result.Inbounds = append(result.Inbounds, AWGRuntimeInbound{
			Tag: tag, TunnelTag: xrayconfig.RuntimeTunnelTagForProtocol(xrayconfig.AWGProtocol, tag),
			ListenPort: OVIntValue(inbound["port"]), TunnelPort: tunnelPort,
			Settings: settings, Peers: []AWGRuntimePeer{},
		})
	}
	return result, nil
}

func AWGRuntimeSettings(inbound map[string]any) map[string]any {
	settings := OVMapValue(inbound["settings"])
	out := make(map[string]any, len(settings))
	for key, value := range settings {
		out[key] = value
	}
	delete(out, "clients")
	return out
}

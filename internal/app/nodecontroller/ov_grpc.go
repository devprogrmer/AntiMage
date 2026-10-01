package nodecontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/antimage/antimage/internal/app/xrayconfig"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func (c Controller) runtimeConfigRequest(ctx context.Context, node NodeRow, operationID string, configJSON string) (*nodev1.RuntimeConfigRequest, error) {
	inbounds, err := c.runtimeInbounds(ctx)
	if err != nil {
		return nil, fmt.Errorf("runtime inbounds: %w", err)
	}
	return c.runtimeConfigRequestFromInbounds(ctx, node, operationID, configJSON, inbounds)
}

func (c Controller) runtimeConfigRequestFromInbounds(ctx context.Context, node NodeRow, operationID string, configJSON string, inbounds []map[string]any) (*nodev1.RuntimeConfigRequest, error) {
	req := &nodev1.RuntimeConfigRequest{
		OperationId:     operationID,
		ConfigJson:      configJSON,
		DesiredRevision: operationRevision(operationID),
	}
	ovRuntime, err := c.repo.ovRuntime(ctx, node.ID, inbounds)
	if err != nil {
		return nil, fmt.Errorf("OV runtime: %w", err)
	}
	l2tpRuntime, err := c.repo.l2tpRuntime(ctx, node.ID, inbounds)
	if err != nil {
		return nil, fmt.Errorf("L2TP runtime: %w", err)
	}
	pptpRuntime, err := c.repo.pptpRuntime(ctx, node.ID, inbounds)
	if err != nil {
		return nil, fmt.Errorf("PPTP runtime: %w", err)
	}
	wgRuntime, err := c.repo.wgRuntime(ctx, node.ID, inbounds)
	if err != nil {
		return nil, fmt.Errorf("WireGuard runtime: %w", err)
	}
	awgRuntime, err := c.repo.awgRuntime(ctx, node.ID, inbounds)
	if err != nil {
		return nil, fmt.Errorf("AmneziaWG runtime: %w", err)
	}
	ikev2Runtime, err := c.repo.remoteAccessRuntimeFromInbounds(ctx, node.ID, xrayconfig.IKEv2Protocol, inbounds)
	if err != nil {
		return nil, fmt.Errorf("IKEv2 runtime: %w", err)
	}
	for index := range ikev2Runtime.Inbounds {
		if ikev2Runtime.Inbounds[index].Settings == nil {
			ikev2Runtime.Inbounds[index].Settings = map[string]any{}
		}
		ikev2Runtime.Inbounds[index].Settings["runtime_server_identity"] =
			strings.TrimSpace(node.Address)
	}
	anyConnectRuntime, err := c.repo.remoteAccessRuntimeFromInbounds(ctx, node.ID, xrayconfig.AnyConnectProtocol, inbounds)
	if err != nil {
		return nil, fmt.Errorf("AnyConnect runtime: %w", err)
	}
	applyNativeRuntimeUsageFactors(
		node.UsageCoefficient,
		inbounds,
		&ovRuntime,
		&l2tpRuntime,
		&pptpRuntime,
		&wgRuntime,
		&awgRuntime,
		&ikev2Runtime,
		&anyConnectRuntime,
	)
	haproxyRuntime, err := c.repo.HAProxyRuntimeForNode(ctx, node.ID)
	if err != nil {
		return nil, fmt.Errorf("HAProxy runtime: %w", err)
	}
	raw, err := json.Marshal(map[string]any{
		"generated_at":         ovRuntime.GeneratedAt,
		"target":               ovRuntime.Target,
		"session_callback":     ovRuntime.SessionCallback,
		"inbounds":             ovRuntime.Inbounds,
		"l2tp_inbounds":        l2tpRuntime.Inbounds,
		"l2tp_generated":       l2tpRuntime.GeneratedAt,
		"pptp_inbounds":        pptpRuntime.Inbounds,
		"pptp_generated":       pptpRuntime.GeneratedAt,
		"wg_inbounds":          wgRuntime.Inbounds,
		"wg_generated":         wgRuntime.GeneratedAt,
		"awg_inbounds":         awgRuntime.Inbounds,
		"awg_generated":        awgRuntime.GeneratedAt,
		"ikev2_inbounds":       ikev2Runtime.Inbounds,
		"ikev2_generated":      ikev2Runtime.GeneratedAt,
		"anyconnect_inbounds":  anyConnectRuntime.Inbounds,
		"anyconnect_generated": anyConnectRuntime.GeneratedAt,
		"haproxy":              haproxyRuntime,
	})
	if err != nil {
		return nil, fmt.Errorf("VPN runtime: %w", err)
	}
	req.OvRuntimeJson = string(raw)
	return req, nil
}

func applyNativeRuntimeUsageFactors(
	nodeCoefficient float64,
	sourceInbounds []map[string]any,
	ovRuntime *OVRuntime,
	l2tpRuntime *L2TPRuntime,
	pptpRuntime *PPTPRuntime,
	wgRuntime *WGRuntime,
	awgRuntime *AWGRuntime,
	ikev2Runtime *RemoteAccessRuntime,
	anyConnectRuntime *RemoteAccessRuntime,
) {
	nodeCoefficient = normalizeUsageFactor(nodeCoefficient)
	inboundCoefficients := runtimeInboundUsageCoefficients(sourceInbounds)
	for inboundIndex := range ovRuntime.Inbounds {
		coefficient := inboundCoefficients[ovRuntime.Inbounds[inboundIndex].Tag]
		for userIndex := range ovRuntime.Inbounds[inboundIndex].Users {
			ovRuntime.Inbounds[inboundIndex].Users[userIndex].UsageCoefficient = nodeCoefficient
			ovRuntime.Inbounds[inboundIndex].Users[userIndex].InboundCoefficient = coefficient
		}
	}
	for inboundIndex := range l2tpRuntime.Inbounds {
		coefficient := inboundCoefficients[l2tpRuntime.Inbounds[inboundIndex].Tag]
		for userIndex := range l2tpRuntime.Inbounds[inboundIndex].Users {
			l2tpRuntime.Inbounds[inboundIndex].Users[userIndex].UsageCoefficient = nodeCoefficient
			l2tpRuntime.Inbounds[inboundIndex].Users[userIndex].InboundCoefficient = coefficient
		}
	}
	for inboundIndex := range pptpRuntime.Inbounds {
		coefficient := inboundCoefficients[pptpRuntime.Inbounds[inboundIndex].Tag]
		for userIndex := range pptpRuntime.Inbounds[inboundIndex].Users {
			pptpRuntime.Inbounds[inboundIndex].Users[userIndex].UsageCoefficient = nodeCoefficient
			pptpRuntime.Inbounds[inboundIndex].Users[userIndex].InboundCoefficient = coefficient
		}
	}
	for inboundIndex := range wgRuntime.Inbounds {
		coefficient := inboundCoefficients[wgRuntime.Inbounds[inboundIndex].Tag]
		for peerIndex := range wgRuntime.Inbounds[inboundIndex].Peers {
			wgRuntime.Inbounds[inboundIndex].Peers[peerIndex].UsageCoefficient = nodeCoefficient
			wgRuntime.Inbounds[inboundIndex].Peers[peerIndex].InboundCoefficient = coefficient
		}
	}
	for inboundIndex := range awgRuntime.Inbounds {
		coefficient := inboundCoefficients[awgRuntime.Inbounds[inboundIndex].Tag]
		for peerIndex := range awgRuntime.Inbounds[inboundIndex].Peers {
			awgRuntime.Inbounds[inboundIndex].Peers[peerIndex].UsageCoefficient = nodeCoefficient
			awgRuntime.Inbounds[inboundIndex].Peers[peerIndex].InboundCoefficient = coefficient
		}
	}
	for inboundIndex := range ikev2Runtime.Inbounds {
		coefficient := inboundCoefficients[ikev2Runtime.Inbounds[inboundIndex].Tag]
		for userIndex := range ikev2Runtime.Inbounds[inboundIndex].Users {
			ikev2Runtime.Inbounds[inboundIndex].Users[userIndex].UsageCoefficient = nodeCoefficient
			ikev2Runtime.Inbounds[inboundIndex].Users[userIndex].InboundCoefficient = coefficient
		}
	}
	for inboundIndex := range anyConnectRuntime.Inbounds {
		coefficient := inboundCoefficients[anyConnectRuntime.Inbounds[inboundIndex].Tag]
		for userIndex := range anyConnectRuntime.Inbounds[inboundIndex].Users {
			anyConnectRuntime.Inbounds[inboundIndex].Users[userIndex].UsageCoefficient = nodeCoefficient
			anyConnectRuntime.Inbounds[inboundIndex].Users[userIndex].InboundCoefficient = coefficient
		}
	}
}

func runtimeInboundUsageCoefficients(inbounds []map[string]any) map[string]float64 {
	result := make(map[string]float64, len(inbounds))
	for _, inbound := range inbounds {
		tag := strings.TrimSpace(OVStringValue(inbound["tag"]))
		if tag == "" {
			continue
		}
		result[tag] = normalizeUsageFactor(OVFloatValue(inbound["usage_coefficient"]))
	}
	return result
}

func OVFloatValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	case string:
		parsed, _ := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed
	default:
		return 0
	}
}

func operationRevision(operationID string) uint64 {
	parts := strings.Split(operationID, "-")
	for index := len(parts) - 1; index >= 0; index-- {
		if revision, err := strconv.ParseUint(parts[index], 10, 64); err == nil {
			return revision
		}
	}
	return 0
}

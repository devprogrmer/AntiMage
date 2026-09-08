package nodeagent

import (
	"encoding/json"
	"fmt"
	"strings"
)

func buildRouteProbeConfig(
	rawConfig string,
	inboundTag string,
	socksPort int,
	apiPort int,
) ([]byte, string, error) {
	var config map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawConfig)), &config); err != nil {
		return nil, "", fmt.Errorf("invalid Xray config: %w", err)
	}

	outbounds, ok := config["outbounds"].([]any)
	if !ok || len(outbounds) == 0 {
		return nil, "", fmt.Errorf("Xray config has no outbounds")
	}

	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" {
		inboundTag = "antimage-route-probe"
	}

	apiTag := fmt.Sprintf("antimage-route-api-%d", apiPort)
	apiInboundTag := apiTag + "-in"

	config["inbounds"] = []any{
		map[string]any{
			"tag":      inboundTag,
			"listen":   "127.0.0.1",
			"port":     socksPort,
			"protocol": "socks",
			"settings": map[string]any{
				"auth": "noauth",
				"udp":  false,
			},
			"sniffing": map[string]any{
				"enabled":      true,
				"destOverride": []string{"http", "tls", "quic"},
				"routeOnly":    true,
			},
		},
		map[string]any{
			"tag":      apiInboundTag,
			"listen":   "127.0.0.1",
			"port":     apiPort,
			"protocol": "dokodemo-door",
			"settings": map[string]any{
				"address": "127.0.0.1",
			},
		},
	}

	config["api"] = map[string]any{
		"tag":      apiTag,
		"services": []string{"StatsService"},
	}
	config["stats"] = map[string]any{}

	policy := objectValue(config["policy"])
	system := objectValue(policy["system"])
	system["statsOutboundUplink"] = true
	system["statsOutboundDownlink"] = true
	policy["system"] = system
	config["policy"] = policy

	routing := objectValue(config["routing"])

	var rules []any
	if existing, ok := routing["rules"].([]any); ok {
		rules = append(rules, existing...)
	}

	apiRule := map[string]any{
		"type":        "field",
		"inboundTag":  []string{apiInboundTag},
		"outboundTag": apiTag,
	}

	routing["rules"] = append([]any{apiRule}, rules...)
	config["routing"] = routing

	raw, err := json.Marshal(config)
	if err != nil {
		return nil, "", fmt.Errorf("encode route probe config: %w", err)
	}

	return raw, apiTag, nil
}

package nodeagent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type xrayRuntimeConfigPortView struct {
	Inbounds []struct {
		Tag  string          `json:"tag"`
		Port json.RawMessage `json:"port"`
	} `json:"inbounds"`
}

// xrayAPIPortFromRuntimeConfig extracts the API_INBOUND port from the exact
// runtime configuration that will be used to start Xray.
//
// The master injects API_INBOUND from nodes.api_port, so once a runtime config
// is received this value is authoritative. The node's XRAY_API_PORT remains
// only a bootstrap/legacy fallback for configurations without API_INBOUND.
func xrayAPIPortFromRuntimeConfig(configJSON string) (int, bool, error) {
	var config xrayRuntimeConfigPortView
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return 0, false, fmt.Errorf("decode runtime config: %w", err)
	}

	for _, inbound := range config.Inbounds {
		if strings.TrimSpace(inbound.Tag) != "API_INBOUND" {
			continue
		}

		port, err := parseRuntimeXrayAPIPort(inbound.Port)
		if err != nil {
			return 0, false, fmt.Errorf("API_INBOUND port: %w", err)
		}

		return port, true, nil
	}

	return 0, false, nil
}

func parseRuntimeXrayAPIPort(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("port is missing")
	}

	var numericPort int
	if err := json.Unmarshal(raw, &numericPort); err == nil {
		return validateRuntimeXrayAPIPort(numericPort)
	}

	var textPort string
	if err := json.Unmarshal(raw, &textPort); err != nil {
		return 0, fmt.Errorf("port must be an integer or numeric string")
	}

	value, err := strconv.Atoi(strings.TrimSpace(textPort))
	if err != nil {
		return 0, fmt.Errorf("invalid port %q", textPort)
	}

	return validateRuntimeXrayAPIPort(value)
}

func validateRuntimeXrayAPIPort(port int) (int, error) {
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("port %d is outside 1..65535", port)
	}
	return port, nil
}

func (s *Server) syncXrayAPIPortFromRuntimeConfig(configJSON string) (int, bool, error) {
	port, found, err := xrayAPIPortFromRuntimeConfig(configJSON)
	if err != nil {
		return 0, found, err
	}

	effectivePort := port
	if !found {
		// Do not retain a port inherited from an older runtime config.
		// Restore the node's immutable bootstrap/environment fallback.
		effectivePort = s.xrayAPIPortFallback
	}

	s.mu.Lock()
	s.cfg.XrayAPIPort = effectivePort
	s.mu.Unlock()

	return effectivePort, found, nil
}

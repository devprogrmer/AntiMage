package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/antimage/antimage/internal/app/nodecontroller"
)

var torCountryPattern = regexp.MustCompile(`^[a-zA-Z]{2}$`)

const torProxyBatchLimit = 20

type torProxyProfile struct {
	Country string
	Port    uint32
	Tag     string
}

func (s *Server) handleTorProxySetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var payload map[string]any
	if err := decodeOptionalJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	target := firstNonEmpty(stringFromAny(payload["target_id"]), stringFromAny(payload["target"]))
	nodeID, isNode, err := nodeIDFromTarget(target, stringFromAny(payload["node_id"]))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	profiles, err := torProfilesFromPayload(payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	configTarget := "master"
	if isNode {
		configTarget = fmt.Sprintf("node:%d", nodeID)
	}
	config, err := s.configRepo.GetTargetRawConfig(r.Context(), configTarget)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	if duplicateTag := duplicateTorOutboundTag(config, profiles); duplicateTag != "" {
		writeError(w, http.StatusConflict, fmt.Sprintf("outbound tag already exists: %s", duplicateTag))
		return
	}

	nodeIDs := []int64{nodeID}
	if !isNode {
		nodes, err := s.nodeController.ConnectedNodeIDs(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		nodeIDs = nodes
	}
	if len(nodeIDs) == 0 {
		writeError(w, http.StatusBadRequest, "no connected nodes found for Tor proxy setup")
		return
	}
	strict := boolFromAny(payload["strict"], true)
	outbounds := make([]map[string]any, 0, len(profiles))
	for _, profile := range profiles {
		outbounds = append(outbounds, torOutbound(profile))
	}
	nodeIDs = append([]int64(nil), nodeIDs...)
	queued := 0
	for _, profile := range profiles {
		for _, nodeID := range nodeIDs {
			if err := s.nodeController.QueueTorProxy(r.Context(), nodecontroller.Request{
				NodeID:         nodeID,
				TorSocksPort:   profile.Port,
				TorExitCountry: profile.Country,
				TorStrictExit:  strict,
			}); err != nil {
				writeError(w, http.StatusBadGateway, err.Error())
				return
			}
			queued++
		}
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"success": true,
		"obj": map[string]any{
			"message":   fmt.Sprintf("%d Tor setup operation(s) queued on %d node(s); the outbounds are ready to save", queued, len(nodeIDs)),
			"outbound":  outbounds[0],
			"outbounds": outbounds,
		},
	})
}

func duplicateTorOutboundTag(config map[string]any, profiles []torProxyProfile) string {
	existingTags := make(map[string]struct{})
	for _, outbound := range outboundMaps(config["outbounds"]) {
		existingTags[strings.TrimSpace(stringFromAny(outbound["tag"]))] = struct{}{}
	}
	for _, profile := range profiles {
		if _, exists := existingTags[profile.Tag]; exists {
			return profile.Tag
		}
	}
	return ""
}

func torProfilesFromPayload(payload map[string]any) ([]torProxyProfile, error) {
	countries, isBatch, err := torCountriesFromPayload(payload)
	if err != nil {
		return nil, err
	}
	if len(countries) > torProxyBatchLimit {
		return nil, fmt.Errorf("at most %d Tor locations can be configured at once", torProxyBatchLimit)
	}

	startValue := payload["start_port"]
	if startValue == nil {
		startValue = payload["port"]
	}
	startPort, err := uint32FromAny(startValue)
	if err != nil || startPort < 1024 || startPort > 65535 {
		return nil, fmt.Errorf("port must be between 1024 and 65535")
	}
	step := uint32(1)
	if payload["port_step"] != nil {
		step, err = uint32FromAny(payload["port_step"])
		if err != nil || step == 0 || step > 1000 {
			return nil, fmt.Errorf("port step must be between 1 and 1000")
		}
	}
	direction := strings.ToLower(strings.TrimSpace(stringFromAny(payload["direction"])))
	if direction == "" {
		direction = "up"
	}
	if direction != "up" && direction != "down" {
		return nil, fmt.Errorf("port direction must be up or down")
	}
	tagPrefix := strings.TrimSpace(stringFromAny(payload["tag_prefix"]))
	if tagPrefix == "" {
		tagPrefix = "tor"
	}
	legacyTag := strings.TrimSpace(stringFromAny(payload["tag"]))

	profiles := make([]torProxyProfile, 0, len(countries))
	for index, country := range countries {
		port := int64(startPort)
		offset := int64(index) * int64(step)
		if direction == "down" {
			port -= offset
		} else {
			port += offset
		}
		if port < 1024 || port > 65535 {
			return nil, fmt.Errorf("generated port %d is outside the 1024-65535 range", port)
		}
		tag := tagPrefix
		if country != "" {
			tag += "-" + country
		}
		if !isBatch && len(countries) == 1 && legacyTag != "" {
			tag = legacyTag
		}
		profiles = append(profiles, torProxyProfile{Country: country, Port: uint32(port), Tag: tag})
	}
	return profiles, nil
}

func torCountriesFromPayload(payload map[string]any) ([]string, bool, error) {
	raw, isBatch := payload["locations"]
	if !isBatch {
		raw = []any{payload["country"]}
	}
	items := make([]string, 0)
	switch value := raw.(type) {
	case []any:
		for _, item := range value {
			items = append(items, splitTorLocations(stringFromAny(item))...)
		}
	case []string:
		for _, item := range value {
			items = append(items, splitTorLocations(item)...)
		}
	default:
		items = append(items, splitTorLocations(stringFromAny(value))...)
	}
	if len(items) == 0 {
		if isBatch {
			return nil, true, fmt.Errorf("at least one Tor location is required")
		}
		items = []string{""}
	}
	seen := make(map[string]struct{}, len(items))
	for index := range items {
		items[index] = strings.ToLower(strings.TrimSpace(items[index]))
		if items[index] != "" && !torCountryPattern.MatchString(items[index]) {
			return nil, isBatch, fmt.Errorf("location %q must be a two-letter ISO code", items[index])
		}
		if _, exists := seen[items[index]]; exists {
			return nil, isBatch, fmt.Errorf("location %q is duplicated", items[index])
		}
		seen[items[index]] = struct{}{}
	}
	return items, isBatch, nil
}

func splitTorLocations(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == ';'
	})
}

func torOutbound(profile torProxyProfile) map[string]any {
	return map[string]any{
		"tag":      profile.Tag,
		"protocol": "socks",
		"settings": map[string]any{
			"servers": []map[string]any{{
				"address": "127.0.0.1",
				"port":    profile.Port,
				"users":   []any{},
			}},
		},
	}
}

func boolFromAny(value any, fallback bool) bool {
	switch v := value.(type) {
	case nil:
		return fallback
	case bool:
		return v
	case string:
		text := strings.ToLower(strings.TrimSpace(v))
		if text == "" {
			return fallback
		}
		return text == "1" || text == "true" || text == "yes" || text == "on"
	default:
		return fallback
	}
}

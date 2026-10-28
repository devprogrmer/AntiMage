package nodeagent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

const (
	openVPNMutationUserIDField = "openvpn_user_id"
	openVPNMutationOpField     = "openvpn_operation"
	openVPNMutationUserField   = "openvpn_user_json"
	openVPNRemoveUserPrefix    = "openvpn-user:"
)

func planOpenVPNUserPolicyMutation(raw, inboundTag string, userID int64, operation string, desired *openVPNRuntimeUser) (string, bool, error) {
	if userID <= 0 {
		return raw, false, fmt.Errorf("OpenVPN user id must be positive")
	}
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" {
		return raw, false, fmt.Errorf("OpenVPN inbound tag is required")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "add" && operation != "update" && operation != "remove" {
		return raw, false, fmt.Errorf("unsupported OpenVPN user operation %q", operation)
	}

	payload, err := parseNativeRuntimePayload(raw)
	if err != nil {
		return raw, false, fmt.Errorf("read committed native policy: %w", err)
	}
	matched := -1
	for i := range payload.OpenVPNInbounds {
		if strings.TrimSpace(payload.OpenVPNInbounds[i].Tag) != inboundTag {
			continue
		}
		if matched >= 0 {
			return raw, false, fmt.Errorf("OpenVPN inbound tag %q is ambiguous", inboundTag)
		}
		matched = i
	}
	if matched < 0 {
		return raw, false, fmt.Errorf("OpenVPN inbound %q is not in committed policy", inboundTag)
	}
	inbound := payload.OpenVPNInbounds[matched]
	userIndex := -1
	for i := range inbound.Users {
		if inbound.Users[i].UserID == userID {
			if userIndex >= 0 {
				return raw, false, fmt.Errorf("OpenVPN user %d occurs more than once in inbound policy", userID)
			}
			userIndex = i
		}
	}
	if operation == "remove" {
		if desired != nil {
			return raw, false, fmt.Errorf("OpenVPN remove mutation must not include desired user")
		}
		if userIndex < 0 {
			return raw, false, nil
		}
		inbound.Users = append(inbound.Users[:userIndex], inbound.Users[userIndex+1:]...)
	} else {
		if desired == nil || desired.UserID != userID || strings.TrimSpace(desired.VPNUsername) == "" || strings.TrimSpace(desired.Password) == "" {
			return raw, false, fmt.Errorf("OpenVPN desired user identity or credentials are invalid")
		}
		for i, other := range inbound.Users {
			if i != userIndex && strings.EqualFold(strings.TrimSpace(other.VPNUsername), strings.TrimSpace(desired.VPNUsername)) {
				return raw, false, fmt.Errorf("OpenVPN VPN username %q is already assigned", desired.VPNUsername)
			}
		}
		if operation == "add" {
			if userIndex >= 0 {
				if reflect.DeepEqual(inbound.Users[userIndex], *desired) {
					return raw, false, nil
				}
				return raw, false, fmt.Errorf("OpenVPN user %d already exists with different state", userID)
			}
			inbound.Users = append(inbound.Users, *desired)
		} else {
			if userIndex < 0 {
				return raw, false, fmt.Errorf("OpenVPN user %d is absent from committed policy", userID)
			}
			if reflect.DeepEqual(inbound.Users[userIndex], *desired) {
				return raw, false, nil
			}
			inbound.Users[userIndex] = *desired
		}
	}
	payload.OpenVPNInbounds[matched] = inbound
	updated, err := json.Marshal(payload)
	if err != nil {
		return raw, false, fmt.Errorf("encode updated OpenVPN policy: %w", err)
	}
	return string(updated), true, nil
}

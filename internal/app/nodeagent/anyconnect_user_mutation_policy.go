package nodeagent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

const (
	anyConnectMutationUserIDField   = "anyconnect_user_id"
	anyConnectMutationOpField       = "anyconnect_operation"
	anyConnectMutationUserField     = "anyconnect_user_json"
	anyConnectMutationNameField     = "anyconnect_username"
	anyConnectMutationRevisionField = "anyconnect_revision"
	anyConnectRemoveUserPrefix      = "anyconnect-user:"
)

// planAnyConnectUserPolicyMutation applies a single user add/update/remove to
// the committed native runtime policy without touching any other inbound or
// user. It reports whether the policy actually changed so callers can treat a
// repeated request as an idempotent no-op.
func planAnyConnectUserPolicyMutation(raw, inboundTag string, userID int64, operation string, desired *anyConnectRuntimeUser) (string, bool, error) {
	if userID <= 0 {
		return raw, false, fmt.Errorf("AnyConnect user id must be positive")
	}
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" {
		return raw, false, fmt.Errorf("AnyConnect inbound tag is required")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "add" && operation != "update" && operation != "remove" {
		return raw, false, fmt.Errorf("unsupported AnyConnect user operation %q", operation)
	}

	payload, err := parseNativeRuntimePayload(raw)
	if err != nil {
		return raw, false, fmt.Errorf("read committed native policy: %w", err)
	}
	matched := -1
	for i := range payload.AnyConnectInbounds {
		if strings.TrimSpace(payload.AnyConnectInbounds[i].Tag) != inboundTag {
			continue
		}
		if matched >= 0 {
			return raw, false, fmt.Errorf("AnyConnect inbound tag %q is ambiguous", inboundTag)
		}
		matched = i
	}
	if matched < 0 {
		return raw, false, fmt.Errorf("AnyConnect inbound %q is not in committed policy", inboundTag)
	}
	inbound := payload.AnyConnectInbounds[matched]
	userIndex := -1
	for i := range inbound.Users {
		if inbound.Users[i].UserID == userID {
			if userIndex >= 0 {
				return raw, false, fmt.Errorf("AnyConnect user %d occurs more than once in inbound policy", userID)
			}
			userIndex = i
		}
	}
	if operation == "remove" {
		if desired != nil {
			return raw, false, fmt.Errorf("AnyConnect remove mutation must not include desired user")
		}
		if userIndex < 0 {
			return raw, false, nil
		}
		inbound.Users = append(inbound.Users[:userIndex], inbound.Users[userIndex+1:]...)
	} else {
		if desired == nil || desired.UserID != userID || strings.TrimSpace(desired.Username) == "" || desired.Password == "" {
			return raw, false, fmt.Errorf("AnyConnect desired user identity or credentials are invalid")
		}
		for i, other := range inbound.Users {
			if i != userIndex && strings.EqualFold(strings.TrimSpace(other.Username), strings.TrimSpace(desired.Username)) {
				return raw, false, fmt.Errorf("AnyConnect username %q is already assigned", desired.Username)
			}
		}
		if operation == "add" {
			if userIndex >= 0 {
				if reflect.DeepEqual(inbound.Users[userIndex], *desired) {
					return raw, false, nil
				}
				return raw, false, fmt.Errorf("AnyConnect user %d already exists with different state", userID)
			}
			inbound.Users = append(inbound.Users, *desired)
		} else {
			if userIndex < 0 {
				return raw, false, fmt.Errorf("AnyConnect user %d is absent from committed policy", userID)
			}
			if reflect.DeepEqual(inbound.Users[userIndex], *desired) {
				return raw, false, nil
			}
			inbound.Users[userIndex] = *desired
		}
	}
	payload.AnyConnectInbounds[matched] = inbound
	updated, err := json.Marshal(payload)
	if err != nil {
		return raw, false, fmt.Errorf("encode updated AnyConnect policy: %w", err)
	}
	return string(updated), true, nil
}

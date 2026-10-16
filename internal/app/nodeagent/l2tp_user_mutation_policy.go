package nodeagent

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
)

func planL2TPUserPolicyMutation(raw, inboundTag string, userID int64, operation string, desired *l2TPRuntimeUser) (string, bool, error) {
	if userID <= 0 {
		return raw, false, fmt.Errorf("L2TP mutation user id must be positive")
	}
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" {
		return raw, false, fmt.Errorf("L2TP inbound tag is required")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "add" && operation != "update" && operation != "remove" {
		return raw, false, fmt.Errorf("unsupported L2TP user operation %q", operation)
	}
	if operation == "remove" {
		if desired != nil {
			return raw, false, fmt.Errorf("L2TP remove mutation cannot include a desired user")
		}
	} else {
		if desired == nil || desired.UserID != userID {
			return raw, false, fmt.Errorf("L2TP desired user identity does not match user %d", userID)
		}
		if err := validateL2TPMutationUser(*desired); err != nil {
			return raw, false, err
		}
	}

	payload, err := parseNativeRuntimePayload(raw)
	if err != nil {
		return raw, false, fmt.Errorf("read committed L2TP policy: %w", err)
	}
	matched := -1
	for i := range payload.L2TPInbounds {
		if strings.TrimSpace(payload.L2TPInbounds[i].Tag) != inboundTag {
			continue
		}
		if matched >= 0 {
			return raw, false, fmt.Errorf("L2TP inbound tag %q is ambiguous", inboundTag)
		}
		matched = i
	}
	if matched < 0 {
		return raw, false, fmt.Errorf("L2TP inbound %q is not in committed policy", inboundTag)
	}
	inbound := payload.L2TPInbounds[matched]
	userIndex := -1
	for i := range inbound.Users {
		if inbound.Users[i].UserID != userID {
			continue
		}
		if userIndex >= 0 {
			return raw, false, fmt.Errorf("L2TP user %d has duplicate entries in inbound %q", userID, inboundTag)
		}
		userIndex = i
	}

	changed := true
	switch operation {
	case "add":
		if userIndex >= 0 {
			if sameL2TPRuntimeUser(inbound.Users[userIndex], *desired) {
				return raw, false, nil
			}
			return raw, false, fmt.Errorf("L2TP user %d already exists; use update", userID)
		}
		inbound.Users = append(inbound.Users, *desired)
	case "update":
		if userIndex < 0 {
			return raw, false, fmt.Errorf("L2TP user %d is not present for update", userID)
		}
		if sameL2TPRuntimeUser(inbound.Users[userIndex], *desired) {
			return raw, false, nil
		}
		inbound.Users[userIndex] = *desired
	case "remove":
		if userIndex < 0 {
			changed = false
		} else {
			inbound.Users = append(inbound.Users[:userIndex], inbound.Users[userIndex+1:]...)
		}
	}
	if !changed {
		return raw, false, nil
	}
	if err := validateL2TPMutationUserConflicts(inbound); err != nil {
		return raw, false, err
	}
	payload.L2TPInbounds[matched] = inbound
	updated, err := json.Marshal(payload)
	if err != nil {
		return raw, false, fmt.Errorf("encode updated L2TP policy: %w", err)
	}
	return string(updated), true, nil
}

func sameL2TPRuntimeUser(left, right l2TPRuntimeUser) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && string(a) == string(b)
}

func validateL2TPMutationUser(user l2TPRuntimeUser) error {
	if user.UserID <= 0 || strings.TrimSpace(l2TPRuntimeUsername(user)) == "" {
		return fmt.Errorf("L2TP desired user identity is invalid")
	}
	for _, value := range []string{user.Username, user.VPNUsername, user.Password, user.IPv4Address} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("L2TP desired user contains a line break or NUL")
		}
	}
	if l2TPUserAllowed(user) && strings.TrimSpace(user.Password) == "" {
		return fmt.Errorf("active L2TP user password is required")
	}
	if address := strings.TrimSpace(user.IPv4Address); address != "" {
		parsed, err := netip.ParseAddr(address)
		if err != nil || !parsed.Is4() {
			return fmt.Errorf("L2TP user IPv4 address is invalid")
		}
	}
	return nil
}

func validateL2TPMutationUserConflicts(inbound l2TPRuntimeInbound) error {
	prefix, err := l2TPPoolPrefix(inbound)
	if err != nil {
		return err
	}
	rangeText, err := l2TPRemoteIPRange(prefix)
	if err != nil {
		return err
	}
	rangeParts := strings.Split(rangeText, "-")
	if len(rangeParts) != 2 {
		return fmt.Errorf("L2TP runtime address range is invalid")
	}
	start, startErr := netip.ParseAddr(rangeParts[0])
	end, endErr := netip.ParseAddr(rangeParts[1])
	if startErr != nil || endErr != nil {
		return fmt.Errorf("L2TP runtime address range is invalid")
	}
	usernames := make(map[string]int64, len(inbound.Users))
	addresses := make(map[netip.Addr]int64, len(inbound.Users))
	for _, user := range inbound.Users {
		if err := validateL2TPMutationUser(user); err != nil {
			return err
		}
		username := strings.TrimSpace(l2TPRuntimeUsername(user))
		if owner, exists := usernames[username]; exists && owner != user.UserID {
			return fmt.Errorf("L2TP username conflicts between users %d and %d", owner, user.UserID)
		}
		usernames[username] = user.UserID
		if strings.TrimSpace(user.IPv4Address) == "" {
			continue
		}
		address, _ := netip.ParseAddr(strings.TrimSpace(user.IPv4Address))
		if address.Compare(start) < 0 || address.Compare(end) > 0 {
			return fmt.Errorf("L2TP user %d address is outside the configured pool", user.UserID)
		}
		if owner, exists := addresses[address]; exists && owner != user.UserID {
			return fmt.Errorf("L2TP IPv4 address conflicts between users %d and %d", owner, user.UserID)
		}
		addresses[address] = user.UserID
	}
	return nil
}

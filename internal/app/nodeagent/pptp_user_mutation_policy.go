package nodeagent

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
)

func planPPTPUserPolicyMutation(raw, inboundTag string, userID int64, operation string, desired *pptpRuntimeUser) (string, bool, error) {
	if userID <= 0 {
		return raw, false, fmt.Errorf("PPTP mutation user id must be positive")
	}
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" {
		return raw, false, fmt.Errorf("PPTP inbound tag is required")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "add" && operation != "update" && operation != "remove" {
		return raw, false, fmt.Errorf("unsupported PPTP user operation %q", operation)
	}
	if operation == "remove" {
		if desired != nil {
			return raw, false, fmt.Errorf("PPTP remove mutation cannot include a desired user")
		}
	} else {
		if desired == nil || desired.UserID != userID {
			return raw, false, fmt.Errorf("PPTP desired user identity does not match user %d", userID)
		}
		if err := validatePPTPMutationUser(*desired); err != nil {
			return raw, false, err
		}
	}

	payload, err := parseNativeRuntimePayload(raw)
	if err != nil {
		return raw, false, fmt.Errorf("read committed PPTP policy: %w", err)
	}
	matched := -1
	for i := range payload.PPTPInbounds {
		if strings.TrimSpace(payload.PPTPInbounds[i].Tag) != inboundTag {
			continue
		}
		if matched >= 0 {
			return raw, false, fmt.Errorf("PPTP inbound tag %q is ambiguous", inboundTag)
		}
		matched = i
	}
	if matched < 0 {
		return raw, false, fmt.Errorf("PPTP inbound %q is not in committed policy", inboundTag)
	}
	inbound := payload.PPTPInbounds[matched]
	userIndex := -1
	for i := range inbound.Users {
		if inbound.Users[i].UserID != userID {
			continue
		}
		if userIndex >= 0 {
			return raw, false, fmt.Errorf("PPTP user %d has duplicate entries in inbound %q", userID, inboundTag)
		}
		userIndex = i
	}

	changed := true
	switch operation {
	case "add":
		if userIndex >= 0 {
			if samePPTPRuntimeUser(inbound.Users[userIndex], *desired) {
				return raw, false, nil
			}
			return raw, false, fmt.Errorf("PPTP user %d already exists; use update", userID)
		}
		inbound.Users = append(inbound.Users, *desired)
	case "update":
		if userIndex < 0 {
			return raw, false, fmt.Errorf("PPTP user %d is not present for update", userID)
		}
		if samePPTPRuntimeUser(inbound.Users[userIndex], *desired) {
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
	if err := validatePPTPMutationUserConflicts(inbound); err != nil {
		return raw, false, err
	}
	payload.PPTPInbounds[matched] = inbound
	updated, err := json.Marshal(payload)
	if err != nil {
		return raw, false, fmt.Errorf("encode updated PPTP policy: %w", err)
	}
	return string(updated), true, nil
}

func samePPTPRuntimeUser(left, right pptpRuntimeUser) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && string(a) == string(b)
}

func validatePPTPMutationUser(user pptpRuntimeUser) error {
	if user.UserID <= 0 || strings.TrimSpace(pptpRuntimeUsername(user)) == "" {
		return fmt.Errorf("PPTP desired user identity is invalid")
	}
	for _, value := range []string{user.Username, user.VPNUsername, user.Password, user.IPv4Address} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("PPTP desired user contains a line break or NUL")
		}
	}
	if pptpUserAllowed(user) && strings.TrimSpace(user.Password) == "" {
		return fmt.Errorf("active PPTP user password is required")
	}
	if address := strings.TrimSpace(user.IPv4Address); address != "" {
		parsed, err := netip.ParseAddr(address)
		if err != nil || !parsed.Is4() {
			return fmt.Errorf("PPTP user IPv4 address is invalid")
		}
	}
	return nil
}

func validatePPTPMutationUserConflicts(inbound pptpRuntimeInbound) error {
	prefix, err := pptpPoolPrefix(inbound)
	if err != nil {
		return err
	}
	start := prefix.Addr().Next().Next()
	if !prefix.Contains(start) {
		return fmt.Errorf("PPTP runtime address range is invalid")
	}
	base := prefix.Addr().As4()
	end := netip.AddrFrom4([4]byte{base[0], base[1], base[2], 254})
	if !prefix.Contains(end) || start.Compare(end) > 0 {
		return fmt.Errorf("PPTP runtime address range is invalid")
	}
	usernames := make(map[string]int64, len(inbound.Users))
	addresses := make(map[netip.Addr]int64, len(inbound.Users))
	for _, user := range inbound.Users {
		if err := validatePPTPMutationUser(user); err != nil {
			return err
		}
		username := strings.TrimSpace(pptpRuntimeUsername(user))
		if owner, exists := usernames[username]; exists && owner != user.UserID {
			return fmt.Errorf("PPTP username conflicts between users %d and %d", owner, user.UserID)
		}
		usernames[username] = user.UserID
		if strings.TrimSpace(user.IPv4Address) == "" {
			continue
		}
		address, _ := netip.ParseAddr(strings.TrimSpace(user.IPv4Address))
		if address.Compare(start) < 0 || address.Compare(end) > 0 {
			return fmt.Errorf("PPTP user %d address is outside the configured pool", user.UserID)
		}
		if owner, exists := addresses[address]; exists && owner != user.UserID {
			return fmt.Errorf("PPTP IPv4 address conflicts between users %d and %d", owner, user.UserID)
		}
		addresses[address] = user.UserID
	}
	return nil
}

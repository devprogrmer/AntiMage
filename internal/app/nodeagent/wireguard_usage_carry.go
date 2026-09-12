package nodeagent

import (
	"fmt"
	"strings"
)

func (s *Server) recordWireGuardUsageCarryLocked(
	inboundTag string,
	interfaceName string,
	publicKey string,
	userID int64,
	total uint64,
) error {
	inboundTag = strings.TrimSpace(inboundTag)
	interfaceName = strings.TrimSpace(interfaceName)
	publicKey = strings.TrimSpace(publicKey)

	if inboundTag == "" {
		return fmt.Errorf("wireguard carry inbound tag is empty")
	}
	if interfaceName == "" {
		return fmt.Errorf("wireguard carry interface is empty")
	}
	if publicKey == "" {
		return fmt.Errorf("wireguard carry public key is empty")
	}
	if userID <= 0 {
		return fmt.Errorf("wireguard carry user id is invalid")
	}

	if s.wireGuardUsageBaseline == nil {
		s.wireGuardUsageBaseline = make(map[string]uint64)
	}
	if s.wireGuardUsageCarry == nil {
		s.wireGuardUsageCarry = make(map[string]wireGuardUsageCarry)
	}

	key := wireGuardUsageBaselineKey(
		inboundTag,
		interfaceName,
		publicKey,
	)

	reference, referenceKnown := s.wireGuardUsageBaseline[key]

	if pending := s.wireGuardUsagePending; pending != nil {
		if value, ok := pending.NextBaseline[key]; ok {
			reference = value
			referenceKnown = true
		}
	}

	previous, hasCarry := s.wireGuardUsageCarry[key]
	if hasCarry {
		if previous.UserID != 0 && previous.UserID != userID {
			return fmt.Errorf(
				"wireguard carry ownership changed for %q: user %d -> %d",
				publicKey,
				previous.UserID,
				userID,
			)
		}
		reference = previous.NextBaseline
		referenceKnown = true
	}

	delta := total
	if referenceKnown {
		if total >= reference {
			delta = total - reference
		} else {
			// The kernel counter restarted. Treat the current total as the
			// first bytes of a new counter epoch.
			delta = total
		}
	}

	if !hasCarry && delta == 0 {
		return nil
	}

	updated := previous
	updated.UserID = userID
	updated.InboundTag = inboundTag

	if delta > 0 {
		if ^uint64(0)-updated.Value < delta {
			return fmt.Errorf(
				"wireguard carry byte counter overflow for user %d",
				userID,
			)
		}
		updated.Value += delta
	}

	if hasCarry || delta > 0 {
		updated.NextBaseline = total
	}

	if hasCarry &&
		updated.Value == previous.Value &&
		updated.NextBaseline == previous.NextBaseline &&
		updated.UserID == previous.UserID &&
		updated.InboundTag == previous.InboundTag {
		return nil
	}

	s.wireGuardUsageCarry[key] = updated

	if err := s.persistWireGuardUsageStateLocked(); err != nil {
		if hasCarry {
			s.wireGuardUsageCarry[key] = previous
		} else {
			delete(s.wireGuardUsageCarry, key)
		}
		return err
	}

	return nil
}
func cloneWireGuardUsageCarryMap(
	source map[string]wireGuardUsageCarry,
) map[string]wireGuardUsageCarry {
	cloned := make(
		map[string]wireGuardUsageCarry,
		len(source),
	)
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

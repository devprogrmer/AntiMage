package nodeagent

import "strings"

func pruneOpenVPNUsageBaselines(
	baseline map[string]uint64,
	scannedInboundTags map[string]struct{},
	activeSessionKeys map[string]struct{},
) bool {
	changed := false

	for sessionKey := range baseline {
		for inboundTag := range scannedInboundTags {
			prefix := inboundTag + "\x00"

			if !strings.HasPrefix(sessionKey, prefix) {
				continue
			}

			if _, active := activeSessionKeys[sessionKey]; !active {
				delete(baseline, sessionKey)
				changed = true
			}

			break
		}
	}

	return changed
}

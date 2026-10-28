package nodecontroller

import (
	"context"
	"strings"
)

// inboundSpeedLimit holds the enforced per-user cap configured on one inbound.
// Values are bits per second and mirror users.upload_speed_limit; 0 is
// unlimited. The cap is independent per user: several users on the same
// inbound may each reach it at the same time.
type inboundSpeedLimit struct {
	Upload   int64
	Download int64
}

// effectiveSpeedLimit resolves the rate a runtime user may actually consume.
// The user's own limit and the inbound's enforced cap are independent maxima,
// so the stricter non-zero value wins. A zero on either side means "no limit
// from that side", which is why zero can never win over a non-zero value.
//
// This is the single authority for the inbound cap: NodeAgent enforces exactly
// the number produced here, and no caller may raise a user limit above the cap.
func effectiveSpeedLimit(userLimit, inboundLimit int64) int64 {
	if userLimit < 0 {
		userLimit = 0
	}
	if inboundLimit < 0 {
		inboundLimit = 0
	}
	switch {
	case inboundLimit == 0:
		return userLimit
	case userLimit == 0:
		return inboundLimit
	case userLimit < inboundLimit:
		return userLimit
	default:
		return inboundLimit
	}
}

// applyInboundSpeedLimit returns the enforced upload/download pair for one
// runtime user. It exists so every protocol caller resolves the cap through
// the same arithmetic instead of open-coding the comparison.
func applyInboundSpeedLimit(limit inboundSpeedLimit, upload, download int64) (int64, int64) {
	return effectiveSpeedLimit(upload, limit.Upload), effectiveSpeedLimit(download, limit.Download)
}

// speedLimitValue reads a numeric speed-limit field defensively. The sanitized
// inbound payload carries JSON-decoded numbers (float64) while the structured
// column path carries int64, so both shapes must resolve to the same value.
func speedLimitValue(value any) int64 {
	return int64(OVIntValue(value))
}

// inboundSpeedLimitFromInbound reads the cap out of one sanitized inbound map.
func inboundSpeedLimitFromInbound(inbound map[string]any) inboundSpeedLimit {
	if inbound == nil {
		return inboundSpeedLimit{}
	}
	return inboundSpeedLimit{
		Upload:   speedLimitValue(inbound["upload_speed_limit"]),
		Download: speedLimitValue(inbound["download_speed_limit"]),
	}
}

// applyInboundSpeedLimitToUsers enforces the inbound cap across a runtime user
// slice in place. It is generic over the concrete user type so every protocol
// shares the same resolution rule.
func applyInboundSpeedLimitToUsers[T any](
	limit inboundSpeedLimit,
	users []T,
	upload func(*T) *int64,
	download func(*T) *int64,
) {
	if limit.Upload == 0 && limit.Download == 0 {
		return
	}
	for i := range users {
		up := upload(&users[i])
		down := download(&users[i])
		*up, *down = applyInboundSpeedLimit(limit, *up, *down)
	}
}

// runtimeInboundSpeedLimits reads the caps straight out of the sanitized
// inbound payloads produced by xrayconfig.manageableInboundsWithTargets, which
// already carries upload_speed_limit / download_speed_limit.
func runtimeInboundSpeedLimits(inbounds []map[string]any) map[string]inboundSpeedLimit {
	result := make(map[string]inboundSpeedLimit, len(inbounds))
	for _, inbound := range inbounds {
		tag := strings.TrimSpace(OVStringValue(inbound["tag"]))
		if tag == "" {
			continue
		}
		result[tag] = inboundSpeedLimit{
			Upload:   speedLimitValue(inbound["upload_speed_limit"]),
			Download: speedLimitValue(inbound["download_speed_limit"]),
		}
	}
	return result
}

// inboundSpeedLimitsByTag loads the caps from the structured inbound columns.
// The targeted mutation paths start from the raw Xray config, which never
// carries these keys (they are stripped before persistence so a rate change
// cannot perturb the config graph), so they read the columns directly.
//
// Missing columns indicate migrations have not completed. Fail the targeted
// mutation instead of treating an enforced cap as unlimited.
func (r Repository) inboundSpeedLimitsByTag(ctx context.Context) (map[string]inboundSpeedLimit, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT tag, COALESCE(upload_speed_limit, 0), COALESCE(download_speed_limit, 0) FROM inbounds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]inboundSpeedLimit{}
	for rows.Next() {
		var tag string
		var limit inboundSpeedLimit
		if err := rows.Scan(&tag, &limit.Upload, &limit.Download); err != nil {
			return nil, err
		}
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		result[tag] = limit
	}
	return result, rows.Err()
}

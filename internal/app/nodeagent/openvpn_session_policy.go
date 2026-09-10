package nodeagent

import (
	"strconv"
	"strings"
	"time"
)

func buildNativeSessionUserPolicies(
	users []openVPNRuntimeUser,
) map[string]nativeSessionUserPolicy {
	policies := make(
		map[string]nativeSessionUserPolicy,
		len(users),
	)

	for _, user := range users {
		username := strings.TrimSpace(user.VPNUsername)
		if username == "" {
			username = strings.TrimSpace(user.Username)
		}
		if username == "" {
			continue
		}

		var dataLimit int64
		if user.DataLimit != nil {
			dataLimit = *user.DataLimit
		}

		var expire int64
		if user.Expire != nil {
			expire = *user.Expire
		}

		policies[username] = nativeSessionUserPolicy{
			Status: strings.ToLower(
				strings.TrimSpace(user.Status),
			),
			UsedTraffic: user.UsedTraffic,
			DataLimit:   dataLimit,
			Expire:      expire,
		}
	}

	return policies
}

func nativeSessionUserPolicyAllowedWithLiveUsage(
	policy nativeSessionUserPolicy,
	liveBytes uint64,
	now time.Time,
) (bool, string) {
	allowed, reason := nativeSessionUserPolicyAllowed(
		policy,
		now,
	)
	if !allowed {
		return false, reason
	}

	if strings.ToLower(strings.TrimSpace(policy.Status)) != "active" {
		return true, ""
	}

	if policy.DataLimit <= 0 {
		return true, ""
	}

	usedTraffic := policy.UsedTraffic
	if usedTraffic < 0 {
		usedTraffic = 0
	}

	if usedTraffic >= policy.DataLimit {
		return false, "data limit reached"
	}

	remaining := uint64(
		policy.DataLimit - usedTraffic,
	)

	if liveBytes >= remaining {
		return false, "data limit reached"
	}

	return true, ""
}
func (s *Server) enforceOpenVPNClientPolicy(
	tag string,
	cfg nativeSessionHelperConfig,
	username string,
	userID int64,
	client openVPNStatusClient,
) bool {
	policy, ok := cfg.Policies[strings.TrimSpace(username)]

	if !ok {
		policy, ok = cfg.Policies[strings.TrimSpace(client.CommonName)]
	}

	if !ok {
		return false
	}

	liveBytes, err := openVPNStatusTotalBytes(client)
	if err != nil {
		liveBytes = 0
	}

	allowed, reason := nativeSessionUserPolicyAllowedWithLiveUsage(
		policy,
		liveBytes,
		time.Now().UTC(),
	)
	if allowed {
		return false
	}

	clientID := strings.TrimSpace(client.ClientID)
	if clientID == "" {
		s.appendLog(
			"openvpn policy denied user but client ID is missing: " +
				tag + " user " +
				strconv.FormatInt(userID, 10),
		)
		return true
	}

	if err := openVPNManagementClientKill(
		cfg.ManagementNetwork,
		cfg.ManagementAddress,
		clientID,
	); err != nil {
		s.appendLog(
			"openvpn policy disconnect failed for " +
				tag + " user " +
				strconv.FormatInt(userID, 10) +
				": " + err.Error(),
		)
		return true
	}

	s.appendLog(
		"openvpn client disconnected by user policy: " +
			tag + " user " +
			strconv.FormatInt(userID, 10) +
			" (" + reason + ")",
	)

	return true
}

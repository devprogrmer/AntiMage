package nodeagent

import (
	"math"
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
			UsedTraffic:        user.UsedTraffic,
			DataLimit:          dataLimit,
			Expire:             expire,
			UploadSpeedLimit:   user.UploadSpeedLimit,
			DownloadSpeedLimit: user.DownloadSpeedLimit,
			UsageCoefficient:   user.UsageCoefficient,
			InboundCoefficient: user.InboundCoefficient,
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

	effectiveLiveBytes := nativeSessionEffectiveLiveUsage(policy, liveBytes)
	if nativeSessionPolicyWouldExceedDataLimit(
		policy,
		effectiveLiveBytes,
	) {
		return false, "data limit reached"
	}

	return true, ""
}

func nativeSessionEffectiveLiveUsage(
	policy nativeSessionUserPolicy,
	rawLiveBytes uint64,
) uint64 {
	if rawLiveBytes == 0 {
		return 0
	}
	factor := nativeSessionUsageFactor(policy.UsageCoefficient) *
		nativeSessionUsageFactor(policy.InboundCoefficient)
	if factor == 1 {
		return rawLiveBytes
	}
	scaled := math.Round(float64(rawLiveBytes) * factor)
	if scaled <= 0 {
		return 0
	}
	if scaled >= float64(^uint64(0)) {
		return ^uint64(0)
	}
	return uint64(scaled)
}

func nativeSessionUsageFactor(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 1
	}
	return value
}

func nativeSessionPolicyWouldExceedDataLimit(
	policy nativeSessionUserPolicy,
	effectiveDelta uint64,
) bool {
	if policy.DataLimit <= 0 {
		return false
	}
	usedTraffic := policy.UsedTraffic
	if usedTraffic < 0 {
		usedTraffic = 0
	}
	if usedTraffic >= policy.DataLimit {
		return true
	}
	remaining := uint64(policy.DataLimit - usedTraffic)
	return effectiveDelta >= remaining
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

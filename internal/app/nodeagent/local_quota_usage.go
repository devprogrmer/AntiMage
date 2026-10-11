package nodeagent

import (
	"fmt"
	"strings"
	"time"
)

type localQuotaOwner struct {
	Protocol   string
	UserID     int64
	InboundTag string
}
type localQuotaView struct {
	Effective        uint64
	PanelUsed        int64
	ReflectedBatchID string
}

// Runtime workers publish aggregate user/inbound observations, not per-peer
// increments. Matching panel epochs may be summed across protocols safely.
func (s *Server) localQuotaAllowed(protocol string, uid int64, tag string, policy nativeSessionUserPolicy, raw uint64, now time.Time) (bool, string) {
	s.localUsageMu.Lock()
	if s.localQuotaViews == nil {
		s.localQuotaViews = map[localQuotaOwner]localQuotaView{}
	}
	owner := localQuotaOwner{protocol, uid, tag}
	if _, exists := s.localQuotaViews[owner]; !exists && len(s.localQuotaViews) >= maxAccountingCounterSeries {
		s.localUsageMu.Unlock()
		err := fmt.Errorf("local quota observation capacity exceeded")
		s.recordLocalAccountingHealth("quota", err)
		return false, err.Error()
	}
	s.localQuotaViews[owner] = localQuotaView{nativeSessionEffectiveLiveUsage(policy, raw), policy.UsedTraffic, policy.ReflectedUsageBatchID}
	var effective uint64
	for key, view := range s.localQuotaViews {
		if key.UserID != uid || view.PanelUsed != policy.UsedTraffic || view.ReflectedBatchID != policy.ReflectedUsageBatchID {
			continue
		}
		if ^uint64(0)-effective < view.Effective {
			s.localUsageMu.Unlock()
			return false, "local quota overflow"
		}
		effective += view.Effective
	}
	s.localUsageMu.Unlock()
	allowed, reason := nativeSessionUserPolicyAllowed(policy, now)
	if !allowed {
		return allowed, reason
	}
	if strings.EqualFold(strings.TrimSpace(policy.Status), "active") && nativeSessionPolicyWouldExceedDataLimit(policy, effective) {
		return false, "data limit reached"
	}
	return true, ""
}

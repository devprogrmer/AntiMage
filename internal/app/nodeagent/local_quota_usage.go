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

type localQuotaAggregateKey struct {
	UserID           int64
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
	if s.localQuotaAggregates == nil {
		s.localQuotaAggregates = make(map[localQuotaAggregateKey]uint64, len(s.localQuotaViews)+1)
		consistent := true
		for key, view := range s.localQuotaViews {
			aggregateKey := localQuotaAggregateKey{key.UserID, view.PanelUsed, view.ReflectedBatchID}
			if ^uint64(0)-s.localQuotaAggregates[aggregateKey] < view.Effective {
				consistent = false
				break
			}
			s.localQuotaAggregates[aggregateKey] += view.Effective
		}
		if !consistent {
			s.localQuotaAggregates = nil
			s.localUsageMu.Unlock()
			err := fmt.Errorf("local quota aggregate is inconsistent")
			s.recordLocalAccountingHealth("quota", err)
			return false, err.Error()
		}
	}
	owner := localQuotaOwner{protocol, uid, tag}
	if _, exists := s.localQuotaViews[owner]; !exists && len(s.localQuotaViews) >= maxAccountingCounterSeries {
		s.localUsageMu.Unlock()
		err := fmt.Errorf("local quota observation capacity exceeded")
		s.recordLocalAccountingHealth("quota", err)
		return false, err.Error()
	}
	newView := localQuotaView{nativeSessionEffectiveLiveUsage(policy, raw), policy.UsedTraffic, policy.ReflectedUsageBatchID}
	newKey := localQuotaAggregateKey{uid, newView.PanelUsed, newView.ReflectedBatchID}
	previous, existed := s.localQuotaViews[owner]
	oldKey := localQuotaAggregateKey{}
	oldAggregate, newAggregate := uint64(0), s.localQuotaAggregates[newKey]
	if existed {
		oldKey = localQuotaAggregateKey{uid, previous.PanelUsed, previous.ReflectedBatchID}
		oldAggregate = s.localQuotaAggregates[oldKey]
		if oldAggregate < previous.Effective {
			s.localUsageMu.Unlock()
			err := fmt.Errorf("local quota aggregate is inconsistent")
			s.recordLocalAccountingHealth("quota", err)
			return false, err.Error()
		}
		oldAggregate -= previous.Effective
		if oldKey == newKey {
			newAggregate = oldAggregate
		}
	}
	if ^uint64(0)-newAggregate < newView.Effective {
		s.localUsageMu.Unlock()
		err := fmt.Errorf("local quota overflow")
		s.recordLocalAccountingHealth("quota", err)
		return false, err.Error()
	}
	newAggregate += newView.Effective
	if existed && oldKey != newKey {
		if oldAggregate == 0 {
			delete(s.localQuotaAggregates, oldKey)
		} else {
			s.localQuotaAggregates[oldKey] = oldAggregate
		}
	}
	s.localQuotaAggregates[newKey] = newAggregate
	s.localQuotaViews[owner] = newView
	s.localUsageMu.Unlock()
	effective := newAggregate
	allowed, reason := nativeSessionUserPolicyAllowed(policy, now)
	if !allowed {
		return allowed, reason
	}
	if strings.EqualFold(strings.TrimSpace(policy.Status), "active") && nativeSessionPolicyWouldExceedDataLimit(policy, effective) {
		return false, "data limit reached"
	}
	return true, ""
}

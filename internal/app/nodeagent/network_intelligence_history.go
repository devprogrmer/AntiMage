package nodeagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"sync"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
)

type networkHistoryScope struct{ node, user int64 }

type networkHistorySampler struct {
	mu      sync.Mutex
	history *ni.QualityHistory
	last    map[networkHistoryScope]time.Time
}

type networkHistoryPoint struct {
	NodeID              int64        `json:"node_id"`
	UserID              int64        `json:"user_id"`
	At                  time.Time    `json:"at"`
	Method              string       `json:"method"`
	ACKRTT              *float64     `json:"ack_rtt_ms"`
	Jitter              *float64     `json:"jitter_ms"`
	RetransmissionDelta *uint64      `json:"retransmission_delta"`
	Findings            []ni.Finding `json:"findings"`
}

type networkHistorySnapshot struct {
	Storage    string                `json:"storage"`
	Collection string                `json:"collection"`
	Truncated  bool                  `json:"truncated"`
	Points     []networkHistoryPoint `json:"points"`
}

type networkHistoryAggregate struct {
	at                      time.Time
	members                 []string
	rttTotal, jitterTotal   float64
	rttWeight, jitterWeight float64
	retransmissions         uint64
	overflow                bool
}

// Snapshot samples observed account quality at most every five seconds. It is
// query-driven and volatile. Poll timestamps never become observation times.
func (s *networkHistorySampler) Snapshot(flows []ni.Flow, epoch string, now time.Time) networkHistorySnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := networkHistorySnapshot{Storage: "volatile", Collection: "on_telemetry_read", Points: []networkHistoryPoint{}}
	if s.history == nil {
		s.history, _ = ni.NewQualityHistory(ni.HistoryLimits{MaxSamples: 4096, MaxNodes: 32, MaxUsersPerNode: 256, SamplesPerUser: 60, Retention: 30 * time.Minute})
		s.last = make(map[networkHistoryScope]time.Time)
	}
	for scope, at := range s.last {
		if !at.After(now.Add(-30 * time.Minute)) {
			delete(s.last, scope)
		}
	}
	aggregates := make(map[networkHistoryScope]*networkHistoryAggregate)
	for _, flow := range flows {
		if flow.Binding.NodeID <= 0 || flow.Binding.UserID <= 0 || flow.At.After(now) || !flow.At.After(now.Add(-time.Minute)) {
			continue
		}
		key := networkHistoryScope{flow.Binding.NodeID, flow.Binding.UserID}
		a := aggregates[key]
		if a == nil {
			a = &networkHistoryAggregate{}
			aggregates[key] = a
		}
		if flow.At.After(a.at) {
			a.at = flow.At
		}
		// Membership changes establish a new counter baseline. Only the digest
		// survives aggregation, with no endpoint or session data in history.
		member, _ := json.Marshal(struct {
			Interface  string
			Generation uint64
			Tuple      ni.Tuple
		}{flow.Binding.Interface, flow.Binding.Generation, flow.Tuple})
		a.members = append(a.members, string(member))
		q := flow.TCPQuality
		if q == nil {
			continue
		}
		if math.MaxUint64-a.retransmissions < q.Retransmissions {
			a.overflow = true
		} else {
			a.retransmissions += q.Retransmissions
		}
		leg := q.UploadACK
		if leg.Samples > 0 && validNetworkHistoryMilliseconds(leg.Mean) && *leg.Mean > 0 {
			a.rttTotal += *leg.Mean * float64(leg.Samples)
			a.rttWeight += float64(leg.Samples)
		}
		if leg.Samples > 1 && validNetworkHistoryMilliseconds(leg.Jitter) {
			a.jitterTotal += *leg.Jitter * float64(leg.Samples-1)
			a.jitterWeight += float64(leg.Samples - 1)
		}
	}
	for scope, a := range aggregates {
		if a.overflow || (!s.last[scope].IsZero() && a.at.Sub(s.last[scope]) < 5*time.Second) {
			continue
		}
		sort.Strings(a.members)
		digest := sha256.New()
		digest.Write([]byte(epoch))
		for _, member := range a.members {
			digest.Write([]byte{0})
			digest.Write([]byte(member))
		}
		input := ni.HistoryInput{NodeID: scope.node, UserID: scope.user, Epoch: hex.EncodeToString(digest.Sum(nil)), At: a.at, Retransmissions: a.retransmissions}
		if a.rttWeight > 0 {
			value := time.Duration(a.rttTotal / a.rttWeight * float64(time.Millisecond))
			input.ACKRTT = &value
		}
		if a.jitterWeight > 0 {
			value := time.Duration(a.jitterTotal / a.jitterWeight * float64(time.Millisecond))
			input.Jitter = &value
		}
		if s.history.Add(input, now) {
			s.last[scope] = a.at
		}
	}
	for scope := range s.last {
		for _, point := range s.history.Snapshot(scope.node, scope.user, now) {
			result.Points = append(result.Points, networkHistoryPoint{NodeID: point.NodeID, UserID: point.UserID, At: point.At, Method: "tcp_ack_upload_observation", ACKRTT: networkHistoryMilliseconds(point.ACKRTT), Jitter: networkHistoryMilliseconds(point.Jitter), RetransmissionDelta: point.RetransmissionDelta, Findings: point.Findings})
		}
	}
	sort.Slice(result.Points, func(i, j int) bool {
		if result.Points[i].At.Equal(result.Points[j].At) {
			if result.Points[i].NodeID == result.Points[j].NodeID {
				return result.Points[i].UserID < result.Points[j].UserID
			}
			return result.Points[i].NodeID < result.Points[j].NodeID
		}
		return result.Points[i].At.After(result.Points[j].At)
	})
	result.Truncated = len(result.Points) > 128
	if result.Truncated {
		result.Points = result.Points[:128]
	}
	return result
}

func validNetworkHistoryMilliseconds(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0 && *value <= 600000
}
func networkHistoryMilliseconds(value *time.Duration) *float64 {
	if value == nil {
		return nil
	}
	ms := float64(*value) / float64(time.Millisecond)
	return &ms
}

package networkintelligence

import (
	"errors"
	"sync"
	"time"
)

type HistoryLimits struct {
	MaxSamples, MaxNodes, MaxUsersPerNode, SamplesPerUser int
	Retention                                             time.Duration
}

// HistoryInput is already aggregated for one account on one node. Epoch must
// change when the cumulative counters reset. No destination or session is kept.
type HistoryInput struct {
	NodeID, UserID                  int64
	Epoch                           string
	At                              time.Time
	ACKRTT, Jitter                  *time.Duration
	Retransmissions, CollectorDrops uint64
}

type HistoryPoint struct {
	NodeID                                  int64     `json:"node_id"`
	UserID                                  int64     `json:"user_id"`
	At                                      time.Time `json:"at"`
	Method                                  string    `json:"method"`
	ACKRTT, Jitter                          *time.Duration
	RetransmissionDelta, CollectorDropDelta *uint64
	Findings                                []Finding `json:"findings"`
}

type historyScope struct{ node, user int64 }
type historySeries struct {
	last   HistoryInput
	points []HistoryPoint
}

// QualityHistory is bounded volatile storage. A restart discards its history.
type QualityHistory struct {
	mu     sync.Mutex
	limits HistoryLimits
	series map[historyScope]*historySeries
	count  int
}

func NewQualityHistory(limits HistoryLimits) (*QualityHistory, error) {
	if limits.MaxSamples < 1 || limits.MaxNodes < 1 || limits.MaxUsersPerNode < 1 || limits.SamplesPerUser < 1 || limits.Retention <= 0 {
		return nil, errors.New("positive history limits required")
	}
	return &QualityHistory{limits: limits, series: make(map[historyScope]*historySeries)}, nil
}

func copyDuration(value *time.Duration) *time.Duration {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func (h *QualityHistory) prune(now time.Time) {
	for key, series := range h.series {
		first := 0
		for first < len(series.points) && !series.points[first].At.After(now.Add(-h.limits.Retention)) {
			first++
		}
		h.count -= first
		series.points = series.points[first:]
		if len(series.points) == 0 {
			delete(h.series, key)
		}
	}
}

// Add rejects duplicate, stale and regressing observations. A counter regression
// creates an unavailable delta for that interval, then establishes a new baseline.
func (h *QualityHistory) Add(input HistoryInput, now time.Time) bool {
	if input.NodeID <= 0 || input.UserID <= 0 || input.Epoch == "" || len(input.Epoch) > 128 || input.At.IsZero() || input.At.After(now) || !input.At.After(now.Add(-h.limits.Retention)) || (input.ACKRTT != nil && *input.ACKRTT <= 0) || (input.Jitter != nil && *input.Jitter < 0) {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prune(now)
	key := historyScope{input.NodeID, input.UserID}
	series := h.series[key]
	if series != nil && !input.At.After(series.last.At) {
		return false
	}
	if series == nil {
		nodes := make(map[int64]bool)
		users := 0
		for scope := range h.series {
			nodes[scope.node] = true
			if scope.node == input.NodeID {
				users++
			}
		}
		if (!nodes[input.NodeID] && len(nodes) >= h.limits.MaxNodes) || users >= h.limits.MaxUsersPerNode || h.count >= h.limits.MaxSamples {
			return false
		}
		series = &historySeries{}
	}
	if h.count >= h.limits.MaxSamples && len(series.points) < h.limits.SamplesPerUser {
		return false
	}
	point := HistoryPoint{NodeID: input.NodeID, UserID: input.UserID, At: input.At, Method: "tcp_ack_observation", ACKRTT: copyDuration(input.ACKRTT), Jitter: copyDuration(input.Jitter)}
	if series.last.Epoch == input.Epoch {
		if input.Retransmissions >= series.last.Retransmissions {
			delta := input.Retransmissions - series.last.Retransmissions
			point.RetransmissionDelta = &delta
		}
		if input.CollectorDrops >= series.last.CollectorDrops {
			delta := input.CollectorDrops - series.last.CollectorDrops
			point.CollectorDropDelta = &delta
		}
	}
	diagnostic := DiagnosticInput{ObservedAt: input.At, ACKRTT: point.ACKRTT, Jitter: point.Jitter}
	if point.RetransmissionDelta != nil {
		diagnostic.Retransmissions = *point.RetransmissionDelta
	}
	if point.CollectorDropDelta != nil {
		diagnostic.CollectorDrops = *point.CollectorDropDelta
	}
	point.Findings = Diagnose(diagnostic, now)
	if len(series.points) == h.limits.SamplesPerUser {
		copy(series.points, series.points[1:])
		series.points[len(series.points)-1] = point
	} else {
		series.points = append(series.points, point)
		h.count++
	}
	input.ACKRTT, input.Jitter = nil, nil
	series.last = input
	h.series[key] = series
	return true
}

func (h *QualityHistory) Snapshot(nodeID, userID int64, now time.Time) []HistoryPoint {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prune(now)
	result := []HistoryPoint{}
	if series := h.series[historyScope{nodeID, userID}]; series != nil {
		for _, point := range series.points {
			point.ACKRTT, point.Jitter = copyDuration(point.ACKRTT), copyDuration(point.Jitter)
			if point.RetransmissionDelta != nil {
				value := *point.RetransmissionDelta
				point.RetransmissionDelta = &value
			}
			if point.CollectorDropDelta != nil {
				value := *point.CollectorDropDelta
				point.CollectorDropDelta = &value
			}
			point.Findings = append([]Finding(nil), point.Findings...)
			result = append(result, point)
		}
	}
	return result
}

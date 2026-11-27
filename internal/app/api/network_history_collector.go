package api

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	"github.com/antimage/antimage/internal/app/nodecontroller"
)

type persistedNetworkPoint struct {
	NodeID          int64     `json:"node_id"`
	UserID          int64     `json:"user_id"`
	At              time.Time `json:"at"`
	RTT             *float64  `json:"ack_rtt_ms"`
	Jitter          *float64  `json:"jitter_ms"`
	Retransmissions *int64    `json:"retransmission_delta"`
}

func (s *Server) startNetworkHistoryWorkers(ctx context.Context) {
	if !s.cfg.networkHistoryWorkersEnabled() || s.db == nil {
		return
	}
	repo := &ni.HistoryRepository{DB: s.db}
	go s.runNetworkHistoryCollector(ctx, repo)
	go runNetworkHistoryRetention(ctx, repo)
}

func (c Config) networkHistoryWorkersEnabled() bool {
	return c.NetworkIntelligenceEnabled && c.NetworkHistoryPersistence
}

func runNetworkHistoryRetention(ctx context.Context, repo *ni.HistoryRepository) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for ctx.Err() == nil {
		poll, cancel := context.WithTimeout(ctx, 15*time.Second)
		_ = repo.Prune(poll, time.Now())
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) runNetworkHistoryCollector(ctx context.Context, repo *ni.HistoryRepository) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	runNetworkHistoryCollector(ctx, ticker.C, s.nodeController, repo, time.Now)
}

type networkHistorySource interface {
	List(context.Context, nodecontroller.Request) (nodecontroller.NodeListResult, error)
	NetworkIntelligence(context.Context, int64) (json.RawMessage, error)
}

func runNetworkHistoryCollector(ctx context.Context, ticks <-chan time.Time, source networkHistorySource, repo *ni.HistoryRepository, clock func() time.Time) {
	offset := 0
	cursors := make(map[int64]int64)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok || ctx.Err() != nil {
				return
			}
			poll, cancel := context.WithTimeout(ctx, 15*time.Second)
			nodes, err := source.List(poll, nodecontroller.Request{})
			if err == nil {
				nodes.Nodes = slices.DeleteFunc(nodes.Nodes, func(node nodecontroller.NodeListItem) bool {
					return node.ID <= 0 || node.Status == "disabled" || node.Status == "limited"
				})
				active := make(map[int64]bool, len(nodes.Nodes))
				for _, node := range nodes.Nodes {
					active[node.ID] = true
				}
				for id := range cursors {
					if !active[id] {
						delete(cursors, id)
					}
				}
			}
			if err == nil && len(nodes.Nodes) != 0 {
				for range min(4, len(nodes.Nodes)) {
					node := nodes.Nodes[offset%len(nodes.Nodes)]
					offset = (offset + 1) % len(nodes.Nodes)
					cursors[node.ID] = collectNetworkHistory(poll, source, repo, node.ID, cursors[node.ID], clock)
				}
			}
			cancel()
		}
	}
}

func collectNetworkHistory(ctx context.Context, source networkHistorySource, repo *ni.HistoryRepository, nodeID, cursor int64, clock func() time.Time) int64 {
	poll, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := source.NetworkIntelligence(poll, nodeID)
	if err != nil {
		return cursor
	}
	var snapshot struct {
		History struct {
			Points []persistedNetworkPoint `json:"points"`
		} `json:"history"`
	}
	if json.Unmarshal(raw, &snapshot) != nil || len(snapshot.History.Points) > 128 {
		return cursor
	}
	now := clock().UTC()
	latest := make(map[int64]persistedNetworkPoint)
	for _, p := range snapshot.History.Points {
		if p.NodeID != nodeID || p.UserID <= 0 || p.At.Before(now.Add(-time.Minute)) || p.At.After(now) {
			continue
		}
		if p.At.After(latest[p.UserID].At) {
			latest[p.UserID] = p
		}
	}
	users := make([]int64, 0, len(latest))
	for id := range latest {
		users = append(users, id)
	}
	slices.Sort(users)
	start, found := slices.BinarySearch(users, cursor)
	if found {
		start++
	}
	for i := range min(8, len(users)) {
		id := users[(start+i)%len(users)]
		p := latest[id]
		cursor = id
		if err := repo.Add(ctx, ni.DurableQualityPoint{
			NodeID: nodeID, UserID: p.UserID, ObservedAt: p.At,
			ACKRTTMilliseconds: p.RTT, JitterMilliseconds: p.Jitter, RetransmissionDelta: p.Retransmissions,
		}, now); err != nil {
			continue
		}
	}
	return cursor
}

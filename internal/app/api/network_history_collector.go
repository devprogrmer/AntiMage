package api

import (
	"context"
	"encoding/json"
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

func (s *Server) runNetworkHistoryCollector(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	repo := &ni.HistoryRepository{DB: s.db}
	offset := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll, cancel := context.WithTimeout(ctx, 15*time.Second)
			nodes, err := s.nodeController.List(poll, nodecontroller.Request{})
			if err == nil && len(nodes.Nodes) != 0 {
				for range min(4, len(nodes.Nodes)) {
					node := nodes.Nodes[offset%len(nodes.Nodes)]
					offset = (offset + 1) % len(nodes.Nodes)
					if node.ID > 0 && node.Status != "disabled" && node.Status != "limited" {
						s.collectNetworkHistory(poll, repo, node.ID)
					}
				}
			}
			cancel()
		}
	}
}

func (s *Server) collectNetworkHistory(ctx context.Context, repo *ni.HistoryRepository, nodeID int64) {
	poll, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := s.nodeController.NetworkIntelligence(poll, nodeID)
	if err != nil {
		return
	}
	var snapshot struct {
		History struct {
			Points []persistedNetworkPoint `json:"points"`
		} `json:"history"`
	}
	if json.Unmarshal(raw, &snapshot) != nil || len(snapshot.History.Points) > 128 {
		return
	}
	now := time.Now().UTC()
	seen := make(map[int64]bool)
	// Node snapshots are ordered newest first.
	for _, p := range snapshot.History.Points {
		if p.NodeID != nodeID || p.UserID <= 0 || seen[p.UserID] || p.At.Before(now.Add(-time.Minute)) || p.At.After(now) {
			continue
		}
		seen[p.UserID] = true
		if err := repo.Add(ctx, ni.DurableQualityPoint{
			NodeID: nodeID, UserID: p.UserID, ObservedAt: p.At,
			ACKRTTMilliseconds: p.RTT, JitterMilliseconds: p.Jitter, RetransmissionDelta: p.Retransmissions,
		}, now); err != nil {
			return
		}
		if len(seen) == 8 {
			return
		}
	}
}

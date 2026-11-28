package api

import (
	"context"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	"github.com/antimage/antimage/internal/app/nodecontroller"
)

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
	nodecontroller.RunNetworkHistoryCollector(ctx, ticker.C, s.nodeController, repo, time.Now)
}

package api

import (
	"context"
	"time"

	"github.com/antimage/antimage/internal/app/logging"
)

const telegramOutboxFallbackInterval = 30 * time.Second
const telegramOutboxRetention = 30 * 24 * time.Hour
const telegramOutboxPruneInterval = 24 * time.Hour
const telegramOutboxPruneBatchSize = 1000
const telegramOutboxPruneMaxBatches = 10

func (s *Server) runTelegramMessageWorker(ctx context.Context) {
	ticker := time.NewTicker(telegramOutboxFallbackInterval)
	defer ticker.Stop()
	lastPrune := time.Time{}
	for {
		if lastPrune.IsZero() || time.Since(lastPrune) >= telegramOutboxPruneInterval {
			prunedTotal := 0
			var pruneErr error
			for batch := 0; batch < telegramOutboxPruneMaxBatches; batch++ {
				pruned, err := s.telegramRepo.PruneOutbox(ctx, time.Now().UTC().Add(-telegramOutboxRetention), telegramOutboxPruneBatchSize)
				if err != nil {
					pruneErr = err
					break
				}
				prunedTotal += pruned
				if pruned < telegramOutboxPruneBatchSize {
					break
				}
			}
			lastPrune = time.Now()
			if pruneErr != nil && ctx.Err() == nil {
				logging.Warnf(logging.ComponentTelegram, "message outbox retention cleanup failed: %v", pruneErr)
			} else if prunedTotal > 0 {
				logging.Infof(logging.ComponentTelegram, "message outbox pruned=%d", prunedTotal)
			}
		}
		if err := s.telegramOutbox.Dispatch(ctx, 32); err != nil && ctx.Err() == nil {
			logging.Warnf(logging.ComponentTelegram, "message outbox dispatch failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.telegramOutboxKick:
		case <-ticker.C:
		}
	}
}

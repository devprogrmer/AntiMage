package api

import (
	"context"
	"time"

	"github.com/antimage/antimage/internal/app/logging"
)

const telegramOutboxFallbackInterval = 30 * time.Second

func (s *Server) runTelegramMessageWorker(ctx context.Context) {
	ticker := time.NewTicker(telegramOutboxFallbackInterval)
	defer ticker.Stop()
	for {
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

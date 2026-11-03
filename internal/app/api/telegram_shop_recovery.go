package api

import (
	"context"
	"time"

	"github.com/antimage/antimage/internal/app/logging"
	"github.com/antimage/antimage/internal/app/shop"
)

const telegramShopRecoveryInterval = 45 * time.Second
const telegramShopRecoveryBatch = 25

// Paid orders may be left between wallet debit and user creation when the
// panel stops. Replaying Provision never charges the wallet again and checks
// the deterministic account marker before marking an order complete.
func (s *Server) runTelegramShopRecovery(ctx context.Context) {
	ticker := time.NewTicker(telegramShopRecoveryInterval)
	defer ticker.Stop()
	s.recoverTelegramShopOrders(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.recoverTelegramShopOrders(ctx)
		}
	}
}

func (s *Server) recoverTelegramShopOrders(ctx context.Context) {
	workerCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	orders := shop.Orders{DB: s.db}
	ids, err := orders.Unfulfilled(workerCtx, telegramShopRecoveryBatch)
	if err != nil {
		if ctx.Err() == nil {
			logging.Warnf(logging.ComponentTelegram, "shop recovery list failed: %v", err)
		}
		return
	}
	provisioner := botAccountProvisioner{server: s}
	recovered := 0
	for _, id := range ids {
		if workerCtx.Err() != nil {
			break
		}
		if _, err := orders.Provision(workerCtx, id, provisioner); err != nil {
			if workerCtx.Err() == nil {
				logging.Warnf(logging.ComponentTelegram, "shop order %s delivery retry failed: %v", id, err)
			}
			continue
		}
		recovered++
	}
	if recovered > 0 {
		logging.Infof(logging.ComponentTelegram, "shop orders recovered=%d", recovered)
	}
}

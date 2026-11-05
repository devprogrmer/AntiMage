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
	s.recoverTelegramTrials(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.recoverTelegramShopOrders(ctx)
			s.recoverTelegramTrials(ctx)
		}
	}
}

func (s *Server) recoverTelegramTrials(ctx context.Context) {
	provisioner := botAccountProvisioner{server: s}
	workerCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	trials := shop.Trials{DB: s.db, Dialect: s.dialect}
	ids, err := trials.Unfulfilled(workerCtx, telegramShopRecoveryBatch)
	if err != nil {
		if ctx.Err() == nil {
			logging.Warnf(logging.ComponentTelegram, "trial recovery list failed: %v", err)
		}
		return
	}
	recovered := 0
	for _, id := range ids {
		if workerCtx.Err() != nil {
			break
		}
		if _, err := trials.Provision(workerCtx, id, provisioner); err != nil {
			if workerCtx.Err() == nil {
				logging.Warnf(logging.ComponentTelegram, "trial for Telegram ID %d delivery retry failed: %v", id, err)
			}
			continue
		}
		recovered++
	}
	if recovered > 0 {
		logging.Infof(logging.ComponentTelegram, "trial accounts recovered=%d", recovered)
	}
}

func (s *Server) recoverTelegramShopOrders(ctx context.Context) {
	workerCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := s.gatewayPayments().RecoverVerified(workerCtx); err != nil && ctx.Err() == nil {
		logging.Warnf(logging.ComponentTelegram, "gateway payment settlement retry failed: %v", err)
	}
	orders := shop.Orders{DB: s.db}
	wallet := shop.Wallet{DB: s.db, Dialect: s.dialect}
	debited, err := orders.DebitedPending(workerCtx, telegramShopRecoveryBatch, s.dialect)
	if err != nil {
		if ctx.Err() == nil {
			logging.Warnf(logging.ComponentTelegram, "shop recovery journal scan failed: %v", err)
		}
		return
	}
	for _, item := range debited {
		if workerCtx.Err() != nil {
			return
		}
		if _, err := orders.Pay(workerCtx, item.TelegramID, item.ID, wallet); err != nil {
			logging.Warnf(logging.ComponentTelegram, "shop order %s debit reconciliation failed: %v", item.ID, err)
		}
	}
	ids, err := orders.Unfulfilled(workerCtx, telegramShopRecoveryBatch)
	if err != nil {
		if ctx.Err() == nil {
			logging.Warnf(logging.ComponentTelegram, "shop recovery list failed: %v", err)
		}
		return
	}
	recovered := 0
	for _, id := range ids {
		if workerCtx.Err() != nil {
			break
		}
		if _, err := (botShopService{server: s}).deliverOrder(workerCtx, id); err != nil {
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

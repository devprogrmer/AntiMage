package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/antimage/antimage/internal/app/shop"
	"github.com/google/uuid"
	"net/http"
	"net/url"
)

func (s *Server) gatewayPayments() shop.GatewayPayments {
	return shop.GatewayPayments{DB: s.db, Dialect: s.dialect, Factory: s.shopGatewayFactory}
}
func (s *Server) gatewayEnabled() bool {
	u, err := url.Parse(s.cfg.ShopPaymentCallbackURL)
	return uuid.Validate(s.cfg.ShopZarinpalMerchantID) == nil && err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == "" && u.Path == "/telegram-payment-return"
}
func (s *Server) createGatewayPayment(ctx context.Context, owner, amount int64, key string) (shop.GatewayPayment, error) {
	if !s.gatewayEnabled() {
		return shop.GatewayPayment{}, shop.ErrPaymentUnavailable
	}
	return s.gatewayPayments().Create(ctx, owner, amount, key, s.cfg.ShopZarinpalMerchantID, s.cfg.ShopZarinpalSandbox, s.cfg.ShopPaymentCallbackURL)
}

func (s *Server) handleTelegramGatewayAPI(w http.ResponseWriter, r *http.Request, owner int64) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	var payment shop.GatewayPayment
	var err error
	if r.URL.Path == "/api/telegram-app/gateway" {
		var input struct {
			AmountToman int64  `json:"amount_toman"`
			RequestKey  string `json:"request_key"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || uuid.Validate(input.RequestKey) != nil {
			writeError(w, 400, "Invalid payment request")
			return
		}
		payment, err = s.createGatewayPayment(r.Context(), owner, input.AmountToman, "tg-gateway:"+input.RequestKey)
	} else if r.URL.Path == "/api/telegram-app/gateway/verify" {
		var input struct {
			PaymentID string `json:"payment_id"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || uuid.Validate(input.PaymentID) != nil {
			writeError(w, 400, "Invalid payment request")
			return
		}
		payment, err = s.gatewayPayments().Check(r.Context(), owner, input.PaymentID)
	} else {
		writeError(w, 404, "Not found")
		return
	}
	if errors.Is(err, shop.ErrPaymentUnavailable) {
		writeError(w, 404, "Payment unavailable")
		return
	}
	if errors.Is(err, shop.ErrReferenceConflict) {
		writeError(w, 409, "Payment request conflict")
		return
	}
	if err != nil {
		writeError(w, 503, "Payment not confirmed; retry the same payment")
		return
	}
	writeJSON(w, 200, payment)
}

func (s *Server) handleTelegramPaymentReturn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	id := r.URL.Query().Get("payment")
	if uuid.Validate(id) != nil {
		writeError(w, 400, "Invalid payment")
		return
	}
	success := false
	if r.URL.Query().Get("Status") == "OK" {
		payment, err := s.gatewayPayments().Verify(r.Context(), id, r.URL.Query().Get("Authority"))
		success = err == nil && payment.Status == "paid"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	message := "پرداخت تأیید نشد. وضعیت را از داخل ربات دوباره بررسی کنید.<hr>Payment not confirmed. Check its status in the bot.<hr>Платёж не подтверждён. Проверьте статус в боте."
	if success {
		message = "پرداخت تأیید شد و کیف پول شارژ شد. به ربات برگردید و سفارش را پرداخت کنید.<hr>Payment confirmed; wallet credited. Return to the bot to pay your order.<hr>Платёж подтверждён, кошелёк пополнен. Вернитесь в бот для оплаты заказа."
	}
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>AntiMage payment</title><main style="max-width:600px;margin:10vh auto;padding:24px;font:18px/1.8 system-ui"><h1>AntiMage</h1><p dir="auto">%s</p></main>`, message)
}

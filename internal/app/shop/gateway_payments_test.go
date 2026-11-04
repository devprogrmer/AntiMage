package shop

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type fakeGateway struct {
	requests, verifies int
	fail               bool
	reference          string
}

func (g *fakeGateway) Request(_ context.Context, amount int64, callback, id string) (string, error) {
	g.requests++
	if amount != 12340 || callback == "" || id == "" {
		return "", fmt.Errorf("incorrect request snapshot")
	}
	return "S000000000000000000000000000000001", nil
}
func (g *fakeGateway) Verify(_ context.Context, amount int64, authority string) (string, error) {
	g.verifies++
	if g.fail || amount != 12340 || authority != "S000000000000000000000000000000001" {
		return "", fmt.Errorf("not paid")
	}
	return g.reference, nil
}
func (g *fakeGateway) CheckoutURL(a string) string {
	return "https://sandbox.zarinpal.com/pg/StartPay/" + a
}

func TestGatewayRequiresVerificationAndRecoversCreditOnce(t *testing.T) {
	db, _ := paymentDB(t)
	if _, err := db.Exec(gatewayPaymentSchema); err != nil {
		t.Fatal(err)
	}
	g := &fakeGateway{reference: "201"}
	s := GatewayPayments{DB: db, Dialect: "sqlite", Factory: func(string, bool) PaymentGateway { return g }}
	ctx := context.Background()
	p, err := s.Create(ctx, 123, 1234, "gateway-request", "11111111-2222-3333-4444-555555555555", true, "https://example.com/telegram-payment-return")
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.Create(ctx, 123, 1234, "gateway-request", "11111111-2222-3333-4444-555555555555", true, "https://example.com/telegram-payment-return"); err != nil || replay.ID != p.ID || g.requests != 1 {
		t.Fatalf("replay=%+v %v requests=%d", replay, err, g.requests)
	}
	if _, err := s.Create(ctx, 456, 1234, "gateway-request", "11111111-2222-3333-4444-555555555555", true, "https://example.com/telegram-payment-return"); !errors.Is(err, ErrReferenceConflict) {
		t.Fatalf("cross-owner replay=%v", err)
	}
	if _, err := s.Verify(ctx, p.ID, "S000000000000000000000000000000999"); !errors.Is(err, ErrPaymentUnavailable) {
		t.Fatalf("forged authority=%v", err)
	}
	g.fail = true
	if _, err := s.Verify(ctx, p.ID, "S000000000000000000000000000000001"); err == nil {
		t.Fatal("unverified payment accepted")
	}
	if balance, _ := (Wallet{DB: db, Dialect: "sqlite"}).Balance(ctx, 123); balance != 100000 {
		t.Fatal("unverified payment credited")
	}
	g.fail = false
	if _, err := db.Exec(`CREATE TRIGGER fail_gateway_paid BEFORE UPDATE OF status ON shop_gateway_payments WHEN NEW.status='paid' BEGIN SELECT RAISE(ABORT,'injected settlement failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, p.ID, "S000000000000000000000000000000001"); err == nil {
		t.Fatal("settlement failure ignored")
	}
	if balance, _ := (Wallet{DB: db, Dialect: "sqlite"}).Balance(ctx, 123); balance != 101234 {
		t.Fatalf("credited balance=%d", balance)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_gateway_paid`); err != nil {
		t.Fatal(err)
	}
	// A new service instance replays only durable, provider-verified records.
	restarted := GatewayPayments{DB: db, Dialect: "sqlite", Factory: s.Factory}
	if err := restarted.RecoverVerified(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if paid, err := restarted.Verify(ctx, p.ID, "S000000000000000000000000000000001"); err != nil || paid.Status != "paid" {
			t.Fatalf("replay=%+v %v", paid, err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shop_wallet_entries WHERE reference LIKE 'gateway:%'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("gateway journal=%d %v", count, err)
	}
	if balance, _ := (Wallet{DB: db, Dialect: "sqlite"}).Balance(ctx, 123); balance != 101234 {
		t.Fatalf("duplicate credit=%d", balance)
	}
}

const gatewayPaymentSchema = `CREATE TABLE shop_gateway_payments (
id VARCHAR(36) PRIMARY KEY, request_key VARCHAR(128) NOT NULL UNIQUE, telegram_id BIGINT NOT NULL,
amount_toman BIGINT NOT NULL, merchant_id VARCHAR(36) NOT NULL, sandbox BOOLEAN NOT NULL,
status VARCHAR(16) NOT NULL, authority VARCHAR(128) UNIQUE, reference VARCHAR(128) UNIQUE,
lease_until BIGINT NOT NULL DEFAULT 0, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`

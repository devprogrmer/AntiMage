package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/antimage/antimage/internal/app/shop"
	"github.com/google/uuid"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type apiGateway struct {
	verified           bool
	requests, verifies int
	callback           string
	amount             int64
}

func (g *apiGateway) Request(_ context.Context, amount int64, callback, id string) (string, error) {
	g.requests++
	g.callback = callback
	g.amount = amount
	return "S000000000000000000000000000000001", nil
}
func (g *apiGateway) Verify(_ context.Context, amount int64, authority string) (string, error) {
	g.verifies++
	if !g.verified || amount != g.amount {
		return "", fmt.Errorf("not verified")
	}
	return "123456", nil
}
func (g *apiGateway) CheckoutURL(a string) string {
	return "https://sandbox.zarinpal.com/pg/StartPay/" + a
}

func TestGatewayAPIAuthenticatesAndDoesNotTrustCallbackStatus(t *testing.T) {
	dir := t.TempDir()
	merchant := "11111111-2222-3333-4444-555555555555"
	s, err := New(Config{Database: "sqlite:///" + filepath.ToSlash(filepath.Join(dir, "gateway.db")), CertificateBase: filepath.Join(dir, "certs"), ExternalAppsBase: filepath.Join(dir, "apps"), ShopZarinpalMerchantID: merchant, ShopZarinpalSandbox: true, ShopPaymentCallbackURL: "https://panel.example/telegram-payment-return"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if _, err := s.telegramRepo.Settings(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE telegram_settings SET use_telegram=1,api_token='gateway-test'`); err != nil {
		t.Fatal(err)
	}
	g := &apiGateway{}
	s.shopGatewayFactory = func(string, bool) shop.PaymentGateway { return g }
	request := func(method, path, body string, owner int64) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if owner > 0 {
			r.Header.Set("X-Telegram-Init-Data", miniAppTestInitData(owner, "gateway-test"))
		}
		r.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, r)
		return rec
	}
	body := fmt.Sprintf(`{"amount_toman":1234,"request_key":%q}`, uuid.NewString())
	if rec := request("POST", "/api/telegram-app/gateway", body, 0); rec.Code != 401 {
		t.Fatalf("unauthenticated=%d", rec.Code)
	}
	rec := request("POST", "/api/telegram-app/gateway", body, 123)
	if rec.Code != 200 {
		t.Fatalf("create=%d %s", rec.Code, rec.Body.String())
	}
	var p shop.GatewayPayment
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if g.amount != 12340 || !strings.Contains(g.callback, "payment="+p.ID) || strings.Contains(rec.Body.String(), merchant) {
		t.Fatalf("snapshot/link=%d %s %s", g.amount, g.callback, rec.Body.String())
	}
	if rec := request("POST", "/api/telegram-app/gateway", body, 123); rec.Code != 200 || g.requests != 1 {
		t.Fatal("checkout duplicated")
	}
	verify := fmt.Sprintf(`{"payment_id":%q}`, p.ID)
	if rec := request("POST", "/api/telegram-app/gateway/verify", verify, 456); rec.Code != 404 || g.verifies != 0 {
		t.Fatalf("cross-owner verify=%d", rec.Code)
	}
	if rec := request("GET", "/api/telegram-app/state", "", 456); strings.Contains(rec.Body.String(), p.ID) {
		t.Fatal("payment leaked to another customer")
	}
	callback := "/telegram-payment-return?payment=" + p.ID + "&Authority=S000000000000000000000000000000001&Status=OK"
	request("GET", callback, "", 0)
	if balance, err := (shop.Wallet{DB: s.db, Dialect: s.dialect}).Balance(t.Context(), 123); err != nil || balance != 0 {
		t.Fatalf("forged OK credited wallet=%d %v", balance, err)
	}
	g.verified = true
	for i := 0; i < 3; i++ {
		if rec := request("GET", callback, "", 0); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Payment confirmed") {
			t.Fatalf("callback=%d %s", rec.Code, rec.Body.String())
		}
	}
	if balance, err := (shop.Wallet{DB: s.db, Dialect: s.dialect}).Balance(t.Context(), 123); err != nil || balance != 1234 {
		t.Fatalf("balance=%d %v", balance, err)
	}
	if rec := request("POST", "/api/telegram-app/gateway/verify", verify, 123); rec.Code != 200 {
		t.Fatalf("check payment=%d", rec.Code)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM shop_wallet_entries`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("journal=%d %v", count, err)
	}
}

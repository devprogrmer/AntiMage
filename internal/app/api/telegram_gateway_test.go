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
	// Spend the verified payment through the real user service and node outbox,
	// rather than replacing account creation with a fake provisioner.
	for _, q := range []string{
		`INSERT INTO admins(id,username,hashed_password,role,permissions,status) VALUES(1,'owner','unused','full_access','{}','active')`,
		`INSERT INTO services(id,name) VALUES(1,'Gateway service')`,
		`INSERT INTO inbounds(tag) VALUES('gateway-in')`,
		`INSERT INTO xray_config(id,data) VALUES(1,'{"inbounds":[{"tag":"gateway-in","protocol":"vless","port":443,"settings":{"decryption":"none"}}]}') ON CONFLICT(id) DO UPDATE SET data=excluded.data`,
		`INSERT INTO hosts(id,inbound_tag,remark,address,is_disabled) VALUES(1,'gateway-in','main','example.com',0)`,
		`INSERT INTO service_hosts(service_id,host_id,sort) VALUES(1,1,0)`,
		`INSERT INTO nodes(id,name,address,port,api_port,status) VALUES(1,'Test node','127.0.0.1',62050,62051,'connected')`,
		`INSERT INTO shop_products(id,service_id,title,price_toman,data_limit_bytes,duration_days,enabled,created_at) VALUES(1,1,'Gateway plan',1000,1073741824,30,1,'2026-01-01')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	orderResponse := request("POST", "/api/telegram-app/orders", fmt.Sprintf(`{"product_id":1,"request_key":%q}`, uuid.NewString()), 123)
	if orderResponse.Code != 200 {
		t.Fatalf("order=%d %s", orderResponse.Code, orderResponse.Body.String())
	}
	var order shop.Order
	if err := json.Unmarshal(orderResponse.Body.Bytes(), &order); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		rec := request("POST", "/api/telegram-app/pay", fmt.Sprintf(`{"order_id":%q}`, order.ID), 123)
		if rec.Code != 200 {
			t.Fatalf("purchase=%d %s", rec.Code, rec.Body.String())
		}
	}
	if balance, err := (shop.Wallet{DB: s.db, Dialect: s.dialect}).Balance(t.Context(), 123); err != nil || balance != 234 {
		t.Fatalf("purchase balance=%d %v", balance, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE telegram_id=123 AND status='on_hold' AND data_limit=1073741824 AND on_hold_expire_duration=2592000`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("real provisioned accounts=%d %v", count, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM node_operations WHERE operation_type='add_user'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("node outbox=%d %v", count, err)
	}
}

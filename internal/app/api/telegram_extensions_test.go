package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTelegramExtensionRecoveryKeepsUsageAndAppliesOnce(t *testing.T) {
	dir := t.TempDir()
	config := Config{Database: "sqlite:///" + filepath.ToSlash(filepath.Join(dir, "extensions.db")), CertificateBase: filepath.Join(dir, "certs"), ExternalAppsBase: filepath.Join(dir, "apps")}
	s, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.db.Close()
		}
	}()
	if _, err := s.telegramRepo.Settings(t.Context()); err != nil {
		t.Fatal(err)
	}
	expire := time.Now().UTC().Unix() + 86400
	for _, q := range []string{
		`UPDATE telegram_settings SET api_token='extension-test',use_telegram=1`,
		`INSERT INTO admins(id,username,hashed_password,role,permissions,status) VALUES(1,'owner','unused','full_access','{}','active')`,
		`INSERT INTO inbounds(tag) VALUES('extension-in')`,
		`INSERT INTO xray_config(id,data) VALUES(1,'{"inbounds":[{"tag":"extension-in","protocol":"vless","port":443,"settings":{"decryption":"none"}}]}') ON CONFLICT(id) DO UPDATE SET data=excluded.data`,
		`INSERT INTO services(id,name) VALUES(1,'Extension service')`,
		`INSERT INTO hosts(id,inbound_tag,remark,address,is_disabled) VALUES(1,'extension-in','main','example.com',0)`,
		`INSERT INTO service_hosts(service_id,host_id,sort) VALUES(1,1,0)`,
		`INSERT INTO nodes(id,name,address,port,api_port,status) VALUES(1,'Node','127.0.0.1',62050,62051,'connected')`,
		fmt.Sprintf(`INSERT INTO users(username,credential_key,status,used_traffic,data_limit,expire,admin_id,service_id,telegram_id) VALUES('alice','00112233445566778899aabbccddeeff','active',100,1000,%d,1,1,123)`, expire),
		`INSERT INTO shop_products(id,service_id,title,price_toman,data_limit_bytes,duration_days,enabled,created_at) VALUES(1,1,'Extension',1000,100,30,1,'2026-01-01')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := (botShopService{server: s}).AdminCredit(t.Context(), 123, 10000, "extension-test"); err != nil {
		t.Fatal(err)
	}
	request := func(path, body string, id int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("X-Telegram-Init-Data", miniAppTestInitData(id, "extension-test"))
		req.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	body := fmt.Sprintf(`{"product_id":1,"username":"alice","action":"renew","request_key":%q}`, uuid.NewString())
	if rec := request("/api/telegram-app/orders", body, 456); rec.Code != 404 {
		t.Fatalf("cross-owner=%d %s", rec.Code, rec.Body.String())
	}
	rec := request("/api/telegram-app/orders", body, 123)
	if rec.Code != 200 {
		t.Fatalf("place=%d %s", rec.Code, rec.Body.String())
	}
	var order struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil || order.ID == "" {
		t.Fatalf("order=%+v %v", order, err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_extension BEFORE UPDATE OF status ON shop_orders WHEN NEW.status='completed' BEGIN SELECT RAISE(ABORT,'injected completion failure'); END`); err != nil {
		t.Fatal(err)
	}
	pay := fmt.Sprintf(`{"order_id":%q}`, order.ID)
	if rec := request("/api/telegram-app/pay", pay, 123); rec.Code != 503 {
		t.Fatalf("injected failure=%d %s", rec.Code, rec.Body.String())
	}
	check := func(wantLimit, wantExpire, wantBalance, wantQueue int64) {
		t.Helper()
		var limit, used, expiry, balance, queued int64
		if err := s.db.QueryRow(`SELECT data_limit,used_traffic,expire FROM users WHERE username='alice'`).Scan(&limit, &used, &expiry); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT balance FROM shop_wallet_accounts WHERE telegram_id=123`).Scan(&balance); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM node_operations WHERE user_id=(SELECT id FROM users WHERE username='alice')`).Scan(&queued); err != nil {
			t.Fatal(err)
		}
		if limit != wantLimit || used != 100 || expiry != wantExpire || balance != wantBalance || queued != wantQueue {
			t.Fatalf("limit=%d usage=%d expire=%d balance=%d queue=%d", limit, used, expiry, balance, queued)
		}
	}
	check(1000, expire, 9000, 0)
	if _, err := s.db.Exec(`DROP TRIGGER fail_extension`); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	// Use a fresh pool for the same file, as a new process would. New caches
	// pools by URL, including closed pools inside this test process.
	config.Database += "?cache=private"
	s, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	s.recoverTelegramShopOrders(t.Context())
	check(1100, expire+30*86400, 9000, 1)
	for i := 0; i < 3; i++ {
		if rec := request("/api/telegram-app/pay", pay, 123); rec.Code != 200 {
			t.Fatalf("retry=%d %s", rec.Code, rec.Body.String())
		}
	}
	check(1100, expire+30*86400, 9000, 1)
	body = fmt.Sprintf(`{"product_id":1,"username":"alice","action":"volume","request_key":%q}`, uuid.NewString())
	rec = request("/api/telegram-app/orders", body, 123)
	if rec.Code != 200 {
		t.Fatalf("volume order=%d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil {
		t.Fatal(err)
	}
	pay = fmt.Sprintf(`{"order_id":%q}`, order.ID)
	if rec := request("/api/telegram-app/pay", pay, 123); rec.Code != 200 {
		t.Fatalf("volume payment=%d %s", rec.Code, rec.Body.String())
	}
	check(1200, expire+30*86400, 8000, 2)
}

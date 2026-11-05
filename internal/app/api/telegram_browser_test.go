package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/shop"
)

// Opt-in browser fixture: only the Telegram SDK is replaced with a signed test
// identity. Every shop/API/database operation uses the real application code.
func TestTelegramMiniAppBrowserPreview(t *testing.T) {
	if os.Getenv("ANTIMAGE_TEST_MINIAPP_BROWSER") != "1" {
		t.Skip("opt-in local browser fixture")
	}
	dir := t.TempDir()
	s, err := New(Config{Database: "sqlite:///" + filepath.ToSlash(filepath.Join(dir, "browser.db")), CertificateBase: filepath.Join(dir, "certs"), ExternalAppsBase: filepath.Join(dir, "apps")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if _, err := s.telegramRepo.Settings(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE telegram_settings SET api_token='browser-test',use_telegram=1`,
		`INSERT INTO admins(id,username,hashed_password,role,permissions,status) VALUES(1,'owner','unused','full_access','{}','active')`,
		`INSERT INTO services(id,name) VALUES(1,'Browser service')`,
		`INSERT INTO inbounds(tag) VALUES('browser-in')`,
		`INSERT INTO xray_config(id,data) VALUES(1,'{"inbounds":[{"tag":"browser-in","protocol":"vless","port":443,"settings":{"decryption":"none"}}]}') ON CONFLICT(id) DO UPDATE SET data=excluded.data`,
		`INSERT INTO hosts(id,inbound_tag,remark,address,is_disabled) VALUES(1,'browser-in','main','example.com',0)`,
		`INSERT INTO service_hosts(service_id,host_id,sort) VALUES(1,1,0)`,
		`INSERT INTO nodes(id,name,address,port,api_port,status) VALUES(1,'Test node','127.0.0.1',62050,62051,'connected')`,
		`INSERT INTO users(username,credential_key,status,used_traffic,data_limit,expire,admin_id,service_id,telegram_id) VALUES('test_alice','00112233445566778899aabbccddeeff','active',1073741824,10737418240,2000000000,1,1,123)`,
		`INSERT INTO shop_products(id,service_id,title,price_toman,data_limit_bytes,duration_days,enabled,created_at) VALUES(1,1,'Starter 10 GB',80000,10737418240,30,1,'2026-01-01')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (shop.Wallet{DB: s.db, Dialect: s.dialect}).Credit(t.Context(), 123, 500000, "browser-test"); err != nil {
		t.Fatal(err)
	}
	page, err := telegramMiniAppFiles.ReadFile("telegram_miniapp.html")
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := json.Marshal(miniAppTestInitData(123, "browser-test"))
	shim := fmt.Sprintf(`<script>window.Telegram={WebApp:{initData:%s,initDataUnsafe:{user:{language_code:'fa'}},ready(){},openLink(u){window.open(u,'_blank','noopener')}}};</script>`, identity)
	html := strings.Replace(string(page), `<script src="https://telegram.org/js/telegram-web-app.js"></script>`, shim, 1)
	finished := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/telegram-app", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})
	mux.HandleFunc("/__test/finish", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		once.Do(func() { close(finished) })
		w.WriteHeader(204)
	})
	mux.Handle("/", s.Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Logf("Browser fixture: %s/telegram-app", srv.URL)
	select {
	case <-finished:
	case <-time.After(15 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
	var limit, usage, expiry int64
	if err := s.db.QueryRow(`SELECT data_limit,used_traffic,expire FROM users WHERE username='test_alice'`).Scan(&limit, &usage, &expiry); err != nil {
		t.Fatal(err)
	}
	if limit != 30*(1<<30) || usage != 1<<30 || expiry != 2000000000+30*86400 {
		t.Fatalf("browser flow incomplete: limit=%d usage=%d expiry=%d", limit, usage, expiry)
	}
}

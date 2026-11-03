package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func miniAppTestInitData(id int64, token string) string {
	user := fmt.Sprintf(`{"id":%d}`, id)
	date := fmt.Sprintf("%d", time.Now().Unix())
	data := "auth_date=" + date + "\nuser=" + user
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(data))
	return "auth_date=" + date + "&user=" + url.QueryEscape(user) + "&hash=" + hex.EncodeToString(mac.Sum(nil))
}

func TestTelegramMiniAppOrderOwnershipAndReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Config{Database: "sqlite:///" + filepath.ToSlash(filepath.Join(dir, "miniapp.db")), CertificateBase: filepath.Join(dir, "certs"), ExternalAppsBase: filepath.Join(dir, "apps")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.db.Close() })
	if _, err := s.telegramRepo.Settings(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE telegram_settings SET api_token = 'mini-test-token', use_telegram = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO shop_products (service_id,title,price_toman,data_limit_bytes,duration_days,enabled,created_at) VALUES (1,'Starter',1000,1073741824,30,1,'2026-01-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	initA, initB := miniAppTestInitData(123, "mini-test-token"), miniAppTestInitData(456, "mini-test-token")
	request := func(method, path, body, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Telegram-Init-Data", auth)
		req.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	if got := request(http.MethodGet, "/api/telegram-app/state", "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("missing initData: %d", got.Code)
	}
	if got := request(http.MethodGet, "/api/telegram-app/state", "", initA+"&user=forged"); got.Code != http.StatusUnauthorized {
		t.Fatalf("forged initData: %d", got.Code)
	}
	if got := request(http.MethodGet, "/api/telegram-app/state", "", initA); got.Code != http.StatusOK {
		t.Fatalf("state: %d %s", got.Code, got.Body.String())
	}
	key := uuid.NewString()
	body := fmt.Sprintf(`{"product_id":1,"request_key":%q}`, key)
	first := request(http.MethodPost, "/api/telegram-app/orders", body, initA)
	if first.Code != http.StatusOK {
		t.Fatalf("place: %d %s", first.Code, first.Body.String())
	}
	var order struct{ ID string }
	if err := json.Unmarshal(first.Body.Bytes(), &order); err != nil || order.ID == "" {
		t.Fatalf("order: %v %s", err, first.Body.String())
	}
	replay := request(http.MethodPost, "/api/telegram-app/orders", body, initA)
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), order.ID) {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	if got := request(http.MethodPost, "/api/telegram-app/orders", body, initB); got.Code != http.StatusConflict {
		t.Fatalf("other customer reused order key: %d", got.Code)
	}
	stateB := request(http.MethodGet, "/api/telegram-app/state", "", initB)
	if stateB.Code != http.StatusOK || strings.Contains(stateB.Body.String(), order.ID) {
		t.Fatalf("other customer saw order: %d %s", stateB.Code, stateB.Body.String())
	}
	if got := request(http.MethodPost, "/api/telegram-app/pay", fmt.Sprintf(`{"order_id":%q}`, order.ID), initB); got.Code != http.StatusNotFound {
		t.Fatalf("other customer paid order: %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, "/api/telegram-app/pay", fmt.Sprintf(`{"order_id":%q}`, order.ID), initA); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("order without active host: %d %s", got.Code, got.Body.String())
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM shop_orders`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("order count=%d err=%v", count, err)
	}
	if err := (botTopupService{server: s}).SetInstructions(t.Context(), "Bank transfer details"); err != nil {
		t.Fatal(err)
	}
	topupBody := fmt.Sprintf(`{"amount_toman":5000,"request_key":%q}`, uuid.NewString())
	topup := request(http.MethodPost, "/api/telegram-app/topups", topupBody, initA)
	if topup.Code != http.StatusOK {
		t.Fatalf("topup: %d %s", topup.Code, topup.Body.String())
	}
	if replay := request(http.MethodPost, "/api/telegram-app/topups", topupBody, initA); replay.Code != http.StatusOK || replay.Body.String() != topup.Body.String() {
		t.Fatalf("topup replay: %d %s", replay.Code, replay.Body.String())
	}
	if other := request(http.MethodGet, "/api/telegram-app/state", "", initB); strings.Contains(other.Body.String(), `"AmountToman":5000`) {
		t.Fatal("other customer saw top-up")
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM shop_wallet_entries`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unapproved topup credited wallet: %d, %v", count, err)
	}
	ticketBody := fmt.Sprintf(`{"message":"Need help","request_key":%q}`, uuid.NewString())
	ticket := request(http.MethodPost, "/api/telegram-app/tickets", ticketBody, initA)
	if ticket.Code != http.StatusOK {
		t.Fatalf("ticket: %d %s", ticket.Code, ticket.Body.String())
	}
	if replay := request(http.MethodPost, "/api/telegram-app/tickets", ticketBody, initA); replay.Code != http.StatusOK || replay.Body.String() != ticket.Body.String() {
		t.Fatalf("ticket replay: %d %s", replay.Code, replay.Body.String())
	}
	if other := request(http.MethodGet, "/api/telegram-app/state", "", initB); strings.Contains(other.Body.String(), "Need help") {
		t.Fatal("other customer saw ticket")
	}
	var createdTicket struct{ ID string }
	if err := json.Unmarshal(ticket.Body.Bytes(), &createdTicket); err != nil {
		t.Fatal(err)
	}
	if own := request(http.MethodGet, "/api/telegram-app/tickets/"+createdTicket.ID, "", initA); own.Code != http.StatusOK || !strings.Contains(own.Body.String(), "Need help") {
		t.Fatalf("own ticket: %d %s", own.Code, own.Body.String())
	}
	if other := request(http.MethodGet, "/api/telegram-app/tickets/"+createdTicket.ID, "", initB); other.Code != http.StatusNotFound {
		t.Fatalf("other customer read ticket: %d %s", other.Code, other.Body.String())
	}
}

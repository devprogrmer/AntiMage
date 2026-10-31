package user

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func newSubscriptionHWIDTestRepo(t *testing.T, limit int64) Repository {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/hwid.sqlite?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, device_limit INTEGER NOT NULL); CREATE TABLE user_subscription_hwids (user_id INTEGER NOT NULL, hwid_hash TEXT NOT NULL, first_used_at DATETIME NOT NULL, last_used_at DATETIME NOT NULL, PRIMARY KEY(user_id,hwid_hash)); INSERT INTO users(id,device_limit) VALUES(1,?)`, limit); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewRepository(db, "sqlite")
}

func TestRegisterSubscriptionHWIDEnforcesLimitAndStoresDigestOnly(t *testing.T) {
	repo := newSubscriptionHWIDTestRepo(t, 2)
	ctx := context.Background()
	for _, id := range []string{"phone-a", "phone-a", "phone-b"} {
		if err := repo.registerSubscriptionHWID(ctx, 1, id); err != nil {
			t.Fatalf("register %q: %v", id, err)
		}
	}
	if err := repo.registerSubscriptionHWID(ctx, 1, "phone-c"); err == nil || !strings.Contains(err.Error(), "Device limit reached") {
		t.Fatalf("fourth distinct device error=%v", err)
	}
	var count int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM user_subscription_hwids WHERE user_id=1`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("registered count=%d, err=%v", count, err)
	}
	var stored string
	if err := repo.db.QueryRow(`SELECT hwid_hash FROM user_subscription_hwids WHERE user_id=1 ORDER BY hwid_hash LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 64 || stored == "phone-a" || stored == fmt.Sprintf("%x", sha256.Sum256([]byte("phone-c"))) {
		t.Fatalf("unexpected persisted HWID value %q", stored)
	}
}

func TestRegisterSubscriptionHWIDRequiresHeaderOnlyWhenLimited(t *testing.T) {
	ctx := context.Background()
	if err := newSubscriptionHWIDTestRepo(t, 0).registerSubscriptionHWID(ctx, 1, ""); err != nil {
		t.Fatalf("unlimited user without HWID: %v", err)
	}
	err := newSubscriptionHWIDTestRepo(t, 1).registerSubscriptionHWID(ctx, 1, "")
	if err == nil || !strings.Contains(err.Error(), "HWID header required") {
		t.Fatalf("limited user missing HWID error=%v", err)
	}
}

func TestRegisterSubscriptionHWIDConcurrentRequestsRespectLimit(t *testing.T) {
	repo := newSubscriptionHWIDTestRepo(t, 1)
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for _, id := range []string{"device-a", "device-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if repo.registerSubscriptionHWID(context.Background(), 1, id) == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	var count int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM user_subscription_hwids WHERE user_id=1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if accepted != 1 || count != 1 {
		t.Fatalf("accepted=%d persisted=%d, want exactly 1", accepted, count)
	}
}

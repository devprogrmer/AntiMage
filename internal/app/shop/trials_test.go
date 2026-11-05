package shop

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestTrialClaimRestartAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trials.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		return db
	}
	db := open()
	for _, statement := range []string{
		`CREATE TABLE shop_trial_config (id INTEGER PRIMARY KEY, service_id BIGINT, data_limit_bytes BIGINT, duration_days BIGINT, enabled INTEGER)`,
		`CREATE TABLE shop_trial_claims (telegram_id BIGINT PRIMARY KEY, claim_id TEXT UNIQUE, service_id BIGINT, data_limit_bytes BIGINT, duration_days BIGINT, status TEXT, username TEXT, created_at TEXT, updated_at TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	store := Trials{DB: db, Dialect: "sqlite"}
	if _, err := store.Claim(ctx, 123); !errors.Is(err, ErrTrialUnavailable) {
		t.Fatalf("unconfigured trial: %v", err)
	}
	if err := store.Configure(ctx, TrialConfig{ServiceID: 42, DataLimitBytes: 1 << 30, DurationDays: 7}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, 123)
	if err != nil || claim.ServiceID != 42 || claim.Status != "pending" {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	if err := store.Configure(ctx, TrialConfig{ServiceID: 44, DataLimitBytes: 2 << 30, DurationDays: 14}); err != nil {
		t.Fatal(err)
	}
	claimAgain, err := store.Claim(ctx, 123)
	if err != nil || claimAgain.ClaimID != claim.ClaimID || claimAgain.ServiceID != 42 || claimAgain.DataLimitBytes != 1<<30 {
		t.Fatalf("duplicate or changed snapshot = %+v, %v", claimAgain, err)
	}
	creator := &fakeProvisioner{accounts: map[string]Account{}, fail: errors.New("delivery offline")}
	if _, err := store.Provision(ctx, 123, creator); err == nil {
		t.Fatal("failed delivery reported success")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	store.DB = db
	defer db.Close()
	creator.fail = nil
	ids, err := store.Unfulfilled(ctx, 10)
	if err != nil || len(ids) != 1 || ids[0] != 123 {
		t.Fatalf("recovery queue = %v, %v", ids, err)
	}
	username, err := store.Provision(ctx, 123, creator)
	if err != nil || len(username) != 34 || creator.creates != 2 {
		t.Fatalf("recovery = %q, creates=%d, %v", username, creator.creates, err)
	}
	if _, err := store.Provision(ctx, 123, creator); err != nil || creator.creates != 2 {
		t.Fatalf("repeat created account: %v, creates=%d", err, creator.creates)
	}
	if err := store.Disable(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, 456); !errors.Is(err, ErrTrialUnavailable) {
		t.Fatalf("disabled trial claim: %v", err)
	}
	if old, err := store.Claim(ctx, 123); err != nil || old.Username.String != username || old.Status != "completed" {
		t.Fatalf("old claim after disable = %+v, %v", old, err)
	}
	if ids, err := store.Unfulfilled(ctx, 10); err != nil || len(ids) != 0 {
		t.Fatalf("completed in recovery = %v, %v", ids, err)
	}
}

func TestTrialRejectsConflictingExistingAccount(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE shop_trial_config (id INTEGER PRIMARY KEY, service_id BIGINT, data_limit_bytes BIGINT, duration_days BIGINT, enabled INTEGER)`,
		`CREATE TABLE shop_trial_claims (telegram_id BIGINT PRIMARY KEY, claim_id TEXT UNIQUE, service_id BIGINT, data_limit_bytes BIGINT, duration_days BIGINT, status TEXT, username TEXT, created_at TEXT, updated_at TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	store := Trials{DB: db, Dialect: "sqlite"}
	if err := store.Configure(ctx, TrialConfig{ServiceID: 42, DataLimitBytes: 1 << 30, DurationDays: 7}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, 123)
	if err != nil {
		t.Fatal(err)
	}
	username := "tr" + claim.ClaimID[0:8] + claim.ClaimID[9:13] + claim.ClaimID[14:18] + claim.ClaimID[19:23] + claim.ClaimID[24:]
	creator := &fakeProvisioner{accounts: map[string]Account{username: {Username: username, TelegramID: 999, ServiceID: 42, DataLimitBytes: 1 << 30, OrderMarker: "someone-else"}}}
	if _, err := store.Provision(ctx, 123, creator); !errors.Is(err, ErrReferenceConflict) {
		t.Fatalf("conflicting account accepted: %v", err)
	}
	if got, err := store.Get(ctx, 123); err != nil || got.Status != "provisioning" {
		t.Fatalf("conflict marked complete: %+v, %v", got, err)
	}
}

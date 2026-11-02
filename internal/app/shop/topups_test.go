package shop

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestReceiptApprovalSurvivesRestartWithoutDoubleCredit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "topup.db")
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
		`CREATE TABLE shop_wallet_accounts (telegram_id BIGINT PRIMARY KEY, balance BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TABLE shop_wallet_entries (reference TEXT PRIMARY KEY, telegram_id BIGINT, delta BIGINT, reason TEXT, created_at TEXT)`,
		`CREATE TABLE shop_topup_requests (id TEXT PRIMARY KEY, telegram_id BIGINT, amount_toman BIGINT, status TEXT, receipt_file_id TEXT, reviewed_by BIGINT, request_key TEXT UNIQUE, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE shop_payment_settings (id INTEGER PRIMARY KEY, instructions TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	topups := Topups{DB: db, Dialect: "sqlite"}
	if err := topups.SetInstructions(ctx, "Bank transfer to 1234"); err != nil {
		t.Fatal(err)
	}
	if instructions, err := topups.Instructions(ctx); err != nil || instructions != "Bank transfer to 1234" {
		t.Fatalf("instructions = %q, %v", instructions, err)
	}
	request, err := topups.Create(ctx, 123, 80000, "telegram-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := topups.SubmitReceipt(ctx, 456, request.ID, "photo-1"); !errors.Is(err, ErrTopupUnavailable) {
		t.Fatalf("other customer submitted receipt: %v", err)
	}
	if _, err := topups.Approve(ctx, request.ID, 999, Wallet{DB: db, Dialect: "sqlite"}); !errors.Is(err, ErrTopupUnavailable) {
		t.Fatalf("unsubmitted receipt approved: %v", err)
	}
	if _, err := topups.SubmitReceipt(ctx, 123, request.ID, "photo-1"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	topups.DB = db
	wallet := Wallet{DB: db, Dialect: "sqlite"}
	// Simulate a crash after the approval claim and wallet credit.
	if _, err := db.Exec(`UPDATE shop_topup_requests SET status = 'approving' WHERE id = ?`, request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := wallet.Credit(ctx, 123, 80000, "topup:"+request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := topups.Reject(ctx, request.ID, 999); !errors.Is(err, ErrTopupUnavailable) {
		t.Fatalf("approval could be rejected after credit: %v", err)
	}
	approved, err := topups.Approve(ctx, request.ID, 999, wallet)
	if err != nil || approved.Status != "approved" {
		t.Fatalf("recovered approval = %+v, %v", approved, err)
	}
	if _, err := topups.Approve(ctx, request.ID, 999, wallet); err != nil {
		t.Fatalf("duplicate approval: %v", err)
	}
	if balance, err := wallet.Balance(ctx, 123); err != nil || balance != 80000 {
		t.Fatalf("balance = %d, %v", balance, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shop_wallet_entries WHERE reference = ?`, "topup:"+request.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("credit entries = %d, %v", count, err)
	}
	if again, err := topups.Create(ctx, 123, 80000, "telegram-1"); err != nil || again.ID != request.ID {
		t.Fatalf("request replay = %+v, %v", again, err)
	}
}

package api

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func TestResellerBudgetConcurrentGrantsCannotExceedLimit(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "budget.db")+"?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	for _, statement := range []string{
		`CREATE TABLE admins (id INTEGER PRIMARY KEY, role TEXT, status TEXT, data_limit BIGINT, created_traffic BIGINT)`,
		`CREATE TABLE admin_created_traffic_logs (admin_id BIGINT, service_id BIGINT, amount BIGINT, action TEXT, created_at TEXT)`,
		`INSERT INTO admins VALUES (1, 'reseller', 'active', 2000, 0)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				results <- false
				return
			}
			if err := reserveResellerBudgetTx(context.Background(), tx, 1, 1250); err != nil {
				_ = tx.Rollback()
				results <- false
				return
			}
			results <- tx.Commit() == nil
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent grants = %d, want 1", successes)
	}
	var spent, events int64
	if err := db.QueryRow(`SELECT created_traffic FROM admins WHERE id = 1`).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM admin_created_traffic_logs`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if spent != 1250 || events != 1 {
		t.Fatalf("spent=%d events=%d, want 1250 and 1", spent, events)
	}
}

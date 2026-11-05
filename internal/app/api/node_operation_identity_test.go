package api

import (
	"context"
	"database/sql"
	"testing"
)

func TestSeparateMutationsWithSameTimestampEnqueueSeparately(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE node_operations (id INTEGER PRIMARY KEY, operation_type TEXT, node_id BIGINT, user_id BIGINT, payload TEXT, status TEXT, attempts INTEGER, idempotency_key TEXT UNIQUE, created_at TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		// Separate state mutations may share a clock timestamp (notably on
		// Windows). This timestamp is metadata, not a request identity.
		if err := enqueueNodeOperationTx(context.Background(), tx, "sync_config", nil, nil, map[string]any{"source": "hosts", "service_ids": []int64{42}, "queued_at": "2026-11-04T12:00:00Z"}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM node_operations`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("distinct mutations queued=%d err=%v; want 2", count, err)
	}
}

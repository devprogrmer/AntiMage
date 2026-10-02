package migrations

import (
	"context"
	"testing"
)

func TestUsageBatchTombstonesUpgradeSQLite(t *testing.T) {
	ctx := context.Background()
	db := openSQLiteTestDB(t)
	if err := RunMigrationsTo(ctx, db, "sqlite", 60); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO node_usage_user_queue (node_id,batch_id,user_id,used_traffic,created_at) VALUES (7,'existing',10,500,CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrationsTo(ctx, db, "sqlite", 61); err != nil {
		t.Fatal(err)
	}
	assertTableColumns(t, ctx, db, "sqlite", "node_usage_batch_tombstones", []string{"node_id", "batch_id", "kind", "pruned"})
	var amount int64
	if err := db.QueryRowContext(ctx, `SELECT used_traffic FROM node_usage_user_queue WHERE batch_id='existing'`).Scan(&amount); err != nil || amount != 500 {
		t.Fatalf("existing usage=%d, %v", amount, err)
	}
	for _, values := range []struct {
		node        int
		batch, kind string
	}{{7, "existing", "user"}, {7, "existing", "outbound"}, {8, "existing", "user"}, {7, "different", "user"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO node_usage_batch_tombstones (node_id,batch_id,kind) VALUES (?,?,?)`, values.node, values.batch, values.kind); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO node_usage_batch_tombstones (node_id,batch_id,kind) VALUES (7,'existing','user')`); err == nil {
		t.Fatal("duplicate batch tombstone accepted")
	}
	if err := RunMigrationsTo(ctx, db, "sqlite", 61); err != nil {
		t.Fatal(err)
	}
	version, err := Version(ctx, db, "sqlite")
	if err != nil || version.GooseVersion != 61 {
		t.Fatalf("version=%+v, %v", version, err)
	}
}

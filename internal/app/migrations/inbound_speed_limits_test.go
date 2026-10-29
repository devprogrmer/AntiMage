package migrations

import (
	"context"
	"testing"
)

func TestInboundSpeedMigrationPreservesExistingCapsAndUsers(t *testing.T) {
	ctx := context.Background()
	db := openSQLiteTestDB(t)
	if err := RunMigrationsTo(ctx, db, "sqlite", 61); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE inbounds ADD COLUMN upload_speed_limit BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE inbounds ADD COLUMN download_speed_limit BIGINT NOT NULL DEFAULT 0`,
		`INSERT INTO inbounds(tag, upload_speed_limit, download_speed_limit) VALUES ('preserved', 8000000, 4000000)`,
		`INSERT INTO goose_db_version(version_id, is_applied) VALUES (75, 1)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := RunMigrations(ctx, db, "sqlite"); err != nil {
			t.Fatal(err)
		}
	}
	var upload, download int64
	if err := db.QueryRowContext(ctx, `SELECT upload_speed_limit, download_speed_limit FROM inbounds WHERE tag='preserved'`).Scan(&upload, &download); err != nil {
		t.Fatal(err)
	}
	if upload != 8000000 || download != 4000000 {
		t.Fatalf("existing cap changed: %d/%d", upload, download)
	}
}

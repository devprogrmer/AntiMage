package user

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	"github.com/antimage/antimage/internal/app/migrations"
	_ "modernc.org/sqlite"
)

func TestResellerChildDeleteRefundsOnlyUnusedTraffic(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "reseller-users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrations.RunMigrations(context.Background(), db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO admins (id, username, role, status) VALUES (10, 'seller', 'reseller', 'active')`,
		`INSERT INTO admins (id, username, created_by, role, status, traffic_limit_mode, created_traffic, data_limit) VALUES (11, 'worker', 'seller', 'standard', 'active', 'created_traffic', 25, 25)`,
		`INSERT INTO users (id, username, admin_id, status, data_limit, used_traffic) VALUES (100, 'subscriber', 11, 'active', 25, 10)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	actor := adminapp.Admin{ID: 11, Username: "worker", CreatedBy: "seller", Role: adminapp.RoleStandard}
	actor.Permissions.Users.Delete = true
	if _, err := NewRepository(db, "sqlite").deleteUserMutation(context.Background(), actor, "subscriber"); err != nil {
		t.Fatal(err)
	}
	var status string
	var used, allocated, deletedUsage int64
	if err := db.QueryRow(`SELECT status, used_traffic FROM users WHERE id = 100`).Scan(&status, &used); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT created_traffic, deleted_users_usage FROM admins WHERE id = 11`).Scan(&allocated, &deletedUsage); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" || used != 10 || allocated != 10 || deletedUsage != 10 {
		t.Fatalf("delete accounting: status=%s used=%d allocated=%d deleted=%d", status, used, allocated, deletedUsage)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	available, err := periodicResetBudgetAvailableTx(context.Background(), tx, 11, 15)
	if err != nil || !available {
		t.Fatalf("unused traffic was not released: available=%v err=%v", available, err)
	}
	tooMuch, err := periodicResetBudgetAvailableTx(context.Background(), tx, 11, 16)
	if err != nil || tooMuch {
		t.Fatalf("consumed traffic was refunded: available=%v err=%v", tooMuch, err)
	}
}

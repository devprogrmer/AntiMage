package user

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	"github.com/antimage/antimage/internal/app/migrations"
	_ "modernc.org/sqlite"
)

func TestResellerAndSubadminCannotDeleteUsers(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE admins (username TEXT, role TEXT); INSERT INTO admins VALUES ('seller', 'reseller')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	repo := Repository{}
	for _, actor := range []adminapp.Admin{
		{Role: adminapp.RoleReseller, Username: "seller"},
		{Role: adminapp.RoleStandard, Username: "worker", CreatedBy: "seller"},
	} {
		if err := repo.ensureResellerUserDeleteAllowedTx(context.Background(), tx, actor); err == nil || !strings.Contains(err.Error(), "cannot be deleted") {
			t.Fatalf("actor=%s delete error=%v", actor.Username, err)
		}
	}
	if err := repo.ensureResellerUserDeleteAllowedTx(context.Background(), tx, adminapp.Admin{Role: adminapp.RoleStandard, Username: "normal", CreatedBy: "root"}); err != nil {
		t.Fatalf("normal admin was blocked: %v", err)
	}
}

func TestResellerChildDeleteDoesNotEraseConsumedTraffic(t *testing.T) {
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
	if _, err := NewRepository(db, "sqlite").deleteUserMutation(context.Background(), actor, "subscriber"); err == nil {
		t.Fatal("reseller child deleted a user despite the hard restriction")
	}
	var status string
	var used, allocated int64
	if err := db.QueryRow(`SELECT status, used_traffic FROM users WHERE id = 100`).Scan(&status, &used); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT created_traffic FROM admins WHERE id = 11`).Scan(&allocated); err != nil {
		t.Fatal(err)
	}
	if status != "active" || used != 10 || allocated != 25 {
		t.Fatalf("delete changed accounting: status=%s used=%d allocated=%d", status, used, allocated)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	available, err := periodicResetBudgetAvailableTx(context.Background(), tx, 11, 25)
	if err != nil || available {
		t.Fatalf("periodic reset escaped exhausted child budget: available=%v err=%v", available, err)
	}
}

package backup

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// This test creates two disposable schemas on the configured test server.
func TestFullImportMySQLRollsBackAfterFileFailure(t *testing.T) {
	serverURL := os.Getenv("ANTIMAGE_TEST_PORTABLE_MYSQL")
	if serverURL == "" {
		t.Skip("set ANTIMAGE_TEST_PORTABLE_MYSQL to a disposable MySQL server URL")
	}
	u, err := url.Parse(serverURL)
	if err != nil || u.User == nil || u.Host == "" {
		t.Fatal("invalid portable backup test server URL")
	}
	password, _ := u.User.Password()
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net, cfg.Addr = u.User.Username(), password, "tcp", u.Host
	server, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	open := func(role string) (*sql.DB, string) {
		t.Helper()
		name := fmt.Sprintf("antimage_rollback_%s_%d", role, time.Now().UnixNano())
		if _, err := server.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := server.Exec("DROP DATABASE " + name); err != nil {
				t.Error(err)
			}
		})
		options := *cfg
		options.DBName = name
		db, err := sql.Open("mysql", options.FormatDSN())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.ExecContext(ctx, "CREATE TABLE items (name VARCHAR(64) NOT NULL)"); err != nil {
			t.Fatal(err)
		}
		copyURL := *u
		copyURL.Path = "/" + name
		return db, copyURL.String()
	}
	sourceDB, sourceURL := open("source")
	targetDB, targetURL := open("target")
	if _, err := sourceDB.ExecContext(ctx, "INSERT INTO items VALUES ('source')"); err != nil {
		t.Fatal(err)
	}
	if _, err := targetDB.ExecContext(ctx, "INSERT INTO items VALUES ('destination')"); err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(t.TempDir(), "source-files")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "value"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceEnv := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(sourceEnv, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	exporter := NewService(sourceDB, "mysql", sourceURL, WithFileRoots([]FileRoot{
		{ArchiveName: "etc_antimage", Path: sourceRoot},
		{ArchiveName: "antimage_env", Path: sourceEnv},
	}))
	archive, err := exporter.Export(ctx, ScopeFull)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(archive.Path)
	targetRoot := filepath.Join(t.TempDir(), "target-files")
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, "value"), []byte("destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	journalDir := filepath.Join(t.TempDir(), "restore-journal")
	importer := NewService(targetDB, "mysql", targetURL, WithFileRoots([]FileRoot{
		{ArchiveName: "etc_antimage", Path: targetRoot},
		{ArchiveName: "antimage_env", Path: filepath.Join(blocker, ".env")},
	}), WithRestoreJournalDir(journalDir))
	if _, err := importer.Import(ctx, archive.Path); err == nil {
		t.Fatal("expected file restore to fail")
	}
	var name string
	if err := targetDB.QueryRowContext(ctx, "SELECT name FROM items").Scan(&name); err != nil || name != "destination" {
		t.Fatalf("database was not rolled back: %q, %v", name, err)
	}
	value, err := os.ReadFile(filepath.Join(targetRoot, "value"))
	if err != nil || string(value) != "destination" {
		t.Fatalf("files were not rolled back: %q, %v", value, err)
	}
	if _, err := os.Stat(journalDir); !os.IsNotExist(err) {
		t.Fatalf("journal remains after rollback: %v", err)
	}
}

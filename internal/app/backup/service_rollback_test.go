package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestFullImportRollsBackDatabaseAndFilesWhenFileRestoreFails(t *testing.T) {
	ctx := context.Background()
	sourcePath := filepath.Join(t.TempDir(), "source.sqlite3")
	sourceDB := openRollbackSQLite(t, sourcePath)
	defer sourceDB.Close()
	if _, err := sourceDB.Exec(`CREATE TABLE items (name TEXT); INSERT INTO items VALUES ('source')`); err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(t.TempDir(), "source-files")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "value"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherSource := filepath.Join(t.TempDir(), "other-source")
	if err := os.WriteFile(otherSource, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	exporter := NewService(sourceDB, "sqlite", rollbackSQLiteURL(sourcePath), WithFileRoots([]FileRoot{
		{ArchiveName: "etc_antimage", Path: sourceRoot},
		{ArchiveName: "antimage_env", Path: otherSource},
	}))
	archive, err := exporter.Export(ctx, ScopeFull)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(archive.Path)

	targetRoot := filepath.Join(t.TempDir(), "target-files")
	if err := os.MkdirAll(filepath.Join(targetRoot, "database"), 0o755); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(targetRoot, "database", "target.sqlite3")
	targetDB := openRollbackSQLite(t, targetPath)
	defer targetDB.Close()
	if _, err := targetDB.Exec(`CREATE TABLE items (name TEXT); INSERT INTO items VALUES ('destination')`); err != nil {
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
	importer := NewService(targetDB, "sqlite", rollbackSQLiteURL(targetPath), WithFileRoots([]FileRoot{
		{ArchiveName: "etc_antimage", Path: targetRoot},
		{ArchiveName: "antimage_env", Path: filepath.Join(blocker, ".env")},
	}), WithRestoreJournalDir(journalDir))
	if _, err := importer.Import(ctx, archive.Path); err == nil {
		t.Fatal("expected file restore to fail")
	}
	var name string
	if err := targetDB.QueryRow(`SELECT name FROM items`).Scan(&name); err != nil || name != "destination" {
		t.Fatalf("database was not rolled back: %q, %v", name, err)
	}
	value, err := os.ReadFile(filepath.Join(targetRoot, "value"))
	if err != nil || string(value) != "destination" {
		t.Fatalf("files were not rolled back: %q, %v", value, err)
	}
	if _, err := os.Stat(journalDir); !os.IsNotExist(err) {
		t.Fatalf("journal remains after successful rollback: %v", err)
	}
}

func openRollbackSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	return db
}

func rollbackSQLiteURL(path string) string {
	return "sqlite:///" + filepath.ToSlash(path)
}

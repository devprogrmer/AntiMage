package backup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInterruptedFullRestoreRecoversAfterProcessExit(t *testing.T) {
	if os.Getenv("ANTIMAGE_RESTORE_CRASH_HELPER") == "1" {
		dir := os.Getenv("ANTIMAGE_RESTORE_TEST_DIR")
		dbPath := filepath.Join(dir, "destination.sqlite3")
		fileRoot := filepath.Join(dir, "destination-files")
		journalDir := filepath.Join(dir, "restore-journal")
		db := openRollbackSQLite(t, dbPath)
		service := NewService(db, "sqlite", rollbackSQLiteURL(dbPath), WithFileRoots([]FileRoot{{ArchiveName: "etc_antimage", Path: fileRoot}}), WithRestoreJournalDir(journalDir))
		prepared, err := service.createRestoreJournalDir()
		if err != nil {
			t.Fatal(err)
		}
		payload, err := service.exportDatabasePayload(context.Background(), prepared)
		if err != nil {
			t.Fatal(err)
		}
		snapshotSource := filepath.Join(dir, "archive-files", "etc_antimage")
		if err := os.MkdirAll(snapshotSource, 0o700); err != nil {
			t.Fatal(err)
		}
		roots, err := service.snapshotFileRoots(filepath.Dir(snapshotSource), filepath.Join(prepared, "files"))
		if err != nil {
			t.Fatal(err)
		}
		if err := service.activateRestoreJournal(prepared, payload, roots); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DELETE FROM items; INSERT INTO items VALUES ('imported')`); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fileRoot, "value"), []byte("imported"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		os.Exit(0) // The next process must recover without an in-memory defer.
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "destination.sqlite3")
	fileRoot := filepath.Join(dir, "destination-files")
	if err := os.MkdirAll(fileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fileRoot, "value"), []byte("destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := openRollbackSQLite(t, dbPath)
	if _, err := db.Exec(`CREATE TABLE items (name TEXT); INSERT INTO items VALUES ('destination')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestInterruptedFullRestoreRecoversAfterProcessExit$")
	cmd.Env = append(os.Environ(), "ANTIMAGE_RESTORE_CRASH_HELPER=1", "ANTIMAGE_RESTORE_TEST_DIR="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v\n%s", err, output)
	}
	db = openRollbackSQLite(t, dbPath)
	defer db.Close()
	service := NewService(db, "sqlite", rollbackSQLiteURL(dbPath), WithFileRoots([]FileRoot{{ArchiveName: "etc_antimage", Path: fileRoot}}), WithRestoreJournalDir(filepath.Join(dir, "restore-journal")))
	if err := service.RecoverInterruptedRestore(context.Background()); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM items`).Scan(&name); err != nil || name != "destination" {
		t.Fatalf("database after restart: %q, %v", name, err)
	}
	data, err := os.ReadFile(filepath.Join(fileRoot, "value"))
	if err != nil || string(data) != "destination" {
		t.Fatalf("files after restart: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "restore-journal")); !os.IsNotExist(err) {
		t.Fatalf("journal not cleared: %v", err)
	}
	if err := service.RecoverInterruptedRestore(context.Background()); err != nil {
		t.Fatalf("second recovery: %v", err)
	}
}

func TestRestoreJournalRejectsPathInsideRestoredRoot(t *testing.T) {
	root := t.TempDir()
	service := NewService(nil, "sqlite", "", WithFileRoots([]FileRoot{{ArchiveName: "etc_antimage", Path: root}}), WithRestoreJournalDir(filepath.Join(root, "journal")))
	if _, err := service.createRestoreJournalDir(); err == nil {
		t.Fatal("journal inside restored root was accepted")
	}
}

func TestPreparingRestoreJournalCanBeClearedBeforeMutation(t *testing.T) {
	root := t.TempDir()
	journal := filepath.Join(t.TempDir(), "journal")
	service := NewService(nil, "sqlite", "", WithFileRoots([]FileRoot{{ArchiveName: "etc_antimage", Path: root}}), WithRestoreJournalDir(journal))
	if _, err := service.createRestoreJournalDir(); err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverInterruptedRestore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("preparation journal remains: %v", err)
	}
}

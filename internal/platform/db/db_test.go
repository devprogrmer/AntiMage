package db

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteDSNUsesSupportedPragmaOptions(t *testing.T) {
	dsn := sqliteDSN("C:/data/antimage.db")
	for _, option := range []string{
		"_pragma=busy_timeout(30000)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
	} {
		if !strings.Contains(dsn, option) {
			t.Fatalf("sqlite DSN %q does not contain supported option %q", dsn, option)
		}
	}
	if strings.Contains(dsn, "_busy_timeout=") || strings.Contains(dsn, "_journal_mode=") {
		t.Fatalf("sqlite DSN %q uses shorthand options unsupported by the pinned driver", dsn)
	}
}

func TestSQLitePoolAllowsDashboardReadsDuringWorkers(t *testing.T) {
	pool, err := Open("sqlite:///" + filepath.Join(t.TempDir(), "antimage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.DB.Close() })
	if got := pool.DB.Stats().MaxOpenConnections; got < 2 {
		t.Fatalf("sqlite pool max open connections = %d, want concurrent dashboard reads while node workers run", got)
	}
}

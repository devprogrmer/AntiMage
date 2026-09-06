package db

import (
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

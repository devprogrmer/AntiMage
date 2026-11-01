package backup

import "testing"

func TestExportUsesAntiMageBackupExtension(t *testing.T) {
	if Extension != ".ambackup" {
		t.Fatalf("backup extension = %q, want %q", Extension, ".ambackup")
	}
}

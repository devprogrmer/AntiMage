package nodeagent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAccountingWALReplaysAndRepairsCrashTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.wal")
	offset, err := appendAccountingWAL(path, 0, []byte("committed-1"))
	if err != nil {
		t.Fatal(err)
	}
	offset, err = appendAccountingWAL(path, offset, []byte("committed-2"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{'A', 'M', 'W'}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var records [][]byte
	gotOffset, count, err := replayAccountingWAL(path, func(raw []byte) error {
		records = append(records, bytes.Clone(raw))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || gotOffset != offset || len(records) != 2 || string(records[0]) != "committed-1" || string(records[1]) != "committed-2" {
		t.Fatalf("replay count=%d offset=%d records=%q", count, gotOffset, records)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != offset {
		t.Fatalf("repaired journal size=%d, want %d", info.Size(), offset)
	}
}

func TestAccountingWALRejectsCorruptCommittedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.wal")
	if _, err := appendAccountingWAL(path, 0, []byte("committed")); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{'X'}, accountingWALHeaderSize); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := replayAccountingWAL(path, nil); err == nil {
		t.Fatal("expected checksum error for corrupted committed record")
	}
}

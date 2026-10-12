//go:build linux

package system

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type transactionFixture struct {
	app, op, kind, target string
	old, targetBytes      []byte
	files                 []BackupFile
	state                 installTransactionState
	archive               ResolvedInstall
}

func TestRestoreWithoutInitialMarkerRequiresOriginalControllerDeadline(t *testing.T) {
	fixture := newTransactionFixture(t, "panel")
	fixture.installActual(t, fixture.targetBytes)
	deadline := time.Now().Add(time.Minute).UnixNano()
	inspect := func(limit int64) (BinaryTransactionEvidence, error) {
		return InspectBinaryTransaction(fixture.app, fixture.op, fixture.op, fixture.kind, fixture.target, runtime.GOARCH, true, limit)
	}
	evidence, err := inspect(deadline)
	if err != nil || evidence.NextAction != "resume_restore" || evidence.Deadline != deadline || !evidence.BackupValid {
		t.Fatalf("missing marker recovery: %+v %v", evidence, err)
	}
	if _, err := inspect(1); err == nil {
		t.Fatal("expired original deadline authorized restore")
	}
	fixture.installActual(t, fixture.old)
	evidence, err = inspect(deadline)
	if err != nil || evidence.NextAction != "restart_only" {
		t.Fatalf("restored bytes replayed: %+v %v", evidence, err)
	}
	if err := os.WriteFile(filepath.Join(fixture.app, ".update-backups", fixture.op, "0"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspect(deadline); err == nil {
		t.Fatal("invalid backup authorized missing-marker restore")
	}
}

func writeFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func newTransactionFixture(t *testing.T, kind string) transactionFixture {
	t.Helper()
	fixture := transactionFixture{app: t.TempDir(), op: "transaction-op", kind: kind, target: "isolated-service"}
	var err error
	fixture.old, err = os.ReadFile("/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	fixture.targetBytes, err = os.ReadFile("/bin/false")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"antimage-node"}
	if kind == "panel" {
		names = []string{"antimage-server", "antimage-cli"}
	}
	backup := BinaryBackup{Identity: fixture.op, TargetType: kind, TargetID: fixture.target, Version: "v1.0.0", OS: "linux", Architecture: runtime.GOARCH, SourceOperationID: fixture.op}
	backupDir := filepath.Join(fixture.app, ".update-backups", fixture.op)
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		t.Fatal(err)
	}
	oldSHA, targetSHA := sha256.Sum256(fixture.old), sha256.Sum256(fixture.targetBytes)
	for i, name := range names {
		destination := filepath.Join(fixture.app, "bin", name)
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, fixture.old, 0755); err != nil {
			t.Fatal(err)
		}
		record := BackupFile{Reference: string(rune('0' + i)), Destination: destination, SHA256: hex.EncodeToString(oldSHA[:]), Size: int64(len(fixture.old)), Mode: 0755, UID: os.Getuid(), GID: os.Getgid()}
		backup.Files = append(backup.Files, record)
		if err := os.WriteFile(filepath.Join(backupDir, record.Reference), fixture.old, 0700); err != nil {
			t.Fatal(err)
		}
		record.SHA256 = hex.EncodeToString(targetSHA[:])
		record.Size = int64(len(fixture.targetBytes))
		fixture.files = append(fixture.files, record)
	}
	writeFixtureJSON(t, filepath.Join(backupDir, "manifest.json"), backup)
	archiveBytes := fixture.targetBytes
	if kind == "panel" {
		var buffer bytes.Buffer
		zipper := gzip.NewWriter(&buffer)
		writer := tar.NewWriter(zipper)
		for _, name := range names {
			if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(fixture.targetBytes))}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(fixture.targetBytes); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := zipper.Close(); err != nil {
			t.Fatal(err)
		}
		archiveBytes = buffer.Bytes()
	}
	archiveSHA := sha256.Sum256(archiveBytes)
	fixture.archive = ResolvedInstall{BuildCatalogEntry: BuildCatalogEntry{Version: "v2.0.0", SHA256: hex.EncodeToString(archiveSHA[:]), Size: int64(len(archiveBytes)), OS: "linux", Architecture: runtime.GOARCH}}
	cache := filepath.Join(fixture.app, ".maintenance-artifacts", fixture.op)
	writeFixtureJSON(t, filepath.Join(cache, "target.json"), map[string]any{"operation_id": fixture.op, "kind": kind, "target": fixture.archive, "artifact_ready": true})
	if err := os.WriteFile(filepath.Join(cache, "resolved-artifact"), archiveBytes, 0600); err != nil {
		t.Fatal(err)
	}
	fixture.state = installTransactionState{OperationID: fixture.op, TargetType: kind, TargetID: fixture.target, Phase: "replacement_ready", Deadline: time.Now().Add(time.Minute).UnixNano(), Files: fixture.files}
	fixture.persist(t)
	return fixture
}

func (f transactionFixture) persist(t *testing.T) {
	writeFixtureJSON(t, filepath.Join(f.app, ".update-transactions", f.op, "state.json"), f.state)
}
func (f transactionFixture) installActual(t *testing.T, data []byte) {
	t.Helper()
	for _, file := range f.files {
		if err := os.WriteFile(file.Destination, data, 0755); err != nil {
			t.Fatal(err)
		}
	}
}
func (f transactionFixture) inspect(t *testing.T, restore bool) BinaryTransactionEvidence {
	t.Helper()
	evidence, err := InspectBinaryTransaction(f.app, f.op, f.op, f.kind, f.target, runtime.GOARCH, restore)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func TestInstallTransactionRecoveryInspectsActualFilesAndArchive(t *testing.T) {
	for _, kind := range []string{"node", "panel"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newTransactionFixture(t, kind)
			for _, phase := range []string{"artifact_ready", "backup_verified", "replacement_ready"} {
				fixture.state.Phase = phase
				fixture.persist(t)
				evidence := fixture.inspect(t, false)
				if evidence.NextAction != "resume_install" || evidence.FilesMatch || !evidence.ArtifactValid || !evidence.BackupValid {
					t.Fatalf("%s: %+v", phase, evidence)
				}
			}
			// Actual commit can precede the DB/file phase ACK: no reinstall is required.
			fixture.installActual(t, fixture.targetBytes)
			for _, phase := range []string{"replacement_ready", "replacement_committed", "runtime_restart_required"} {
				fixture.state.Phase = phase
				fixture.persist(t)
				evidence := fixture.inspect(t, false)
				if evidence.NextAction != "restart_only" || !evidence.FilesMatch {
					t.Fatalf("committed %s: %+v", phase, evidence)
				}
			}
			// A persisted committed marker is not authoritative over wrong actual bytes.
			fixture.installActual(t, fixture.old)
			evidence := fixture.inspect(t, false)
			if evidence.NextAction != "manual_recovery_required" || evidence.FilesMatch {
				t.Fatalf("false committed identity: %+v", evidence)
			}
			fixture.state.Phase = "replacement_ready"
			fixture.state.Deadline = time.Now().Add(-time.Minute).UnixNano()
			fixture.persist(t)
			if evidence := fixture.inspect(t, false); evidence.NextAction != "manual_recovery_required" {
				t.Fatal("expired original deadline was reset")
			}
			// Tampering with marker hashes cannot redefine the immutable archive target.
			oldSHA := sha256.Sum256(fixture.old)
			for i := range fixture.state.Files {
				fixture.state.Files[i].SHA256 = hex.EncodeToString(oldSHA[:])
				fixture.state.Files[i].Size = int64(len(fixture.old))
			}
			fixture.persist(t)
			evidence = fixture.inspect(t, false)
			if evidence.ArtifactValid || evidence.NextAction != "manual_recovery_required" {
				t.Fatalf("marker changed archive identity: %+v", evidence)
			}
		})
	}
}

func TestRestoreTransactionRecoveryValidatesBackupAndProductionFiles(t *testing.T) {
	for _, kind := range []string{"node", "panel"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newTransactionFixture(t, kind)
			fixture.installActual(t, fixture.targetBytes)
			manifest, err := os.ReadFile(filepath.Join(fixture.app, ".update-backups", fixture.op, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(manifest)
			state := map[string]any{"identity": fixture.op, "manifest_sha256": hex.EncodeToString(digest[:]), "phase": "rollback_prepared", "deadline_at_unix_nanos": time.Now().Add(time.Minute).UnixNano()}
			path := filepath.Join(fixture.app, ".update-backups", fixture.op, "restore-state.json")
			for _, phase := range []string{"rollback_prepared", "backup_verified", "restore_ready"} {
				state["phase"] = phase
				writeFixtureJSON(t, path, state)
				evidence := fixture.inspect(t, true)
				if evidence.NextAction != "resume_restore" || evidence.FilesMatch {
					t.Fatalf("%s: %+v", phase, evidence)
				}
			}
			fixture.installActual(t, fixture.old)
			for _, phase := range []string{"restore_ready", "restore_committed", "rollback_restart_required"} {
				state["phase"] = phase
				writeFixtureJSON(t, path, state)
				evidence := fixture.inspect(t, true)
				if evidence.NextAction != "restart_only" || !evidence.FilesMatch {
					t.Fatalf("restored %s: %+v", phase, evidence)
				}
			}
			if err := os.WriteFile(filepath.Join(fixture.app, ".update-backups", fixture.op, "0"), []byte("invalid-backup"), 0700); err != nil {
				t.Fatal(err)
			}
			evidence, err := InspectBinaryTransaction(fixture.app, fixture.op, fixture.op, kind, fixture.target, runtime.GOARCH, true)
			if err == nil || evidence.BackupValid || evidence.NextAction != "manual_recovery_required" {
				t.Fatalf("invalid backup accepted: %+v %v", evidence, err)
			}
		})
	}
}

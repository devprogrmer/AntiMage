package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingBackupWriter struct{ err error }

func (w failingBackupWriter) Write([]byte) (int, error) { return 0, w.err }

func TestBackupArchiveReportsFinalCompressionWriteFailure(t *testing.T) {
	buildDir := t.TempDir()
	for _, name := range []string{ManifestName, DatabaseSQLName} {
		if err := os.WriteFile(filepath.Join(buildDir, name), []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := errors.New("disk full")
	service := NewService(nil, "mysql", "")
	if err := service.writeArchiveToWriter(failingBackupWriter{err: want}, buildDir, DatabaseSQLName, ScopeDatabase); !errors.Is(err, want) {
		t.Fatalf("archive finalize error = %v, want %v", err, want)
	}
}

func TestSafeExtractRejectsInvalidGzipTrailer(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.ambackup")
			writeBackupArchiveForTest(t, path, map[string][]byte{ManifestName: []byte(`{"format":"antimage"}`)})
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "missing" {
				raw = raw[:len(raw)-8]
			} else {
				raw[len(raw)-8] ^= 0xff
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := safeExtract(path, t.TempDir()); err == nil {
				t.Fatal("invalid gzip trailer accepted as a complete backup")
			}
		})
	}
}

func TestWriteArchivePassesFullIntegrityRead(t *testing.T) {
	buildDir := t.TempDir()
	for name, content := range map[string]string{ManifestName: "manifest", DatabaseSQLName: "database"} {
		if err := os.WriteFile(filepath.Join(buildDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "valid.ambackup")
	service := NewService(nil, "mysql", "")
	if err := service.writeArchive(path, buildDir, DatabaseSQLName, ScopeDatabase); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := safeExtract(path, destination); err != nil {
		t.Fatalf("valid exported archive failed integrity read: %v", err)
	}
	for name, content := range map[string]string{ManifestName: "manifest", DatabaseSQLName: "database"} {
		if raw, err := os.ReadFile(filepath.Join(destination, name)); err != nil || string(raw) != content {
			t.Fatalf("extracted %s = %q, %v", name, raw, err)
		}
	}
}

func TestRestoreDatabaseRejectsManifestPayloadOutsideExtraction(t *testing.T) {
	root := t.TempDir()
	extracted := filepath.Join(root, "extracted")
	if err := os.Mkdir(extracted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.sql"), []byte("outside archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, "sqlite", "")
	for _, name := range []string{"../outside.sql", filepath.Join(root, "outside.sql")} {
		m := manifest{Database: manifestDB{Payload: name, PayloadType: "mysql-dump"}}
		_, _, _, err := service.restoreDatabasePayload(context.Background(), extracted, m, false)
		if err == nil || !strings.Contains(err.Error(), "unsafe paths") {
			t.Errorf("payload %q: expected unsafe path error, got %v", name, err)
		}
	}
}

func TestImportRejectsUnsafeArchivePath(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "unsafe-backup.dat")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "../evil", Mode: 0o600, Size: int64(len("bad"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte("bad")); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	service := NewService(nil, "sqlite", "sqlite:///tmp/antimage.sqlite3")
	_, err = service.Import(context.Background(), archivePath)
	if err == nil || !strings.Contains(err.Error(), "unsafe paths") {
		t.Fatalf("expected unsafe path error, got %v", err)
	}
}

func TestSafeExtractRejectsOversizedEntry(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "oversized-backup.dat")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "large", Mode: 0o600, Size: maxBackupExtractBytes + 1}); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	err = safeExtract(archivePath, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected extraction size limit error, got %v", err)
	}
}

func TestImportRejectsInvalidManifest(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "invalid-backup.dat")
	writeBackupArchiveForTest(t, archivePath, map[string][]byte{
		ManifestName: []byte(`{"format":"wrong","version":1}`),
	})
	service := NewService(nil, "sqlite", "sqlite:///tmp/antimage.sqlite3")
	_, err := service.Import(context.Background(), archivePath)
	if err == nil || !strings.Contains(err.Error(), "Invalid backup manifest format") {
		t.Fatalf("expected invalid manifest error, got %v", err)
	}
}

func TestMySQLMissingDumpTool(t *testing.T) {
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))
	service := NewService(nil, "mysql", "mysql://user:pass@127.0.0.1:3306/antimage")
	_, err := service.Export(context.Background(), ScopeDatabase)
	if err == nil || !strings.Contains(err.Error(), "Required database tool is not installed") {
		t.Fatalf("expected missing tool error, got %v", err)
	}
}

func TestFullRestoreKeepsDestinationMySQLCredentials(t *testing.T) {
	targetEnv := filepath.Join(t.TempDir(), "antimage_env")
	if err := os.WriteFile(targetEnv, []byte(strings.Join([]string{
		`ANTIMAGE_DATABASE_FLAVOR="mysql"`,
		`MYSQL_DATABASE="antimage"`,
		`MYSQL_USER="antimage"`,
		`MYSQL_PASSWORD="destination-password"`,
		`MYSQL_ROOT_PASSWORD="destination-root-password"`,
		`SQLALCHEMY_DATABASE_URL="mysql+pymysql://antimage:destination-password@127.0.0.1:3306/antimage"`,
	}, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	filesDir := t.TempDir()
	sourceEnv := filepath.Join(filesDir, "antimage_env")
	if err := os.WriteFile(sourceEnv, []byte(strings.Join([]string{
		`PANEL_DOMAIN="source.example.com"`,
		`MYSQL_PASSWORD="source-password"`,
		`MYSQL_ROOT_PASSWORD="source-root-password"`,
		`SQLALCHEMY_DATABASE_URL="mysql+pymysql://antimage:source-password@127.0.0.1:3306/antimage"`,
	}, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	service := NewService(nil, "mysql", "mysql+pymysql://antimage:destination-password@127.0.0.1:3306/antimage", WithFileRoots([]FileRoot{
		{ArchiveName: "antimage_env", Path: targetEnv},
	}))
	if err := service.preserveLocalDatabaseEnv(filesDir); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(sourceEnv)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, expected := range []string{
		`PANEL_DOMAIN="source.example.com"`,
		`MYSQL_PASSWORD="destination-password"`,
		`MYSQL_ROOT_PASSWORD="destination-root-password"`,
		`SQLALCHEMY_DATABASE_URL="mysql+pymysql://antimage:destination-password@127.0.0.1:3306/antimage"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("restored env missing %q in %q", expected, text)
		}
	}
	if strings.Contains(text, "source-password") {
		t.Fatalf("restored env retained source database credentials: %q", text)
	}
}

func TestMySQLAccessDeniedBackupError(t *testing.T) {
	err := mysqlBackupCommandError("restore", "ERROR 1045 (28000): Access denied for user 'antimage'@'localhost'")
	if !strings.Contains(err.Error(), "configured database credentials") || !strings.Contains(err.Error(), "MYSQL_PASSWORD") {
		t.Fatalf("expected actionable credential error, got %v", err)
	}
}

func writeBackupArchiveForTest(t *testing.T, archivePath string, files map[string][]byte) {
	t.Helper()
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Preview checks the archive and destination prerequisites without restoring
// data. The import repeats these checks because the destination can change
// between preview and confirmation.
func (s *Service) Preview(ctx context.Context, archivePath string, migrate bool) (PreviewResult, error) {
	if stat, err := os.Stat(archivePath); err != nil || stat.IsDir() {
		return PreviewResult{}, Error{Message: "Backup file not found"}
	}
	extractDir, err := os.MkdirTemp("", "antimage-backup-preview-*")
	if err != nil {
		return PreviewResult{}, err
	}
	defer os.RemoveAll(extractDir)
	if err := safeExtract(archivePath, extractDir); err != nil {
		return PreviewResult{}, err
	}
	m, err := loadManifest(filepath.Join(extractDir, ManifestName))
	if err != nil {
		return PreviewResult{}, err
	}
	scope, err := validateScope(m.Scope)
	if err != nil {
		return PreviewResult{}, err
	}
	if m.Database.Tables < 0 || m.Database.Rows < 0 {
		return PreviewResult{}, Error{Message: "Backup manifest has invalid database counts"}
	}
	result := PreviewResult{
		Scope: scope, CreatedAt: m.CreatedAt, Tables: m.Database.Tables,
		Rows: m.Database.Rows, FileRoots: []string{}, Warnings: []string{}, Mode: "replace",
	}
	if migrate {
		result.Mode = "migration"
		if (s.dialect != "mysql" && s.dialect != "mariadb") || m.Database.PayloadType != "mysql-dump" || m.Database.Payload == "" {
			return PreviewResult{}, Error{Message: "Migration without source nodes requires a native MySQL/MariaDB backup and destination."}
		}
		if s.db == nil {
			return PreviewResult{}, Error{Message: "Destination database connection is not available."}
		}
		var enrolled int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE LOWER(COALESCE(status, '')) <> 'deleted'`).Scan(&enrolled); err != nil {
			return PreviewResult{}, err
		}
		if enrolled > 0 {
			return PreviewResult{}, Error{Message: "Migration requires a destination panel without enrolled nodes. Add nodes after restoring the backup."}
		}
		result.Warnings = append(result.Warnings, "Source nodes will not be enrolled. Assign restored inbound definitions to new nodes after import.")
	}
	if err := s.previewDatabasePayload(ctx, extractDir, m, migrate); err != nil {
		return PreviewResult{}, err
	}
	if scope == ScopeFull {
		for _, root := range s.fileRoots {
			if root.ArchiveName == "" || root.Path == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(extractDir, FilesPrefix, root.ArchiveName)); err == nil {
				result.FileRoots = append(result.FileRoots, root.ArchiveName)
			} else if !os.IsNotExist(err) {
				return PreviewResult{}, err
			}
		}
		if len(result.FileRoots) == 0 {
			result.Warnings = append(result.Warnings, "This full backup has no recognized file payloads for this installation.")
		}
	}
	result.Warnings = append(result.Warnings, "Preview does not execute database restore, file replacement, or a live user connection test.")
	return result, nil
}

func (s *Service) previewDatabasePayload(ctx context.Context, extractDir string, m manifest, migrate bool) error {
	if m.Database.Payload == "" {
		if migrate {
			return Error{Message: "Migration requires a native MySQL/MariaDB backup."}
		}
		if !isRegularFile(filepath.Join(extractDir, DatabaseDumpName)) {
			return Error{Message: "Backup database payload is missing"}
		}
		return nil
	}
	payloadName, err := safeArchiveName(m.Database.Payload)
	if err != nil {
		return err
	}
	payloadPath := filepath.Join(extractDir, payloadName)
	if !isRegularFile(payloadPath) {
		return Error{Message: "Backup database payload is missing"}
	}
	switch m.Database.PayloadType {
	case "sqlite-file":
		if s.dialect != "sqlite" {
			return Error{Message: "This backup contains SQLite, but the destination does not use SQLite"}
		}
		if _, err := s.sqliteDatabasePath(); err != nil {
			return err
		}
		return validateSQLiteFile(payloadPath)
	case "mysql-dump":
		if s.dialect != "mysql" && s.dialect != "mariadb" {
			return Error{Message: "This backup contains MySQL/MariaDB, but the destination does not use it"}
		}
		if s.db == nil {
			return Error{Message: "Destination database connection is not available."}
		}
		if err := s.db.PingContext(ctx); err != nil {
			return err
		}
		if _, err := s.mysqlDatabaseName(); err != nil {
			return err
		}
		if _, err := findExecutable([]string{"mariadb", "mysql"}); err != nil {
			return err
		}
		if _, err := findExecutable([]string{"mariadb-dump", "mysqldump"}); err != nil {
			return err
		}
		if migrate {
			sourceEngine, err := portableMySQLSourceEngine(payloadPath)
			if err != nil {
				return err
			}
			if sourceEngine != "" {
				var version string
				if err := s.db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
					return err
				}
				targetEngine := "mysql"
				if strings.Contains(strings.ToLower(version), "mariadb") {
					targetEngine = "mariadb"
				}
				if sourceEngine != targetEngine {
					return Error{Message: "Migration requires the same database engine as the backup. The backup uses " + sourceEngine + " and the destination uses " + targetEngine + "."}
				}
			}
		}
		filtered, err := os.CreateTemp(extractDir, "preview-filtered-*.sql")
		if err != nil {
			return err
		}
		if err := filtered.Close(); err != nil {
			return err
		}
		if migrate {
			if err := preparePortableMySQLDump(payloadPath, filtered.Name()); err != nil {
				return Error{Message: fmt.Sprintf("Cannot prepare this backup for migration: %v", err)}
			}
			return nil
		}
		return filterMySQLDumpForDatabase(payloadPath, filtered.Name())
	default:
		return Error{Message: "Backup database payload type is not supported"}
	}
}

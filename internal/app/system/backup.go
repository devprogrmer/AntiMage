package system

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/antimage/antimage/internal/app/migrations"
)

type BackupFile struct {
	Reference   string `json:"reference"`
	Destination string `json:"destination"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	Mode        uint32 `json:"mode"`
	UID         int    `json:"uid"`
	GID         int    `json:"gid"`
}
type BinaryBackup struct {
	CurrentRunningVersion string       `json:"current_running_version,omitempty"`
	OS                    string       `json:"os"`
	Architecture          string       `json:"architecture"`
	Identity              string       `json:"identity"`
	TargetType            string       `json:"target_type"`
	TargetID              string       `json:"target_id"`
	Version               string       `json:"version"`
	Commit                string       `json:"commit"`
	CreatedAt             int64        `json:"created_at"`
	SourceOperationID     string       `json:"source_operation_id"`
	SchemaVersion         *int64       `json:"schema_version,omitempty"`
	Files                 []BackupFile `json:"files"`
	Valid                 bool         `json:"valid"`
	UnavailableReason     string       `json:"unavailable_reason,omitempty"`
}
type RollbackRequest struct {
	BackupIdentity string `json:"backup_identity"`
	Reason         string `json:"reason"`
	Confirm        bool   `json:"confirm"`
}

var backupIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`)

func ReadBinaryBackup(application, identity, targetType, targetID, arch string) (BinaryBackup, error) {
	var backup BinaryBackup
	if !backupIdentityPattern.MatchString(identity) {
		return backup, fmt.Errorf("invalid backup identity")
	}
	application, err := filepath.Abs(application)
	if err != nil {
		return backup, err
	}
	application, err = filepath.EvalSymlinks(application)
	if err != nil {
		return backup, err
	}
	root := filepath.Join(application, ".update-backups", identity)
	for _, path := range []string{filepath.Join(application, ".update-backups"), root, filepath.Join(root, "manifest.json")} {
		info, err := os.Lstat(path)
		if err != nil {
			return backup, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return backup, fmt.Errorf("backup path cannot be a symlink")
		}
	}
	payload, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return backup, err
	}
	if err := json.Unmarshal(payload, &backup); err != nil {
		return backup, err
	}
	if backup.Identity != identity || backup.TargetType != targetType || backup.TargetID != targetID || backup.Version == "" || len(backup.Files) == 0 {
		return backup, fmt.Errorf("backup metadata does not match target")
	}
	if backup.OS != "linux" || backup.Architecture != arch {
		return backup, fmt.Errorf("backup platform metadata is incompatible")
	}
	machines := map[string]elf.Machine{"amd64": elf.EM_X86_64, "386": elf.EM_386, "arm64": elf.EM_AARCH64, "arm": elf.EM_ARM, "ppc64le": elf.EM_PPC64, "s390x": elf.EM_S390, "riscv64": elf.EM_RISCV}
	executable := false
	references := map[string]bool{}
	destinations := map[string]bool{}
	for _, file := range backup.Files {
		if !regexp.MustCompile(`^[0-9]+$`).MatchString(file.Reference) {
			return backup, fmt.Errorf("invalid backup reference")
		}
		relative, err := filepath.Rel(application, file.Destination)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return backup, fmt.Errorf("backup destination escapes installation")
		}
		if references[file.Reference] || destinations[file.Destination] {
			return backup, fmt.Errorf("duplicate backup reference or destination")
		}
		references[file.Reference] = true
		destinations[file.Destination] = true
		parent, err := filepath.EvalSymlinks(filepath.Dir(file.Destination))
		if err != nil {
			return backup, err
		}
		parentRelative, err := filepath.Rel(application, parent)
		if err != nil || parentRelative == ".." || strings.HasPrefix(parentRelative, ".."+string(filepath.Separator)) || filepath.IsAbs(parentRelative) {
			return backup, fmt.Errorf("backup destination parent escapes installation")
		}
		if destinationInfo, err := os.Lstat(file.Destination); err == nil && destinationInfo.Mode()&os.ModeSymlink != 0 {
			return backup, fmt.Errorf("backup destination cannot be a symlink")
		}
		path := filepath.Join(root, file.Reference)
		info, err := os.Lstat(path)
		if err != nil {
			return backup, err
		}
		if !info.Mode().IsRegular() || info.Size() != file.Size {
			return backup, fmt.Errorf("backup size or type mismatch")
		}
		stream, err := os.Open(path)
		if err != nil {
			return backup, err
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, stream)
		closeErr := stream.Close()
		if copyErr != nil {
			return backup, copyErr
		}
		if closeErr != nil {
			return backup, closeErr
		}
		if !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), file.SHA256) {
			return backup, fmt.Errorf("backup checksum mismatch")
		}
		if file.Mode&0111 != 0 {
			binary, err := elf.Open(path)
			if err != nil {
				return backup, fmt.Errorf("backup executable is not ELF: %w", err)
			}
			machine := binary.Machine
			binary.Close()
			if expected, ok := machines[arch]; !ok || machine != expected {
				return backup, fmt.Errorf("backup executable architecture mismatch")
			}
			executable = true
		}
	}
	if !executable {
		return backup, fmt.Errorf("backup contains no recoverable executable")
	}
	backup.Valid = true
	return backup, nil
}

func (s *MaintenanceService) Backups() ([]BinaryBackup, error) {
	entries, err := os.ReadDir(filepath.Join(appDir(), ".update-backups"))
	if os.IsNotExist(err) {
		return []BinaryBackup{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]BinaryBackup, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !backupIdentityPattern.MatchString(entry.Name()) {
			continue
		}
		backup, err := ReadBinaryBackup(appDir(), entry.Name(), "panel", serviceName(), runtime.GOARCH)
		if err != nil {
			backup.Identity = entry.Name()
			backup.Valid = false
			backup.UnavailableReason = "Backup integrity or compatibility verification failed"
		}
		result = append(result, backup)
	}
	return result, nil
}

func (s *MaintenanceService) Rollback(ctx context.Context, req RollbackRequest) (MaintenanceOperationSnapshot, error) {
	current := s.Runtime.Info()
	if err := requireBinaryRuntime(current); err != nil {
		return MaintenanceOperationSnapshot{}, err
	}
	if !req.Confirm {
		return MaintenanceOperationSnapshot{}, MaintenanceError{Status: http.StatusBadRequest, Detail: "Rollback confirmation is required"}
	}
	backup, err := ReadBinaryBackup(appDir(), req.BackupIdentity, "panel", serviceName(), runtime.GOARCH)
	if err != nil {
		return MaintenanceOperationSnapshot{}, MaintenanceError{Status: http.StatusUnprocessableEntity, Detail: "Backup is unavailable or failed integrity verification"}
	}
	if backup.SchemaVersion != nil && s.ops != nil && s.ops.db != nil {
		version, err := migrations.Version(ctx, s.ops.db, s.ops.dialect)
		if err != nil {
			return MaintenanceOperationSnapshot{}, err
		}
		if version.GooseVersion > *backup.SchemaVersion {
			return MaintenanceOperationSnapshot{}, MaintenanceError{Status: http.StatusConflict, Detail: "Backup requires an unsafe schema downgrade"}
		}
	}
	return s.startOperation(ctx, "rollback", []string{"update-rollback", "--backup-id", backup.Identity, "--target-version", backup.Version, "--reason", req.Reason}, "Restoring previous panel version", current.RunningVersion, current.ProcessStartedAt)
}

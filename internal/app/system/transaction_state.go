package system

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BinaryTransactionEvidence is a read-only summary of actual production files,
// verified archive and operation-owned backup. Paths/URLs/configs are omitted.
type BinaryTransactionEvidence struct {
	OperationID    string `json:"operation_id"`
	Kind           string `json:"kind"`
	Phase          string `json:"phase"`
	BackupIdentity string `json:"backup_identity"`
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
	TargetVersion  string `json:"target_version,omitempty"`
	FilesMatch     bool   `json:"files_match"`
	BackupValid    bool   `json:"backup_valid"`
	ArtifactValid  bool   `json:"artifact_valid"`
	Deadline       int64  `json:"deadline_at_unix_nanos"`
	NextAction     string `json:"next_action"`
}

type installTransactionState struct {
	OperationID string       `json:"operation_id"`
	TargetType  string       `json:"target_type"`
	TargetID    string       `json:"target_id"`
	Phase       string       `json:"phase"`
	Deadline    int64        `json:"deadline_at_unix_nanos"`
	Files       []BackupFile `json:"files"`
}

func readPrivateJSON(application, path string, value any) error {
	root, err := filepath.Abs(application)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, abs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("transaction path escapes application")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("transaction path contains a symlink")
		}
	}
	file, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(io.LimitReader(file, 2<<20)).Decode(value)
}

func actualTransactionFilesMatch(application string, files []BackupFile) bool {
	if len(files) == 0 || len(files) > 64 {
		return false
	}
	destinations := map[string]bool{}
	for _, file := range files {
		if file.SHA256 == "" || file.Size <= 0 || destinations[file.Destination] {
			return false
		}
		destinations[file.Destination] = true
		relative, err := filepath.Rel(application, file.Destination)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return false
		}
		resolved, err := filepath.EvalSymlinks(file.Destination)
		if err != nil || resolved != filepath.Clean(file.Destination) {
			return false
		}
		info, err := os.Lstat(file.Destination)
		if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size || uint32(info.Mode().Perm()) != file.Mode&0777 || !transactionFileOwnerMatches(info, file) {
			return false
		}
		stream, err := os.Open(file.Destination)
		if err != nil {
			return false
		}
		hash := sha256.New()
		_, err = io.Copy(hash, io.LimitReader(stream, file.Size+1))
		_ = stream.Close()
		if err != nil || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
			return false
		}
	}
	return true
}

func InspectBinaryTransaction(application, operation, backupID, kind, targetID, arch string, restoreRequested bool, originalDeadline ...int64) (BinaryTransactionEvidence, error) {
	evidence := BinaryTransactionEvidence{OperationID: operation, Kind: kind, NextAction: "manual_recovery_required"}
	if !backupIdentityPattern.MatchString(operation) {
		return evidence, fmt.Errorf("invalid transaction operation identity")
	}
	if backupID == "" {
		backupID = operation
	}
	evidence.BackupIdentity = backupID
	backup, backupErr := ReadBinaryBackup(application, backupID, kind, targetID, arch)
	evidence.BackupValid = backupErr == nil
	if restoreRequested {
		var restore struct {
			Identity       string `json:"identity"`
			ManifestSHA256 string `json:"manifest_sha256"`
			Phase          string `json:"phase"`
			Deadline       int64  `json:"deadline_at_unix_nanos"`
		}
		path := filepath.Join(application, ".update-backups", backupID, "restore-state.json")
		stateErr := readPrivateJSON(application, path, &restore)
		if stateErr != nil {
			// The executor can disappear before the first restore marker. Only
			// the controller's persisted deadline may authorize that missing step.
			if !os.IsNotExist(stateErr) || !evidence.BackupValid || len(originalDeadline) != 1 || originalDeadline[0] <= time.Now().UnixNano() {
				return evidence, stateErr
			}
			restore.Identity, restore.Phase, restore.Deadline = backupID, "rollback_prepared", originalDeadline[0]
		}
		data, err := os.ReadFile(filepath.Join(application, ".update-backups", backupID, "manifest.json"))
		if err != nil {
			return evidence, err
		}
		hash := sha256.Sum256(data)
		if stateErr != nil {
			restore.ManifestSHA256 = hex.EncodeToString(hash[:])
		}
		if restore.Identity != backupID || restore.ManifestSHA256 != hex.EncodeToString(hash[:]) || !evidence.BackupValid {
			return evidence, fmt.Errorf("restore backup identity unavailable")
		}
		evidence.Phase, evidence.Deadline, evidence.TargetVersion = restore.Phase, restore.Deadline, backup.Version
		evidence.FilesMatch = actualTransactionFilesMatch(application, backup.Files)
		switch restore.Phase {
		case "rollback_prepared", "backup_verified", "restore_ready":
			if evidence.FilesMatch {
				evidence.NextAction = "restart_only"
			} else if time.Now().UnixNano() < evidence.Deadline {
				evidence.NextAction = "resume_restore"
			}
		case "restore_committed", "rollback_restart_required":
			if evidence.FilesMatch {
				evidence.NextAction = "restart_only"
			}
		default:
			return evidence, fmt.Errorf("unsupported restore transaction phase")
		}
		return evidence, nil
	}
	var tx installTransactionState
	if err := readPrivateJSON(application, filepath.Join(application, ".update-transactions", operation, "state.json"), &tx); err != nil {
		return evidence, err
	}
	if tx.OperationID != operation || tx.TargetType != kind || tx.TargetID != targetID {
		return evidence, fmt.Errorf("install transaction target mismatch")
	}
	evidence.Phase, evidence.Deadline = tx.Phase, tx.Deadline
	evidence.FilesMatch = actualTransactionFilesMatch(application, tx.Files)
	var artifact struct {
		OperationID string          `json:"operation_id"`
		Kind        string          `json:"kind"`
		Target      ResolvedInstall `json:"target"`
		Ready       bool            `json:"artifact_ready"`
	}
	cache := filepath.Join(application, ".maintenance-artifacts", operation)
	if err := readPrivateJSON(application, filepath.Join(cache, "target.json"), &artifact); err == nil && artifact.OperationID == operation && artifact.Kind == kind && artifact.Ready {
		evidence.ArtifactSHA256, evidence.TargetVersion = artifact.Target.SHA256, artifact.Target.Version
		archivePath := filepath.Join(cache, "resolved-artifact")
		evidence.ArtifactValid = actualArtifactMatches(archivePath, artifact.Target) && transactionFilesBoundToArchive(archivePath, kind, artifact.Target, tx.Files)
	}
	if !evidence.BackupValid || !evidence.ArtifactValid {
		return evidence, nil
	}
	switch tx.Phase {
	case "artifact_ready", "backup_verified", "replacement_ready":
		if evidence.FilesMatch {
			evidence.NextAction = "restart_only"
		} else if time.Now().UnixNano() < tx.Deadline {
			evidence.NextAction = "resume_install"
		}
	case "replacement_committed", "runtime_restart_required":
		if evidence.FilesMatch {
			evidence.NextAction = "restart_only"
		}
	default:
		return evidence, fmt.Errorf("unsupported install transaction phase")
	}
	return evidence, nil
}

func transactionFilesBoundToArchive(path, kind string, target ResolvedInstall, files []BackupFile) bool {
	if kind == "node" {
		return len(files) == 1 && files[0].SHA256 == target.SHA256 && files[0].Size == target.Size
	}
	if kind != "panel" || len(files) != 2 {
		return false
	}
	stream, err := os.Open(path)
	if err != nil {
		return false
	}
	defer stream.Close()
	zipped, err := gzip.NewReader(stream)
	if err != nil {
		return false
	}
	defer zipped.Close()
	reader := tar.NewReader(zipped)
	hashes := map[string]string{}
	sizes := map[string]int64{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false
		}
		name := strings.TrimPrefix(header.Name, "./")
		if name != "antimage-server" && name != "antimage-cli" {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > 2<<30 || hashes[name] != "" {
			return false
		}
		digest := sha256.New()
		if _, err := io.Copy(digest, io.LimitReader(reader, header.Size+1)); err != nil {
			return false
		}
		hashes[name] = hex.EncodeToString(digest.Sum(nil))
		sizes[name] = header.Size
	}
	if len(hashes) != 2 {
		return false
	}
	seen := map[string]bool{}
	for _, file := range files {
		name := filepath.Base(file.Destination)
		if seen[name] || hashes[name] != file.SHA256 || sizes[name] != file.Size {
			return false
		}
		seen[name] = true
	}
	return true
}

func actualArtifactMatches(path string, target ResolvedInstall) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || target.Size <= 0 || info.Size() != target.Size {
		return false
	}
	stream, err := os.Open(path)
	if err != nil {
		return false
	}
	defer stream.Close()
	hash := sha256.New()
	_, err = io.Copy(hash, io.LimitReader(stream, target.Size+1))
	return err == nil && hex.EncodeToString(hash.Sum(nil)) == target.SHA256
}

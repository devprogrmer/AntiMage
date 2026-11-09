package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

const restoreJournalActive = "active.json"
const restoreJournalComplete = "complete"
const restoreJournalPreparing = "preparing"

type durableRootSnapshot struct {
	Target  string      `json:"target"`
	Copy    string      `json:"copy"`
	Mode    fs.FileMode `json:"mode"`
	Existed bool        `json:"existed"`
}

type durableRestoreJournal struct {
	Database databasePayload       `json:"database"`
	Roots    []durableRootSnapshot `json:"roots"`
}

// RecoverInterruptedRestore must run before migrations and before the API starts.
// It is safe to call again: an incomplete rollback keeps its journal in place.
func (s *Service) RecoverInterruptedRestore(ctx context.Context) error {
	s.restoreMu.Lock()
	defer s.restoreMu.Unlock()
	return s.recoverInterruptedRestore(ctx)
}

func (s *Service) createRestoreJournalDir() (string, error) {
	dir, err := filepath.Abs(s.restoreJournalDir)
	if err != nil {
		return "", err
	}
	for _, root := range s.fileRoots {
		if root.Path == "" {
			continue
		}
		path, err := filepath.Abs(root.Path)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(path, dir)
		if err == nil && (rel == "." || rel != ".." && !filepath.IsAbs(rel) && !hasParentPrefix(rel)) {
			return "", fmt.Errorf("restore journal must be outside restored root %s", path)
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	if err := syncDirectory(filepath.Dir(dir)); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := writeDurableMarker(dir, restoreJournalPreparing, []byte("preparing\n")); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func hasParentPrefix(path string) bool {
	return len(path) >= 3 && path[:3] == ".."+string(os.PathSeparator)
}

func (s *Service) activateRestoreJournal(dir string, database databasePayload, roots []fileRootSnapshot) error {
	if database.ArchiveName != DatabaseSQLiteName && database.ArchiveName != DatabaseSQLName {
		return fmt.Errorf("invalid restore journal database payload")
	}
	journal := durableRestoreJournal{Database: database}
	for _, root := range roots {
		journal.Roots = append(journal.Roots, durableRootSnapshot{
			Target: root.target, Copy: filepath.Base(root.copyPath), Mode: root.mode, Existed: root.existed,
		})
	}
	if err := syncTree(dir); err != nil {
		return err
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return writeDurableMarker(dir, restoreJournalActive, data)
}

func (s *Service) completeRestoreJournal(dir string) error {
	if err := writeDurableMarker(dir, restoreJournalComplete, []byte("complete\n")); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (s *Service) recoverInterruptedRestore(ctx context.Context) error {
	if s.restoreJournalDir == "" {
		return nil
	}
	dir, err := filepath.Abs(s.restoreJournalDir)
	if err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("restore journal path is not a directory")
	}
	if _, err := os.Lstat(filepath.Join(dir, restoreJournalComplete)); err == nil {
		return os.RemoveAll(dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, restoreJournalActive))
	if errors.Is(err, os.ErrNotExist) {
		if _, preparedErr := os.Lstat(filepath.Join(dir, restoreJournalPreparing)); preparedErr != nil {
			return fmt.Errorf("restore journal has no ownership marker: %w", preparedErr)
		}
		return os.RemoveAll(dir) // No mutation starts before the active marker.
	}
	if err != nil {
		return err
	}
	var journal durableRestoreJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return err
	}
	if journal.Database.ArchiveName != DatabaseSQLiteName && journal.Database.ArchiveName != DatabaseSQLName {
		return fmt.Errorf("invalid restore journal payload")
	}
	if !isRegularFile(filepath.Join(dir, journal.Database.ArchiveName)) {
		return fmt.Errorf("restore journal database snapshot missing")
	}
	allowed := make(map[string]bool, len(s.fileRoots))
	for _, root := range s.fileRoots {
		if root.Path == "" {
			continue
		}
		path, err := filepath.Abs(root.Path)
		if err != nil {
			return err
		}
		allowed[path] = true
	}
	snapshots := make([]fileRootSnapshot, 0, len(journal.Roots))
	for _, root := range journal.Roots {
		if !allowed[root.Target] || filepath.Base(root.Copy) != root.Copy || root.Copy == "." || root.Copy == ".." {
			return fmt.Errorf("restore journal root does not match configured file roots")
		}
		copyPath := filepath.Join(dir, "files", root.Copy)
		if root.Existed {
			if _, err := os.Lstat(copyPath); err != nil {
				return fmt.Errorf("restore journal file snapshot missing: %w", err)
			}
		}
		snapshots = append(snapshots, fileRootSnapshot{target: root.Target, copyPath: copyPath, mode: root.Mode, existed: root.Existed})
	}
	// A failed file rollback leaves the database snapshot intact for another attempt.
	if err := s.rollbackFileRoots(snapshots); err != nil {
		return err
	}
	rollback := manifest{}
	rollback.Database.Payload = journal.Database.ArchiveName
	rollback.Database.PayloadType = journal.Database.PayloadType
	if _, _, _, err := s.restoreDatabasePayload(ctx, dir, rollback, false); err != nil {
		return err
	}
	return s.completeRestoreJournal(dir)
}

func writeDurableMarker(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, ".marker-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func syncTree(dir string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return syncDirectory(path)
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		err = f.Sync()
		return errors.Join(err, f.Close())
	})
}

func syncDirectory(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = f.Sync()
	return errors.Join(err, f.Close())
}

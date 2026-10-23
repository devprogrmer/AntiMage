package nodeagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type anyConnectUserFileBackup struct {
	path   string
	data   []byte
	mode   os.FileMode
	exists bool
}

func anyConnectReadUserConfigs(dir string) (map[string][]byte, error) {
	result := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		result[entry.Name()] = raw
	}
	return result, nil
}

// publishAnyConnectUserFiles rewrites only the target user's credential entry
// (ocpasswd preserves every other entry) and per-user config, then swaps the
// files into place. The staged password file is written with ocpasswd itself so
// the on-disk hash format stays identical to a full sync.
func (s *Server) publishAnyConnectUserFiles(next anyConnectRuntimeInbound, files anyConnectRuntimeFiles, userID int64, username string) ([]anyConnectUserFileBackup, error) {
	username = strings.TrimSpace(username)
	if err := validateOpenVPNUsername(username); err != nil || username == "" {
		return nil, fmt.Errorf("invalid AnyConnect mutation username: %w", errOrNil(err))
	}
	root := filepath.Dir(files.PasswordFile)
	stageRoot, err := os.MkdirTemp(root, ".users-mutate-")
	if err != nil {
		return nil, err
	}
	cleanupStage := true
	defer func() {
		if cleanupStage {
			_ = os.RemoveAll(stageRoot)
		}
	}()
	stagedPassword := filepath.Join(stageRoot, "ocpasswd")
	if current, readErr := os.ReadFile(files.PasswordFile); readErr == nil {
		if err := os.WriteFile(stagedPassword, current, 0600); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(readErr) {
		return nil, readErr
	}

	var desired *anyConnectRuntimeUser
	for i := range next.Users {
		if next.Users[i].UserID == userID {
			copy := next.Users[i]
			desired = &copy
			break
		}
	}
	if desired == nil {
		if err := runAnyConnectOcpasswd(stagedPassword, username, "", true); err != nil {
			return nil, err
		}
	} else {
		if err := runAnyConnectOcpasswd(stagedPassword, username, desired.Password, false); err != nil {
			return nil, err
		}
	}
	if err := os.Chmod(stagedPassword, 0600); err != nil {
		return nil, err
	}

	stagedUsers := filepath.Join(stageRoot, "users")
	if err := os.MkdirAll(stagedUsers, 0700); err != nil {
		return nil, err
	}
	stagedUserPath := filepath.Join(stagedUsers, username)
	liveUserPath := filepath.Join(files.UserConfigDir, username)
	removeUserConfig := desired == nil
	if desired != nil {
		cfg := renderAnyConnectUserConfig(*desired, next.Settings)
		if strings.TrimSpace(cfg) == "" {
			removeUserConfig = true
		} else if err := os.WriteFile(stagedUserPath, []byte(cfg), 0600); err != nil {
			return nil, err
		}
	}

	targets := []string{files.PasswordFile, liveUserPath}
	backups := make([]anyConnectUserFileBackup, 0, len(targets))
	for _, path := range targets {
		info, err := os.Lstat(path)
		backup := anyConnectUserFileBackup{path: path, mode: 0600}
		if err == nil {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("refuse to mutate non-regular AnyConnect file %s", filepath.Base(path))
			}
			backup.mode, backup.exists = info.Mode().Perm(), true
			backup.data, err = os.ReadFile(path)
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		backups = append(backups, backup)
	}
	if err := os.MkdirAll(files.UserConfigDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Rename(stagedPassword, files.PasswordFile); err != nil {
		return nil, fmt.Errorf("publish AnyConnect password file: %w", err)
	}
	if removeUserConfig {
		if err := os.Remove(liveUserPath); err != nil && !os.IsNotExist(err) {
			return nil, errors.Join(fmt.Errorf("remove AnyConnect user config: %w", err), restoreAnyConnectUserFileBackups(backups))
		}
	} else if err := os.Rename(stagedUserPath, liveUserPath); err != nil {
		return nil, errors.Join(fmt.Errorf("publish AnyConnect user config: %w", err), restoreAnyConnectUserFileBackups(backups))
	}
	cleanupStage = false
	return backups, nil
}

func restoreAnyConnectUserFileBackups(backups []anyConnectUserFileBackup) error {
	var errs []error
	for i := len(backups) - 1; i >= 0; i-- {
		backup := backups[i]
		if !backup.exists {
			if err := os.Remove(backup.path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
			continue
		}
		if err := atomicWriteFile(backup.path, backup.data, backup.mode); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// restoreAnyConnectUserFiles rolls the live credential file and the target
// user's config back to the captured pre-mutation bytes.
func restoreAnyConnectUserFiles(backups []anyConnectUserFileBackup, previousUsers []byte, previousCCD map[string][]byte, files anyConnectRuntimeFiles) error {
	var errs []error
	if previousUsers != nil {
		if err := atomicWriteFile(files.PasswordFile, previousUsers, 0600); err != nil {
			errs = append(errs, err)
		}
	}
	entries, err := os.ReadDir(files.UserConfigDir)
	if err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := previousCCD[entry.Name()]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(files.UserConfigDir, entry.Name())); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	for name, raw := range previousCCD {
		path := filepath.Join(files.UserConfigDir, name)
		current, readErr := os.ReadFile(path)
		if readErr == nil && string(current) == string(raw) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := atomicWriteFile(path, raw, 0600); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func runAnyConnectOcpasswd(passwordFile, username, password string, remove bool) error {
	args := []string{"-c", passwordFile}
	if remove {
		args = append(args, "-d")
	}
	args = append(args, username)
	cmd := anyConnectCommand("ocpasswd", args...)
	if !remove {
		cmd.Stdin = strings.NewReader(password + "\n" + password + "\n")
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("update AnyConnect password record for user %q: %w: %s", username, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func errOrNil(err error) error {
	if err == nil {
		return fmt.Errorf("username is required")
	}
	return err
}

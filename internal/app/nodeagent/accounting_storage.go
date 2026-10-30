package nodeagent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

var accountingAtomicFileSync = func(file *os.File) error { return file.Sync() }
var accountingWALFileSync = func(file *os.File) error { return file.Sync() }
var accountingDirectorySync = syncAccountingDirectory

var accountingDurableDirectories = struct {
	sync.Mutex
	paths map[string]struct{}
}{paths: make(map[string]struct{})}

func writeAccountingState(path string, raw []byte) error {
	if len(raw) > maxOfflineAccountingBytes {
		return fmt.Errorf("accounting state exceeds safe byte capacity")
	}
	if err := ensureAccountingDirectoryDurable(filepath.Dir(path)); err != nil {
		return err
	}
	if err := atomicWriteFile(path, raw, 0600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	return accountingDirectorySync(filepath.Dir(path))
}

// Sync each directory chain once per process, then only the containing
// directory after atomic renames. The file sync and parent sync preserve the
// replace durability contract without repeatedly syncing every ancestor.
func ensureAccountingDirectoryDurable(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	path = filepath.Clean(path)
	accountingDurableDirectories.Lock()
	defer accountingDurableDirectories.Unlock()
	if _, ok := accountingDurableDirectories.paths[path]; ok {
		return nil
	}
	chain := make([]string, 0, 8)
	for current := path; ; current = filepath.Dir(current) {
		chain = append(chain, current)
		if filepath.Dir(current) == current {
			break
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if err := accountingDirectorySync(chain[i]); err != nil {
			return err
		}
	}
	accountingDurableDirectories.paths[path] = struct{}{}
	return nil
}

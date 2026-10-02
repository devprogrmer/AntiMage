package nodeagent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func writeAccountingState(path string, raw []byte) error {
	if len(raw) > maxOfflineAccountingBytes {
		return fmt.Errorf("accounting state exceeds safe byte capacity")
	}
	if err := atomicWriteFile(path, raw, 0600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	for name := filepath.Dir(path); ; name = filepath.Dir(name) {
		dir, err := os.Open(name)
		if err != nil {
			return err
		}
		syncErr := dir.Sync()
		closeErr := dir.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		if filepath.Dir(name) == name {
			return nil
		}
	}
}

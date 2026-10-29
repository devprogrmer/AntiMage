//go:build linux

package nodeagent

import (
	"os"
	"path/filepath"
	"syscall"
)

func withPPPAdmissionLock(root string, fn func() error) error {
	if err := os.MkdirAll(filepath.Join(root, "ppp-admission"), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(root, "ppp-admission", "admission.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return fn()
}

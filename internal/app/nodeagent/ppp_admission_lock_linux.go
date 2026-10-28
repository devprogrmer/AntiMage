//go:build linux

package nodeagent

import (
	"os"
	"path/filepath"
	"syscall"
)

func withPPPAdmissionLock(root string, fn func() error) error {
	release, err := acquirePPPAdmissionLock(root)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

func acquirePPPAdmissionLock(root string) (func(), error) {
	if err := os.MkdirAll(filepath.Join(root, "ppp-admission"), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, "ppp-admission", "admission.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

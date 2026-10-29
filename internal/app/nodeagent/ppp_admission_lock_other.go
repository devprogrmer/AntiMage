//go:build !linux

package nodeagent

import "sync"

var pppAdmissionLockMu sync.Mutex

func withPPPAdmissionLock(_ string, fn func() error) error {
	pppAdmissionLockMu.Lock()
	defer pppAdmissionLockMu.Unlock()
	return fn()
}

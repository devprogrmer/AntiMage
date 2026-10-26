package nodeagent

import (
	"crypto/sha256"
	"os"
)

// The offline usage-state files were rewritten and fsynced on every checkpoint
// tick. For protocols with no activity the serialized state is byte-identical
// from one tick to the next, so the atomic replace plus the directory fsync
// chain repeat identical durable state. On a production filesystem that is pure
// syscall and I/O pressure; the AntiMage-s3 baseline reported
// openvpn/l2tp/pptp state mtime changing on every sample while they had zero
// users.
//
// The skip decision is only ever an optimisation. If the on-disk file does not
// provably match the bytes this process last persisted, the write happens
// exactly as before, so no caller can observe a skipped write it depended on.

// accountingStateDedupMaxPaths bounds the per-server hint map. Every path is
// derived from cfg.DataDir and a fixed protocol suffix, so the set is tiny in
// production; the bound only exists so a caller with a generated path can never
// grow the map without limit. Reaching it drops the hints rather than silently
// disabling the optimisation, which costs at most one redundant durable write
// per path before the map repopulates.
const accountingStateDedupMaxPaths = 64

type accountingStateFingerprint struct {
	hash    [32]byte
	size    int64
	modTime int64
	path    string
	info    os.FileInfo
}

// accountingStateUnchanged reports whether the file on disk is provably the
// exact file this process last published for these bytes. Size, modification
// time and file identity must all match. Every writer in this package publishes
// through an atomic rename of a fresh temporary file, so a rewrite by any code
// path yields a different file identity and a changed mtime. os.SameFile is
// used instead of raw inode access so the check compiles and stays correct on
// Windows as well as Unix.
func accountingStateUnchanged(previous accountingStateFingerprint, digest [32]byte) bool {
	if previous.hash != digest || previous.info == nil {
		return false
	}
	info, err := os.Stat(previous.path)
	if err != nil {
		return false
	}
	if info.Size() != previous.size {
		return false
	}
	if info.ModTime().UnixNano() != previous.modTime {
		return false
	}
	return os.SameFile(previous.info, info)
}

// writeAccountingStateDeduped skips the durable write when the exact bytes were
// already persisted and the file still provably matches on disk. All other
// behaviour, including the failure modes, is identical to writeAccountingState.
//
// A failed write never records a fingerprint, so it is retried on the next
// tick. The hint map is per Server, so one instance's temporary directory can
// never mask another instance's write.
func (s *Server) writeAccountingStateDeduped(path string, raw []byte) error {
	digest := sha256.Sum256(raw)

	s.accountingStateDedupMu.Lock()
	if s.accountingStateDedupKnown == nil {
		s.accountingStateDedupKnown = map[string]accountingStateFingerprint{}
	}
	previous, seen := s.accountingStateDedupKnown[path]
	s.accountingStateDedupMu.Unlock()

	if seen && accountingStateUnchanged(previous, digest) {
		return nil
	}

	if err := writeAccountingState(path, raw); err != nil {
		return err
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}

	s.accountingStateDedupMu.Lock()
	if s.accountingStateDedupKnown == nil {
		s.accountingStateDedupKnown = map[string]accountingStateFingerprint{}
	}
	if len(s.accountingStateDedupKnown) >= accountingStateDedupMaxPaths && !seen {
		s.accountingStateDedupKnown = map[string]accountingStateFingerprint{}
	}
	s.accountingStateDedupKnown[path] = accountingStateFingerprint{
		hash:    digest,
		size:    info.Size(),
		modTime: info.ModTime().UnixNano(),
		path:    path,
		info:    info,
	}
	s.accountingStateDedupMu.Unlock()
	return nil
}

// resetAccountingStateDedup drops every cached hint for this Server. Tests use
// it when they mutate a state file on disk behind the writer.
func (s *Server) resetAccountingStateDedup() {
	s.accountingStateDedupMu.Lock()
	s.accountingStateDedupKnown = map[string]accountingStateFingerprint{}
	s.accountingStateDedupMu.Unlock()
}

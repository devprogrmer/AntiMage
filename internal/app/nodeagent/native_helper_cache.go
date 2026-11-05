package nodeagent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const maxNativeHelperCacheBytes = 8 << 20
const maxNativeHelperCacheEntries = 128

type nativeHelperEntry struct {
	digest [32]byte
	value  any
	size   int
	used   uint64
}

// Cached helper maps/slices are immutable. This reader always opens and hashes
// the current file, so same-size/same-mtime edits, deletion, unreadable files,
// generation markers and malformed replacements cannot reuse stale policy.
func readNativeHelper[T any](s *Server, path string) (T, error) {
	var out T
	s.nativeHelperMu.Lock()
	defer s.nativeHelperMu.Unlock()
	file, err := os.Open(path)
	if err != nil {
		s.forgetNativeHelperLocked(path)
		return out, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxOfflineAccountingBytes {
		return out, fmt.Errorf("native helper is not a regular file within byte capacity")
	}
	size := int(info.Size())
	if cap(s.nativeHelperScratch) < size+1 {
		s.nativeHelperScratch = make([]byte, size+1)
	}
	if size+1 > maxNativeHelperCacheBytes {
		defer func() { s.nativeHelperScratch = nil }()
	}
	raw := s.nativeHelperScratch[:size+1]
	n, err := io.ReadFull(file, raw)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return out, err
	}
	if n != size {
		return out, fmt.Errorf("native helper changed size during read; retry required")
	}
	raw = raw[:n]
	digest := sha256.Sum256(raw)
	s.nativeHelperClock++
	if entry, ok := s.nativeHelperEntries[path]; ok && entry.digest == digest {
		if cached, ok := entry.value.(T); ok {
			entry.used = s.nativeHelperClock
			s.nativeHelperEntries[path] = entry
			return cached, nil
		}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	s.forgetNativeHelperLocked(path)
	if size > maxNativeHelperCacheBytes {
		return out, nil
	}
	if s.nativeHelperEntries == nil {
		s.nativeHelperEntries = make(map[string]nativeHelperEntry)
	}
	for len(s.nativeHelperEntries) >= maxNativeHelperCacheEntries || s.nativeHelperBytes+size > maxNativeHelperCacheBytes {
		oldestPath := ""
		oldestUsed := ^uint64(0)
		for key, entry := range s.nativeHelperEntries {
			if entry.used <= oldestUsed {
				oldestPath, oldestUsed = key, entry.used
			}
		}
		s.forgetNativeHelperLocked(oldestPath)
	}
	s.nativeHelperEntries[path] = nativeHelperEntry{digest, out, size, s.nativeHelperClock}
	s.nativeHelperBytes += size
	return out, nil
}

func (s *Server) forgetNativeHelperLocked(path string) {
	if entry, ok := s.nativeHelperEntries[path]; ok {
		s.nativeHelperBytes -= entry.size
		delete(s.nativeHelperEntries, path)
	}
}

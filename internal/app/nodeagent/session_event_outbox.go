package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const maxNativeSessionOutboxEvents = 4096

var readNativeSessionOutboxDir = os.ReadDir

// Helper driven VPNs can keep admitting locally valid sessions while the panel
// is unreachable. Persist callback events before returning success so reconnect
// and disconnect metadata can be replayed after the panel or node comes back.
func (s *Server) sendNativeSessionEventOfflineSafe(ctx context.Context, callback nativeRuntimeSessionCallback, event nativeSessionEvent) error {
	callback.URL = strings.TrimSpace(callback.URL)
	if callback.URL == "" {
		return nil
	}
	key, shouldSend, tracked := s.reserveNativeSessionHeartbeat(callback, event)
	if !shouldSend {
		// A prior delivery may be durably queued while the panel was down. Keep
		// replaying that queue even when this duplicate heartbeat is coalesced.
		return s.flushNativeSessionOutbox(ctx, callback)
	}
	forgetReservation := func() {
		if tracked {
			s.forgetNativeSessionHeartbeat(key)
		}
	}
	if err := s.flushNativeSessionOutbox(ctx, callback); err != nil {
		if errors.Is(err, errNativeSessionDeviceLimit) || !nativeSessionCallbackRetryable(err) {
			forgetReservation()
			return err
		}
		if err := s.enqueueNativeSessionEvent(event); err != nil {
			forgetReservation()
			return err
		}
		return nil
	}
	err := s.sendNativeSessionEvent(ctx, callback, event)
	if err == nil || errors.Is(err, errNativeSessionDeviceLimit) {
		if errors.Is(err, errNativeSessionDeviceLimit) {
			forgetReservation()
		}
		return err
	}
	if !nativeSessionCallbackRetryable(err) {
		forgetReservation()
		return err
	}
	if err := s.enqueueNativeSessionEvent(event); err != nil {
		forgetReservation()
		return err
	}
	return nil
}

func nativeSessionCallbackRetryable(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "send native session event:") || strings.Contains(message, "callback returned 5")
}

func (s *Server) nativeSessionOutboxDir() string {
	return filepath.Join(s.cfg.DataDir, "native-session-outbox")
}

func nativeSessionEventKey(event nativeSessionEvent) (string, error) {
	raw, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) enqueueNativeSessionEvent(event nativeSessionEvent) error {
	dir := s.nativeSessionOutboxDir()
	key, err := nativeSessionEventKey(event)
	if err != nil {
		return err
	}
	s.nativeSessionOutboxMu.Lock()
	defer s.nativeSessionOutboxMu.Unlock()
	if err := s.loadNativeSessionOutboxLocked(dir); err != nil {
		return err
	}
	if _, exists := s.nativeSessionOutboxKeys[key]; exists {
		return nil
	}
	if s.nativeSessionOutboxEntryCount >= maxNativeSessionOutboxEvents {
		return fmt.Errorf("native session outbox capacity exceeded")
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%020d-%s.json", time.Now().UTC().UnixNano(), key)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create native session outbox: %w", err)
	}
	if err := writeAccountingState(filepath.Join(dir, name), raw); err != nil {
		return fmt.Errorf("persist native session event: %w", err)
	}
	s.nativeSessionOutboxFiles = append(s.nativeSessionOutboxFiles, name)
	sort.Strings(s.nativeSessionOutboxFiles)
	s.nativeSessionOutboxKeys[key] = struct{}{}
	s.nativeSessionOutboxEntryCount++
	return nil
}

func (s *Server) flushNativeSessionOutbox(ctx context.Context, callback nativeRuntimeSessionCallback) error {
	dir := s.nativeSessionOutboxDir()
	s.nativeSessionOutboxFlushMu.Lock()
	defer s.nativeSessionOutboxFlushMu.Unlock()
	for {
		s.nativeSessionOutboxMu.Lock()
		if err := s.loadNativeSessionOutboxLocked(dir); err != nil {
			s.nativeSessionOutboxMu.Unlock()
			return err
		}
		if len(s.nativeSessionOutboxFiles) == 0 {
			s.nativeSessionOutboxMu.Unlock()
			return nil
		}
		name := s.nativeSessionOutboxFiles[0]
		s.nativeSessionOutboxMu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read queued native session event: %w", err)
		}
		var event nativeSessionEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("parse queued native session event: %w", err)
		}
		if err := s.sendNativeSessionEvent(ctx, callback, event); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("prune delivered native session event: %w", err)
		}
		s.nativeSessionOutboxMu.Lock()
		s.nativeSessionOutboxFiles = removeNativeSessionOutboxFile(s.nativeSessionOutboxFiles, name)
		if key := nativeSessionOutboxKeyFromName(name); key != "" {
			delete(s.nativeSessionOutboxKeys, key)
		}
		if s.nativeSessionOutboxEntryCount > 0 {
			s.nativeSessionOutboxEntryCount--
		}
		s.nativeSessionOutboxMu.Unlock()
		if err := syncNativeSessionOutboxDir(dir); err != nil {
			return err
		}
	}
}

// loadNativeSessionOutboxLocked initializes a per-process FIFO/key index. The
// durable directory is read once per data directory; enqueue/flush then update
// the index as files are atomically persisted and acknowledged.
func (s *Server) loadNativeSessionOutboxLocked(dir string) error {
	if s.nativeSessionOutboxLoaded && s.nativeSessionOutboxLoadedDir == dir {
		return nil
	}
	entries, err := readNativeSessionOutboxDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read native session outbox: %w", err)
	}
	s.nativeSessionOutboxLoadedDir = dir
	s.nativeSessionOutboxLoaded = true
	s.nativeSessionOutboxFiles = s.nativeSessionOutboxFiles[:0]
	s.nativeSessionOutboxKeys = make(map[string]struct{}, len(entries))
	s.nativeSessionOutboxEntryCount = len(entries)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		s.nativeSessionOutboxFiles = append(s.nativeSessionOutboxFiles, entry.Name())
		if key := nativeSessionOutboxKeyFromName(entry.Name()); key != "" {
			s.nativeSessionOutboxKeys[key] = struct{}{}
		}
	}
	return nil
}

func nativeSessionOutboxKeyFromName(name string) string {
	name = strings.TrimSuffix(name, ".json")
	separator := strings.LastIndexByte(name, '-')
	if separator < 0 {
		return ""
	}
	key := name[separator+1:]
	if len(key) != sha256.Size*2 {
		return ""
	}
	return key
}

func removeNativeSessionOutboxFile(files []string, name string) []string {
	for index, file := range files {
		if file == name {
			return append(files[:index], files[index+1:]...)
		}
	}
	return files
}

func syncNativeSessionOutboxDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync native session outbox: %w", err)
	}
	return nil
}

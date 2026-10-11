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

// Helper driven VPNs can keep admitting locally valid sessions while the panel
// is unreachable. Persist callback events before returning success so reconnect
// and disconnect metadata can be replayed after the panel or node comes back.
func (s *Server) sendNativeSessionEventOfflineSafe(ctx context.Context, callback nativeRuntimeSessionCallback, event nativeSessionEvent) error {
	callback.URL = strings.TrimSpace(callback.URL)
	if callback.URL == "" {
		return nil
	}
	if err := s.flushNativeSessionOutbox(ctx, callback); err != nil {
		if errors.Is(err, errNativeSessionDeviceLimit) || !nativeSessionCallbackRetryable(err) {
			return err
		}
		return s.enqueueNativeSessionEvent(event)
	}
	err := s.sendNativeSessionEvent(ctx, callback, event)
	if err == nil || errors.Is(err, errNativeSessionDeviceLimit) {
		return err
	}
	if !nativeSessionCallbackRetryable(err) {
		return err
	}
	return s.enqueueNativeSessionEvent(event)
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
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read native session outbox: %w", err)
	}
	key, err := nativeSessionEventKey(event)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), key) {
			return nil
		}
	}
	if len(entries) >= maxNativeSessionOutboxEvents {
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
	return nil
}

func (s *Server) flushNativeSessionOutbox(ctx context.Context, callback nativeRuntimeSessionCallback) error {
	dir := s.nativeSessionOutboxDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read native session outbox: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(dir, entry.Name())
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
		if err := syncNativeSessionOutboxDir(dir); err != nil {
			return err
		}
	}
	return nil
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

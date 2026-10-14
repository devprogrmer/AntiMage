package nodeagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	errNativeSessionDeviceLimit = errors.New("device limit reached")

	nativeSessionHTTPClient = &http.Client{
		Timeout: 5 * time.Second,
	}
)

type nativeSessionEvent struct {
	NodeID     int64  `json:"node_id"`
	UserID     int64  `json:"user_id"`
	Protocol   string `json:"protocol"`
	InboundTag string `json:"inbound_tag,omitempty"`
	SessionID  string `json:"session_id"`
	AssignedIP string `json:"assigned_ip,omitempty"`
	ClientIP   string `json:"client_ip,omitempty"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceType string `json:"device_type,omitempty"`
	ClientName string `json:"client_name,omitempty"`
	Platform   string `json:"platform,omitempty"`
	Event      string `json:"event"`
}

const (
	nativeSessionSeenCoalesceInterval = 30 * time.Second
	maxNativeSessionSeenKeys          = 32768
)

type nativeSessionHeartbeatKey struct {
	nodeID     int64
	userID     int64
	protocol   string
	inboundTag string
	sessionID  string
	callbackID [32]byte
}

type nativeSessionSeenState struct {
	at          time.Time
	fingerprint [32]byte
}

func nativeSessionHeartbeatIdentity(callback nativeRuntimeSessionCallback, event nativeSessionEvent) (nativeSessionHeartbeatKey, [32]byte) {
	nodeID := event.NodeID
	if nodeID <= 0 {
		nodeID = callback.NodeID
	}
	callbackID := sha256.Sum256([]byte(strings.TrimSpace(callback.URL) + "\x00" + strings.TrimSpace(callback.Token)))
	event.NodeID = nodeID
	event.Event = strings.ToLower(strings.TrimSpace(event.Event))
	eventStateRaw, _ := json.Marshal(event)
	key := nativeSessionHeartbeatKey{
		nodeID:     nodeID,
		userID:     event.UserID,
		protocol:   strings.ToLower(strings.TrimSpace(event.Protocol)),
		inboundTag: strings.TrimSpace(event.InboundTag),
		sessionID:  strings.TrimSpace(event.SessionID),
		callbackID: callbackID,
	}
	return key, sha256.Sum256(eventStateRaw)
}

func (s *Server) reserveNativeSessionHeartbeat(callback nativeRuntimeSessionCallback, event nativeSessionEvent) (nativeSessionHeartbeatKey, bool, bool) {
	key, fingerprint := nativeSessionHeartbeatIdentity(callback, event)
	eventName := strings.ToLower(strings.TrimSpace(event.Event))
	s.nativeSessionSeenMu.Lock()
	defer s.nativeSessionSeenMu.Unlock()
	if eventName != "seen" {
		if eventName == "start" || eventName == "stop" {
			delete(s.nativeSessionSeenSent, key)
		}
		return key, true, false
	}
	if key.nodeID <= 0 || key.userID <= 0 || key.sessionID == "" {
		return key, true, false
	}
	if s.nativeSessionSeenSent == nil {
		s.nativeSessionSeenSent = make(map[nativeSessionHeartbeatKey]nativeSessionSeenState)
	}
	now := time.Now()
	previous, exists := s.nativeSessionSeenSent[key]
	if exists && previous.fingerprint == fingerprint && now.Sub(previous.at) < nativeSessionSeenCoalesceInterval {
		return key, false, false
	}
	if exists || len(s.nativeSessionSeenSent) < maxNativeSessionSeenKeys {
		s.nativeSessionSeenSent[key] = nativeSessionSeenState{at: now, fingerprint: fingerprint}
		return key, true, true
	}
	for staleKey, state := range s.nativeSessionSeenSent {
		if now.Sub(state.at) >= nativeSessionSeenCoalesceInterval {
			delete(s.nativeSessionSeenSent, staleKey)
		}
	}
	if len(s.nativeSessionSeenSent) < maxNativeSessionSeenKeys {
		s.nativeSessionSeenSent[key] = nativeSessionSeenState{at: now, fingerprint: fingerprint}
		return key, true, true
	}
	// Under extreme cardinality, favor delivering the heartbeat over retaining
	// an unbounded process-local dedupe index.
	return key, true, false
}

func (s *Server) forgetNativeSessionHeartbeat(key nativeSessionHeartbeatKey) {
	s.nativeSessionSeenMu.Lock()
	delete(s.nativeSessionSeenSent, key)
	s.nativeSessionSeenMu.Unlock()
}

func (s *Server) sendNativeSessionEvent(
	ctx context.Context,
	callback nativeRuntimeSessionCallback,
	event nativeSessionEvent,
) error {
	callback.URL = strings.TrimSpace(callback.URL)
	callback.Token = strings.TrimSpace(callback.Token)

	if callback.URL == "" {
		return nil
	}

	if callback.NodeID <= 0 {
		return fmt.Errorf("native session callback node_id is required")
	}

	event.NodeID = callback.NodeID
	event.Event = strings.ToLower(strings.TrimSpace(event.Event))

	switch event.Event {
	case "start", "seen", "stop":
	default:
		return fmt.Errorf(
			"unsupported native session event %q",
			event.Event,
		)
	}

	if event.UserID <= 0 {
		return fmt.Errorf("native session user_id is required")
	}
	if strings.TrimSpace(event.SessionID) == "" {
		return fmt.Errorf("native session_id is required")
	}
	if strings.TrimSpace(event.Protocol) == "" {
		return fmt.Errorf("native session protocol is required")
	}

	raw, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal native session event: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		callback.URL,
		bytes.NewReader(raw),
	)
	if err != nil {
		return fmt.Errorf("create native session request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	if callback.Token != "" {
		req.Header.Set(
			"Authorization",
			"Bearer "+callback.Token,
		)
	}

	resp, err := nativeSessionHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("send native session event: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	if resp.StatusCode == http.StatusConflict {
		return errNativeSessionDeviceLimit
	}

	detail := strings.TrimSpace(string(body))
	if detail == "" {
		detail = resp.Status
	}

	return fmt.Errorf(
		"native session callback returned %d: %s",
		resp.StatusCode,
		detail,
	)
}

func (s *Server) dispatchNativeSessionEvents(
	callback nativeRuntimeSessionCallback,
	events []nativeSessionEvent,
	onResult func(nativeSessionEvent, error),
) {
	if strings.TrimSpace(callback.URL) == "" || len(events) == 0 {
		return
	}
	events = append([]nativeSessionEvent(nil), events...)
	go func() {
		for _, event := range events {
			timeout := nativeSessionHTTPClient.Timeout
			if timeout <= 0 {
				timeout = 5 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			err := s.sendNativeSessionEventOfflineSafe(ctx, callback, event)
			cancel()
			if onResult != nil {
				onResult(event, err)
			}
		}
	}()
}

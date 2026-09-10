package nodeagent

import (
	"bytes"
	"context"
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
	Event      string `json:"event"`
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

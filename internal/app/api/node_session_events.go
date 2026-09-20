package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/antimage/antimage/internal/app/nodecontroller"
)

type nodeSessionEventPayload struct {
	Token      string `json:"token,omitempty"`
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

func (s *Server) handleNodeSessionEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var payload nodeSessionEventPayload
	if err := decodeOptionalJSON(r, &payload); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	if token := bearerToken(r); token != "" {
		payload.Token = token
	}
	if err := s.validateNodeSessionEvent(r.Context(), payload); err != nil {
		writeStatusError(w, err)
		return
	}
	if strings.EqualFold(strings.TrimSpace(payload.Event), "ready") {
		nodeID := payload.NodeID
		if err := s.nodeControllerQueueSync(r.Context(), &nodeID, map[string]any{"source": "node_ready"}); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		s.kickNodeOperationsSoon()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if err := s.applyNodeSessionEvent(r.Context(), payload); err != nil {
		if errors.Is(err, errDeviceLimitReached) {
			writeError(w, http.StatusConflict, "device limit reached")
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

var errDeviceLimitReached = errors.New("device limit reached")

func (s *Server) validateNodeSessionEvent(ctx context.Context, payload nodeSessionEventPayload) error {
	event := strings.ToLower(strings.TrimSpace(payload.Event))
	if payload.NodeID <= 0 {
		return statusError{status: http.StatusBadRequest, detail: "node_id is required"}
	}
	if event != "ready" && (payload.UserID <= 0 || strings.TrimSpace(payload.SessionID) == "") {
		return statusError{status: http.StatusBadRequest, detail: "node_id, user_id and session_id are required"}
	}
	if event != "ready" {
		switch strings.ToLower(strings.TrimSpace(payload.Protocol)) {
		case "ov", "openvpn", "l2tp", "pptp", "wg", "wireguard", "awg", "amneziawg", "ikev2", "anyconnect":
		default:
			return statusError{status: http.StatusBadRequest, detail: "unsupported protocol"}
		}
	}
	switch event {
	case "ready", "start", "stop", "seen":
	default:
		return statusError{status: http.StatusBadRequest, detail: "unsupported event"}
	}
	var cert string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(certificate, '') FROM nodes WHERE id = ? AND LOWER(COALESCE(status, '')) <> 'deleted' LIMIT 1`, payload.NodeID).Scan(&cert)
	if err == sql.ErrNoRows {
		return statusError{status: http.StatusForbidden, detail: "node not found"}
	}
	if err != nil {
		return err
	}
	secret, err := s.nodeSessionCallbackSecret(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(secret) == "" {
		return statusError{status: http.StatusForbidden, detail: "node session secret is not configured"}
	}
	expected := nodecontroller.NodeSessionEventToken(secret, payload.NodeID, cert)
	if expected == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(strings.TrimSpace(payload.Token))) != 1 {
		return statusError{status: http.StatusForbidden, detail: "invalid node session token"}
	}
	return nil
}

func (s *Server) nodeSessionCallbackSecret(ctx context.Context) (string, error) {
	var adminSecret, legacySecret sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT admin_secret_key, secret_key FROM jwt ORDER BY id LIMIT 1`).Scan(&adminSecret, &legacySecret)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if value := strings.TrimSpace(adminSecret.String); value != "" {
		return value, nil
	}
	return strings.TrimSpace(legacySecret.String), nil
}

func (s *Server) applyNodeSessionEvent(ctx context.Context, payload nodeSessionEventPayload) error {
	s.sessionAdmissionMu.Lock()
	defer s.sessionAdmissionMu.Unlock()

	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	switch strings.ToLower(strings.TrimSpace(payload.Event)) {
	case "start", "seen":
		if _, err := tx.ExecContext(ctx, `
UPDATE vpn_user_sessions
SET last_seen_at = ?, ended_at = ?
WHERE user_id = ? AND ended_at IS NULL
  AND COALESCE(assigned_ip, '') = '' AND COALESCE(client_ip, '') = ''`,
			dbTimestamp(now), dbTimestamp(now), payload.UserID); err != nil {
			return err
		}
		if normalizedVPNProtocol(payload.Protocol) == "ov" && strings.TrimSpace(payload.AssignedIP) != "" {
			if _, err := tx.ExecContext(ctx, `
UPDATE vpn_user_sessions
SET last_seen_at = ?, ended_at = ?
WHERE node_id = ? AND user_id = ? AND protocol = 'ov' AND COALESCE(inbound_tag, '') = ?
  AND COALESCE(assigned_ip, '') = ? AND session_id <> ? AND ended_at IS NULL`,
				dbTimestamp(now),
				dbTimestamp(now),
				payload.NodeID,
				payload.UserID,
				strings.TrimSpace(payload.InboundTag),
				strings.TrimSpace(payload.AssignedIP),
				strings.TrimSpace(payload.SessionID),
			); err != nil {
				return err
			}
		}
		allowed, err := sessionAdmissionAllowed(ctx, tx, payload)
		if err != nil {
			return err
		}
		if !allowed {
			return errDeviceLimitReached
		}
		if err := upsertVPNSession(ctx, tx, payload, now); err != nil {
			return err
		}
	case "stop":
		if err := endVPNSession(ctx, tx, payload, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func sessionAdmissionAllowed(ctx context.Context, tx *sql.Tx, payload nodeSessionEventPayload) (bool, error) {
	var ipLimit, deviceLimit int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(ip_limit, 0), COALESCE(device_limit, 0) FROM users WHERE id = ?`, payload.UserID).Scan(&ipLimit, &deviceLimit); err != nil {
		return false, err
	}
	if ipLimit <= 0 && deviceLimit <= 0 {
		return true, nil
	}

	rows, err := tx.QueryContext(ctx, `
SELECT node_id, session_id, COALESCE(assigned_ip, ''), COALESCE(client_ip, ''), COALESCE(device_id, '')
FROM vpn_user_sessions
WHERE user_id = ? AND ended_at IS NULL`, payload.UserID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	incomingIP := globalSessionDeviceKey(payload.AssignedIP, payload.ClientIP)
	incomingDevice := strings.TrimSpace(payload.DeviceID)
	ips := map[string]struct{}{}
	devices := map[string]struct{}{}
	for rows.Next() {
		var nodeID int64
		var sessionID, assignedIP, clientIP, deviceID string
		if err := rows.Scan(&nodeID, &sessionID, &assignedIP, &clientIP, &deviceID); err != nil {
			return false, err
		}
		if nodeID == payload.NodeID && strings.TrimSpace(sessionID) == strings.TrimSpace(payload.SessionID) {
			continue
		}
		if key := globalSessionDeviceKey(assignedIP, clientIP); key != "" {
			ips[key] = struct{}{}
		}
		if deviceID = strings.TrimSpace(deviceID); deviceID != "" {
			devices[deviceID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if ipLimit > 0 && incomingIP != "" {
		if _, exists := ips[incomingIP]; !exists && int64(len(ips)) >= ipLimit {
			return false, nil
		}
	}
	if deviceLimit > 0 && incomingDevice != "" && supportsHardDeviceIdentity(payload.Protocol) {
		if _, exists := devices[incomingDevice]; !exists && int64(len(devices)) >= deviceLimit {
			return false, nil
		}
	}
	return true, nil
}

func supportsHardDeviceIdentity(protocol string) bool {
	switch normalizedVPNProtocol(protocol) {
	case "wg", "amneziawg":
		return true
	default:
		return false
	}
}

func globalSessionDeviceKey(assignedIP, clientIP string) string {
	if value := strings.TrimSpace(clientIP); value != "" {
		return "client:" + value
	}
	if value := strings.TrimSpace(assignedIP); value != "" {
		return "assigned:" + value
	}
	return ""
}

func upsertVPNSession(ctx context.Context, tx *sql.Tx, payload nodeSessionEventPayload, now time.Time) error {
	res, err := tx.ExecContext(ctx, `
UPDATE vpn_user_sessions
SET user_id = ?, protocol = ?, inbound_tag = ?, assigned_ip = ?, client_ip = ?, device_id = ?, device_type = ?, client_name = ?, platform = ?, last_seen_at = ?, ended_at = NULL
WHERE node_id = ? AND session_id = ?`,
		payload.UserID,
		normalizedVPNProtocol(payload.Protocol),
		nullableTrimmed(payload.InboundTag),
		nullableTrimmed(payload.AssignedIP),
		nullableTrimmed(payload.ClientIP),
		nullableTrimmed(payload.DeviceID), normalizeDeviceMetadata(payload.DeviceType), normalizeDeviceMetadata(payload.ClientName), normalizeDeviceMetadata(payload.Platform),
		dbTimestamp(now),
		payload.NodeID,
		strings.TrimSpace(payload.SessionID),
	)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO vpn_user_sessions (node_id, user_id, protocol, inbound_tag, session_id, assigned_ip, client_ip, device_id, device_type, client_name, platform, started_at, last_seen_at, ended_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		payload.NodeID,
		payload.UserID,
		normalizedVPNProtocol(payload.Protocol),
		nullableTrimmed(payload.InboundTag),
		strings.TrimSpace(payload.SessionID),
		nullableTrimmed(payload.AssignedIP),
		nullableTrimmed(payload.ClientIP),
		nullableTrimmed(payload.DeviceID), normalizeDeviceMetadata(payload.DeviceType), normalizeDeviceMetadata(payload.ClientName), normalizeDeviceMetadata(payload.Platform),
		dbTimestamp(now),
		dbTimestamp(now),
	)
	return err
}

func endVPNSession(ctx context.Context, tx *sql.Tx, payload nodeSessionEventPayload, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
UPDATE vpn_user_sessions
SET last_seen_at = ?, ended_at = ?
WHERE node_id = ? AND session_id = ? AND user_id = ? AND ended_at IS NULL`,
		dbTimestamp(now),
		dbTimestamp(now),
		payload.NodeID,
		strings.TrimSpace(payload.SessionID),
		payload.UserID,
	)
	return err
}

func normalizedVPNProtocol(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openvpn":
		return "ov"
	case "wireguard":
		return "wg"
	case "awg":
		return "amneziawg"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeDeviceMetadata(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Unknown"
	}
	if len(value) > 64 {
		return value[:64]
	}
	return value
}

func nullableTrimmed(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

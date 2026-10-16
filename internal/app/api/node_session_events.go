package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
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

// Admission remains serialized per user across all nodes because IP/device
// limits are global to that user. Locks are created only while a user has
// waiters and are removed after the last callback releases its reference.

// Native session helpers may emit duplicate heartbeats. Persist at most one
// last_seen_at write per session every 30 seconds; admission still runs when a
// seen event has no active row yet.
const sessionSeenPersistInterval = 30 * time.Second

type sessionAdmissionLockRef struct {
	mu   sync.Mutex
	refs int
}

func (s *Server) lockSessionAdmission(userID int64) func() {
	s.admissionLockMu.Lock()
	if s.admissionLockRefs == nil {
		s.admissionLockRefs = make(map[int64]*sessionAdmissionLockRef)
	}
	ref := s.admissionLockRefs[userID]
	if ref == nil {
		ref = &sessionAdmissionLockRef{}
		s.admissionLockRefs[userID] = ref
	}
	ref.refs++
	s.admissionLockMu.Unlock()

	ref.mu.Lock()
	return func() {
		ref.mu.Unlock()
		s.admissionLockMu.Lock()
		ref.refs--
		if ref.refs == 0 && s.admissionLockRefs[userID] == ref {
			delete(s.admissionLockRefs, userID)
		}
		s.admissionLockMu.Unlock()
	}
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
	if event := strings.ToLower(strings.TrimSpace(payload.Event)); event == "start" || event == "seen" {
		// Durable node outboxes can replay a start after its user was deleted
		// while the panel was unavailable. Acknowledge that stale event as a
		// no-op so it cannot pin later valid events in the FIFO. Stop events are
		// still processed below to close any session row created before deletion.
		var userID int64
		err := s.db.QueryRowContext(r.Context(), `
SELECT id FROM users
WHERE id = ? AND LOWER(COALESCE(status, '')) <> 'deleted'
LIMIT 1`, payload.UserID).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": "deleted_user"})
			return
		}
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
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
	var cert, adminSecret, legacySecret sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(n.certificate, ''), COALESCE(j.admin_secret_key, ''), COALESCE(j.secret_key, '')
FROM nodes n
LEFT JOIN (SELECT admin_secret_key, secret_key FROM jwt ORDER BY id LIMIT 1) j ON 1 = 1
WHERE n.id = ? AND LOWER(COALESCE(n.status, '')) <> 'deleted'
LIMIT 1`, payload.NodeID).Scan(&cert, &adminSecret, &legacySecret)
	if err == sql.ErrNoRows {
		return statusError{status: http.StatusForbidden, detail: "node not found"}
	}
	if err != nil {
		return err
	}
	secret := strings.TrimSpace(adminSecret.String)
	if secret == "" {
		secret = strings.TrimSpace(legacySecret.String)
	}
	if strings.TrimSpace(secret) == "" {
		return statusError{status: http.StatusForbidden, detail: "node session secret is not configured"}
	}
	expected := nodecontroller.NodeSessionEventToken(secret, payload.NodeID, cert.String)
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
	now := time.Now().UTC()
	if strings.EqualFold(strings.TrimSpace(payload.Event), "seen") {
		active, err := s.persistSeenForExistingSession(ctx, payload, now)
		if err != nil {
			return err
		}
		if active {
			return nil
		}
	}

	unlockAdmission := s.lockSessionAdmission(payload.UserID)
	defer unlockAdmission()

	now = time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if strings.EqualFold(strings.TrimSpace(payload.Event), "seen") {
		var active int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM vpn_user_sessions WHERE node_id = ? AND user_id = ? AND session_id = ? AND ended_at IS NULL LIMIT 1`, payload.NodeID, payload.UserID, strings.TrimSpace(payload.SessionID)).Scan(&active)
		if err == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE vpn_user_sessions SET last_seen_at = ? WHERE node_id = ? AND user_id = ? AND session_id = ? AND ended_at IS NULL AND last_seen_at < ?`, dbTimestamp(now), payload.NodeID, payload.UserID, strings.TrimSpace(payload.SessionID), dbTimestamp(now.Add(-sessionSeenPersistInterval))); err != nil {
				return err
			}
			return tx.Commit()
		}
		if err != sql.ErrNoRows {
			return err
		}
	}

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
		if normalizedVPNProtocol(payload.Protocol) == "ov" &&
			strings.TrimSpace(payload.AssignedIP) != "" &&
			strings.TrimSpace(payload.DeviceID) == "" {
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
		allowed, err := sessionAdmissionAllowed(ctx, tx, payload, s.dialect)
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

// Existing session heartbeats do not participate in admission: atomically
// touch the active row before acquiring the per-user admission lock. New or
// ended sessions still fall through to the serialized admission transaction.
func (s *Server) persistSeenForExistingSession(ctx context.Context, payload nodeSessionEventPayload, now time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE vpn_user_sessions
SET last_seen_at = ?
WHERE node_id = ? AND user_id = ? AND session_id = ? AND ended_at IS NULL AND last_seen_at < ?`,
		dbTimestamp(now), payload.NodeID, payload.UserID, strings.TrimSpace(payload.SessionID), dbTimestamp(now.Add(-sessionSeenPersistInterval)))
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed > 0 {
		return true, nil
	}
	var active int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM vpn_user_sessions WHERE node_id = ? AND user_id = ? AND session_id = ? AND ended_at IS NULL LIMIT 1`, payload.NodeID, payload.UserID, strings.TrimSpace(payload.SessionID)).Scan(&active)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, err
}

func sessionAdmissionAllowed(ctx context.Context, tx *sql.Tx, payload nodeSessionEventPayload, dialect string) (bool, error) {
	var ipLimit, deviceLimit int64
	if err := tx.QueryRowContext(ctx, sessionAdmissionPolicyQuery(dialect), payload.UserID).Scan(&ipLimit, &deviceLimit); err != nil {
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

func sessionAdmissionPolicyQuery(dialect string) string {
	query := `SELECT COALESCE(ip_limit, 0), COALESCE(device_limit, 0) FROM users WHERE id = ?`
	dialect = strings.ToLower(strings.TrimSpace(dialect))
	if strings.Contains(dialect, "mysql") || strings.Contains(dialect, "maria") {
		query += " FOR UPDATE"
	}
	return query
}

func supportsHardDeviceIdentity(protocol string) bool {
	switch normalizedVPNProtocol(protocol) {
	case "wg", "amneziawg", "ov":
		// WG/AWG use stable cryptographic peer identity.
		// OpenVPN uses a per-concurrent-session identity because the stock
		// protocol does not expose a hardware-stable device identifier.
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

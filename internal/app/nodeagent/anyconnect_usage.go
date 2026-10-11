package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

var anyConnectUsageCommandContext = exec.CommandContext

type anyConnectUsageSample struct {
	UserID     int64    `json:"user_id"`
	InboundTag string   `json:"inbound_tag"`
	Value      uint64   `json:"value"`
	Online     bool     `json:"online"`
	IPs        []string `json:"ips,omitempty"`
	Upload     uint64   `json:"upload,omitempty"`
	Download   uint64   `json:"download,omitempty"`
}
type anyConnectUsagePendingBatch struct {
	SeenUnix     int64                   `json:"seen_unix,omitempty"`
	BatchID      string                  `json:"batch_id"`
	Samples      []anyConnectUsageSample `json:"samples"`
	NextBaseline map[string]uint64       `json:"next_baseline"`
}
type anyConnectUsageDiskState struct {
	Baseline         map[string]uint64            `json:"baseline"`
	Pending          *anyConnectUsagePendingBatch `json:"pending,omitempty"`
	LastAckedBatchID string                       `json:"last_acked_batch_id,omitempty"`
}
type anyConnectLiveSession struct {
	StartedAt                          string
	DisconnectID, GenerationID         string
	ID, Username, ClientIP, AssignedIP string
	Received, Sent, RXSpeed, TXSpeed   uint64
}

func parseAnyConnectUsersJSON(raw []byte) ([]anyConnectLiveSession, error) {
	var root any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	switch value := root.(type) {
	case []any:
	case map[string]any:
		valid := false
		for _, key := range []string{"users", "sessions", "user_list"} {
			if _, ok := value[key].([]any); ok {
				valid = true
			}
		}
		if !valid {
			return nil, fmt.Errorf("unknown occtl users response")
		}
	default:
		return nil, fmt.Errorf("invalid occtl users response")
	}
	rows := findAnyConnectRows(root)
	out := make([]anyConnectLiveSession, 0, len(rows))
	for _, row := range rows {
		username := jsonString(row, "username", "user", "name")
		if username == "" {
			continue
		}
		for _, keys := range [][]string{{"raw_rx", "bytes_in", "rx_bytes", "rx"}, {"raw_tx", "bytes_out", "tx_bytes", "tx"}} {
			normalized := normalizeAnyConnectRow(row)
			for _, key := range keys {
				if value, ok := normalized[key]; ok {
					if _, err := strconv.ParseUint(strings.TrimSpace(fmt.Sprint(value)), 10, 64); err != nil {
						return nil, fmt.Errorf("invalid occtl counter %s: %w", key, err)
					}
					break
				}
			}
		}
		out = append(out, anyConnectLiveSession{StartedAt: jsonString(row, "connected_at", "connected_since", "created_at", "session_start"), DisconnectID: jsonString(row, "id"), GenerationID: jsonString(row, "full_session", "session", "session_id"), ID: jsonString(row, "full_session", "session", "session_id", "id"), Username: username, ClientIP: jsonString(row, "remote_ip", "ip_real", "client_ip"), AssignedIP: jsonString(row, "ipv4", "ip_remote", "vpn_ip", "assigned_ip", "local_device_ip"), Received: jsonUint(row, "raw_rx", "bytes_in", "rx_bytes", "rx"), Sent: jsonUint(row, "raw_tx", "bytes_out", "tx_bytes", "tx"), RXSpeed: jsonUint(row, "rx_per_sec"), TXSpeed: jsonUint(row, "tx_per_sec")})
	}
	return out, nil
}
func findAnyConnectRows(v any) []map[string]any {
	switch x := v.(type) {
	case []any:
		out := []map[string]any{}
		for _, item := range x {
			if row, ok := item.(map[string]any); ok {
				out = append(out, row)
			}
		}
		return out
	case map[string]any:
		for _, k := range []string{"users", "sessions", "user_list"} {
			if child, ok := x[k]; ok {
				return findAnyConnectRows(child)
			}
		}
	}
	return nil
}
func jsonString(row map[string]any, keys ...string) string {
	normalized := normalizeAnyConnectRow(row)
	for _, k := range keys {
		if v, ok := normalized[normalizeAnyConnectJSONKey(k)]; ok {
			return strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return ""
}
func jsonUint(row map[string]any, keys ...string) uint64 {
	normalized := normalizeAnyConnectRow(row)
	for _, k := range keys {
		if v, ok := normalized[normalizeAnyConnectJSONKey(k)]; ok {
			switch n := v.(type) {
			case json.Number:
				u, _ := strconv.ParseUint(string(n), 10, 64)
				return u
			case float64:
				if n > 0 {
					return uint64(n)
				}
			case string:
				u, _ := strconv.ParseUint(strings.TrimSpace(n), 10, 64)
				return u
			}
		}
	}
	return 0
}

func normalizeAnyConnectRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for key, value := range row {
		out[normalizeAnyConnectJSONKey(key)] = value
	}
	return out
}

func normalizeAnyConnectJSONKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.NewReplacer("-", "_", " ", "_").Replace(key)
	return key
}

func appendUniqueString(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}
func anyConnectUsageBatchProto(p *anyConnectUsagePendingBatch) *nodev1.UserUsageBatch {
	if p == nil {
		return &nodev1.UserUsageBatch{}
	}
	batch := &nodev1.UserUsageBatch{BatchId: p.BatchID}
	now := offlinePendingSeenUnix(p.SeenUnix, p.BatchID)
	for _, s := range p.Samples {
		uid := "anyconnect:" + strconv.FormatInt(s.UserID, 10)
		if s.Value == 0 && s.Online {
			uid = "online:" + uid
		}
		batch.Stats = append(batch.Stats, &nodev1.UserUsageSample{Uid: uid, Value: s.Value, InboundTag: s.InboundTag})
		ips := make([]*nodev1.OnlineIP, 0, len(s.IPs))
		for _, ip := range s.IPs {
			ips = append(ips, &nodev1.OnlineIP{Ip: ip, LastSeenUnix: now})
		}
		if len(ips) > 0 {
			batch.OnlineIps = append(batch.OnlineIps, &nodev1.OnlineUserIP{Uid: "anyconnect:" + strconv.FormatInt(s.UserID, 10), Ips: ips})
		}
		batch.Speeds = append(batch.Speeds, &nodev1.UserTrafficSpeed{Uid: "anyconnect:" + strconv.FormatInt(s.UserID, 10), Upload: s.Upload, Download: s.Download})
	}
	return batch
}
func (s *Server) ackAnyConnectUserUsage(_ context.Context, req *nodev1.AckUsageRequest) (*nodev1.AckUsageResponse, error) {
	id := strings.TrimSpace(req.GetBatchId())
	if id == "" {
		return &nodev1.AckUsageResponse{}, nil
	}
	s.anyConnectUsageMu.Lock()
	defer s.anyConnectUsageMu.Unlock()
	if err := s.ensureAnyConnectUsageStateLoadedLocked(); err != nil {
		return nil, err
	}
	if s.anyConnectUsageLastAckedBatchID == id {
		return &nodev1.AckUsageResponse{Acknowledged: true}, nil
	}
	p := s.anyConnectUsagePending
	if p == nil || p.BatchID != id {
		return &nodev1.AckUsageResponse{}, nil
	}
	oldBase, oldID := s.anyConnectUsageBaseline, s.anyConnectUsageLastAckedBatchID
	next, err := offlineACKBaseline(s.anyConnectUsageBaseline, p.NextBaseline)
	if err != nil {
		return nil, err
	}
	s.anyConnectUsageBaseline = next
	s.anyConnectUsagePending = nil
	s.anyConnectUsageLastAckedBatchID = id
	if err := s.persistAnyConnectUsageStateLocked(); err != nil {
		s.anyConnectUsageBaseline = oldBase
		s.anyConnectUsagePending = p
		s.anyConnectUsageLastAckedBatchID = oldID
		return nil, err
	}
	return &nodev1.AckUsageResponse{Acknowledged: true}, nil
}
func (s *Server) anyConnectUsageStatePath() string {
	return filepath.Join(s.cfg.DataDir, "anyconnect", "usage-state.json")
}
func (s *Server) ensureAnyConnectUsageStateLoadedLocked() error {
	if s.anyConnectUsageLoaded {
		return nil
	}
	raw, err := readOfflineAccountingState(s.anyConnectUsageStatePath())
	if os.IsNotExist(err) {
		s.anyConnectUsageLoaded = true
		return nil
	}
	if err != nil {
		return err
	}
	var state anyConnectUsageDiskState
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if state.Baseline == nil {
		state.Baseline = map[string]uint64{}
	}
	if len(state.Baseline) > maxAccountingCounterSeries {
		return fmt.Errorf("AnyConnect usage state exceeds safe series capacity")
	}
	if state.Pending != nil && strings.TrimSpace(state.Pending.BatchID) == "" {
		return fmt.Errorf("AnyConnect pending batch has no id")
	}
	s.anyConnectUsageBaseline = state.Baseline
	s.anyConnectUsagePending = state.Pending
	s.anyConnectUsageLastAckedBatchID = state.LastAckedBatchID
	s.anyConnectUsageLoaded = true
	return nil
}
func (s *Server) persistAnyConnectUsageStateLocked() error {
	path := s.anyConnectUsageStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(anyConnectUsageDiskState{Baseline: s.anyConnectUsageBaseline, Pending: s.anyConnectUsagePending, LastAckedBatchID: s.anyConnectUsageLastAckedBatchID})
	if err != nil {
		return err
	}
	return persistOfflineAccountingFile(path, raw)
}

package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type l2TPUsageRuntimeConfig struct {
	InboundTag string           `json:"inbound_tag"`
	Users      map[string]int64 `json:"users"`
}

type l2TPPPPSession struct {
	Interface string
	PeerIP    string
}

type l2TPUsageSample struct {
	UserID     int64  `json:"user_id"`
	InboundTag string `json:"inbound_tag"`
	Value      uint64 `json:"value"`
	Online     bool   `json:"online"`
	IP         string `json:"ip"`
}

type l2TPUsagePendingBatch struct {
	SeenUnix     int64             `json:"seen_unix,omitempty"`
	BatchID      string            `json:"batch_id"`
	Samples      []l2TPUsageSample `json:"samples"`
	NextBaseline map[string]uint64 `json:"next_baseline"`
}

var l2TPPPPInterfacePattern = regexp.MustCompile(`^ppp[0-9]+$`)

func parseL2TPPPPInterfaces(raw string) []l2TPPPPSession {
	result := make([]l2TPPPPSession, 0)

	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		iface := strings.TrimSuffix(fields[1], ":")
		if at := strings.IndexByte(iface, '@'); at >= 0 {
			iface = iface[:at]
		}
		if !l2TPPPPInterfacePattern.MatchString(iface) {
			continue
		}

		peer := ""
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "peer" {
				peer = strings.SplitN(fields[i+1], "/", 2)[0]
				break
			}
		}

		addr, err := netip.ParseAddr(peer)
		if err != nil || !addr.Is4() {
			continue
		}

		result = append(result, l2TPPPPSession{
			Interface: iface,
			PeerIP:    addr.String(),
		})
	}

	return result
}

func readL2TPInterfaceCounter(
	iface string,
	counter string,
) (uint64, error) {
	if !l2TPPPPInterfacePattern.MatchString(iface) {
		return 0, fmt.Errorf("invalid PPP interface %q", iface)
	}
	if counter != "rx_bytes" && counter != "tx_bytes" {
		return 0, fmt.Errorf("invalid PPP counter %q", counter)
	}

	raw, err := os.ReadFile(filepath.Join(
		"/sys/class/net",
		iface,
		"statistics",
		counter,
	))
	if err != nil {
		return 0, err
	}

	return strconv.ParseUint(
		strings.TrimSpace(string(raw)),
		10,
		64,
	)
}

func l2TPUsageBaselineKey(
	inboundTag string,
	iface string,
	peerIP string,
) string {
	return strings.TrimSpace(inboundTag) + "|" +
		strings.TrimSpace(iface) + "|" +
		strings.TrimSpace(peerIP)
}

func l2TPUsageDelta(
	current uint64,
	baseline uint64,
	exists bool,
) uint64 {
	if !exists {
		return current
	}
	if current < baseline {
		return current
	}
	return current - baseline
}

func (s *Server) activeL2TPRuntimeTags() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	tags := make([]string, 0, len(s.l2TPRuntimes))
	for tag := range s.l2TPRuntimes {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

func (s *Server) collectL2TPUserUsage(
	ctx context.Context,
	_ *nodev1.CollectUsageRequest,
) (*nodev1.UserUsageBatch, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.l2TPUsageMu.Lock()
	defer s.l2TPUsageMu.Unlock()

	if err := s.ensureL2TPUsageStateLoadedLocked(); err != nil {
		return nil, err
	}

	nextBaseline := make(
		map[string]uint64,
		len(s.l2TPUsageBaseline),
	)
	for key, value := range s.l2TPUsageBaseline {
		nextBaseline[key] = value
	}

	roots, err := offlineHelperRoots(s.cfg.DataDir, "l2tp")
	if err != nil {
		return nil, err
	}
	var rawPPP []byte
	if len(roots) > 0 {
		rawPPP, err = readOfflinePPPQuery(ctx)
	}
	if err != nil {
		// The final spool remains collectable when the interface command fails.
		rawPPP = nil
	}

	pppSessions := parseL2TPPPPInterfaces(string(rawPPP))

	// Seed legacy pending native snapshots: their deltas are already in the batch.
	if pending := s.l2TPUsagePending; pending != nil {
		legacy := true
		for key := range pending.NextBaseline {
			if _, _, ok := offlineAccountingOwner(key); ok {
				legacy = false
				break
			}
		}
		if legacy {
			values := make([]offlinePendingSample, 0, len(pending.Samples))
			for _, sample := range pending.Samples {
				values = append(values, offlinePendingSample{sample.UserID, sample.InboundTag, sample.Value})
			}
			if err := offlineSeedLegacyPending(nextBaseline, pending.BatchID, values); err != nil {
				return nil, err
			}
			for key, value := range pending.NextBaseline {
				if nextBaseline[key] < value {
					nextBaseline[key] = value
				}
			}
		}
	}

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	aggregated := make(map[aggregateKey]l2TPUsageSample)
	for key, total := range nextBaseline {
		uid, tag, ok := offlineAccountingOwner(key)
		if !ok {
			continue
		}
		sent := nextBaseline[offlineAccountingSentKey(key)]
		if sent > total {
			return nil, fmt.Errorf("invalid offline accounting baseline")
		}
		if total > sent {
			aggregated[aggregateKey{uid, tag}] = l2TPUsageSample{UserID: uid, InboundTag: tag, Value: total - sent}
		}
	}

	for _, root := range roots {
		tag := filepath.Base(root)

		rawConfig, err := os.ReadFile(
			filepath.Join(root, "usage-helper.json"),
		)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			s.appendLog(
				"l2tp usage config read failed for " +
					tag + ": " + err.Error(),
			)
			continue
		}

		var cfg l2TPUsageRuntimeConfig
		if err := json.Unmarshal(rawConfig, &cfg); err != nil {
			s.appendLog(
				"l2tp usage config parse failed for " +
					tag + ": " + err.Error(),
			)
			continue
		}

		if strings.TrimSpace(cfg.InboundTag) == "" {
			cfg.InboundTag = tag
		}

		for _, session := range pppSessions {
			userID := cfg.Users[session.PeerIP]
			if userID <= 0 {
				continue
			}

			rx, err := pppOfflineReadCounter(
				session.Interface,
				"rx_bytes",
			)
			if err != nil {
				s.appendLog(
					"l2tp rx counter read failed for " +
						session.Interface + ": " + err.Error(),
				)
				continue
			}

			tx, err := pppOfflineReadCounter(
				session.Interface,
				"tx_bytes",
			)
			if err != nil {
				s.appendLog(
					"l2tp tx counter read failed for " +
						session.Interface + ": " + err.Error(),
				)
				continue
			}

			if ^uint64(0)-rx < tx {
				continue
			}
			total := rx + tx

			baselineKey := l2TPUsageBaselineKey(
				cfg.InboundTag,
				session.Interface,
				session.PeerIP,
			)
			identity, err := pppOfflineReadIdentity(session.Interface)
			if err != nil {
				return nil, err
			}
			legacyKey := baselineKey
			baselineKey += "\x00" + identity
			if record, sessionErr := pppOfflineActiveSession(root, session.Interface, session.PeerIP, userID); sessionErr == nil {
				if _, finalErr := os.Stat(filepath.Join(root, "ppp-accounting", "final", record.ID+".json")); finalErr == nil {
					continue
				} else if !os.IsNotExist(finalErr) {
					return nil, finalErr
				}
				baselineKey = legacyKey + "\x00session:" + record.ID
			} else if !os.IsNotExist(sessionErr) {
				return nil, sessionErr
			}
			baseline, exists :=
				nextBaseline[baselineKey]

			if !exists {
				baseline, exists = nextBaseline[legacyKey]
				delete(nextBaseline, legacyKey)
			}

			delta := l2TPUsageDelta(
				total,
				baseline,
				exists,
			)
			nextBaseline[baselineKey] = total

			key := aggregateKey{
				UserID:     userID,
				InboundTag: cfg.InboundTag,
			}

			sample := aggregated[key]
			sample.UserID = userID
			sample.InboundTag = cfg.InboundTag
			sample.Online = true
			sample.IP = session.PeerIP

			owner := offlineAccountingTotalKey(userID, cfg.InboundTag)
			if ^uint64(0)-sample.Value < delta || ^uint64(0)-nextBaseline[owner] < delta {
				return nil, fmt.Errorf("offline accounting overflow")
			}
			sample.Value += delta
			nextBaseline[owner] += delta
			aggregated[key] = sample
		}
		finals, err := pppOfflineFinalRecords(root)
		if err != nil {
			return nil, err
		}
		for _, record := range finals {
			if record.InboundTag != cfg.InboundTag {
				return nil, fmt.Errorf("PPP final inbound mismatch")
			}
			delta, err := pppOfflineAdvanceFinal(nextBaseline, record)
			if err != nil {
				return nil, err
			}
			if delta == 0 {
				continue
			}
			key := aggregateKey{record.UserID, record.InboundTag}
			sample := aggregated[key]
			if ^uint64(0)-sample.Value < delta {
				return nil, fmt.Errorf("PPP aggregate overflow")
			}
			sample.UserID = record.UserID
			sample.InboundTag = record.InboundTag
			sample.Value += delta
			aggregated[key] = sample
		}
	}

	if len(nextBaseline) > maxAccountingCounterSeries {
		return nil, fmt.Errorf("offline accounting capacity exceeded; refusing to discard usage")
	}
	if preview := offlinePreview(ctx); preview != nil {
		preview.Baseline = nextBaseline
		if pending := s.l2TPUsagePending; pending != nil {
			preview.PendingID = pending.BatchID
			for _, sample := range pending.Samples {
				preview.PendingSamples = append(preview.PendingSamples, offlinePendingSample{sample.UserID, sample.InboundTag, sample.Value})
			}
		}
		return &nodev1.UserUsageBatch{}, nil
	}
	previousSampled := s.l2TPUsageBaseline
	s.l2TPUsageBaseline = nextBaseline
	if err := s.persistL2TPUsageStateLocked(); err != nil {
		s.l2TPUsageBaseline = previousSampled
		return nil, err
	}
	if s.l2TPUsagePending != nil {
		return l2TPUsageBatchProto(s.l2TPUsagePending), nil
	}
	if offlineCheckpointOnly(ctx) {
		return &nodev1.UserUsageBatch{}, nil
	}

	if len(aggregated) == 0 {
		return &nodev1.UserUsageBatch{}, nil
	}

	keys := make([]aggregateKey, 0, len(aggregated))
	for key := range aggregated {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].UserID == keys[j].UserID {
			return keys[i].InboundTag <
				keys[j].InboundTag
		}
		return keys[i].UserID < keys[j].UserID
	})

	samples := make(
		[]l2TPUsageSample,
		0,
		len(keys),
	)
	for _, key := range keys {
		samples = append(samples, aggregated[key])
	}

	pending := &l2TPUsagePendingBatch{
		SeenUnix: time.Now().UTC().Unix(),
		BatchID: fmt.Sprintf(
			"l2tp-%d",
			time.Now().UTC().UnixNano(),
		),
		Samples:      samples,
		NextBaseline: nextBaseline,
	}

	s.l2TPUsagePending = pending
	if err := s.persistL2TPUsageStateLocked(); err != nil {
		s.l2TPUsagePending = nil
		return nil, err
	}

	return l2TPUsageBatchProto(pending), nil
}

func (s *Server) ackL2TPUserUsage(
	_ context.Context,
	req *nodev1.AckUsageRequest,
) (*nodev1.AckUsageResponse, error) {
	batchID := strings.TrimSpace(req.GetBatchId())
	if batchID == "" {
		return &nodev1.AckUsageResponse{
			Acknowledged: false,
		}, nil
	}

	s.l2TPUsageMu.Lock()
	defer s.l2TPUsageMu.Unlock()

	if err := s.ensureL2TPUsageStateLoadedLocked(); err != nil {
		return nil, err
	}

	if s.l2TPUsageLastAckedBatchID == batchID {
		return &nodev1.AckUsageResponse{
			Acknowledged: true,
		}, nil
	}

	pending := s.l2TPUsagePending
	if pending == nil || pending.BatchID != batchID {
		return &nodev1.AckUsageResponse{
			Acknowledged: false,
		}, nil
	}

	previousBaseline := s.l2TPUsageBaseline
	previousLastAcked := s.l2TPUsageLastAckedBatchID

	values := make([]offlinePendingSample, 0, len(pending.Samples))
	for _, sample := range pending.Samples {
		values = append(values, offlinePendingSample{sample.UserID, sample.InboundTag, sample.Value})
	}
	ackBaseline, err := offlineAckSnapshot(s.l2TPUsageBaseline, pending.NextBaseline, batchID, values)
	if err != nil {
		return nil, err
	}
	s.l2TPUsageBaseline = ackBaseline
	s.l2TPUsagePending = nil
	s.l2TPUsageLastAckedBatchID = batchID

	if err := s.persistL2TPUsageStateLocked(); err != nil {
		s.l2TPUsageBaseline = previousBaseline
		s.l2TPUsagePending = pending
		s.l2TPUsageLastAckedBatchID = previousLastAcked
		return nil, err
	}

	return &nodev1.AckUsageResponse{
		Acknowledged: true,
	}, nil
}

func l2TPUsageBatchProto(
	pending *l2TPUsagePendingBatch,
) *nodev1.UserUsageBatch {
	if pending == nil {
		return &nodev1.UserUsageBatch{}
	}

	stats := make(
		[]*nodev1.UserUsageSample,
		0,
		len(pending.Samples),
	)
	onlineIPs := make(
		[]*nodev1.OnlineUserIP,
		0,
		len(pending.Samples),
	)

	now := pending.SeenUnix
	if now == 0 {
		now = time.Now().UTC().Unix()
	}

	for _, sample := range pending.Samples {
		uid := "l2tp:" +
			strconv.FormatInt(sample.UserID, 10)

		statUID := uid
		if sample.Value == 0 && sample.Online {
			statUID = "online:" + uid
		}

		stats = append(
			stats,
			&nodev1.UserUsageSample{
				Uid:        statUID,
				Value:      sample.Value,
				InboundTag: sample.InboundTag,
			},
		)

		if sample.Online &&
			strings.TrimSpace(sample.IP) != "" {
			onlineIPs = append(
				onlineIPs,
				&nodev1.OnlineUserIP{
					Uid: uid,
					Ips: []*nodev1.OnlineIP{
						{
							Ip:           sample.IP,
							LastSeenUnix: now,
						},
					},
				},
			)
		}
	}

	return &nodev1.UserUsageBatch{
		BatchId:   pending.BatchID,
		Stats:     stats,
		OnlineIps: onlineIPs,
	}
}

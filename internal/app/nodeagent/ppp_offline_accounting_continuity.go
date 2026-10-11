package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Reserved keys keep raw retained totals separate from sampled native counters.
const offlineTotalPrefix = "\x00offline-total\x00"
const offlineSentPrefix = "\x00offline-sent\x00"

type offlineCheckpointContextKey struct{}

type offlinePreviewContextKey struct{}
type offlineAccountingPreview struct {
	Baseline       map[string]uint64
	PendingID      string
	PendingSamples []offlinePendingSample
	OpenVPNClients map[string][]openVPNStatusClient
}

func offlinePreview(ctx context.Context) *offlineAccountingPreview {
	preview, _ := ctx.Value(offlinePreviewContextKey{}).(*offlineAccountingPreview)
	return preview
}

func offlinePreviewPendingRaw(preview *offlineAccountingPreview, uid int64, tag string) (uint64, error) {
	var total uint64
	for _, sample := range preview.PendingSamples {
		if sample.UserID != uid || sample.InboundTag != tag {
			continue
		}
		if ^uint64(0)-total < sample.Value {
			return 0, fmt.Errorf("pending raw usage overflow")
		}
		total += sample.Value
	}
	return total, nil
}

type offlinePendingSample struct {
	UserID     int64
	InboundTag string
	Value      uint64
}

func offlineSeedLegacyPending(baseline map[string]uint64, id string, samples []offlinePendingSample) error {
	marker := "\x00offline-legacy\x00" + id
	if baseline[marker] != 0 {
		return nil
	}
	for _, sample := range samples {
		key := offlineAccountingTotalKey(sample.UserID, sample.InboundTag)
		if ^uint64(0)-baseline[key] < sample.Value {
			return fmt.Errorf("legacy pending usage overflow")
		}
		baseline[key] += sample.Value
	}
	baseline[marker] = 1
	return nil
}

func offlineAckSnapshot(current, snapshot map[string]uint64, id string, samples []offlinePendingSample) (map[string]uint64, error) {
	next := offlineAccountingAckBaseline(current, snapshot)
	if next["\x00offline-legacy\x00"+id] != 0 {
		for _, sample := range samples {
			key := offlineAccountingSentKey(offlineAccountingTotalKey(sample.UserID, sample.InboundTag))
			if ^uint64(0)-next[key] < sample.Value {
				return nil, fmt.Errorf("legacy ACK overflow")
			}
			next[key] += sample.Value
		}
		delete(next, "\x00offline-legacy\x00"+id)
	}
	return next, nil
}

var pppOfflineQuery = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "ip", "-o", "-4", "addr", "show").Output()
}

type offlinePPPQueryContextKey struct{}
type offlinePPPQuerySnapshot struct {
	Raw []byte
	Err error
}

func readOfflinePPPQuery(ctx context.Context) ([]byte, error) {
	if snapshot, ok := ctx.Value(offlinePPPQueryContextKey{}).(offlinePPPQuerySnapshot); ok {
		return snapshot.Raw, snapshot.Err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return pppOfflineQuery(queryCtx)
}

func offlineCheckpointOnly(ctx context.Context) bool {
	v, _ := ctx.Value(offlineCheckpointContextKey{}).(bool)
	return v
}

func offlineAccountingTotalKey(uid int64, tag string) string {
	return offlineTotalPrefix + strconv.FormatInt(uid, 10) + "\x00" + tag
}

func offlineAccountingSentKey(key string) string { return offlineSentPrefix + key }

func offlineAccountingOwner(key string) (int64, string, bool) {
	if !strings.HasPrefix(key, offlineTotalPrefix) {
		return 0, "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(key, offlineTotalPrefix), "\x00", 2)
	if len(parts) != 2 {
		return 0, "", false
	}
	uid, err := strconv.ParseInt(parts[0], 10, 64)
	return uid, parts[1], err == nil && uid > 0
}

func offlineAccountingAckBaseline(current, snapshot map[string]uint64) map[string]uint64 {
	legacy := true
	for key := range current {
		if _, _, ok := offlineAccountingOwner(key); ok {
			legacy = false
			break
		}
	}
	next := make(map[string]uint64, len(current)+len(snapshot))
	for key, value := range current {
		next[key] = value
	}
	for key, value := range snapshot {
		if _, _, ok := offlineAccountingOwner(key); ok {
			next[offlineAccountingSentKey(key)] = value
		} else if !strings.HasPrefix(key, offlineSentPrefix) {
			// Legacy pending batches carry native snapshots only.
			if _, exists := next[key]; !exists || legacy {
				next[key] = value
			}
		}
	}
	return next
}

// ACK is transport confirmation, not proof that UsedTraffic reflects the batch.
// Only an exact reflected snapshot permits subtraction from retained raw totals.
func (s *Server) offlinePolicyRaw(baseline map[string]uint64, protocol string, uid int64, tag, pending, reflected string, pendingRaw uint64) (uint64, error) {
	key := offlineAccountingTotalKey(uid, tag)
	total, hasRetainedTotal := baseline[key]
	sent := baseline[offlineAccountingSentKey(key)]
	if !hasRetainedTotal {
		if sent != 0 {
			return 0, fmt.Errorf("legacy quota state has sent total without retained total")
		}
		total = pendingRaw
	}
	if sent > total {
		return 0, fmt.Errorf("invalid offline quota baseline")
	}
	raw := total - sent
	reflectedPending, err := s.localPendingUsageReflected(protocol, uid, pending, reflected)
	if err != nil {
		return 0, err
	}
	if reflectedPending {
		if pendingRaw > raw {
			return 0, fmt.Errorf("reflected pending usage exceeds retained raw total")
		}
		raw -= pendingRaw
	}
	credit, err := s.localAwaitingReflectionUsage(protocol, uid, tag, pending, reflected)
	if err != nil {
		return 0, err
	}
	if ^uint64(0)-raw < credit {
		return 0, fmt.Errorf("offline quota usage overflow")
	}
	return raw + credit, nil
}

func pppOfflineInterfaceIdentity(iface string) (string, error) {
	if !l2TPPPPInterfacePattern.MatchString(iface) {
		return "", fmt.Errorf("invalid PPP interface")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("read PPP boot identity: %w", err)
	}
	index, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "ifindex"))
	if err != nil {
		return "", fmt.Errorf("read PPP interface identity: %w", err)
	}
	if strings.TrimSpace(string(boot)) == "" || strings.TrimSpace(string(index)) == "" {
		return "", fmt.Errorf("empty PPP identity")
	}
	return strings.TrimSpace(string(boot)) + ":" + strings.TrimSpace(string(index)), nil
}

// One bounded interface query per protocol; no per-user commands.
func (s *Server) checkpointPPPOffline(ctx context.Context) error {
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var raw []byte
	var queryErr error
	l2Roots, l2Err := offlineHelperRoots(s.cfg.DataDir, "l2tp")
	pptpRoots, pptpErr := offlineHelperRoots(s.cfg.DataDir, "pptp")
	if err := errors.Join(l2Err, pptpErr); err != nil {
		return err
	}
	if len(l2Roots)+len(pptpRoots) > 0 {
		raw, queryErr = pppOfflineQuery(queryCtx)
	}
	ctx = context.WithValue(ctx, offlinePPPQueryContextKey{}, offlinePPPQuerySnapshot{raw, queryErr})
	ctx = context.WithValue(ctx, offlineCheckpointContextKey{}, true)
	_, l2Err = s.collectL2TPUserUsage(ctx, nil)
	_, pptpErr = s.collectPPTPUserUsage(ctx, nil)
	return errors.Join(queryErr, l2Err, pptpErr)
}

func (s *Server) previewPPPOffline(ctx context.Context) (map[string]*offlineAccountingPreview, error) {
	rootsL2, err := offlineHelperRoots(s.cfg.DataDir, "l2tp")
	if err != nil {
		return nil, err
	}
	rootsPPTP, err := offlineHelperRoots(s.cfg.DataDir, "pptp")
	if err != nil {
		return nil, err
	}
	var raw []byte
	var queryErr error
	if len(rootsL2)+len(rootsPPTP) > 0 {
		raw, queryErr = readOfflinePPPQuery(ctx)
	}
	ctx = context.WithValue(ctx, offlinePPPQueryContextKey{}, offlinePPPQuerySnapshot{raw, queryErr})
	result := map[string]*offlineAccountingPreview{"l2tp": {}, "pptp": {}}
	_, l2Err := s.collectL2TPUserUsage(context.WithValue(ctx, offlinePreviewContextKey{}, result["l2tp"]), nil)
	_, pptpErr := s.collectPPTPUserUsage(context.WithValue(ctx, offlinePreviewContextKey{}, result["pptp"]), nil)
	return result, errors.Join(queryErr, l2Err, pptpErr)
}

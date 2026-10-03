package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// Reserved baseline entries share the same atomic transaction as pending and ACK.
const offlineNativePrefix = "\x00native:"
const offlineUsagePrefix = "\x00usage:"
const offlineBatchMarker = "\x00offline-v1"
const offlineOnlinePrefix = "\x00online:"
const offlineSeedClosed = "\x00seed-closed"

var offlineReadBootID = offlineBootID

const maxOfflineAccountingBytes = 64 << 20

type offlineAccountingOutput struct {
	bytes.Buffer
	exceeded bool
}

func (out *offlineAccountingOutput) Write(raw []byte) (int, error) {
	count := len(raw)
	available := maxOfflineAccountingBytes - out.Len()
	if len(raw) > available {
		raw = raw[:available]
		out.exceeded = true
	}
	_, _ = out.Buffer.Write(raw)
	return count, nil
}

func offlineCommandOutput(command *exec.Cmd) ([]byte, error) {
	output := &offlineAccountingOutput{}
	command.Stdout = output
	if err := command.Run(); err != nil {
		return nil, err
	}
	if output.exceeded {
		return nil, fmt.Errorf("accounting snapshot exceeds safe byte capacity")
	}
	return output.Bytes(), nil
}

func readOfflineAccountingState(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxOfflineAccountingBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxOfflineAccountingBytes {
		return nil, fmt.Errorf("accounting state exceeds safe byte capacity")
	}
	return raw, nil
}

type offlineUsageOwner struct {
	UserID int64
	Tag    string
}
type offlineUsageObservation struct {
	Generation, Legacy string
	Owner              offlineUsageOwner
	Native             uint64
}

func offlineOwnerKey(owner offlineUsageOwner) string {
	raw, _ := json.Marshal(owner)
	return offlineUsagePrefix + string(raw)
}

func offlineGeneration(parts ...string) string {
	raw, _ := json.Marshal(parts)
	return offlineNativePrefix + string(raw)
}

func offlinePendingSeenUnix(seen int64, batchID string) int64 {
	if seen > 0 {
		return seen
	}
	split := strings.LastIndexByte(batchID, '-')
	if split >= 0 {
		if nanos, err := strconv.ParseInt(batchID[split+1:], 10, 64); err == nil && nanos >= int64(time.Second) {
			return nanos / int64(time.Second)
		}
	}
	return 1
}

func offlineBootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("accounting boot identity: %w", err)
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", fmt.Errorf("empty accounting boot identity")
	}
	return id, nil
}

func advanceOfflineUsage(previous, legacy map[string]uint64, observations []offlineUsageObservation) (map[string]uint64, error) {
	next := make(map[string]uint64, len(previous))
	for k, v := range previous {
		next[k] = v
	}
	seen := map[string]bool{}
	for _, observation := range observations {
		if seen[observation.Generation] {
			return nil, fmt.Errorf("duplicate accounting generation")
		}
		seen[observation.Generation] = true
		base, exists := next[observation.Generation]
		migrationKey := "\x00migrated:" + observation.Legacy
		if !exists && next[migrationKey] == 0 && next[offlineSeedClosed] == 0 {
			base = legacy[observation.Legacy]
			next[migrationKey] = 1
		}
		delta := observation.Native
		if observation.Native >= base {
			delta -= base
		}
		key := offlineOwnerKey(observation.Owner)
		if ^uint64(0)-next[key] < delta {
			return nil, fmt.Errorf("offline accounting overflow")
		}
		next[key] += delta
		next[observation.Generation] = observation.Native
	}
	// Keep generation tombstones: dropping one could charge a reappearing SA twice.
	if len(next) > maxAccountingCounterSeries {
		return nil, fmt.Errorf("offline accounting capacity exceeded; usage retained")
	}
	return next, nil
}

func offlineACKBaseline(current, captured map[string]uint64) (map[string]uint64, error) {
	next := map[string]uint64{}
	if captured[offlineBatchMarker] == 1 {
		for k, v := range current {
			next[k] = v
		}
		for k, v := range captured {
			if !strings.HasPrefix(k, offlineUsagePrefix) {
				continue
			}
			if next[k] < v {
				return nil, fmt.Errorf("offline ACK exceeds retained usage")
			}
			next[k] -= v
			if next[k] == 0 {
				delete(next, k)
			}
		}
	} else {
		for k, v := range captured {
			next[k] = v
		}
		for k, v := range current {
			if strings.HasPrefix(k, "\x00") {
				next[k] = v
			}
		}
	}
	return next, nil
}

func offlineSamples(baseline map[string]uint64) ([]ikev2UsageSample, map[string]uint64, error) {
	live := map[string]ikev2UsageSample{}
	for k := range baseline {
		if strings.HasPrefix(k, offlineOnlinePrefix) {
			var sample ikev2UsageSample
			if err := json.Unmarshal([]byte(strings.TrimPrefix(k, offlineOnlinePrefix)), &sample); err != nil {
				return nil, nil, err
			}
			live[offlineOwnerKey(offlineUsageOwner{sample.UserID, sample.InboundTag})] = sample
		}
	}
	keys := []string{}
	for k := range baseline {
		if strings.HasPrefix(k, offlineUsagePrefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	samples := []ikev2UsageSample{}
	captured := map[string]uint64{offlineBatchMarker: 1}
	for _, k := range keys {
		var owner offlineUsageOwner
		if err := json.Unmarshal([]byte(strings.TrimPrefix(k, offlineUsagePrefix)), &owner); err != nil {
			return nil, nil, err
		}
		if owner.UserID <= 0 || owner.Tag == "" {
			return nil, nil, fmt.Errorf("invalid offline usage owner")
		}
		sample := live[k]
		sample.UserID = owner.UserID
		sample.InboundTag = owner.Tag
		sample.Value = baseline[k]
		samples = append(samples, sample)
		captured[k] = baseline[k]
	}
	return samples, captured, nil
}

func offlineReplaceOnline(baseline map[string]uint64, live map[string]ikev2UsageSample) error {
	for k := range baseline {
		if strings.HasPrefix(k, offlineOnlinePrefix) {
			delete(baseline, k)
		}
	}
	for _, sample := range live {
		raw, err := json.Marshal(sample)
		if err != nil {
			return err
		}
		baseline[offlineOnlinePrefix+string(raw)] = 0
		key := offlineOwnerKey(offlineUsageOwner{sample.UserID, sample.InboundTag})
		if _, ok := baseline[key]; !ok {
			baseline[key] = 0
		}
	}
	if len(baseline) > maxAccountingCounterSeries {
		return fmt.Errorf("offline accounting capacity exceeded")
	}
	return nil
}

func compactOfflineBoot(previous map[string]uint64, boot string) (map[string]uint64, error) {
	next := map[string]uint64{}
	retired := false
	for k, v := range previous {
		if strings.HasPrefix(k, offlineNativePrefix) {
			var parts []string
			if err := json.Unmarshal([]byte(strings.TrimPrefix(k, offlineNativePrefix)), &parts); err != nil || len(parts) == 0 {
				return nil, fmt.Errorf("invalid persisted native accounting generation")
			}
			if parts[0] != boot {
				retired = true
				continue
			}
		}
		next[k] = v
	}
	if retired {
		next[offlineSeedClosed] = 1
		for k := range next {
			if !strings.HasPrefix(k, "\x00") || strings.HasPrefix(k, "\x00migrated:") {
				delete(next, k)
			}
		}
	}
	return next, nil
}

var ikev2OfflineSnapshot = func(ctx context.Context) ([]ikev2RawSA, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := offlineCommandOutput(exec.CommandContext(queryCtx, "swanctl", "--list-sas", "--raw"))
	if err != nil {
		return nil, fmt.Errorf("ikev2 accounting snapshot: %w", err)
	}
	text := string(raw)
	if !strings.Contains(text, "list-sas reply {") {
		return nil, fmt.Errorf("incomplete swanctl accounting snapshot")
	}
	depth := 0
	for _, character := range text {
		if character == '{' {
			depth++
		}
		if character == '}' {
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("invalid swanctl accounting snapshot")
			}
		}
	}
	if depth != 0 || strings.Contains(text, "success=no") {
		return nil, fmt.Errorf("unsuccessful swanctl accounting snapshot")
	}
	for _, key := range []string{"bytes-in", "bytes-out"} {
		for offset := 0; offset < len(text); {
			index := strings.Index(text[offset:], key+"=")
			if index < 0 {
				break
			}
			offset += index
			if _, err := strconv.ParseUint(ikev2RawScalar(text[offset:], key), 10, 64); err != nil {
				return nil, fmt.Errorf("invalid swanctl accounting counter: %w", err)
			}
			offset += len(key) + 1
		}
	}
	return parseIKEv2SwanctlRaw(text), nil
}

func (s *Server) offlineIKEv2Runtimes() (map[string]ikev2RuntimeInbound, error) {
	return s.offlineIKEv2RuntimesForCheckpoint(false)
}

func (s *Server) offlineIKEv2RuntimesForCheckpoint(persist bool) (map[string]ikev2RuntimeInbound, error) {
	policy, err := s.currentRuntimePolicy()
	if err != nil {
		return nil, err
	}
	if policy != nil {
		payload, err := parseNativeRuntimePayload(policy.NativeJSON)
		if err != nil {
			return nil, err
		}
		runtimes := map[string]ikev2RuntimeInbound{}
		for _, inbound := range payload.IKEv2Inbounds {
			runtimes[ikev2ConnectionName(inbound.Tag)] = inbound
		}
		return runtimes, nil
	}
	runtimes := s.activeIKEv2RuntimeInbounds()
	if len(runtimes) > 0 {
		if !persist {
			return runtimes, nil
		}
		// Persist only policy/identity information; credentials are not needed for sampling.
		saved := map[string]ikev2RuntimeInbound{}
		for k, inbound := range runtimes {
			inbound.Settings = nil
			inbound.Users = append([]ikev2RuntimeUser(nil), inbound.Users...)
			for i := range inbound.Users {
				inbound.Users[i].Password = ""
			}
			saved[k] = inbound
		}
		raw, err := json.Marshal(saved)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(s.cfg.DataDir, "ikev2", "offline-runtimes.json")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		if err := persistOfflineAccountingFile(path, raw); err != nil {
			return nil, err
		}
		return runtimes, nil
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "ikev2", "offline-runtimes.json"))
	if os.IsNotExist(err) {
		return runtimes, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &runtimes); err != nil {
		return nil, err
	}
	return runtimes, nil
}

var persistOfflineAccountingFile = writeOfflineAccountingFile

func writeOfflineAccountingFile(path string, raw []byte) error {
	if len(raw) > maxOfflineAccountingBytes {
		return fmt.Errorf("accounting state exceeds safe byte capacity")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".usage-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	// Directory fsync makes replacement durable on the Linux production node.
	if err := dir.Sync(); err != nil && os.PathSeparator != '\\' {
		return err
	}
	return nil
}

func offlineUnackedUsage(baseline map[string]uint64, owner offlineUsageOwner, captured map[string]uint64, samples []ikev2UsageSample) (uint64, error) {
	raw := baseline[offlineOwnerKey(owner)]
	if captured != nil && captured[offlineBatchMarker] != 1 {
		for _, sample := range samples {
			if sample.UserID == owner.UserID && sample.InboundTag == owner.Tag {
				if ^uint64(0)-raw < sample.Value {
					return 0, fmt.Errorf("legacy pending usage overflow")
				}
				raw += sample.Value
			}
		}
	}
	return raw, nil
}

func (s *Server) offlineQuotaRaw(protocol string, owner offlineUsageOwner, baseline, captured map[string]uint64, samples []ikev2UsageSample, pendingID, reflectedID string) (uint64, error) {
	raw, err := offlineUnackedUsage(baseline, owner, captured, samples)
	if err != nil {
		return 0, err
	}
	if pendingID != "" {
		reflected, err := s.localPendingUsageReflected(protocol, owner.UserID, pendingID, reflectedID)
		if err != nil {
			return 0, err
		}
		if reflected {
			for _, sample := range samples {
				if sample.UserID == owner.UserID && sample.InboundTag == owner.Tag {
					if raw < sample.Value {
						return 0, fmt.Errorf("reflected pending usage exceeds retained raw usage")
					}
					raw -= sample.Value
				}
			}
		}
	}
	credit, err := s.localAwaitingReflectionUsage(protocol, owner.UserID, owner.Tag, pendingID, reflectedID)
	if err != nil {
		return 0, err
	}
	if ^uint64(0)-raw < credit {
		return 0, fmt.Errorf("offline quota credit overflow")
	}
	return raw + credit, nil
}

var ikev2OfflineTerminate = (*Server).terminateIKEv2SA

type ikev2OfflineDenial struct {
	SA     ikev2RawSA
	Reason string
}

// Only observed remote endpoints count; tunnel addresses and identities do not.
func offlineIPLimitExceeded(accepted map[string]bool, remote string, limit int64) bool {
	if limit <= 0 {
		return false
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(remote))
	if err != nil || ip.IsUnspecified() || ip.Zone() != "" {
		return false
	}
	key := ip.Unmap().String()
	if accepted[key] {
		return false
	}
	if int64(len(accepted)) >= limit {
		return true
	}
	accepted[key] = true
	return false
}

func (s *Server) quotaCheckIKEv2Offline(ctx context.Context) error {
	s.ikev2UsageMu.Lock()
	defer s.ikev2UsageMu.Unlock()
	if err := s.ensureIKEv2UsageStateLoadedLocked(); err != nil {
		return err
	}
	runtimes, err := s.offlineIKEv2Runtimes()
	if err != nil {
		return err
	}
	if len(runtimes) == 0 {
		return nil
	}
	sas, err := ikev2OfflineSnapshot(ctx)
	if err != nil {
		return err
	}
	next, err := s.previewIKEv2OfflineLocked(runtimes, sas)
	if err != nil {
		return err
	}
	denials := []ikev2OfflineDenial{}
	byUser := map[int64][]ikev2PolicySession{}
	for _, sa := range sas {
		if !strings.EqualFold(strings.TrimSpace(sa.State), "ESTABLISHED") {
			continue
		}
		inbound, ok := runtimes[sa.ConnectionName]
		if !ok {
			continue
		}
		identity := sa.RemoteEAPID
		if identity == "" {
			identity = sa.RemoteID
		}
		user, ok := ikev2PolicyFindUser(inbound, identity)
		if !ok {
			if ikev2PolicyUsesEAP(inbound) {
				denials = append(denials, ikev2OfflineDenial{sa, "unknown or disabled user"})
			}
			continue
		}
		var captured map[string]uint64
		var samples []ikev2UsageSample
		pendingID := ""
		if p := s.ikev2UsagePending; p != nil {
			captured = p.NextBaseline
			samples = p.Samples
			pendingID = p.BatchID
		}
		raw, err := s.offlineQuotaRaw("ikev2", offlineUsageOwner{user.UserID, inbound.Tag}, next, captured, samples, pendingID, user.ReflectedUsageBatchID)
		if err != nil {
			return err
		}
		if reason := ikev2PolicyReason(user, time.Now(), raw); reason != "" {
			denials = append(denials, ikev2OfflineDenial{sa, reason})
			continue
		}
		policy := nativeSessionUserPolicy{Status: user.Status, UsedTraffic: user.UsedTraffic, UsageCoefficient: user.UsageCoefficient, InboundCoefficient: user.InboundCoefficient, ReflectedUsageBatchID: user.ReflectedUsageBatchID}
		if policy.Status == "" {
			policy.Status = "active"
		}
		if user.DataLimit != nil {
			policy.DataLimit = *user.DataLimit
		}
		if user.Expire != nil {
			policy.Expire = *user.Expire
		}
		if allowed, reason := s.localQuotaAllowed("ikev2", user.UserID, inbound.Tag, policy, raw, time.Now()); !allowed {
			denials = append(denials, ikev2OfflineDenial{sa, reason})
			continue
		}
		byUser[user.UserID] = append(byUser[user.UserID], ikev2PolicySession{User: user, SA: sa})
	}
	for _, sessions := range byUser {
		sort.SliceStable(sessions, func(i, j int) bool { return ikev2PolicySAOrder(sessions[i]) < ikev2PolicySAOrder(sessions[j]) })
		ips := map[string]bool{}
		kept := int64(0)
		for _, session := range sessions {
			if limit := session.User.DeviceLimit; limit > 0 && kept >= limit {
				denials = append(denials, ikev2OfflineDenial{session.SA, "device limit reached"})
				continue
			}
			if offlineIPLimitExceeded(ips, session.SA.RemoteHost, session.User.IPLimit) {
				denials = append(denials, ikev2OfflineDenial{session.SA, "IP limit reached"})
				continue
			}
			kept++
		}
	}
	if len(denials) > 0 {
		if err := s.persistIKEv2OfflinePreviewLocked(next); err != nil {
			return err
		}
		for _, denial := range denials {
			if err := ikev2OfflineTerminate(s, ctx, denial.SA, denial.Reason); err != nil {
				return err
			}
		}
	}
	// Keep volatile samples between durable checkpoints, including disappeared SAs.
	s.ikev2UsageBaseline = next
	return nil
}

func (s *Server) checkpointIKEv2Offline(ctx context.Context) error {
	s.ikev2UsageMu.Lock()
	defer s.ikev2UsageMu.Unlock()
	return s.checkpointIKEv2OfflineLocked(ctx)
}

func (s *Server) checkpointIKEv2OfflineLocked(ctx context.Context) error {
	if err := s.ensureIKEv2UsageStateLoadedLocked(); err != nil {
		return err
	}
	runtimes, err := s.offlineIKEv2RuntimesForCheckpoint(true)
	if err != nil {
		return err
	}
	if len(runtimes) == 0 {
		return nil
	}
	sas, err := ikev2OfflineSnapshot(ctx)
	if err != nil {
		return err
	}
	next, err := s.previewIKEv2OfflineLocked(runtimes, sas)
	if err != nil {
		return err
	}
	return s.persistIKEv2OfflinePreviewLocked(next)
}

func (s *Server) persistIKEv2OfflinePreviewLocked(next map[string]uint64) error {
	old := s.ikev2UsageBaseline
	s.ikev2UsageBaseline = next
	if err := s.persistIKEv2UsageStateLocked(); err != nil {
		s.ikev2UsageBaseline = old
		return err
	}
	return nil
}

func (s *Server) previewIKEv2OfflineLocked(runtimes map[string]ikev2RuntimeInbound, sas []ikev2RawSA) (map[string]uint64, error) {
	boot, err := offlineReadBootID()
	if err != nil {
		return nil, err
	}
	observations := []offlineUsageObservation{}
	live := map[string]ikev2UsageSample{}
	for _, sa := range sas {
		inbound, ok := runtimes[sa.ConnectionName]
		if !ok {
			continue
		}
		identity := sa.RemoteEAPID
		if identity == "" {
			identity = sa.RemoteID
		}
		user, ok := ikev2PolicyFindUser(inbound, identity)
		if !ok || user.UserID <= 0 {
			continue
		}
		owner := offlineUsageOwner{user.UserID, inbound.Tag}
		key := offlineOwnerKey(owner)
		sample := live[key]
		sample.UserID = user.UserID
		sample.InboundTag = inbound.Tag
		if strings.EqualFold(sa.State, "ESTABLISHED") {
			sample.Online = true
			sample.IPs = ikev2AppendUniqueIP(sample.IPs, sa.RemoteHost)
		}
		live[key] = sample
		for _, child := range sa.Children {
			if sa.UniqueID == "" || child.UniqueID == "" || sa.InitiatorSPI == "" || sa.ResponderSPI == "" {
				return nil, fmt.Errorf("IKEv2 accounting lacks generation identity")
			}
			if ^uint64(0)-child.BytesIn < child.BytesOut {
				return nil, fmt.Errorf("IKEv2 native counter overflow")
			}
			observations = append(observations, offlineUsageObservation{
				Generation: offlineGeneration(boot, inbound.Tag, sa.UniqueID, sa.InitiatorSPI, sa.ResponderSPI, child.UniqueID, child.SPIIn, child.SPIOut),
				Legacy:     ikev2UsageBaselineKey(inbound.Tag, sa, child), Owner: offlineUsageOwner{user.UserID, inbound.Tag}, Native: child.BytesIn + child.BytesOut,
			})
		}
	}
	legacy := s.ikev2UsageBaseline
	if p := s.ikev2UsagePending; p != nil && p.NextBaseline[offlineBatchMarker] != 1 {
		legacy = p.NextBaseline
	}
	compacted, err := compactOfflineBoot(s.ikev2UsageBaseline, boot)
	if err != nil {
		return nil, err
	}
	next, err := advanceOfflineUsage(compacted, legacy, observations)
	if err != nil {
		return nil, err
	}
	if err := offlineReplaceOnline(next, live); err != nil {
		return nil, err
	}

	return next, nil
}

func (s *Server) collectIKEv2UserUsage(ctx context.Context, _ *nodev1.CollectUsageRequest) (*nodev1.UserUsageBatch, error) {
	s.ikev2UsageMu.Lock()
	defer s.ikev2UsageMu.Unlock()
	if err := s.ensureIKEv2UsageStateLoadedLocked(); err != nil {
		return nil, err
	}
	if s.ikev2UsagePending != nil {
		return ikev2UsageBatchProto(s.ikev2UsagePending), nil
	}
	sampleErr := s.checkpointIKEv2OfflineLocked(ctx)
	samples, captured, err := offlineSamples(s.ikev2UsageBaseline)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		if sampleErr != nil {
			return nil, sampleErr
		}
		return &nodev1.UserUsageBatch{}, nil
	}
	if sampleErr != nil {
		s.appendLog("ikev2 offline checkpoint degraded: " + sampleErr.Error())
		for i := range samples {
			samples[i].Online = false
			samples[i].IPs = nil
		}
	}
	p := &ikev2UsagePendingBatch{SeenUnix: time.Now().Unix(), BatchID: fmt.Sprintf("ikev2-%d", time.Now().UnixNano()), Samples: samples, NextBaseline: captured}
	s.ikev2UsagePending = p
	if err := s.persistIKEv2UsageStateLocked(); err != nil {
		s.ikev2UsagePending = nil
		return nil, err
	}
	return ikev2UsageBatchProto(p), nil
}

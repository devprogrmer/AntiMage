package nodeagent

import (
	"sort"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func accountingPendingBytes[T any](samples []T, value func(T) uint64) (uint64, bool) {
	var total uint64
	for _, sample := range samples {
		amount := value(sample)
		if ^uint64(0)-total < amount {
			return ^uint64(0), true
		}
		total += amount
	}
	return total, false
}

// accountingHealth is a read-only snapshot. An unloaded durable state remains
// explicitly unknown; a dashboard request never triggers a disk read or write.
func (s *Server) accountingHealth() *nodev1.AccountingHealth {
	protocols := []*nodev1.AccountingProtocolStatus{
		{Protocol: "xray"}, {Protocol: "wireguard"}, {Protocol: "amneziawg"},
		{Protocol: "openvpn"}, {Protocol: "l2tp"}, {Protocol: "pptp"},
		{Protocol: "ikev2"}, {Protocol: "anyconnect"},
	}
	s.mu.Lock()
	for _, item := range protocols {
		item.Failure = s.localAccountingFailures[item.Protocol]
		item.LastCheckpointUnix = s.accountingLastCheckpoint[item.Protocol]
		// L2TP and PPTP share the PPP checkpoint worker but retain separate
		// durable usage batches. Show the worker result on both rows.
		if item.Protocol == "l2tp" || item.Protocol == "pptp" {
			item.LastCheckpointUnix = s.accountingLastCheckpoint["ppp"]
			if item.Failure == "" {
				item.Failure = s.localAccountingFailures["ppp"]
			}
		}
	}
	var extras []string
	for protocol := range s.localAccountingFailures {
		switch protocol {
		case "xray", "wireguard", "amneziawg", "openvpn", "l2tp", "pptp", "ppp", "ikev2", "anyconnect":
		default:
			extras = append(extras, protocol)
		}
	}
	sort.Strings(extras)
	for _, protocol := range extras {
		protocols = append(protocols, &nodev1.AccountingProtocolStatus{Protocol: protocol, Failure: s.localAccountingFailures[protocol], LastCheckpointUnix: s.accountingLastCheckpoint[protocol]})
	}
	s.mu.Unlock()

	s.xrayUsageMu.Lock()
	protocols[0].StateLoaded = s.xrayUsageLoaded
	protocols[0].LastAckedBatchId = s.xrayUsageLastAckedBatchID
	if pending := s.xrayUsagePending; pending != nil {
		protocols[0].PendingBatchId = pending.BatchID
		protocols[0].PendingBytes, protocols[0].PendingBytesOverflow = accountingPendingBytes(pending.Samples, func(sample xrayUsageSample) uint64 { return sample.Value })
	}
	s.xrayUsageMu.Unlock()

	s.wireGuardUsageMu.Lock()
	protocols[1].StateLoaded = s.wireGuardUsageLoaded
	protocols[1].LastAckedBatchId = s.wireGuardUsageLastAckedBatchID
	if pending := s.wireGuardUsagePending; pending != nil {
		protocols[1].PendingBatchId = pending.BatchID
		protocols[1].PendingBytes, protocols[1].PendingBytesOverflow = accountingPendingBytes(pending.Samples, func(sample wireGuardUsageSample) uint64 { return sample.Value })
	}
	s.wireGuardUsageMu.Unlock()

	s.amneziaWGUsageMu.Lock()
	protocols[2].StateLoaded = s.amneziaWGUsageLoaded
	protocols[2].LastAckedBatchId = s.amneziaWGUsageLastAckedBatchID
	if pending := s.amneziaWGUsagePending; pending != nil {
		protocols[2].PendingBatchId = pending.BatchID
		protocols[2].PendingBytes, protocols[2].PendingBytesOverflow = accountingPendingBytes(pending.Samples, func(sample amneziaWGUsageSample) uint64 { return sample.Value })
	}
	s.amneziaWGUsageMu.Unlock()

	s.openVPNUsageMu.Lock()
	protocols[3].StateLoaded = s.openVPNUsageLoaded
	protocols[3].LastAckedBatchId = s.openVPNUsageLastAckedBatchID
	if pending := s.openVPNUsagePending; pending != nil {
		protocols[3].PendingBatchId = pending.BatchID
		protocols[3].PendingBytes, protocols[3].PendingBytesOverflow = accountingPendingBytes(pending.Samples, func(sample openVPNUsageSample) uint64 { return sample.Value })
	}
	s.openVPNUsageMu.Unlock()

	s.l2TPUsageMu.Lock()
	protocols[4].StateLoaded = s.l2TPUsageLoaded
	protocols[4].LastAckedBatchId = s.l2TPUsageLastAckedBatchID
	if pending := s.l2TPUsagePending; pending != nil {
		protocols[4].PendingBatchId = pending.BatchID
		protocols[4].PendingBytes, protocols[4].PendingBytesOverflow = accountingPendingBytes(pending.Samples, func(sample l2TPUsageSample) uint64 { return sample.Value })
	}
	s.l2TPUsageMu.Unlock()

	s.pptpUsageMu.Lock()
	protocols[5].StateLoaded = s.pptpUsageLoaded
	protocols[5].LastAckedBatchId = s.pptpUsageLastAckedBatchID
	if pending := s.pptpUsagePending; pending != nil {
		protocols[5].PendingBatchId = pending.BatchID
		protocols[5].PendingBytes, protocols[5].PendingBytesOverflow = accountingPendingBytes(pending.Samples, func(sample pptpUsageSample) uint64 { return sample.Value })
	}
	s.pptpUsageMu.Unlock()

	s.ikev2UsageMu.Lock()
	protocols[6].StateLoaded = s.ikev2UsageLoaded
	protocols[6].LastAckedBatchId = s.ikev2UsageLastAckedBatchID
	if pending := s.ikev2UsagePending; pending != nil {
		protocols[6].PendingBatchId = pending.BatchID
		protocols[6].PendingBytes, protocols[6].PendingBytesOverflow = accountingPendingBytes(pending.Samples, func(sample ikev2UsageSample) uint64 { return sample.Value })
	}
	s.ikev2UsageMu.Unlock()

	s.anyConnectUsageMu.Lock()
	protocols[7].StateLoaded = s.anyConnectUsageLoaded
	protocols[7].LastAckedBatchId = s.anyConnectUsageLastAckedBatchID
	if pending := s.anyConnectUsagePending; pending != nil {
		protocols[7].PendingBatchId = pending.BatchID
		protocols[7].PendingBytes, protocols[7].PendingBytesOverflow = accountingPendingBytes(pending.Samples, func(sample anyConnectUsageSample) uint64 { return sample.Value })
	}
	s.anyConnectUsageMu.Unlock()

	health := &nodev1.AccountingHealth{Protocols: protocols}
	s.localUsageMu.Lock()
	health.ReflectionLedgerLoaded = s.localUsageLoaded
	if s.localUsageLoaded {
		health.AwaitingReflectionBatches = uint32(len(s.localUsageDeliveries))
		if len(s.localUsageDeliveries) > 0 {
			index := s.localUsageIndexLocked()
			health.AwaitingReflectionBytes = index.Bytes
			health.ReflectionBytesOverflow = index.Overflow
			health.OldestReflectionBatchUnix = index.OldestBatchUnix
		}
	}
	s.localUsageMu.Unlock()
	return health
}

// Root delivery IDs are generated when the durable batch is created. Unknown
// legacy IDs remain undated rather than producing a false stale warning.
func reflectionBatchUnix(batchID string) int64 {
	prefix, stamp, ok := strings.Cut(batchID, "-")
	if !ok || (prefix != "combined" && prefix != "merged") || len(stamp) != 19 {
		return 0
	}
	nanos, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || nanos <= 0 {
		return 0
	}
	return nanos / int64(time.Second)
}

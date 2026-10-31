package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

func accountingCheckpointInterval(value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return time.Second, nil
	}
	interval, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || interval < time.Second || interval > time.Minute {
		return 0, fmt.Errorf("accounting checkpoint interval must be between 1s and 1m")
	}
	return interval, nil
}

func (s *Server) checkpointXrayStatsLocked(stats []xrayStat) error {
	return s.checkpointXrayGenerationLocked(stats, s.xrayAccountingGeneration)
}

func (s *Server) checkpointXrayGenerationLocked(stats []xrayStat, generation string) error {
	if generation == "" {
		generation = s.xrayAccountingGeneration
	}
	scratch := s.xrayCounterScratch
	next, err := s.previewXrayAccountingCountersLockedInto(stats, generation, scratch)
	if err != nil {
		return err
	}
	changed := len(next) != len(s.xrayAccountingCounters) || generation != s.xrayAccountingGeneration
	for key, counter := range next {
		if s.xrayAccountingCounters[key] != counter {
			changed = true
			break
		}
	}
	if !changed {
		s.xrayCounterScratch = next
		if s.xrayAccountingJournalRecords >= xrayAccountingJournalCompactRecords || s.xrayAccountingJournalOffset >= xrayAccountingJournalCompactBytes {
			return s.persistXrayUsageStateLocked()
		}
		return nil
	}
	previous, previousGeneration := s.xrayAccountingCounters, s.xrayAccountingGeneration
	record := xrayAccountingJournalRecord{Sequence: s.xrayAccountingJournalSequence + 1, Generation: generation}
	if generation != previousGeneration || (!s.xrayAccountingSnapshotPresent && s.xrayAccountingJournalSequence == 0) {
		record.Full = true
		record.Entries = make([]xrayAccountingJournalEntry, 0, len(next))
		for key, counter := range next {
			record.Entries = append(record.Entries, xrayAccountingJournalEntry{Key: key, Counter: counter})
		}
	} else {
		record.Entries = make([]xrayAccountingJournalEntry, 0, 16)
		for key, counter := range next {
			previousCounter, exists := previous[key]
			if !exists || previousCounter != counter {
				record.Entries = append(record.Entries, xrayAccountingJournalEntry{Key: key, Counter: counter})
			}
		}
		for key := range previous {
			if _, ok := next[key]; !ok {
				record.Removed = append(record.Removed, key)
			}
		}
	}
	raw, err := json.Marshal(record)
	if err != nil {
		s.xrayCounterScratch = scratch
		return fmt.Errorf("marshal xray accounting journal: %w", err)
	}
	newOffset, err := xrayAccountingJournalAppend(s.xrayUsageJournalPath(), s.xrayAccountingJournalOffset, raw)
	if err != nil {
		s.xrayCounterScratch = scratch
		return err
	}
	s.xrayAccountingCounters, s.xrayAccountingGeneration = next, generation
	s.xrayCounterScratch = previous
	s.xrayAccountingJournalSequence = record.Sequence
	s.xrayAccountingJournalOffset = newOffset
	s.xrayAccountingJournalRecords++
	if s.xrayAccountingJournalRecords >= xrayAccountingJournalCompactRecords || s.xrayAccountingJournalOffset >= xrayAccountingJournalCompactBytes {
		return s.persistXrayUsageStateLocked()
	}
	return nil
}

func (s *Server) previewXrayAccountingCountersLocked(stats []xrayStat, generation string) (map[string]accountingCounter, error) {
	return s.previewXrayAccountingCountersLockedInto(stats, generation, nil)
}

func (s *Server) previewXrayAccountingCountersLockedInto(stats []xrayStat, generation string, next map[string]accountingCounter) (map[string]accountingCounter, error) {
	if generation == "" {
		generation = s.xrayAccountingGeneration
	}
	native := s.xrayNativeScratch
	if native == nil {
		native = make(map[string]uint64, len(s.xrayAccountingCounters))
		s.xrayNativeScratch = native
	}
	clear(native)
	if s.xrayStatKeyCache == nil {
		s.xrayStatKeyCache = make(map[string]xrayStatNativeKey, len(s.xrayAccountingCounters))
	}
	for _, stat := range stats {
		resolved, cached := s.xrayStatKeyCache[stat.Name]
		if !cached {
			if len(s.xrayStatKeyCache) >= xrayStatKeyCacheMaxSeries {
				clear(s.xrayStatKeyCache)
			}
			resolved = resolveXrayStatNativeKey(stat.Name)
			s.xrayStatKeyCache[stat.Name] = resolved
		}
		if !resolved.OK || stat.Value < 0 {
			continue
		}
		native[resolved.Key] = uint64(stat.Value)
	}
	pendingSeeds := map[string]uint64(nil)
	if s.xrayUsagePending != nil {
		pendingSeeds = s.xrayUsagePending.NextBaseline
	}
	seed := func(key string) uint64 {
		value := s.xrayUsageBaseline[key]
		if pending := pendingSeeds[key]; pending > value {
			return pending
		}
		return value
	}
	previousCounters := s.xrayAccountingCounters
	generationChanged := generation != "" && generation != s.xrayAccountingGeneration && len(s.xrayAccountingCounters) > 0
	if generationChanged {
		previousCounters = make(map[string]accountingCounter, len(s.xrayAccountingCounters))
		for key, counter := range s.xrayAccountingCounters {
			counter.Native = 0
			previousCounters[key] = counter
		}
	}
	return advanceAccountingCountersInto(next, previousCounters, native, seed)
}

func (s *Server) checkpointXrayAccounting(ctx context.Context) error {
	s.mu.Lock()
	running := s.lastRuntime != nil
	path, port := s.cfg.XrayPath, s.cfg.XrayAPIPort
	generation := s.xrayRuntimeGeneration
	s.mu.Unlock()
	if !running {
		return nil
	}
	stats, err := s.cachedXrayStatsClient(path, port).queryStats(ctx, "user>>>", false)
	if err != nil {
		return err
	}
	return s.checkpointXrayAccountingSample(stats, generation)
}

func (s *Server) checkpointXrayAccountingSample(stats []xrayStat, generation string) error {
	s.xrayUsageMu.Lock()
	defer s.xrayUsageMu.Unlock()
	if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
		return err
	}
	s.mu.Lock()
	unchanged := generation == s.xrayRuntimeGeneration
	s.mu.Unlock()
	if !unchanged {
		return fmt.Errorf("xray runtime changed during accounting snapshot")
	}
	return s.checkpointXrayGenerationLocked(stats, generation)
}

// xrayLocalAccountingTick reuses one daemon stats snapshot when the checkpoint
// and quota deadlines coincide. Both consumers still commit through their
// existing serialized accounting paths.
func (s *Server) xrayLocalAccountingTick(ctx context.Context, checkpoint, enforce bool) error {
	s.mu.Lock()
	snapshot := s.runtimePolicy
	running := s.lastRuntime != nil
	path, port, generation := s.cfg.XrayPath, s.cfg.XrayAPIPort, s.xrayRuntimeGeneration
	s.mu.Unlock()
	if !running || (!checkpoint && !enforce) {
		return nil
	}
	var policyIndex *xrayQuotaPolicyIndex
	if enforce {
		var err error
		policyIndex, err = s.xrayQuotaPolicyIndexFor(snapshot)
		if err != nil {
			return err
		}
		enforce = len(policyIndex.Policies) > 0
	}
	if !checkpoint && !enforce {
		return nil
	}
	stats, err := s.cachedXrayStatsClient(path, port).queryStats(ctx, "user>>>", false)
	if err != nil {
		return err
	}
	if checkpoint {
		if err := s.checkpointXrayAccountingSample(stats, generation); err != nil {
			return err
		}
	}
	if enforce {
		return s.quotaCheckXrayOfflineWithStats(ctx, snapshot, generation, policyIndex, stats)
	}
	return nil
}

func (s *Server) runXrayAccountingCheckpoints(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sampleCtx, cancel := context.WithTimeout(ctx, interval)
			err := s.checkpointXrayAccounting(sampleCtx)
			cancel()
			if ctx.Err() == nil {
				s.recordLocalAccountingHealth("xray", err)
			}
		}
	}
}

func nodeAccountingCheckpointInterval() (time.Duration, error) {
	return accountingCheckpointInterval(os.Getenv("ANTIMAGE_NODE_ACCOUNTING_CHECKPOINT_INTERVAL"))
}

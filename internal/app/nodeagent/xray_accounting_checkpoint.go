package nodeagent

import (
	"context"
	"errors"
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
	// The result map is scratch: it is only kept when the counters actually
	// changed, in which case the superseded map becomes the next scratch. An
	// unchanged tick therefore stops paying for a fresh map sized by the whole
	// counter series set.
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
		return nil
	}
	previous, previousGeneration := s.xrayAccountingCounters, s.xrayAccountingGeneration
	s.xrayAccountingCounters, s.xrayAccountingGeneration = next, generation
	s.xrayCounterScratch = previous
	if err := s.persistXrayUsageStateLocked(); err != nil {
		s.xrayAccountingCounters, s.xrayAccountingGeneration = previous, previousGeneration
		s.xrayCounterScratch = scratch
		return err
	}
	return nil
}

func (s *Server) previewXrayAccountingCountersLocked(stats []xrayStat, generation string) (map[string]accountingCounter, error) {
	return s.previewXrayAccountingCountersLockedInto(stats, generation, nil)
}

// xrayStatNativeKey is the resolved outcome of one stat name: the persistent
// "email:direction" counter key to accumulate under, or ok=false when the stat
// is not a countable user traffic series.
//
// The resolution is a pure function of the stat name, so memoizing it cannot
// change which series are counted or how they are keyed. It only avoids
// re-parsing the same names on every tick: the parse walks the ">>>" fields,
// lowercases them, decodes the base64 inbound tag in the email and allocates the
// concatenated counter key, for every series of every quota and checkpoint tick.
type xrayStatNativeKey struct {
	Key string
	OK  bool
}

// xrayStatKeyCacheMaxSeries bounds the memo. Production series counts are the
// enforced user x inbound set (hundreds to a few thousand); reaching the bound
// simply drops the memo, which refills from the next tick's names and can never
// change an accounting result.
const xrayStatKeyCacheMaxSeries = 65536

func (s *Server) previewXrayAccountingCountersLockedInto(stats []xrayStat, generation string, next map[string]accountingCounter) (map[string]accountingCounter, error) {
	if generation == "" {
		generation = s.xrayAccountingGeneration
	}
	// Reuse one scratch map for the native sample instead of allocating a fresh
	// map sized by every counter series on each quota/checkpoint tick. The map
	// never escapes this function, and both callers hold xrayUsageMu.
	native := s.xrayNativeScratch
	if native == nil {
		native = make(map[string]uint64, len(s.xrayAccountingCounters))
		s.xrayNativeScratch = native
	}
	clear(native)
	cache := s.xrayStatKeyCache
	if cache == nil {
		cache = make(map[string]xrayStatNativeKey, len(s.xrayAccountingCounters))
		s.xrayStatKeyCache = cache
	}
	for _, stat := range stats {
		resolved, cached := cache[stat.Name]
		if !cached {
			if len(cache) >= xrayStatKeyCacheMaxSeries {
				cache = make(map[string]xrayStatNativeKey, xrayStatKeyCacheMaxSeries)
				s.xrayStatKeyCache = cache
			}
			resolved = resolveXrayStatNativeKey(stat.Name)
			cache[stat.Name] = resolved
		}
		if !resolved.OK || stat.Value < 0 {
			continue
		}
		native[resolved.Key] = uint64(stat.Value)
	}
	// Existing pre-journal batches contain native counters. Seed from their
	// snapshot so migration cannot count their pending bytes a second time.
	// Resolved lazily so the seed map is not copied on every tick.
	pendingSeeds := map[string]uint64(nil)
	if s.xrayUsagePending != nil {
		pendingSeeds = s.xrayUsagePending.NextBaseline
	}
	seed := func(key string) uint64 {
		value := s.xrayUsageBaseline[key]
		if pending := pendingSeeds[key]; pending > value {
			value = pending
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

// resolveXrayStatNativeKey is the single source of truth for turning a stat name
// into a native counter key. It performs exactly the checks the tick used to
// inline, so the memoized and unmemoized paths accept and reject the same names.
func resolveXrayStatNativeKey(statName string) xrayStatNativeKey {
	name, ok := parseXrayStatName(statName)
	if !ok || name.Type != "user" || name.Metric != "traffic" {
		return xrayStatNativeKey{}
	}
	identity, err := parseXrayUserEmail(name.Email)
	if err != nil || identity.UserID <= 0 {
		return xrayStatNativeKey{}
	}
	return xrayStatNativeKey{Key: name.Email + ":" + name.Direction, OK: true}
}

func (s *Server) checkpointXrayAccounting(ctx context.Context) error {
	s.xrayUsageMu.Lock()
	defer s.xrayUsageMu.Unlock()
	if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
		return err
	}
	s.mu.Lock()
	running := s.lastRuntime != nil && s.xrayRuntimeGeneration != ""
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
	s.mu.Lock()
	unchanged := generation == s.xrayRuntimeGeneration
	s.mu.Unlock()
	if !unchanged {
		return fmt.Errorf("xray runtime changed during accounting snapshot")
	}
	return s.checkpointXrayGenerationLocked(stats, generation)
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

// xrayLocalAccountingTick shares one native stats sample between checkpointing
// and quota enforcement when both deadlines coincide.
func (s *Server) xrayLocalAccountingTick(ctx context.Context, checkpoint, enforce bool) error {
	s.mu.Lock()
	snapshot := s.runtimePolicy
	running := s.lastRuntime != nil && s.xrayRuntimeGeneration != ""
	path, port, generation := s.cfg.XrayPath, s.cfg.XrayAPIPort, s.xrayRuntimeGeneration
	s.mu.Unlock()
	if !running || (!checkpoint && !enforce) {
		return nil
	}
	var policyIndex *xrayQuotaPolicyIndex
	var tickErrors []error
	if enforce {
		var err error
		policyIndex, err = s.xrayQuotaPolicyIndexFor(snapshot)
		if err != nil {
			tickErrors = append(tickErrors, localHealthError("policy", err))
			enforce = false
		} else {
			enforce = xrayPoliciesNeedLocalQuotaCheck(policyIndex.Policies)
		}
	}
	if !checkpoint && !enforce {
		return errors.Join(tickErrors...)
	}
	stats, err := s.cachedXrayStatsClient(path, port).queryStats(ctx, "user>>>", false)
	if err != nil {
		tickErrors = append(tickErrors, localHealthError("runtime", err))
		return errors.Join(tickErrors...)
	}
	if checkpoint {
		if err := s.checkpointXrayAccountingSample(stats, generation); err != nil {
			tickErrors = append(tickErrors, localHealthError("accounting", err))
		} else {
			s.recordLocalDomainHealth("accounting", "xray", nil)
		}
	}
	if enforce {
		if err := s.quotaCheckXrayOfflineWithStats(ctx, snapshot, generation, policyIndex, stats); err != nil {
			tickErrors = append(tickErrors, localHealthError("policy", err))
		} else {
			s.recordLocalDomainHealth("policy", "xray", nil)
		}
	}
	return errors.Join(tickErrors...)
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
				s.recordLocalDomainHealth("accounting", "xray", err)
			}
		}
	}
}

func nodeAccountingCheckpointInterval() (time.Duration, error) {
	return accountingCheckpointInterval(os.Getenv("ANTIMAGE_NODE_ACCOUNTING_CHECKPOINT_INTERVAL"))
}

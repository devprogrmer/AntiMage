package nodeagent

import (
	"context"
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
	next, err := s.previewXrayAccountingCountersLocked(stats, generation)
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
		return nil
	}
	previous, previousGeneration := s.xrayAccountingCounters, s.xrayAccountingGeneration
	s.xrayAccountingCounters, s.xrayAccountingGeneration = next, generation
	if err := s.persistXrayUsageStateLocked(); err != nil {
		s.xrayAccountingCounters, s.xrayAccountingGeneration = previous, previousGeneration
		return err
	}
	return nil
}

func (s *Server) previewXrayAccountingCountersLocked(stats []xrayStat, generation string) (map[string]accountingCounter, error) {
	if generation == "" {
		generation = s.xrayAccountingGeneration
	}
	native := make(map[string]uint64)
	seeds := make(map[string]uint64, len(s.xrayUsageBaseline))
	for key, value := range s.xrayUsageBaseline {
		seeds[key] = value
	}
	// Existing pre-journal batches contain native counters. Seed from their
	// snapshot so migration cannot count their pending bytes a second time.
	if s.xrayUsagePending != nil {
		for key, value := range s.xrayUsagePending.NextBaseline {
			if value > seeds[key] {
				seeds[key] = value
			}
		}
	}
	for _, stat := range stats {
		name, ok := parseXrayStatName(stat.Name)
		if !ok || name.Type != "user" || name.Metric != "traffic" || stat.Value < 0 {
			continue
		}
		identity, err := parseXrayUserEmail(name.Email)
		if err != nil || identity.UserID <= 0 {
			continue
		}
		native[name.Email+":"+name.Direction] = uint64(stat.Value)
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
	return advanceAccountingCounters(previousCounters, native, seeds)
}

func (s *Server) checkpointXrayAccounting(ctx context.Context) error {
	s.xrayUsageMu.Lock()
	defer s.xrayUsageMu.Unlock()
	if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
		return err
	}
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
	s.mu.Lock()
	unchanged := generation == s.xrayRuntimeGeneration
	s.mu.Unlock()
	if !unchanged {
		return fmt.Errorf("xray runtime changed during accounting snapshot")
	}
	return s.checkpointXrayGenerationLocked(stats, generation)
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

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
	next, err := advanceAccountingCounters(s.xrayAccountingCounters, native, seeds)
	if err != nil {
		return err
	}
	changed := len(next) != len(s.xrayAccountingCounters)
	for key, counter := range next {
		if s.xrayAccountingCounters[key] != counter {
			changed = true
			break
		}
	}
	if !changed {
		return nil
	}
	previous := s.xrayAccountingCounters
	s.xrayAccountingCounters = next
	if err := s.persistXrayUsageStateLocked(); err != nil {
		s.xrayAccountingCounters = previous
		return err
	}
	return nil
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
	s.mu.Unlock()
	if !running {
		return nil
	}
	stats, err := newXrayStatsClient(path, port).queryStats(ctx, "user>>>", false)
	if err != nil {
		return err
	}
	return s.checkpointXrayStatsLocked(stats)
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
			if err != nil && ctx.Err() == nil {
				s.appendLog("xray accounting checkpoint failed: " + err.Error())
			}
		}
	}
}

func nodeAccountingCheckpointInterval() (time.Duration, error) {
	return accountingCheckpointInterval(os.Getenv("ANTIMAGE_NODE_ACCOUNTING_CHECKPOINT_INTERVAL"))
}

package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func localQuotaInterval(value string) (time.Duration, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "0", "off", "disabled", "false":
		return 0, nil
	case "":
		return 100 * time.Millisecond, nil
	}
	interval, err := time.ParseDuration(value)
	if err != nil || interval < 50*time.Millisecond || interval > 2*time.Second {
		return 0, fmt.Errorf("node quota interval must be between 50ms and 2s, or disabled")
	}
	return interval, nil
}

func (s *Server) wireGuardOfflineTick(ctx context.Context, enforce, checkpoint bool) error {
	s.wireGuardUsageMu.Lock()
	defer s.wireGuardUsageMu.Unlock()
	if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		return err
	}
	root := filepath.Join(s.cfg.DataDir, "wireguard", "inbounds")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	configs := make([]wireGuardUsageRuntimeConfig, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name(), "usage-helper.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var cfg wireGuardUsageRuntimeConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("offline wireguard policy: %w", err)
		}
		configs = append(configs, cfg)
	}
	if len(configs) == 0 {
		return nil
	}
	raw, err := wireGuardDumpAll(ctx)
	if err != nil {
		return err
	}
	dump, err := parseWireGuardAllDump(string(raw))
	if err != nil {
		return err
	}
	type resolvedConfig struct {
		cfg   wireGuardUsageRuntimeConfig
		iface wireGuardInterfaceDump
	}
	resolved := make([]resolvedConfig, 0, len(configs))
	for _, cfg := range configs {
		iface, ok := dump[strings.TrimSpace(cfg.InterfaceName)]
		if cfg.InterfaceName == "" {
			matches := []wireGuardInterfaceDump{}
			for _, candidate := range dump {
				if cfg.ListenPort > 0 && candidate.ListenPort == cfg.ListenPort {
					matches = append(matches, candidate)
				}
			}
			if len(matches) == 1 {
				iface, ok = matches[0], true
			}
		}
		if !ok {
			// The interface can disappear during an intentional runtime stop.
			// Retain prior usage; absence is not evidence it was reflected.
			continue
		}
		resolved = append(resolved, resolvedConfig{cfg, iface})
		if err := s.checkpointWireGuardGenerationLocked(cfg, iface.Name, iface.Peers, checkpoint); err != nil {
			return err
		}
	}
	if enforce {
		for _, item := range resolved {
			s.enforceWireGuardPoliciesLocked(item.cfg, item.iface.Name, item.iface.Peers, time.Now().UTC())
		}
	}
	return nil
}

func (s *Server) runLocalQuotaScheduler(ctx context.Context, quotaInterval, checkpointInterval time.Duration) {
	type worker struct {
		name       string
		checkpoint func(context.Context) error
		quota      func(context.Context) error
	}
	workers := []worker{
		{"xray", s.checkpointXrayAccounting, s.quotaCheckXrayOffline},
		{"amneziawg", s.checkpointAmneziaWGOffline, s.quotaCheckAmneziaWGOffline},
		{"openvpn", s.checkpointOpenVPNOffline, s.quotaCheckOpenVPNOffline},
		{"ppp", s.checkpointPPPOffline, s.quotaCheckPPPOffline},
		{"ikev2", s.checkpointIKEv2Offline, s.quotaCheckIKEv2Offline},
		{"anyconnect", s.checkpointAnyConnectOffline, s.quotaCheckAnyConnectOffline},
	}
	var group sync.WaitGroup
	for _, w := range workers {
		group.Add(1)
		go func(w worker) {
			defer group.Done()
			s.runLocalAccountingWorker(ctx, w.name, quotaInterval, checkpointInterval, w.checkpoint, w.quota)
		}(w)
	}
	defer group.Wait()
	s.runLocalAccountingWorker(ctx, "wireguard", quotaInterval, checkpointInterval,
		func(ctx context.Context) error { return s.wireGuardOfflineTick(ctx, false, true) },
		func(ctx context.Context) error { return s.wireGuardOfflineTick(ctx, true, false) })
}

// One serial worker per native runtime coalesces overdue ticks: slow daemons do
// not block other protocols and never accumulate overlapping commands.
func (s *Server) runLocalAccountingWorker(ctx context.Context, name string, quotaInterval, checkpointInterval time.Duration, checkpointFn, quotaFn func(context.Context) error) {
	interval := checkpointInterval
	if quotaInterval > 0 && quotaInterval < interval {
		interval = quotaInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	nextQuota, nextCheckpoint := time.Now(), time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			enforce := quotaInterval > 0 && !now.Before(nextQuota)
			checkpoint := !now.Before(nextCheckpoint)
			if !enforce && !checkpoint {
				continue
			}
			if enforce {
				for !nextQuota.After(now) {
					nextQuota = nextQuota.Add(quotaInterval)
				}
			}
			if checkpoint {
				for !nextCheckpoint.After(now) {
					nextCheckpoint = nextCheckpoint.Add(checkpointInterval)
				}
			}
			timeout := checkpointInterval
			if timeout > 2*time.Second {
				timeout = 2 * time.Second
			}
			sampleCtx, cancel := context.WithTimeout(ctx, timeout)
			var err error
			if checkpoint {
				err = checkpointFn(sampleCtx)
			}
			if enforce && err == nil {
				err = quotaFn(sampleCtx)
			}
			cancel()
			if ctx.Err() == nil {
				s.recordLocalAccountingHealth(name, err)
			}
		}
	}
}

func (s *Server) recordLocalAccountingHealth(protocol string, err error) {
	s.mu.Lock()
	if s.localAccountingFailures == nil {
		s.localAccountingFailures = make(map[string]string)
	}
	previous := s.localAccountingFailures[protocol]
	message := ""
	if err != nil {
		message = err.Error()
		s.localAccountingFailures[protocol] = message
	} else {
		delete(s.localAccountingFailures, protocol)
	}
	s.mu.Unlock()
	if message != "" && message != previous {
		s.appendLog("local " + protocol + " accounting/quota degraded: " + message)
	}
}

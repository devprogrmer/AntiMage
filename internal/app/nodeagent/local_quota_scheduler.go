package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
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
		{"amneziawg", s.checkpointAmneziaWGOffline, s.quotaCheckAmneziaWGOffline},
		{"openvpn", s.checkpointOpenVPNOffline, s.quotaCheckOpenVPNOffline},
		{"ppp", s.checkpointPPPOffline, s.quotaCheckPPPOffline},
		{"ikev2", s.checkpointIKEv2Offline, s.quotaCheckIKEv2Offline},
		{"anyconnect", s.checkpointAnyConnectOffline, s.quotaCheckAnyConnectOffline},
	}
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		s.runLocalAccountingWorkerWithWakeAdaptive(ctx, "xray", quotaInterval, checkpointInterval, s.localQuotaWakeChannel("xray"), s.xrayQuotaAdaptiveInterval,
			func(sampleCtx context.Context, enforce, checkpoint bool) error {
				return s.xrayLocalAccountingTick(sampleCtx, checkpoint, enforce)
			})
	}()
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
	s.runLocalAccountingWorkerWithWakeAdaptive(ctx, name, quotaInterval, checkpointInterval, s.localQuotaWakeChannel(name), nil, func(sampleCtx context.Context, enforce, checkpoint bool) error {
		var errs []error
		if checkpoint {
			if err := checkpointFn(sampleCtx); err != nil {
				errs = append(errs, localHealthError("accounting", err))
			} else {
				s.recordLocalDomainHealth("accounting", name, nil)
			}
		}
		if enforce {
			if err := quotaFn(sampleCtx); err != nil {
				errs = append(errs, localHealthError("policy", err))
			} else {
				s.recordLocalDomainHealth("policy", name, nil)
			}
		}
		return errors.Join(errs...)
	})
}

func (s *Server) runLocalAccountingWorkerWithTick(ctx context.Context, name string, quotaInterval, checkpointInterval time.Duration, tick func(context.Context, bool, bool) error) {
	s.runLocalAccountingWorkerWithWakeAdaptive(ctx, name, quotaInterval, checkpointInterval, nil, nil, tick)
}

func (s *Server) localQuotaWakeChannel(name string) chan struct{} {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	s.mu.Lock()
	if s.localQuotaWakeups == nil {
		s.localQuotaWakeups = make(map[string]chan struct{})
	}
	wake := s.localQuotaWakeups[name]
	if wake == nil {
		wake = make(chan struct{}, 1)
		s.localQuotaWakeups[name] = wake
	}
	s.mu.Unlock()
	return wake
}

func (s *Server) wakeLocalQuota(name string) {
	wake := s.localQuotaWakeChannel(name)
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *Server) wakeLocalQuotaForRuntime(rawValues ...string) {
	var xray, wireguard, amneziaWG, openVPN, ppp, ikev2, anyconnect bool
	for _, raw := range rawValues {
		payload, err := parseNativeRuntimePayload(raw)
		if err != nil {
			continue
		}
		xray = xray || len(payload.XrayPolicies) > 0
		wireguard = wireguard || len(payload.WireGuardInbounds) > 0
		amneziaWG = amneziaWG || len(payload.AmneziaWGInbounds) > 0
		openVPN = openVPN || len(payload.OpenVPNInbounds) > 0
		ppp = ppp || len(payload.L2TPInbounds) > 0 || len(payload.PPTPInbounds) > 0
		ikev2 = ikev2 || len(payload.IKEv2Inbounds) > 0
		anyconnect = anyconnect || len(payload.AnyConnectInbounds) > 0
	}
	if xray {
		s.wakeLocalQuota("xray")
	}
	if wireguard {
		s.wakeLocalQuota("wireguard")
	}
	if amneziaWG {
		s.wakeLocalQuota("amneziawg")
	}
	if openVPN {
		s.wakeLocalQuota("openvpn")
	}
	if ppp {
		s.wakeLocalQuota("ppp")
	}
	if ikev2 {
		s.wakeLocalQuota("ikev2")
	}
	if anyconnect {
		s.wakeLocalQuota("anyconnect")
	}
}

func (s *Server) runLocalAccountingWorkerWithWakeAdaptive(ctx context.Context, name string, quotaInterval, checkpointInterval time.Duration, wake <-chan struct{}, adaptiveInterval func() time.Duration, tick func(context.Context, bool, bool) error) {
	now := time.Now()
	nextQuota, nextCheckpoint := time.Time{}, time.Time{}
	timer := time.NewTimer(time.Hour)
	stopQuotaTimer(timer)
	defer timer.Stop()
	var timerC <-chan time.Time
	if quotaInterval > 0 {
		nextQuota = now
	}
	if checkpointInterval > 0 {
		nextCheckpoint = now
	}
	for {
		next := nearestQuotaDeadline(nextQuota, nextCheckpoint)
		stopQuotaTimer(timer)
		timerC = nil
		if !next.IsZero() {
			wait := time.Until(next)
			if wait < 0 {
				wait = 0
			}
			timer.Reset(wait)
			timerC = timer.C
		}
		select {
		case <-ctx.Done():
			stopQuotaTimer(timer)
			return
		case <-wake:
			stopQuotaTimer(timer)
			if quotaInterval > 0 {
				now := time.Now()
				checkpoint := checkpointInterval > 0 && !now.Before(nextCheckpoint)
				err := s.runWokenQuotaTick(ctx, checkpointInterval, checkpoint, tick)
				if ctx.Err() == nil {
					s.recordLocalWorkerHealth(name, err)
				}
				now = time.Now()
				nextQuota = quotaDeadlineAfter(now, nextQuotaDelay(quotaInterval, adaptiveInterval, err))
				if checkpoint {
					nextCheckpoint = now.Add(checkpointInterval)
				}
			}
		case firedAt := <-timerC:
			timerC = nil
			enforce := quotaInterval > 0 && !firedAt.Before(nextQuota)
			checkpoint := checkpointInterval > 0 && !firedAt.Before(nextCheckpoint)
			if !enforce && !checkpoint {
				continue
			}
			timeout := checkpointInterval
			if timeout <= 0 || timeout > 2*time.Second {
				timeout = 2 * time.Second
			}
			sampleCtx, cancel := context.WithTimeout(ctx, timeout)
			err := tick(sampleCtx, enforce, checkpoint)
			cancel()
			if ctx.Err() == nil {
				s.recordLocalWorkerHealth(name, err)
			}
			now = time.Now()
			if enforce {
				nextQuota = quotaDeadlineAfter(now, nextQuotaDelay(quotaInterval, adaptiveInterval, err))
			}
			if checkpoint {
				nextCheckpoint = now.Add(checkpointInterval)
			}
		}
	}
}

func stopQuotaTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (s *Server) runWokenQuotaTick(ctx context.Context, checkpointInterval time.Duration, checkpoint bool, tick func(context.Context, bool, bool) error) error {
	timeout := checkpointInterval
	if timeout <= 0 || timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	sampleCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return tick(sampleCtx, true, checkpoint)
}

func nextQuotaDelay(minimum time.Duration, adaptiveInterval func() time.Duration, tickErr error) time.Duration {
	if adaptiveInterval == nil {
		return minimum
	}
	interval := adaptiveInterval()
	if interval <= 0 {
		return 0
	}
	if tickErr != nil {
		return minimum
	}
	if interval < minimum {
		return minimum
	}
	return interval
}

func quotaDeadlineAfter(now time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		return time.Time{}
	}
	return now.Add(interval)
}

func nearestQuotaDeadline(quota, checkpoint time.Time) time.Time {
	if quota.IsZero() {
		return checkpoint
	}
	if checkpoint.IsZero() || quota.Before(checkpoint) {
		return quota
	}
	return checkpoint
}

func (s *Server) recordLocalAccountingHealth(protocol string, err error) {
	s.recordLocalDomainHealth("accounting", protocol, err)
}

type localHealthErrorValue struct {
	domain string
	err    error
}

func (e localHealthErrorValue) Error() string { return e.err.Error() }
func (e localHealthErrorValue) Unwrap() error { return e.err }

func localHealthError(domain string, err error) error {
	if err == nil {
		return nil
	}
	return localHealthErrorValue{domain: domain, err: err}
}

func (s *Server) recordLocalWorkerHealth(protocol string, err error) {
	var visit func(error)
	visit = func(current error) {
		if current == nil {
			return
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child)
			}
			return
		}
		if domainErr, ok := current.(localHealthErrorValue); ok {
			s.recordLocalDomainHealth(domainErr.domain, protocol, domainErr.err)
			return
		}
		s.recordLocalAccountingHealth(protocol, current)
	}
	visit(err)
	if err == nil {
		s.recordLocalDomainHealth("accounting", protocol, nil)
		s.recordLocalDomainHealth("policy", protocol, nil)
		s.recordLocalDomainHealth("runtime", protocol, nil)
	}
}

func (s *Server) recordLocalDomainHealth(domain, protocol string, err error) {
	protocol = domain + ":" + protocol
	s.mu.Lock()
	var failures map[string]string
	switch domain {
	case "policy":
		if s.localPolicyFailures == nil {
			s.localPolicyFailures = make(map[string]string)
		}
		failures = s.localPolicyFailures
	case "runtime":
		if s.localRuntimeFailures == nil {
			s.localRuntimeFailures = make(map[string]string)
		}
		failures = s.localRuntimeFailures
	default:
		if s.localAccountingFailures == nil {
			s.localAccountingFailures = make(map[string]string)
		}
		failures = s.localAccountingFailures
	}
	previous := failures[protocol]
	message := ""
	if err != nil {
		message = err.Error()
		failures[protocol] = message
	} else {
		delete(failures, protocol)
	}
	s.mu.Unlock()
	if message != "" && message != previous {
		s.appendLog("local " + protocol + " degraded: " + message)
	}
}

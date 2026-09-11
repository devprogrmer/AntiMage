package nodeagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	wireGuardRuntimeRun = func(
		ctx context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	wireGuardRuntimeLookPath = exec.LookPath
	wireGuardRuntimeGOOS     = runtime.GOOS
)

func (s *Server) wireGuardInterfaceOwnedByAntiMage(
	tag string,
	interfaceName string,
) (bool, error) {
	tag = strings.TrimSpace(tag)
	interfaceName = strings.TrimSpace(interfaceName)
	if tag == "" || interfaceName == "" {
		return false, nil
	}

	s.mu.Lock()
	state, ok := s.wireGuardRuntimes[tag]
	s.mu.Unlock()
	if ok && strings.TrimSpace(state.InterfaceName) == interfaceName {
		return true, nil
	}

	persisted, err := s.loadWireGuardRuntimeStates()
	if err != nil {
		return false, fmt.Errorf(
			"load managed wireguard runtime state: %w",
			err,
		)
	}
	state, ok = persisted[tag]
	if ok && strings.TrimSpace(state.InterfaceName) == interfaceName {
		return true, nil
	}
	return false, nil
}

func (s *Server) applyWireGuardRuntime(
	prepared preparedWireGuardRuntime,
) error {
	if wireGuardRuntimeGOOS != "linux" {
		return fmt.Errorf(
			"wireguard %q: native runtime is supported only on linux",
			prepared.Tag,
		)
	}

	ipPath, err := wireGuardRuntimeLookPath("ip")
	if err != nil {
		return fmt.Errorf("wireguard %q: ip command not installed", prepared.Tag)
	}
	wgPath, err := wireGuardRuntimeLookPath("wg")
	if err != nil {
		return fmt.Errorf("wireguard %q: wg command not installed", prepared.Tag)
	}
	sysctlPath, err := wireGuardRuntimeLookPath("sysctl")
	if err != nil {
		return fmt.Errorf("wireguard %q: sysctl command not installed", prepared.Tag)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if prepared.ExplicitInterface {
		if _, err := wireGuardRuntimeRun(
			ctx,
			ipPath,
			"link",
			"show",
			"dev",
			prepared.InterfaceName,
		); err == nil {
			owned, err := s.wireGuardInterfaceOwnedByAntiMage(
				prepared.Tag,
				prepared.InterfaceName,
			)
			if err != nil {
				return fmt.Errorf(
					"wireguard %q: verify explicit interface ownership: %w",
					prepared.Tag,
					err,
				)
			}
			if !owned {
				return fmt.Errorf(
					"wireguard %q: explicit interface %q already exists and is not managed by AntiMage",
					prepared.Tag,
					prepared.InterfaceName,
				)
			}
		}
	}

	if err := runWireGuardRuntimeRequired(
		ctx,
		sysctlPath,
		"-w",
		"net.ipv4.ip_forward=1",
	); err != nil {
		return err
	}

	created := false
	if _, err := wireGuardRuntimeRun(
		ctx,
		ipPath,
		"link",
		"show",
		"dev",
		prepared.InterfaceName,
	); err != nil {
		if err := runWireGuardRuntimeRequired(
			ctx,
			ipPath,
			"link",
			"add",
			"dev",
			prepared.InterfaceName,
			"type",
			"wireguard",
		); err != nil {
			return fmt.Errorf(
				"wireguard %q: create interface: %w",
				prepared.Tag,
				err,
			)
		}
		created = true
	}

	rollbackCreated := func() {
		if !created {
			return
		}
		_, _ = wireGuardRuntimeRun(
			ctx,
			ipPath,
			"link",
			"delete",
			"dev",
			prepared.InterfaceName,
		)
	}

	if err := runWireGuardRuntimeRequired(
		ctx,
		wgPath,
		"syncconf",
		prepared.InterfaceName,
		prepared.ConfigPath,
	); err != nil {
		rollbackCreated()
		return fmt.Errorf("wireguard %q: sync config: %w", prepared.Tag, err)
	}

	if err := runWireGuardRuntimeRequired(
		ctx,
		ipPath,
		"-4",
		"address",
		"flush",
		"dev",
		prepared.InterfaceName,
		"scope",
		"global",
	); err != nil {
		rollbackCreated()
		return fmt.Errorf("wireguard %q: flush address: %w", prepared.Tag, err)
	}

	if err := runWireGuardRuntimeRequired(
		ctx,
		ipPath,
		"address",
		"add",
		prepared.ServerCIDR,
		"dev",
		prepared.InterfaceName,
	); err != nil {
		rollbackCreated()
		return fmt.Errorf(
			"wireguard %q: assign server address: %w",
			prepared.Tag,
			err,
		)
	}

	if prepared.MTU > 0 {
		if prepared.MTU < 576 || prepared.MTU > 1500 {
			rollbackCreated()
			return fmt.Errorf(
				"wireguard %q: invalid MTU %d",
				prepared.Tag,
				prepared.MTU,
			)
		}
		if err := runWireGuardRuntimeRequired(
			ctx,
			ipPath,
			"link",
			"set",
			"dev",
			prepared.InterfaceName,
			"mtu",
			fmt.Sprint(prepared.MTU),
		); err != nil {
			rollbackCreated()
			return fmt.Errorf("wireguard %q: set MTU: %w", prepared.Tag, err)
		}
	}

	if err := runWireGuardRuntimeRequired(
		ctx,
		ipPath,
		"link",
		"set",
		"dev",
		prepared.InterfaceName,
		"up",
	); err != nil {
		rollbackCreated()
		return fmt.Errorf(
			"wireguard %q: bring interface up: %w",
			prepared.Tag,
			err,
		)
	}

	state := wireGuardRuntimeState{
		Tag:           prepared.Tag,
		InterfaceName: prepared.InterfaceName,
		ConfigPath:    prepared.ConfigPath,
		ServerCIDR:    prepared.ServerCIDR,
		SourceCIDR:    prepared.SourceCIDR,
		MTU:           prepared.MTU,
	}
	if err := s.persistWireGuardRuntimeState(state); err != nil {
		rollbackCreated()
		return fmt.Errorf(
			"wireguard %q: persist runtime state: %w",
			prepared.Tag,
			err,
		)
	}

	s.mu.Lock()
	s.wireGuardRuntimes[prepared.Tag] = state
	s.mu.Unlock()

	s.appendLog(fmt.Sprintf(
		"wireguard runtime applied: tag=%s interface=%s listen=%d peers=%d",
		prepared.Tag,
		prepared.InterfaceName,
		prepared.Inbound.ListenPort,
		len(prepared.Inbound.Peers),
	))
	return nil
}

func runWireGuardRuntimeRequired(
	ctx context.Context,
	name string,
	args ...string,
) error {
	output, err := wireGuardRuntimeRun(ctx, name, args...)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf(
			"wireguard runtime command %q failed: %w",
			name,
			err,
		)
	}
	return fmt.Errorf(
		"wireguard runtime command %q failed: %w: %s",
		name,
		err,
		detail,
	)
}

func removeWireGuardInterface(interfaceName string) error {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" || wireGuardRuntimeGOOS != "linux" {
		return nil
	}
	ipPath, err := wireGuardRuntimeLookPath("ip")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := wireGuardRuntimeRun(
		ctx,
		ipPath,
		"link",
		"show",
		"dev",
		interfaceName,
	); err != nil {
		return nil
	}
	return runWireGuardRuntimeRequired(
		ctx,
		ipPath,
		"link",
		"delete",
		"dev",
		interfaceName,
	)
}

func (s *Server) stopRemovedWireGuardRuntimes(
	desired map[string]preparedWireGuardRuntime,
) {
	persisted, err := s.loadWireGuardRuntimeStates()
	if err != nil {
		s.appendLog(
			"wireguard runtime manifest reconcile failed: " + err.Error(),
		)
	}
	for tag, old := range persisted {
		next, keep := desired[tag]
		if keep && next.InterfaceName == old.InterfaceName {
			continue
		}
		if err := removeWireGuardInterface(old.InterfaceName); err != nil {
			s.appendLog(
				"remove stale wireguard interface failed: " +
					tag + ": " + err.Error(),
			)
			continue
		}
		if !keep {
			_ = os.RemoveAll(filepath.Dir(
				s.wireGuardRuntimeManifestPath(tag),
			))
		}
	}

	s.mu.Lock()
	removed := make([]wireGuardRuntimeState, 0)
	for tag, state := range s.wireGuardRuntimes {
		next, keep := desired[tag]
		if keep && next.InterfaceName == state.InterfaceName {
			continue
		}
		removed = append(removed, state)
		delete(s.wireGuardRuntimes, tag)
	}
	s.mu.Unlock()

	for _, state := range removed {
		if err := removeWireGuardInterface(state.InterfaceName); err != nil {
			s.appendLog(
				"remove wireguard interface failed: " +
					state.Tag + ": " + err.Error(),
			)
		}
	}
}

func (s *Server) stopAllWireGuardRuntimes() {
	statesByInterface := make(map[string]wireGuardRuntimeState)

	persisted, err := s.loadWireGuardRuntimeStates()
	if err != nil {
		s.appendLog(
			"load persisted wireguard runtimes for cleanup failed: " +
				err.Error(),
		)
	} else {
		for _, state := range persisted {
			if strings.TrimSpace(state.InterfaceName) == "" {
				continue
			}
			statesByInterface[state.InterfaceName] = state
		}
	}

	s.mu.Lock()
	for _, state := range s.wireGuardRuntimes {
		if strings.TrimSpace(state.InterfaceName) == "" {
			continue
		}
		statesByInterface[state.InterfaceName] = state
	}
	s.wireGuardRuntimes = make(map[string]wireGuardRuntimeState)
	s.mu.Unlock()

	for _, state := range statesByInterface {
		if err := removeWireGuardInterface(state.InterfaceName); err != nil {
			s.appendLog(
				"stop wireguard interface failed: " +
					state.Tag + ": " + err.Error(),
			)
			continue
		}
		if strings.TrimSpace(state.Tag) != "" {
			_ = os.RemoveAll(filepath.Dir(
				s.wireGuardRuntimeManifestPath(state.Tag),
			))
		}
	}

	s.cleanupWireGuardRoutingAll()
}

package nodeagent

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var (
	openVPNNetworkRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	openVPNNetworkLookPath = exec.LookPath
	openVPNNetworkGOOS     = runtime.GOOS
)

func ensureOpenVPNTProxyChain(
	ctx context.Context,
	iptablesPath string,
) error {
	checkChain := []string{
		"-w", "5",
		"-t", "mangle",
		"-S", openVPNTProxyChain,
	}

	if _, err := openVPNNetworkRun(
		ctx,
		iptablesPath,
		checkChain...,
	); err != nil {
		if err := runOpenVPNNetworkRequired(
			ctx,
			iptablesPath,
			"-w", "5",
			"-t", "mangle",
			"-N", openVPNTProxyChain,
		); err != nil {
			return err
		}
	}

	checkJump := []string{
		"-w", "5",
		"-t", "mangle",
		"-C", "PREROUTING",
		"-j", openVPNTProxyChain,
	}

	if _, err := openVPNNetworkRun(
		ctx,
		iptablesPath,
		checkJump...,
	); err == nil {
		return nil
	}

	return runOpenVPNNetworkRequired(
		ctx,
		iptablesPath,
		"-w", "5",
		"-t", "mangle",
		"-A", "PREROUTING",
		"-j", openVPNTProxyChain,
	)
}
func (s *Server) ensureOpenVPNTProxy(spec openVPNTProxySpec) error {
	if !spec.Enabled {
		return nil
	}

	if openVPNNetworkGOOS != "linux" {
		return fmt.Errorf("openvpn tproxy is supported only on linux")
	}

	ipPath, err := openVPNNetworkLookPath("ip")
	if err != nil {
		return fmt.Errorf("openvpn tproxy: ip command not installed")
	}

	iptablesPath, err := openVPNNetworkLookPath("iptables")
	if err != nil {
		return fmt.Errorf("openvpn tproxy: iptables command not installed")
	}

	sysctlPath, err := openVPNNetworkLookPath("sysctl")
	if err != nil {
		return fmt.Errorf("openvpn tproxy: sysctl command not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := runOpenVPNNetworkRequired(
		ctx,
		sysctlPath,
		"-w",
		"net.ipv4.ip_forward=1",
	); err != nil {
		return err
	}

	rules, err := openVPNNetworkRun(ctx, ipPath, "rule", "show")
	if err != nil {
		return fmt.Errorf(
			"openvpn tproxy: ip rule show: %w: %s",
			err,
			strings.TrimSpace(string(rules)),
		)
	}

	if !openVPNPolicyRulePresent(string(rules), spec) {
		if err := runOpenVPNNetworkRequired(
			ctx,
			ipPath,
			spec.policyRuleArgs()...,
		); err != nil {
			return err
		}
	}

	if err := runOpenVPNNetworkRequired(
		ctx,
		ipPath,
		spec.localRouteArgs()...,
	); err != nil {
		return err
	}

	if err := ensureOpenVPNTProxyChain(
		ctx,
		iptablesPath,
	); err != nil {
		return err
	}

	for _, protocol := range []string{"tcp", "udp"} {
		checkArgs, err := spec.iptablesArgs("-C", protocol)
		if err != nil {
			return err
		}

		checkArgs = append([]string{"-w", "5"}, checkArgs...)

		if _, err := openVPNNetworkRun(ctx, iptablesPath, checkArgs...); err == nil {
			continue
		}

		addArgs, err := spec.iptablesArgs("-A", protocol)
		if err != nil {
			return err
		}

		addArgs = append([]string{"-w", "5"}, addArgs...)

		if err := runOpenVPNNetworkRequired(
			ctx,
			iptablesPath,
			addArgs...,
		); err != nil {
			return err
		}
	}

	s.appendLog(fmt.Sprintf(
		"openvpn tproxy applied: interface=%s source=%s tunnel_port=%d",
		spec.Interface,
		spec.SourceCIDR,
		spec.TunnelPort,
	))

	return nil
}

func (s *Server) removeOpenVPNTProxy(spec openVPNTProxySpec) error {
	if !spec.Enabled || openVPNNetworkGOOS != "linux" {
		return nil
	}

	iptablesPath, err := openVPNNetworkLookPath("iptables")
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, protocol := range []string{"tcp", "udp"} {
		checkArgs, err := spec.iptablesArgs("-C", protocol)
		if err != nil {
			return err
		}

		checkArgs = append([]string{"-w", "5"}, checkArgs...)

		if _, err := openVPNNetworkRun(ctx, iptablesPath, checkArgs...); err != nil {
			continue
		}

		deleteArgs, err := spec.iptablesArgs("-D", protocol)
		if err != nil {
			return err
		}

		deleteArgs = append([]string{"-w", "5"}, deleteArgs...)

		if err := runOpenVPNNetworkRequired(
			ctx,
			iptablesPath,
			deleteArgs...,
		); err != nil {
			return err
		}
	}

	return nil
}

func runOpenVPNNetworkRequired(
	ctx context.Context,
	name string,
	args ...string,
) error {
	output, err := openVPNNetworkRun(ctx, name, args...)
	if err == nil {
		return nil
	}

	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf(
			"openvpn network command %q failed: %w",
			name,
			err,
		)
	}

	return fmt.Errorf(
		"openvpn network command %q failed: %w: %s",
		name,
		err,
		detail,
	)
}

func openVPNPolicyRulePresent(
	raw string,
	spec openVPNTProxySpec,
) bool {
	markMask := "fwmark " + spec.markMask()
	lookup := "lookup " + strconv.Itoa(spec.Table)
	table := "table " + strconv.Itoa(spec.Table)
	priority := strconv.Itoa(spec.Priority) + ":"

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)

		hasMark := strings.Contains(
			strings.ToLower(line),
			strings.ToLower(markMask),
		)

		hasTable := strings.Contains(line, lookup) ||
			strings.Contains(line, table)

		hasPriority := strings.HasPrefix(
			line,
			priority,
		)

		if hasMark &&
			hasTable &&
			hasPriority {
			return true
		}
	}

	return false
}

func (s *Server) applyOpenVPNTProxy(tag string, spec openVPNTProxySpec) error {
	s.mu.Lock()
	previous, exists := s.openVPNTProxySpecs[tag]
	s.mu.Unlock()

	if exists && previous != spec {
		if err := s.removeOpenVPNTProxy(previous); err != nil {
			return fmt.Errorf(
				"openvpn %q: remove previous tproxy: %w",
				tag,
				err,
			)
		}

		s.mu.Lock()
		delete(s.openVPNTProxySpecs, tag)
		s.mu.Unlock()

		exists = false
	}

	if !spec.Enabled {
		if exists {
			if err := s.removeOpenVPNTProxy(previous); err != nil {
				return fmt.Errorf(
					"openvpn %q: remove tproxy: %w",
					tag,
					err,
				)
			}

			s.mu.Lock()
			delete(s.openVPNTProxySpecs, tag)
			s.mu.Unlock()
		}

		s.cleanupOpenVPNTProxyPolicyIfUnused()
		return nil
	}

	if err := s.ensureOpenVPNTProxy(spec); err != nil {
		return fmt.Errorf(
			"openvpn %q: apply tproxy: %w",
			tag,
			err,
		)
	}

	s.mu.Lock()
	s.openVPNTProxySpecs[tag] = spec
	s.mu.Unlock()

	return nil
}

func (s *Server) removeOpenVPNTProxyForTag(tag string) error {
	s.mu.Lock()
	spec, exists := s.openVPNTProxySpecs[tag]
	delete(s.openVPNTProxySpecs, tag)
	s.mu.Unlock()

	if !exists {
		return nil
	}

	return s.removeOpenVPNTProxy(spec)
}

func (s *Server) stopRemovedOpenVPNTProxySpecs(desired map[string]struct{}) {
	s.mu.Lock()

	removed := make(map[string]openVPNTProxySpec)

	for tag, spec := range s.openVPNTProxySpecs {
		if _, exists := desired[tag]; exists {
			continue
		}

		removed[tag] = spec
		delete(s.openVPNTProxySpecs, tag)
	}

	s.mu.Unlock()

	for tag, spec := range removed {
		if err := s.removeOpenVPNTProxy(spec); err != nil {
			s.appendLog("remove openvpn tproxy failed: " + tag + ": " + err.Error())
		}
	}
}

func (s *Server) stopAllOpenVPNTProxySpecs() {
	s.mu.Lock()

	specs := make(map[string]openVPNTProxySpec, len(s.openVPNTProxySpecs))

	for tag, spec := range s.openVPNTProxySpecs {
		specs[tag] = spec
	}

	s.openVPNTProxySpecs = make(map[string]openVPNTProxySpec)

	s.mu.Unlock()

	for tag, spec := range specs {
		if err := s.removeOpenVPNTProxy(spec); err != nil {
			s.appendLog("remove openvpn tproxy failed: " + tag + ": " + err.Error())
		}
	}
	s.cleanupOpenVPNTProxyPolicyIfUnused()
}

func cleanupOpenVPNTProxyChain(
	ctx context.Context,
	iptablesPath string,
) {
	checkJump := []string{
		"-w", "5",
		"-t", "mangle",
		"-C", "PREROUTING",
		"-j", openVPNTProxyChain,
	}

	if _, err := openVPNNetworkRun(
		ctx,
		iptablesPath,
		checkJump...,
	); err == nil {
		_, _ = openVPNNetworkRun(
			ctx,
			iptablesPath,
			"-w", "5",
			"-t", "mangle",
			"-D", "PREROUTING",
			"-j", openVPNTProxyChain,
		)
	}

	if _, err := openVPNNetworkRun(
		ctx,
		iptablesPath,
		"-w", "5",
		"-t", "mangle",
		"-S", openVPNTProxyChain,
	); err != nil {
		return
	}

	_, _ = openVPNNetworkRun(
		ctx,
		iptablesPath,
		"-w", "5",
		"-t", "mangle",
		"-F", openVPNTProxyChain,
	)

	_, _ = openVPNNetworkRun(
		ctx,
		iptablesPath,
		"-w", "5",
		"-t", "mangle",
		"-X", openVPNTProxyChain,
	)
}

func (s *Server) cleanupOpenVPNTProxyPolicyIfUnused() {
	s.mu.Lock()
	unused := len(s.openVPNTProxySpecs) == 0
	s.mu.Unlock()

	if !unused || openVPNNetworkGOOS != "linux" {
		return
	}

	ipPath, err := openVPNNetworkLookPath("ip")
	if err != nil {
		return
	}

	iptablesPath, iptablesErr :=
		openVPNNetworkLookPath("iptables")

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	if iptablesErr == nil {
		cleanupOpenVPNTProxyChain(
			ctx,
			iptablesPath,
		)
	}

	_, _ = openVPNNetworkRun(
		ctx,
		ipPath,
		"route", "del",
		"local", "0.0.0.0/0",
		"dev", "lo",
		"table", strconv.Itoa(openVPNTProxyTable),
	)

	_, _ = openVPNNetworkRun(
		ctx,
		ipPath,
		"rule", "del",
		"priority",
		strconv.Itoa(openVPNTProxyRulePriority),
		"fwmark",
		fmt.Sprintf(
			"0x%x/0x%x",
			openVPNTProxyMark,
			openVPNTProxyMask,
		),
		"table",
		strconv.Itoa(openVPNTProxyTable),
	)
}

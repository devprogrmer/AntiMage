package nodeagent

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

func (s openVPNTProxySpec) iptablesArgsForChain(action, chain, protocol string) ([]string, error) {
	args, err := s.iptablesArgs(action, protocol)
	if err != nil {
		return nil, err
	}
	for i, arg := range args {
		if arg == openVPNTProxyChain {
			args[i] = chain
		}
	}
	return args, nil
}

func ensureTaggedTProxyChain(ctx context.Context, iptablesPath, chain string) error {
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "mangle", "-S", chain); err != nil {
		if err := runOpenVPNNetworkRequired(ctx, iptablesPath, "-w", "5", "-t", "mangle", "-N", chain); err != nil {
			return err
		}
	}
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "mangle", "-C", "PREROUTING", "-j", chain); err == nil {
		return nil
	}
	return runOpenVPNNetworkRequired(ctx, iptablesPath, "-w", "5", "-t", "mangle", "-A", "PREROUTING", "-j", chain)
}

func (s *Server) ensureTaggedTProxy(spec openVPNTProxySpec, chain, label string) error {
	if !spec.Enabled {
		return nil
	}
	if openVPNNetworkGOOS != "linux" {
		return fmt.Errorf("%s tproxy is supported only on linux", label)
	}
	ipPath, err := openVPNNetworkLookPath("ip")
	if err != nil {
		return fmt.Errorf("%s tproxy: ip command not installed", label)
	}
	iptablesPath, err := openVPNNetworkLookPath("iptables")
	if err != nil {
		return fmt.Errorf("%s tproxy: iptables command not installed", label)
	}
	sysctlPath, err := openVPNNetworkLookPath("sysctl")
	if err != nil {
		return fmt.Errorf("%s tproxy: sysctl command not installed", label)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runOpenVPNNetworkRequired(ctx, sysctlPath, "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}
	rules, err := openVPNNetworkRun(ctx, ipPath, "rule", "show")
	if err != nil {
		return fmt.Errorf("%s tproxy: ip rule show: %w", label, err)
	}
	if !openVPNPolicyRulePresent(string(rules), spec) {
		if err := runOpenVPNNetworkRequired(ctx, ipPath, spec.policyRuleArgs()...); err != nil {
			return err
		}
	}
	if err := runOpenVPNNetworkRequired(ctx, ipPath, spec.localRouteArgs()...); err != nil {
		return err
	}
	if err := ensureTaggedTProxyChain(ctx, iptablesPath, chain); err != nil {
		return err
	}
	for _, protocol := range []string{"tcp", "udp"} {
		checkArgs, err := spec.iptablesArgsForChain("-C", chain, protocol)
		if err != nil {
			return err
		}
		checkArgs = append([]string{"-w", "5"}, checkArgs...)
		if _, err := openVPNNetworkRun(ctx, iptablesPath, checkArgs...); err == nil {
			continue
		}
		addArgs, err := spec.iptablesArgsForChain("-A", chain, protocol)
		if err != nil {
			return err
		}
		addArgs = append([]string{"-w", "5"}, addArgs...)
		if err := runOpenVPNNetworkRequired(ctx, iptablesPath, addArgs...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) removeTaggedTProxy(spec openVPNTProxySpec, chain string) error {
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
		checkArgs, err := spec.iptablesArgsForChain("-C", chain, protocol)
		if err != nil {
			return err
		}
		checkArgs = append([]string{"-w", "5"}, checkArgs...)
		if _, err := openVPNNetworkRun(ctx, iptablesPath, checkArgs...); err != nil {
			continue
		}
		deleteArgs, err := spec.iptablesArgsForChain("-D", chain, protocol)
		if err != nil {
			return err
		}
		deleteArgs = append([]string{"-w", "5"}, deleteArgs...)
		if err := runOpenVPNNetworkRequired(ctx, iptablesPath, deleteArgs...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) applyTaggedTProxy(tag string, spec openVPNTProxySpec, specs *map[string]openVPNTProxySpec, chain, label string) error {
	s.mu.Lock()
	previous, exists := (*specs)[tag]
	s.mu.Unlock()
	if exists && previous != spec {
		if err := s.removeTaggedTProxy(previous, chain); err != nil {
			return fmt.Errorf("%s %q: remove previous tproxy: %w", label, tag, err)
		}
		s.mu.Lock()
		delete(*specs, tag)
		s.mu.Unlock()
		exists = false
	}
	if !spec.Enabled {
		if exists {
			if err := s.removeTaggedTProxy(previous, chain); err != nil {
				return fmt.Errorf("%s %q: remove tproxy: %w", label, tag, err)
			}
			s.mu.Lock()
			delete(*specs, tag)
			s.mu.Unlock()
		}
		return s.cleanupTaggedTProxyPolicyIfUnused(specs, chain)
	}
	if err := s.ensureTaggedTProxy(spec, chain, label); err != nil {
		return fmt.Errorf("%s %q: apply tproxy: %w", label, tag, err)
	}
	s.mu.Lock()
	(*specs)[tag] = spec
	s.mu.Unlock()
	return nil
}

func (s *Server) removeTaggedTProxyForTag(tag string, specs *map[string]openVPNTProxySpec, chain string) error {
	s.mu.Lock()
	spec, exists := (*specs)[tag]
	delete(*specs, tag)
	s.mu.Unlock()
	if !exists {
		return nil
	}
	return s.removeTaggedTProxy(spec, chain)
}

func (s *Server) stopRemovedTaggedTProxySpecs(desired map[string]struct{}, specs *map[string]openVPNTProxySpec, chain, label string) {
	s.mu.Lock()
	removed := make(map[string]openVPNTProxySpec)
	for tag, spec := range *specs {
		if _, exists := desired[tag]; exists {
			continue
		}
		removed[tag] = spec
		delete(*specs, tag)
	}
	s.mu.Unlock()
	for tag, spec := range removed {
		if err := s.removeTaggedTProxy(spec, chain); err != nil {
			s.appendLog("remove " + label + " tproxy failed: " + tag + ": " + err.Error())
		}
	}
}

func (s *Server) stopAllTaggedTProxySpecs(specs *map[string]openVPNTProxySpec, chain, label string) {
	s.mu.Lock()
	copied := make(map[string]openVPNTProxySpec, len(*specs))
	for tag, spec := range *specs {
		copied[tag] = spec
	}
	*specs = make(map[string]openVPNTProxySpec)
	s.mu.Unlock()
	for tag, spec := range copied {
		if err := s.removeTaggedTProxy(spec, chain); err != nil {
			s.appendLog("remove " + label + " tproxy failed: " + tag + ": " + err.Error())
		}
	}
	if err := s.cleanupTaggedTProxyPolicyIfUnused(specs, chain); err != nil {
		s.appendLog("cleanup " + label + " tproxy policy failed: " + err.Error())
	}
}

func cleanupTaggedTProxyChain(ctx context.Context, iptablesPath, chain string) {
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "mangle", "-C", "PREROUTING", "-j", chain); err == nil {
		_, _ = openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "mangle", "-D", "PREROUTING", "-j", chain)
	}
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "mangle", "-S", chain); err != nil {
		return
	}
	_, _ = openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "mangle", "-F", chain)
	_, _ = openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "mangle", "-X", chain)
}

func (s *Server) cleanupTaggedTProxyPolicyIfUnused(specs *map[string]openVPNTProxySpec, chain string) error {
	s.mu.Lock()
	unused := len(*specs) == 0
	s.mu.Unlock()
	if !unused || openVPNNetworkGOOS != "linux" {
		return nil
	}
	ipPath, err := openVPNNetworkLookPath("ip")
	if err != nil {
		return nil
	}
	iptablesPath, iptablesErr := openVPNNetworkLookPath("iptables")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if iptablesErr == nil {
		cleanupTaggedTProxyChain(ctx, iptablesPath, chain)
	}
	var spec openVPNTProxySpec
	switch chain {
	case l2TPTProxyChain:
		spec = openVPNTProxySpec{Mark: l2TPTProxyMark, Mask: l2TPTProxyMask, Table: l2TPTProxyTable, Priority: l2TPTProxyRulePriority}
	case pptpTProxyChain:
		spec = openVPNTProxySpec{Mark: pptpTProxyMark, Mask: pptpTProxyMask, Table: pptpTProxyTable, Priority: pptpTProxyRulePriority}
	default:
		return nil
	}
	if err := runOpenVPNNetworkAlreadyCleanOK(ctx, ipPath, "route", "del", "local", "0.0.0.0/0", "dev", "lo", "table", strconv.Itoa(spec.Table)); err != nil {
		return err
	}
	return runOpenVPNNetworkAlreadyCleanOK(ctx, ipPath, "rule", "del", "priority", strconv.Itoa(spec.Priority), "fwmark", spec.markMask(), "table", strconv.Itoa(spec.Table))
}

func (s *Server) applyTaggedNAT(tag string, spec openVPNNATSpec, specs *map[string]openVPNNATSpec, chain, label string) error {
	if !spec.Enabled || openVPNNATGOOS != "linux" {
		return nil
	}
	iptablesPath, err := openVPNNetworkLookPath("iptables")
	if err != nil {
		return fmt.Errorf("%s NAT: iptables command not installed", label)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureTaggedNATChain(ctx, iptablesPath, chain); err != nil {
		return err
	}
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "nat", "-C", chain, "-s", spec.SourceCIDR, "-j", "MASQUERADE"); err != nil {
		if err := runOpenVPNNetworkRequired(ctx, iptablesPath, "-w", "5", "-t", "nat", "-A", chain, "-s", spec.SourceCIDR, "-j", "MASQUERADE"); err != nil {
			return err
		}
	}
	s.mu.Lock()
	(*specs)[tag] = spec
	s.mu.Unlock()
	return nil
}

func ensureTaggedNATChain(ctx context.Context, iptablesPath, chain string) error {
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "nat", "-S", chain); err != nil {
		if err := runOpenVPNNetworkRequired(ctx, iptablesPath, "-w", "5", "-t", "nat", "-N", chain); err != nil {
			return err
		}
	}
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "nat", "-C", "POSTROUTING", "-j", chain); err == nil {
		return nil
	}
	return runOpenVPNNetworkRequired(ctx, iptablesPath, "-w", "5", "-t", "nat", "-A", "POSTROUTING", "-j", chain)
}

func (s *Server) removeTaggedNATForTag(tag string, specs *map[string]openVPNNATSpec, chain string) error {
	s.mu.Lock()
	delete(*specs, tag)
	unused := len(*specs) == 0
	s.mu.Unlock()
	if unused {
		return s.cleanupTaggedNAT(chain)
	}
	return nil
}

func (s *Server) stopRemovedTaggedNATSpecs(desired map[string]struct{}, specs *map[string]openVPNNATSpec, chain, label string) {
	s.mu.Lock()
	for tag := range *specs {
		if _, ok := desired[tag]; !ok {
			delete(*specs, tag)
		}
	}
	unused := len(*specs) == 0
	s.mu.Unlock()
	if unused {
		if err := s.cleanupTaggedNAT(chain); err != nil {
			s.appendLog("cleanup " + label + " NAT failed: " + err.Error())
		}
	}
}

func (s *Server) stopAllTaggedNATSpecs(specs *map[string]openVPNNATSpec, chain, label string) {
	s.mu.Lock()
	*specs = make(map[string]openVPNNATSpec)
	s.mu.Unlock()
	if err := s.cleanupTaggedNAT(chain); err != nil {
		s.appendLog("cleanup " + label + " NAT failed: " + err.Error())
	}
}

func (s *Server) cleanupTaggedNAT(chain string) error {
	if openVPNNATGOOS != "linux" {
		return nil
	}
	iptablesPath, err := openVPNNetworkLookPath("iptables")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "nat", "-C", "POSTROUTING", "-j", chain); err == nil {
		if err := runOpenVPNNetworkAlreadyCleanOK(ctx, iptablesPath, "-w", "5", "-t", "nat", "-D", "POSTROUTING", "-j", chain); err != nil {
			return err
		}
	}
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "nat", "-S", chain); err != nil {
		return nil
	}
	if err := runOpenVPNNetworkAlreadyCleanOK(ctx, iptablesPath, "-w", "5", "-t", "nat", "-F", chain); err != nil {
		return err
	}
	return runOpenVPNNetworkAlreadyCleanOK(ctx, iptablesPath, "-w", "5", "-t", "nat", "-X", chain)
}

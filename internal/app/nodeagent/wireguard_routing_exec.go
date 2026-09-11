package nodeagent

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	wireGuardRoutingRun = func(
		ctx context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	wireGuardRoutingLookPath = exec.LookPath
	wireGuardRoutingGOOS     = runtime.GOOS
)

func (s *Server) reconcileWireGuardRouting(
	prepared []preparedWireGuardRuntime,
) error {
	tproxySpecs := make([]wireGuardRoutingSpec, 0)
	natSpecs := make([]wireGuardRoutingSpec, 0)

	for _, item := range prepared {
		switch item.Routing.Mode {
		case wireGuardRoutingTProxy:
			tproxySpecs = append(tproxySpecs, item.Routing)
		case wireGuardRoutingNAT:
			natSpecs = append(natSpecs, item.Routing)
		case wireGuardRoutingNone:
		default:
			return fmt.Errorf(
				"wireguard %q: unknown routing mode %q",
				item.Tag,
				item.Routing.Mode,
			)
		}
	}

	if len(tproxySpecs) == 0 && len(natSpecs) == 0 {
		s.cleanupWireGuardRoutingAll()
		return nil
	}
	if wireGuardRoutingGOOS != "linux" {
		return fmt.Errorf("wireguard routing is supported only on linux")
	}

	iptablesPath, err := wireGuardRoutingLookPath("iptables")
	if err != nil {
		return fmt.Errorf("wireguard routing: iptables command not installed")
	}
	sysctlPath, err := wireGuardRoutingLookPath("sysctl")
	if err != nil {
		return fmt.Errorf("wireguard routing: sysctl command not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := runWireGuardRoutingRequired(
		ctx,
		sysctlPath,
		"-w",
		"net.ipv4.ip_forward=1",
	); err != nil {
		return err
	}

	cleanupWireGuardTProxy(ctx, iptablesPath)
	cleanupWireGuardNAT(ctx, iptablesPath)

	sort.Slice(tproxySpecs, func(i, j int) bool {
		if tproxySpecs[i].Interface == tproxySpecs[j].Interface {
			return tproxySpecs[i].TunnelPort < tproxySpecs[j].TunnelPort
		}
		return tproxySpecs[i].Interface < tproxySpecs[j].Interface
	})
	sort.Slice(natSpecs, func(i, j int) bool {
		if natSpecs[i].SourceCIDR == natSpecs[j].SourceCIDR {
			return natSpecs[i].Interface < natSpecs[j].Interface
		}
		return natSpecs[i].SourceCIDR < natSpecs[j].SourceCIDR
	})

	if len(tproxySpecs) > 0 {
		if err := applyWireGuardTProxy(ctx, iptablesPath, tproxySpecs); err != nil {
			return err
		}
	}

	if len(natSpecs) > 0 {
		if err := applyWireGuardNAT(ctx, iptablesPath, natSpecs); err != nil {
			return err
		}
	}

	return nil
}

func applyWireGuardTProxy(
	ctx context.Context,
	iptablesPath string,
	specs []wireGuardRoutingSpec,
) error {
	ipPath, err := wireGuardRoutingLookPath("ip")
	if err != nil {
		return fmt.Errorf("wireguard tproxy: ip command not installed")
	}

	spec := specs[0]
	if err := runWireGuardRoutingRequired(
		ctx,
		ipPath,
		"rule", "add",
		"priority", strconv.Itoa(spec.Priority),
		"fwmark", spec.markMask(),
		"table", strconv.Itoa(spec.Table),
	); err != nil {
		return err
	}

	if err := runWireGuardRoutingRequired(
		ctx,
		ipPath,
		"route", "replace",
		"local", "0.0.0.0/0",
		"dev", "lo",
		"table", strconv.Itoa(spec.Table),
	); err != nil {
		return err
	}

	if err := ensureWireGuardChain(
		ctx,
		iptablesPath,
		"mangle",
		"PREROUTING",
		wireGuardTProxyChain,
	); err != nil {
		return err
	}

	for _, item := range specs {
		for _, protocol := range []string{"tcp", "udp"} {
			args, err := item.tproxyArgs("-A", protocol)
			if err != nil {
				return err
			}
			args = append([]string{"-w", "5"}, args...)
			if err := runWireGuardRoutingRequired(
				ctx,
				iptablesPath,
				args...,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyWireGuardNAT(
	ctx context.Context,
	iptablesPath string,
	specs []wireGuardRoutingSpec,
) error {
	if err := ensureWireGuardChain(
		ctx,
		iptablesPath,
		"nat",
		"POSTROUTING",
		wireGuardNATChain,
	); err != nil {
		return err
	}
	if err := ensureWireGuardChain(
		ctx,
		iptablesPath,
		"filter",
		"FORWARD",
		wireGuardForwardChain,
	); err != nil {
		return err
	}

	seenCIDRs := make(map[string]struct{})
	for _, item := range specs {
		if _, exists := seenCIDRs[item.SourceCIDR]; !exists {
			args, err := item.natArgs("-A")
			if err != nil {
				return err
			}
			args = append([]string{"-w", "5"}, args...)
			if err := runWireGuardRoutingRequired(
				ctx,
				iptablesPath,
				args...,
			); err != nil {
				return err
			}
			seenCIDRs[item.SourceCIDR] = struct{}{}
		}

		outArgs, err := item.forwardOutArgs("-A")
		if err != nil {
			return err
		}
		outArgs = append([]string{"-w", "5"}, outArgs...)
		if err := runWireGuardRoutingRequired(ctx, iptablesPath, outArgs...); err != nil {
			return err
		}

		returnArgs, err := item.forwardReturnArgs("-A")
		if err != nil {
			return err
		}
		returnArgs = append([]string{"-w", "5"}, returnArgs...)
		if err := runWireGuardRoutingRequired(ctx, iptablesPath, returnArgs...); err != nil {
			return err
		}
	}
	return nil
}

func ensureWireGuardChain(
	ctx context.Context,
	iptablesPath string,
	table string,
	parent string,
	chain string,
) error {
	tableArgs := []string{}
	if table != "filter" {
		tableArgs = []string{"-t", table}
	}

	create := append([]string{"-w", "5"}, tableArgs...)
	create = append(create, "-N", chain)
	if output, err := wireGuardRoutingRun(ctx, iptablesPath, create...); err != nil {
		detail := strings.ToLower(strings.TrimSpace(string(output)))
		if !strings.Contains(detail, "chain already exists") {
			return fmt.Errorf(
				"wireguard routing command %q failed: %w: %s",
				iptablesPath,
				err,
				strings.TrimSpace(string(output)),
			)
		}
	}

	checkJump := append([]string{"-w", "5"}, tableArgs...)
	checkJump = append(checkJump, "-C", parent, "-j", chain)
	if _, err := wireGuardRoutingRun(ctx, iptablesPath, checkJump...); err == nil {
		return nil
	}

	addJump := append([]string{"-w", "5"}, tableArgs...)
	addJump = append(addJump, "-A", parent, "-j", chain)
	return runWireGuardRoutingRequired(ctx, iptablesPath, addJump...)
}

func runWireGuardRoutingRequired(
	ctx context.Context,
	name string,
	args ...string,
) error {
	output, err := wireGuardRoutingRun(ctx, name, args...)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf(
			"wireguard routing command %q failed: %w",
			name,
			err,
		)
	}
	return fmt.Errorf(
		"wireguard routing command %q failed: %w: %s",
		name,
		err,
		detail,
	)
}

func (s *Server) cleanupWireGuardRoutingAll() {
	if wireGuardRoutingGOOS != "linux" {
		return
	}
	iptablesPath, err := wireGuardRoutingLookPath("iptables")
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cleanupWireGuardTProxy(ctx, iptablesPath)
	cleanupWireGuardNAT(ctx, iptablesPath)
}

func cleanupWireGuardTProxy(
	ctx context.Context,
	iptablesPath string,
) {
	cleanupWireGuardChain(
		ctx,
		iptablesPath,
		"mangle",
		"PREROUTING",
		wireGuardTProxyChain,
	)

	ipPath, err := wireGuardRoutingLookPath("ip")
	if err != nil {
		return
	}

	_, _ = wireGuardRoutingRun(
		ctx,
		ipPath,
		"route", "del",
		"local", "0.0.0.0/0",
		"dev", "lo",
		"table", strconv.Itoa(wireGuardTProxyTable),
	)
	_, _ = wireGuardRoutingRun(
		ctx,
		ipPath,
		"rule", "del",
		"priority", strconv.Itoa(wireGuardTProxyRulePriority),
		"fwmark", fmt.Sprintf(
			"0x%x/0x%x",
			wireGuardTProxyMark,
			wireGuardTProxyMask,
		),
		"table", strconv.Itoa(wireGuardTProxyTable),
	)
}

func cleanupWireGuardNAT(
	ctx context.Context,
	iptablesPath string,
) {
	cleanupWireGuardChain(
		ctx,
		iptablesPath,
		"nat",
		"POSTROUTING",
		wireGuardNATChain,
	)
	cleanupWireGuardChain(
		ctx,
		iptablesPath,
		"filter",
		"FORWARD",
		wireGuardForwardChain,
	)
}

func cleanupWireGuardChain(
	ctx context.Context,
	iptablesPath string,
	table string,
	parent string,
	chain string,
) {
	tableArgs := []string{}
	if table != "filter" {
		tableArgs = []string{"-t", table}
	}

	for {
		check := append([]string{"-w", "5"}, tableArgs...)
		check = append(check, "-C", parent, "-j", chain)
		if _, err := wireGuardRoutingRun(ctx, iptablesPath, check...); err != nil {
			break
		}

		del := append([]string{"-w", "5"}, tableArgs...)
		del = append(del, "-D", parent, "-j", chain)
		if _, err := wireGuardRoutingRun(ctx, iptablesPath, del...); err != nil {
			break
		}
	}

	flush := append([]string{"-w", "5"}, tableArgs...)
	flush = append(flush, "-F", chain)
	_, _ = wireGuardRoutingRun(ctx, iptablesPath, flush...)

	deleteChain := append([]string{"-w", "5"}, tableArgs...)
	deleteChain = append(deleteChain, "-X", chain)
	_, _ = wireGuardRoutingRun(ctx, iptablesPath, deleteChain...)
}

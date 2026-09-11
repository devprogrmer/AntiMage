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
		if wireGuardRoutingGOOS != "linux" {
			return nil
		}

		// Routing tools are optional when no WireGuard routing is desired.
		// Stale AntiMage routing cleanup is best-effort in this path.
		if _, err := wireGuardRoutingLookPath("iptables"); err != nil {
			return nil
		}
		if err := s.cleanupWireGuardRoutingAll(); err != nil {
			s.appendLog(
				"cleanup stale wireguard routing failed: " + err.Error(),
			)
		}
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

	if err := cleanupWireGuardRoutingManaged(
		ctx,
		iptablesPath,
	); err != nil {
		return err
	}

	rollback := func(applyErr error) error {
		return rollbackWireGuardRouting(
			ctx,
			iptablesPath,
			applyErr,
		)
	}

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
			return rollback(err)
		}
	}

	if len(natSpecs) > 0 {
		if err := applyWireGuardNAT(ctx, iptablesPath, natSpecs); err != nil {
			return rollback(err)
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
	output, checkErr := wireGuardRoutingRun(ctx, iptablesPath, checkJump...)
	if checkErr == nil {
		return nil
	}
	if !wireGuardRoutingMissing(output, checkErr) {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return fmt.Errorf(
				"wireguard routing check command %q failed: %w",
				iptablesPath,
				checkErr,
			)
		}
		return fmt.Errorf(
			"wireguard routing check command %q failed: %w: %s",
			iptablesPath,
			checkErr,
			detail,
		)
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

func wireGuardRoutingMissing(output []byte, err error) bool {
	if err == nil {
		return false
	}

	detail := strings.ToLower(strings.TrimSpace(string(output)))

	if wireGuardRoutingObjectMissing(output) ||
		strings.Contains(
			detail,
			"bad rule (does a matching rule exist in that chain?)",
		) {
		return true
	}

	// Explicit sentinel used by command fakes in tests.
	if strings.EqualFold(strings.TrimSpace(err.Error()), "missing") {
		return true
	}

	// Exit status by itself is not proof that the resource is absent.
	return false
}

func cleanupWireGuardRoutingManaged(
	ctx context.Context,
	iptablesPath string,
) error {
	tproxyErr := cleanupWireGuardTProxy(ctx, iptablesPath)
	natErr := cleanupWireGuardNAT(ctx, iptablesPath)

	switch {
	case tproxyErr != nil && natErr != nil:
		return fmt.Errorf(
			"wireguard routing cleanup failed: tproxy: %v; nat: %v",
			tproxyErr,
			natErr,
		)
	case tproxyErr != nil:
		return tproxyErr
	default:
		return natErr
	}
}

func rollbackWireGuardRouting(
	ctx context.Context,
	iptablesPath string,
	applyErr error,
) error {
	cleanupErr := cleanupWireGuardRoutingManaged(
		ctx,
		iptablesPath,
	)
	if cleanupErr != nil {
		return fmt.Errorf(
			"%w; wireguard routing rollback failed: %v",
			applyErr,
			cleanupErr,
		)
	}
	return applyErr
}

func wireGuardRoutingObjectMissing(output []byte) bool {
	detail := strings.ToLower(strings.TrimSpace(string(output)))
	return strings.Contains(detail, "no chain/target/match by that name") ||
		strings.Contains(detail, "does not exist")
}

func wireGuardRoutingDeleteMissing(output []byte) bool {
	detail := strings.ToLower(strings.TrimSpace(string(output)))
	return strings.Contains(detail, "no such process") ||
		strings.Contains(detail, "no such file or directory") ||
		strings.Contains(detail, "cannot find device") ||
		strings.Contains(detail, "not found")
}

func runWireGuardRoutingDeleteIfPresent(
	ctx context.Context,
	name string,
	args ...string,
) error {
	output, err := wireGuardRoutingRun(ctx, name, args...)
	if err == nil {
		return nil
	}
	if wireGuardRoutingDeleteMissing(output) {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf(
			"wireguard routing cleanup command %q failed: %w",
			name,
			err,
		)
	}
	return fmt.Errorf(
		"wireguard routing cleanup command %q failed: %w: %s",
		name,
		err,
		detail,
	)
}

func (s *Server) cleanupWireGuardRoutingAll() error {
	if wireGuardRoutingGOOS != "linux" {
		return nil
	}

	iptablesPath, err := wireGuardRoutingLookPath("iptables")
	if err != nil {
		return fmt.Errorf(
			"wireguard routing: iptables command not installed",
		)
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	return cleanupWireGuardRoutingManaged(
		ctx,
		iptablesPath,
	)
}

func cleanupWireGuardTProxy(
	ctx context.Context,
	iptablesPath string,
) error {
	chainErr := cleanupWireGuardChain(
		ctx,
		iptablesPath,
		"mangle",
		"PREROUTING",
		wireGuardTProxyChain,
	)

	ipPath, pathErr := wireGuardRoutingLookPath("ip")
	if pathErr != nil {
		if chainErr != nil {
			return fmt.Errorf(
				"wireguard tproxy cleanup failed: chain: %v; ip command not installed",
				chainErr,
			)
		}
		return fmt.Errorf(
			"wireguard routing cleanup: ip command not installed",
		)
	}

	routeErr := runWireGuardRoutingDeleteIfPresent(
		ctx,
		ipPath,
		"route", "del",
		"local", "0.0.0.0/0",
		"dev", "lo",
		"table", strconv.Itoa(wireGuardTProxyTable),
	)

	ruleErr := runWireGuardRoutingDeleteIfPresent(
		ctx,
		ipPath,
		"rule", "del",
		"priority",
		strconv.Itoa(wireGuardTProxyRulePriority),
		"fwmark",
		fmt.Sprintf(
			"0x%x/0x%x",
			wireGuardTProxyMark,
			wireGuardTProxyMask,
		),
		"table",
		strconv.Itoa(wireGuardTProxyTable),
	)

	parts := make([]string, 0, 3)

	if chainErr != nil {
		parts = append(
			parts,
			"chain: "+chainErr.Error(),
		)
	}
	if routeErr != nil {
		parts = append(
			parts,
			"route: "+routeErr.Error(),
		)
	}
	if ruleErr != nil {
		parts = append(
			parts,
			"rule: "+ruleErr.Error(),
		)
	}

	if len(parts) > 0 {
		return fmt.Errorf(
			"wireguard tproxy cleanup failed: %s",
			strings.Join(parts, "; "),
		)
	}

	return nil
}

func cleanupWireGuardNAT(
	ctx context.Context,
	iptablesPath string,
) error {
	natErr := cleanupWireGuardChain(
		ctx,
		iptablesPath,
		"nat",
		"POSTROUTING",
		wireGuardNATChain,
	)

	forwardErr := cleanupWireGuardChain(
		ctx,
		iptablesPath,
		"filter",
		"FORWARD",
		wireGuardForwardChain,
	)

	switch {
	case natErr != nil && forwardErr != nil:
		return fmt.Errorf(
			"wireguard NAT cleanup failed: nat: %v; forward: %v",
			natErr,
			forwardErr,
		)

	case natErr != nil:
		return natErr

	default:
		return forwardErr
	}
}

func cleanupWireGuardChain(
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

	for {
		check := append([]string{"-w", "5"}, tableArgs...)
		check = append(check, "-C", parent, "-j", chain)
		output, err := wireGuardRoutingRun(ctx, iptablesPath, check...)
		if err != nil {
			if wireGuardRoutingMissing(output, err) {
				break
			}
			return fmt.Errorf(
				"wireguard routing cleanup check failed: %w: %s",
				err,
				strings.TrimSpace(string(output)),
			)
		}

		del := append([]string{"-w", "5"}, tableArgs...)
		del = append(del, "-D", parent, "-j", chain)
		if output, err := wireGuardRoutingRun(ctx, iptablesPath, del...); err != nil {
			return fmt.Errorf(
				"wireguard routing cleanup delete jump failed: %w: %s",
				err,
				strings.TrimSpace(string(output)),
			)
		}
	}

	flush := append([]string{"-w", "5"}, tableArgs...)
	flush = append(flush, "-F", chain)
	if output, err := wireGuardRoutingRun(ctx, iptablesPath, flush...); err != nil &&
		!wireGuardRoutingObjectMissing(output) {
		return fmt.Errorf(
			"wireguard routing cleanup flush failed: %w: %s",
			err,
			strings.TrimSpace(string(output)),
		)
	}

	deleteChain := append([]string{"-w", "5"}, tableArgs...)
	deleteChain = append(deleteChain, "-X", chain)
	if output, err := wireGuardRoutingRun(
		ctx,
		iptablesPath,
		deleteChain...,
	); err != nil && !wireGuardRoutingObjectMissing(output) {
		return fmt.Errorf(
			"wireguard routing cleanup delete chain failed: %w: %s",
			err,
			strings.TrimSpace(string(output)),
		)
	}
	return nil
}

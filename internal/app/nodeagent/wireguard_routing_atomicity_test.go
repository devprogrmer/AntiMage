package nodeagent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEnsureWireGuardChainDoesNotAppendOnUnknownCheckError(t *testing.T) {
	oldRun := wireGuardRoutingRun
	wireGuardRoutingRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		switch {
		case strings.Contains(call, " -N ANTIMAGE_WG_TPROXY"):
			return []byte("iptables: Chain already exists."), errors.New("exit status 1")
		case strings.Contains(call, " -C PREROUTING -j ANTIMAGE_WG_TPROXY"):
			return []byte("Permission denied"), errors.New("exit status 4")
		default:
			return nil, nil
		}
	}
	t.Cleanup(func() {
		wireGuardRoutingRun = oldRun
	})

	err := ensureWireGuardChain(
		context.Background(),
		"iptables",
		"mangle",
		"PREROUTING",
		wireGuardTProxyChain,
	)
	if err == nil || !strings.Contains(err.Error(), "routing check command") {
		t.Fatalf("error = %v", err)
	}
}

func TestCleanupWireGuardChainUnknownCheckDoesNotMutate(t *testing.T) {
	oldRun := wireGuardRoutingRun
	var calls []string
	wireGuardRoutingRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		if strings.Contains(call, " -C PREROUTING -j ANTIMAGE_WG_TPROXY") {
			return []byte("Permission denied"), errors.New("exit status 4")
		}
		return nil, nil
	}
	t.Cleanup(func() {
		wireGuardRoutingRun = oldRun
	})

	cleanupWireGuardChain(
		context.Background(),
		"iptables",
		"mangle",
		"PREROUTING",
		wireGuardTProxyChain,
	)

	joined := strings.Join(calls, "\n")
	for _, forbidden := range []string{
		" -D PREROUTING -j ANTIMAGE_WG_TPROXY",
		" -F ANTIMAGE_WG_TPROXY",
		" -X ANTIMAGE_WG_TPROXY",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("cleanup mutated state after unknown check error %q:\n%s", forbidden, joined)
		}
	}
}

func TestReconcileWireGuardRoutingRollsBackOnPartialApplyFailure(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	oldGOOS := wireGuardRoutingGOOS
	oldLookPath := wireGuardRoutingLookPath
	oldRun := wireGuardRoutingRun
	wireGuardRoutingGOOS = "linux"
	wireGuardRoutingLookPath = func(name string) (string, error) {
		return name, nil
	}

	var calls []string
	wireGuardRoutingRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)

		if name == "iptables" && strings.Contains(call, " -C ") {
			return nil, errors.New("missing")
		}
		if strings.Contains(call, "ip route replace local 0.0.0.0/0") {
			return []byte("simulated route failure"), errors.New("exit status 2")
		}
		return nil, nil
	}
	t.Cleanup(func() {
		wireGuardRoutingGOOS = oldGOOS
		wireGuardRoutingLookPath = oldLookPath
		wireGuardRoutingRun = oldRun
	})

	err := server.reconcileWireGuardRouting([]preparedWireGuardRuntime{{
		Tag: "wg-main",
		Routing: wireGuardRoutingSpec{
			Mode:       wireGuardRoutingTProxy,
			Interface:  "amwgdeadbeef",
			SourceCIDR: "10.69.0.0/16",
			TunnelPort: 41940,
			Mark:       wireGuardTProxyMark,
			Mask:       wireGuardTProxyMask,
			Table:      wireGuardTProxyTable,
			Priority:   wireGuardTProxyRulePriority,
		},
	}})
	if err == nil {
		t.Fatal("expected partial apply failure")
	}

	routeFailure := -1
	for i, call := range calls {
		if strings.Contains(call, "ip route replace local 0.0.0.0/0") {
			routeFailure = i
			break
		}
	}
	if routeFailure < 0 {
		t.Fatalf("route failure command not observed:\n%s", strings.Join(calls, "\n"))
	}

	rolledBackRule := false
	for _, call := range calls[routeFailure+1:] {
		if strings.Contains(call, "ip rule del priority 10020") {
			rolledBackRule = true
			break
		}
	}
	if !rolledBackRule {
		t.Fatalf("policy rule was not rolled back after partial failure:\n%s", strings.Join(calls, "\n"))
	}
}

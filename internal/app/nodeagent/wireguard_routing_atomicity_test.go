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

	err := cleanupWireGuardChain(
		context.Background(),
		"iptables",
		"mangle",
		"PREROUTING",
		wireGuardTProxyChain,
	)
	if err == nil || !strings.Contains(err.Error(), "cleanup check failed") {
		t.Fatalf("error = %v", err)
	}

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

func TestReconcileWireGuardRoutingStopsOnUnknownCleanupError(t *testing.T) {
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
		if name == "iptables" &&
			strings.Contains(call, " -C PREROUTING -j ANTIMAGE_WG_TPROXY") {
			return []byte("Permission denied"), errors.New("exit status 4")
		}
		if name == "iptables" && strings.Contains(call, " -C ") {
			return nil, errors.New("missing")
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
	if err == nil || !strings.Contains(err.Error(), "cleanup check failed") {
		t.Fatalf("error = %v", err)
	}

	joined := strings.Join(calls, "\n")
	if strings.Contains(joined, "ip rule add priority 10020") {
		t.Fatalf(
			"routing apply continued after unknown cleanup error:\n%s",
			joined,
		)
	}
}

func TestCleanupWireGuardTProxyStillDeletesRuleWhenRouteDeleteFails(t *testing.T) {
	oldLookPath := wireGuardRoutingLookPath
	oldRun := wireGuardRoutingRun
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

		if name == "iptables" &&
			strings.Contains(call, " -C PREROUTING -j ANTIMAGE_WG_TPROXY") {
			return nil, errors.New("missing")
		}
		if strings.Contains(call, "ip route del local 0.0.0.0/0") {
			return []byte("RTNETLINK answers: Operation not permitted"), errors.New("exit status 2")
		}
		return nil, nil
	}
	t.Cleanup(func() {
		wireGuardRoutingLookPath = oldLookPath
		wireGuardRoutingRun = oldRun
	})

	err := cleanupWireGuardTProxy(context.Background(), "iptables")
	if err == nil {
		t.Fatal("expected cleanup error")
	}

	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "ip rule del priority 10020") {
		t.Fatalf(
			"policy rule cleanup was skipped after route cleanup error:\n%s",
			joined,
		)
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

type wireGuardRoutingExitError struct {
	code int
	msg  string
}

func (e wireGuardRoutingExitError) Error() string {
	return e.msg
}

func (e wireGuardRoutingExitError) ExitCode() int {
	return e.code
}

func TestWireGuardRoutingMissingDoesNotTreatExitOneErrorAsAbsent(t *testing.T) {
	err := wireGuardRoutingExitError{
		code: 1,
		msg:  "exit status 1",
	}

	if wireGuardRoutingMissing(
		[]byte("iptables: Permission denied"),
		err,
	) {
		t.Fatal(
			"exit status 1 with Permission denied was treated as missing",
		)
	}

	if !wireGuardRoutingMissing(
		[]byte(
			"iptables: Bad rule (does a matching rule exist in that chain?).",
		),
		err,
	) {
		t.Fatal("known missing-rule output was not classified as missing")
	}
}

func TestCleanupWireGuardTProxyContinuesAfterChainFailure(t *testing.T) {
	oldLookPath := wireGuardRoutingLookPath
	oldRun := wireGuardRoutingRun

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

		if name == "iptables" &&
			strings.Contains(
				call,
				"-C PREROUTING -j ANTIMAGE_WG_TPROXY",
			) {
			return []byte("Permission denied"), errors.New("exit status 4")
		}

		return nil, nil
	}

	t.Cleanup(func() {
		wireGuardRoutingLookPath = oldLookPath
		wireGuardRoutingRun = oldRun
	})

	err := cleanupWireGuardTProxy(
		context.Background(),
		"iptables",
	)
	if err == nil {
		t.Fatal("expected cleanup error")
	}

	joined := strings.Join(calls, "\n")

	if !strings.Contains(
		joined,
		"ip route del local 0.0.0.0/0",
	) {
		t.Fatalf(
			"route cleanup skipped after chain failure:\n%s",
			joined,
		)
	}

	if !strings.Contains(
		joined,
		"ip rule del priority 10020",
	) {
		t.Fatalf(
			"rule cleanup skipped after chain failure:\n%s",
			joined,
		)
	}
}

func TestCleanupWireGuardNATContinuesAfterNATChainFailure(t *testing.T) {
	oldRun := wireGuardRoutingRun

	var calls []string

	wireGuardRoutingRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)

		if name == "iptables" &&
			strings.Contains(call, "-t nat") &&
			strings.Contains(
				call,
				"-C POSTROUTING -j ANTIMAGE_WG_NAT",
			) {
			return []byte("Permission denied"), errors.New("exit status 4")
		}

		if name == "iptables" &&
			strings.Contains(
				call,
				"-C FORWARD -j ANTIMAGE_WG_FORWARD",
			) {
			return nil, errors.New("missing")
		}

		return nil, nil
	}

	t.Cleanup(func() {
		wireGuardRoutingRun = oldRun
	})

	err := cleanupWireGuardNAT(
		context.Background(),
		"iptables",
	)
	if err == nil {
		t.Fatal("expected cleanup error")
	}

	joined := strings.Join(calls, "\n")

	if !strings.Contains(
		joined,
		"-C FORWARD -j ANTIMAGE_WG_FORWARD",
	) {
		t.Fatalf(
			"FORWARD cleanup skipped after NAT-chain failure:\n%s",
			joined,
		)
	}
}

func TestRollbackWireGuardRoutingStillCleansNATWhenTProxyCleanupFails(
	t *testing.T,
) {
	oldLookPath := wireGuardRoutingLookPath
	oldRun := wireGuardRoutingRun

	wireGuardRoutingLookPath = func(name string) (string, error) {
		return name, nil
	}

	sawNATCleanup := false

	wireGuardRoutingRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")

		if name == "iptables" &&
			strings.Contains(call, "-t mangle") &&
			strings.Contains(
				call,
				"-C PREROUTING -j ANTIMAGE_WG_TPROXY",
			) {
			return []byte("Permission denied"), errors.New("exit status 4")
		}

		if name == "iptables" &&
			strings.Contains(call, "-t nat") &&
			strings.Contains(
				call,
				"-C POSTROUTING -j ANTIMAGE_WG_NAT",
			) {
			sawNATCleanup = true
			return nil, errors.New("missing")
		}

		if name == "iptables" &&
			strings.Contains(
				call,
				"-C FORWARD -j ANTIMAGE_WG_FORWARD",
			) {
			return nil, errors.New("missing")
		}

		return nil, nil
	}

	t.Cleanup(func() {
		wireGuardRoutingLookPath = oldLookPath
		wireGuardRoutingRun = oldRun
	})

	err := rollbackWireGuardRouting(
		context.Background(),
		"iptables",
		errors.New("simulated apply failure"),
	)
	if err == nil {
		t.Fatal("expected rollback error")
	}

	if !sawNATCleanup {
		t.Fatal(
			"NAT cleanup was skipped after TProxy rollback failure",
		)
	}
}

func TestReconcileWireGuardRoutingFailurePointRollbackMatrix(t *testing.T) {
	tests := []struct {
		name         string
		failContains []string
	}{
		{
			name: "mangle jump append",
			failContains: []string{
				"iptables -w 5 -t mangle -A PREROUTING -j ANTIMAGE_WG_TPROXY",
			},
		},
		{
			name: "tcp tproxy append",
			failContains: []string{
				"iptables -w 5 -t mangle -A ANTIMAGE_WG_TPROXY",
				" -p tcp ",
			},
		},
		{
			name: "udp tproxy append",
			failContains: []string{
				"iptables -w 5 -t mangle -A ANTIMAGE_WG_TPROXY",
				" -p udp ",
			},
		},
		{
			name: "nat masquerade append",
			failContains: []string{
				"iptables -w 5 -t nat -A ANTIMAGE_WG_NAT",
				" -j MASQUERADE",
			},
		},
		{
			name: "forward append",
			failContains: []string{
				"iptables -w 5 -A ANTIMAGE_WG_FORWARD",
				" -i amwgnat ",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := New(Config{DataDir: t.TempDir()})

			oldGOOS := wireGuardRoutingGOOS
			oldLookPath := wireGuardRoutingLookPath
			oldRun := wireGuardRoutingRun

			wireGuardRoutingGOOS = "linux"
			wireGuardRoutingLookPath = func(name string) (string, error) {
				return name, nil
			}

			var calls []string
			failureIndex := -1
			failed := false

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

				matchesFailure := true
				for _, part := range tt.failContains {
					if !strings.Contains(call, part) {
						matchesFailure = false
						break
					}
				}

				if !failed && matchesFailure {
					failed = true
					failureIndex = len(calls) - 1
					return []byte("simulated apply failure"), errors.New("exit status 2")
				}

				return nil, nil
			}

			t.Cleanup(func() {
				wireGuardRoutingGOOS = oldGOOS
				wireGuardRoutingLookPath = oldLookPath
				wireGuardRoutingRun = oldRun
			})

			prepared := []preparedWireGuardRuntime{
				{
					Tag: "wg-tproxy",
					Routing: wireGuardRoutingSpec{
						Mode:       wireGuardRoutingTProxy,
						Interface:  "amwgtproxy",
						SourceCIDR: "10.69.0.0/16",
						TunnelPort: 41940,
						Mark:       wireGuardTProxyMark,
						Mask:       wireGuardTProxyMask,
						Table:      wireGuardTProxyTable,
						Priority:   wireGuardTProxyRulePriority,
					},
				},
				{
					Tag: "wg-nat",
					Routing: wireGuardRoutingSpec{
						Mode:       wireGuardRoutingNAT,
						Interface:  "amwgnat",
						SourceCIDR: "10.70.0.0/16",
					},
				},
			}

			err := server.reconcileWireGuardRouting(prepared)
			if err == nil {
				t.Fatal("expected simulated apply failure")
			}

			if !failed || failureIndex < 0 {
				t.Fatalf(
					"failure point was never reached; calls:\n%s",
					strings.Join(calls, "\n"),
				)
			}

			afterFailure := strings.Join(
				calls[failureIndex+1:],
				"\n",
			)

			requiredRollbackAttempts := []string{
				"iptables -w 5 -t mangle -F ANTIMAGE_WG_TPROXY",
				"ip route del local 0.0.0.0/0 dev lo table 202",
				"ip rule del priority 10020",
				"iptables -w 5 -t nat -F ANTIMAGE_WG_NAT",
				"iptables -w 5 -F ANTIMAGE_WG_FORWARD",
			}

			for _, required := range requiredRollbackAttempts {
				if !strings.Contains(afterFailure, required) {
					t.Fatalf(
						"rollback command %q missing after failure:\n%s",
						required,
						afterFailure,
					)
				}
			}
		})
	}
}

func TestReconcileWireGuardRoutingNoSpecsDoesNotRequireIptables(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	oldGOOS := wireGuardRoutingGOOS
	oldLookPath := wireGuardRoutingLookPath
	oldRun := wireGuardRoutingRun

	wireGuardRoutingGOOS = "linux"
	wireGuardRoutingLookPath = func(name string) (string, error) {
		if name == "iptables" {
			return "", errors.New("not found")
		}
		return name, nil
	}
	wireGuardRoutingRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		t.Fatalf(
			"routing command unexpectedly executed without routing specs: %s %s",
			name,
			strings.Join(args, " "),
		)
		return nil, nil
	}

	t.Cleanup(func() {
		wireGuardRoutingGOOS = oldGOOS
		wireGuardRoutingLookPath = oldLookPath
		wireGuardRoutingRun = oldRun
	})

	if err := server.reconcileWireGuardRouting(nil); err != nil {
		t.Fatalf(
			"no routing specs should not require iptables: %v",
			err,
		)
	}
}

func TestReconcileWireGuardRoutingNoSpecsCleanupFailureIsNonFatal(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	oldGOOS := wireGuardRoutingGOOS
	oldLookPath := wireGuardRoutingLookPath
	oldRun := wireGuardRoutingRun

	wireGuardRoutingGOOS = "linux"

	wireGuardRoutingLookPath = func(name string) (string, error) {
		switch name {
		case "iptables":
			return "iptables", nil
		case "ip":
			return "", errors.New("not found")
		default:
			return name, nil
		}
	}

	sawCleanup := false

	wireGuardRoutingRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")

		if name == "iptables" {
			sawCleanup = true
		}

		if name == "iptables" && strings.Contains(call, " -C ") {
			return nil, errors.New("missing")
		}

		return nil, nil
	}

	t.Cleanup(func() {
		wireGuardRoutingGOOS = oldGOOS
		wireGuardRoutingLookPath = oldLookPath
		wireGuardRoutingRun = oldRun
	})

	if err := server.reconcileWireGuardRouting(nil); err != nil {
		t.Fatalf(
			"no-routing stale cleanup failure must be non-fatal: %v",
			err,
		)
	}

	if !sawCleanup {
		t.Fatal("expected best-effort stale routing cleanup attempt")
	}
}

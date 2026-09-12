package nodeagent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWireGuardPolicyEnforcementWithoutSessionCallback(
	t *testing.T,
) {
	now := time.Unix(2_000_000_000, 0)

	tests := []struct {
		name             string
		policy           nativeSessionUserPolicy
		accounting       bool
		baseline         uint64
		received         uint64
		sent             uint64
		wantRemove       bool
		wantCarry        uint64
		wantNextBaseline uint64
	}{
		{
			name: "disabled",
			policy: nativeSessionUserPolicy{
				Status: "disabled",
			},
			accounting: false,
			wantRemove: true,
		},
		{
			name: "expired",
			policy: nativeSessionUserPolicy{
				Status: "active",
				Expire: now.Unix(),
			},
			accounting: false,
			wantRemove: true,
		},
		{
			name: "static quota",
			policy: nativeSessionUserPolicy{
				Status:      "active",
				UsedTraffic: 1000,
				DataLimit:   1000,
			},
			accounting: false,
			wantRemove: true,
		},
		{
			name: "on hold",
			policy: nativeSessionUserPolicy{
				Status:      "on_hold",
				UsedTraffic: 5000,
				DataLimit:   1000,
				Expire:      now.Add(-time.Hour).Unix(),
			},
			accounting: false,
			wantRemove: false,
		},
		{
			name: "below live quota",
			policy: nativeSessionUserPolicy{
				Status:      "active",
				UsedTraffic: 900,
				DataLimit:   1000,
			},
			accounting: true,
			baseline:   100,
			received:   100,
			sent:       99,
			wantRemove: false,
		},
		{
			name: "at live quota",
			policy: nativeSessionUserPolicy{
				Status:      "active",
				UsedTraffic: 900,
				DataLimit:   1000,
			},
			accounting:       true,
			baseline:         100,
			received:         100,
			sent:             100,
			wantRemove:       true,
			wantCarry:        100,
			wantNextBaseline: 200,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := New(Config{DataDir: t.TempDir()})
			server.wireGuardUsageLoaded = true

			key := wireGuardUsageBaselineKey(
				"wg-main",
				"wg-test0",
				"peer-a",
			)

			if test.accounting {
				server.wireGuardUsageBaseline[key] =
					test.baseline
			}

			accountingEnabled := test.accounting

			cfg := wireGuardUsageRuntimeConfig{
				InboundTag:        "wg-main",
				InterfaceName:     "wg-test0",
				AccountingEnabled: &accountingEnabled,
				Peers: map[string]int64{
					"peer-a": 42,
				},
				Policies: map[string]nativeSessionUserPolicy{
					"peer-a": test.policy,
				},
			}

			oldLookPath := wireGuardRuntimeLookPath
			oldRun := wireGuardRuntimeRun

			wireGuardRuntimeLookPath = func(
				name string,
			) (string, error) {
				return name, nil
			}

			var calls []string

			wireGuardRuntimeRun = func(
				_ context.Context,
				name string,
				args ...string,
			) ([]byte, error) {
				calls = append(
					calls,
					name+" "+strings.Join(args, " "),
				)
				return nil, nil
			}

			t.Cleanup(func() {
				wireGuardRuntimeLookPath = oldLookPath
				wireGuardRuntimeRun = oldRun
			})

			err := server.reconcileWireGuardSessions(
				context.Background(),
				cfg,
				"wg-test0",
				[]wireGuardPeerCounters{{
					PublicKey:       "peer-a",
					Endpoint:        "198.51.100.4:20000",
					LatestHandshake: now.Unix(),
					ReceivedBytes:   test.received,
					SentBytes:       test.sent,
				}},
				now,
			)
			if err != nil {
				t.Fatal(err)
			}

			if test.wantRemove {
				if len(calls) != 1 {
					t.Fatalf(
						"runtime calls = %#v, want one remove",
						calls,
					)
				}

				want := "wg set wg-test0 peer peer-a remove"
				if calls[0] != want {
					t.Fatalf(
						"runtime call = %q, want %q",
						calls[0],
						want,
					)
				}

				suppressionKey := wireGuardDynamicSuppressionKey(
					"wg-main",
					"peer-a",
				)
				if _, ok := server.wireGuardDynamicSuppressedPeers[suppressionKey]; !ok {
					t.Fatal("policy disconnect did not suppress peer from unchanged runtime apply")
				}
			} else if len(calls) != 0 {
				t.Fatalf(
					"unexpected runtime calls: %#v",
					calls,
				)
			}

			if test.wantCarry > 0 {
				carry, ok := server.wireGuardUsageCarry[key]
				if !ok {
					t.Fatal("accounting carry missing")
				}

				if carry.Value != test.wantCarry {
					t.Fatalf(
						"carry = %d, want %d",
						carry.Value,
						test.wantCarry,
					)
				}

				if carry.NextBaseline != test.wantNextBaseline {
					t.Fatalf(
						"carry baseline = %d, want %d",
						carry.NextBaseline,
						test.wantNextBaseline,
					)
				}
			}
		})
	}
}

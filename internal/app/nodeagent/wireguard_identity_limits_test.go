package nodeagent

import (
	"context"
	"testing"
	"time"
)

func TestWireGuardDeviceCredentialsAndIPsAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		name         string
		devices, ips int64
		sharedIP     bool
		kept         int
	}{
		{"one-one", 1, 1, true, 1}, {"two-one-shared", 2, 1, true, 2}, {"one-two", 1, 2, false, 1}, {"two-one-distinct", 2, 1, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(Config{DataDir: t.TempDir()})
			if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
				t.Fatal(err)
			}
			oldLook, oldRun := wireGuardRuntimeLookPath, wireGuardRuntimeRun
			wireGuardRuntimeLookPath = func(name string) (string, error) { return name, nil }
			removals := 0
			wireGuardRuntimeRun = func(context.Context, string, ...string) ([]byte, error) { removals++; return nil, nil }
			t.Cleanup(func() { wireGuardRuntimeLookPath, wireGuardRuntimeRun = oldLook, oldRun })
			policy := nativeSessionUserPolicy{Status: "active", DeviceLimit: tc.devices, IPLimit: tc.ips}
			cfg := wireGuardUsageRuntimeConfig{InboundTag: "wg", Peers: map[string]int64{"a": 10, "b": 10}, Policies: map[string]nativeSessionUserPolicy{"a": policy, "b": policy}}
			secondIP := "198.51.100.2:20000"
			if tc.sharedIP {
				secondIP = "198.51.100.1:30000"
			}
			now := time.Now()
			peers := []wireGuardPeerCounters{{PublicKey: "a", Endpoint: "198.51.100.1:10000", LatestHandshake: now.Unix()}, {PublicKey: "b", Endpoint: secondIP, LatestHandshake: now.Unix()}}
			remaining := s.enforceWireGuardPoliciesLocked(cfg, "wg0", peers, now)
			if len(remaining) != tc.kept || removals != 2-tc.kept {
				t.Fatalf("kept=%d removals=%d", len(remaining), removals)
			}
		})
	}
}

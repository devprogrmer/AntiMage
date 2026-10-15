package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestLocalQuotaInterval(t *testing.T) {
	mockWireGuardGenerationIdentity(t)
	for input, expected := range map[string]time.Duration{"": 100 * time.Millisecond, "50ms": 50 * time.Millisecond, "2s": 2 * time.Second, "0": 0, "off": 0, "disabled": 0, "false": 0, " OFF ": 0} {
		actual, err := localQuotaInterval(input)
		if err != nil || actual != expected {
			t.Fatalf("interval %q = %v, %v", input, actual, err)
		}
	}
	for _, input := range []string{"49ms", "2001ms", "garbage", "-1s"} {
		if _, err := localQuotaInterval(input); err == nil {
			t.Fatalf("invalid interval accepted: %s", input)
		}
	}
}

func TestQuotaWorkerPolicyWakeRunsImmediately(t *testing.T) {
	s := New(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	ticks := make(chan struct{}, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runLocalAccountingWorkerWithWakeAdaptive(ctx, "xray", time.Hour, 0, wake, nil,
			func(context.Context, bool, bool) error {
				ticks <- struct{}{}
				return nil
			})
	}()
	select {
	case <-ticks: // initial policy/runtime evaluation
	case <-time.After(time.Second):
		t.Fatal("initial quota evaluation did not run")
	}
	wake <- struct{}{}
	select {
	case <-ticks:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("policy wake waited for the one-hour quota interval")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("quota worker did not stop after cancellation")
	}
}

func TestNativeQuotaWorkerSleepsWithoutEnforceablePoliciesAndWakesOnChange(t *testing.T) {
	s := New(Config{})
	policyJSON, err := json.Marshal(nativeRuntimePayload{OpenVPNInbounds: []openVPNRuntimeInbound{{Users: []openVPNRuntimeUser{{Status: "active"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	s.runtimePolicy = &runtimePolicySnapshot{Revision: 1, Checksum: "unlimited", NativeJSON: string(policyJSON)}
	minimum := 40 * time.Millisecond
	adaptive := func() time.Duration { return s.nativeQuotaAdaptiveInterval("openvpn", minimum) }
	if got := adaptive(); got != 0 {
		t.Fatalf("unlimited user schedule=%s, want disabled periodic policy polling", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	ticks := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runLocalAccountingWorkerWithWakeAdaptive(ctx, "openvpn", minimum, 0, wake, adaptive,
			func(context.Context, bool, bool) error { ticks <- struct{}{}; return nil })
	}()
	select {
	case <-ticks:
		t.Fatal("worker ran a quota check with no enforceable user")
	case <-time.After(120 * time.Millisecond):
	}

	dataLimit := int64(1000)
	changedJSON, err := json.Marshal(nativeRuntimePayload{OpenVPNInbounds: []openVPNRuntimeInbound{{Users: []openVPNRuntimeUser{{Status: "active", DataLimit: &dataLimit}}}}})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.runtimePolicy = &runtimePolicySnapshot{Revision: 2, Checksum: "limited", NativeJSON: string(changedJSON)}
	s.mu.Unlock()
	wake <- struct{}{}
	select {
	case <-ticks:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("new enforceable policy did not wake immediately")
	}
	select {
	case <-ticks:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not restore periodic checks for the newly enforceable policy")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("quota worker did not stop after cancellation")
	}
}

func TestNativeQuotaPolicyScheduleCachesByRuntimeChecksumAndFailsSafe(t *testing.T) {
	s := New(Config{})
	minimum := 50 * time.Millisecond
	unlimited, err := json.Marshal(nativeRuntimePayload{WireGuardInbounds: []wireGuardRuntimeInbound{{Peers: []wireGuardRuntimePeer{{Status: "active"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	s.runtimePolicy = &runtimePolicySnapshot{Revision: 1, Checksum: "wg-unlimited", NativeJSON: string(unlimited)}
	if got := s.nativeQuotaAdaptiveInterval("wireguard", minimum); got != 0 {
		t.Fatalf("unlimited WireGuard schedule=%s", got)
	}

	deviceLimited, err := json.Marshal(nativeRuntimePayload{WireGuardInbounds: []wireGuardRuntimeInbound{{Peers: []wireGuardRuntimePeer{{Status: "active", DeviceLimit: 1}}}}})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.runtimePolicy = &runtimePolicySnapshot{Revision: 2, Checksum: "wg-device-limit", NativeJSON: string(deviceLimited)}
	s.mu.Unlock()
	if got := s.nativeQuotaAdaptiveInterval("wireguard", minimum); got != minimum {
		t.Fatalf("device-limited WireGuard schedule=%s, want configured minimum %s", got, minimum)
	}

	s.mu.Lock()
	s.runtimePolicy = &runtimePolicySnapshot{Revision: 3, Checksum: "malformed", NativeJSON: "{"}
	s.mu.Unlock()
	if got := s.nativeQuotaAdaptiveInterval("wireguard", minimum); got != minimum {
		t.Fatalf("malformed policy schedule=%s, want fail-safe configured minimum %s", got, minimum)
	}
}

func TestNativeQuotaPolicyIndexRecognizesAllWorkersAndLeavesUnlimitedUsersIdle(t *testing.T) {
	limit := int64(100)
	unlimited := nativeRuntimePayload{
		OpenVPNInbounds:    []openVPNRuntimeInbound{{Users: []openVPNRuntimeUser{{Status: "active"}}}},
		L2TPInbounds:       []l2TPRuntimeInbound{{Users: []l2TPRuntimeUser{{Status: "active"}}}},
		PPTPInbounds:       []pptpRuntimeInbound{{Users: []pptpRuntimeUser{{Status: "active"}}}},
		AmneziaWGInbounds:  []amneziaWGRuntimeInbound{{Peers: []amneziaWGRuntimePeer{{Status: "active"}}}},
		WireGuardInbounds:  []wireGuardRuntimeInbound{{Peers: []wireGuardRuntimePeer{{Status: "active"}}}},
		IKEv2Inbounds:      []ikev2RuntimeInbound{{Users: []ikev2RuntimeUser{{Status: "active"}}}},
		AnyConnectInbounds: []anyConnectRuntimeInbound{{Users: []anyConnectRuntimeUser{{Status: "active"}}}},
	}
	for _, protocol := range []string{"openvpn", "ppp", "amneziawg", "wireguard", "ikev2", "anyconnect"} {
		if nativeQuotaPayloadHasEnforceableUser(unlimited, protocol) {
			t.Errorf("unlimited %s user triggered periodic quota polling", protocol)
		}
	}
	limited := nativeRuntimePayload{
		OpenVPNInbounds:    []openVPNRuntimeInbound{{Users: []openVPNRuntimeUser{{Status: "active", DataLimit: &limit}}}},
		L2TPInbounds:       []l2TPRuntimeInbound{{Users: []l2TPRuntimeUser{{Status: "disabled"}}}},
		PPTPInbounds:       []pptpRuntimeInbound{{Users: []pptpRuntimeUser{{Status: "active", Expire: &limit}}}},
		AmneziaWGInbounds:  []amneziaWGRuntimeInbound{{Peers: []amneziaWGRuntimePeer{{Status: "active", IPLimit: 1}}}},
		WireGuardInbounds:  []wireGuardRuntimeInbound{{Peers: []wireGuardRuntimePeer{{Status: "active", DeviceLimit: 1}}}},
		IKEv2Inbounds:      []ikev2RuntimeInbound{{Users: []ikev2RuntimeUser{{Status: "active", DataLimit: &limit}}}},
		AnyConnectInbounds: []anyConnectRuntimeInbound{{Users: []anyConnectRuntimeUser{{Status: "active", Expire: &limit}}}},
	}
	for _, protocol := range []string{"openvpn", "ppp", "amneziawg", "wireguard", "ikev2", "anyconnect"} {
		if !nativeQuotaPayloadHasEnforceableUser(limited, protocol) {
			t.Errorf("enforceable %s user was omitted from periodic quota checks", protocol)
		}
	}
}

func TestQuotaWakeSignalsOnlyItsProtocolWorker(t *testing.T) {
	s := New(Config{})
	xray := s.localQuotaWakeChannel("xray")
	openVPN := s.localQuotaWakeChannel("openvpn")
	s.wakeLocalQuota("xray")
	select {
	case <-xray:
	default:
		t.Fatal("Xray worker did not receive its wake")
	}
	select {
	case <-openVPN:
		t.Fatal("Xray policy change woke unrelated OpenVPN worker")
	default:
	}
}

func TestRuntimePolicyRemovalStillWakesFormerProtocol(t *testing.T) {
	s := New(Config{})
	xray := s.localQuotaWakeChannel("xray")
	s.wakeLocalQuotaForRuntime(`{"xray_policies":[{"email":"alice","status":"active","data_limit":1}]}`, `{}`)
	select {
	case <-xray:
	default:
		t.Fatal("removing the last Xray policy did not wake its former quota worker")
	}
}

func TestXrayDisablesSeparateQuotaPollingWithoutEnforceablePolicies(t *testing.T) {
	s := New(Config{})
	s.xrayQuotaPolicyIndex = &xrayQuotaPolicyIndex{EnforcedPositions: nil}
	if got := s.xrayQuotaAdaptiveInterval(); got != 0 {
		t.Fatalf("quota interval without enforceable policies=%s, want disabled", got)
	}
	if got := quotaDeadlineAfter(time.Now(), nextQuotaDelay(100*time.Millisecond, s.xrayQuotaAdaptiveInterval, nil)); !got.IsZero() {
		t.Fatalf("quota deadline without enforceable policies=%s, want zero", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	ticks := make(chan struct{}, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runLocalAccountingWorkerWithWakeAdaptive(ctx, "xray", 50*time.Millisecond, 0, wake, s.xrayQuotaAdaptiveInterval,
			func(context.Context, bool, bool) error {
				ticks <- struct{}{}
				return nil
			})
	}()
	select {
	case <-ticks:
		cancel()
		t.Fatal("quota worker ran with no enforceable Xray policy")
	case <-time.After(150 * time.Millisecond):
	}
	wake <- struct{}{}
	select {
	case <-ticks:
	case <-time.After(100 * time.Millisecond):
		cancel()
		t.Fatal("event wake did not run while periodic quota polling was disabled")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("quota worker did not stop after cancellation")
	}
}

func TestAccountingDegradationDoesNotFailNodeHealth(t *testing.T) {
	s := New(Config{})
	s.localAccountingFailures = map[string]string{"wireguard": "counter read failed"}

	response, err := s.Health(context.Background(), &nodev1.HealthRequest{})
	if err != nil {
		t.Fatalf("node health failed with recoverable accounting degradation: %v", err)
	}
	if response.GetRuntime().GetMessage() != "healthy; accounting degraded" {
		t.Fatalf("health message=%q", response.GetRuntime().GetMessage())
	}
	for _, capability := range response.GetRuntime().GetCapabilities() {
		if capability == "accounting_degraded" {
			return
		}
	}
	t.Fatal("health response omitted accounting_degraded capability")
}

func TestLocalWorkerSeparatesAccountingAndPolicyHealthAndKeepsEnforcementRunning(t *testing.T) {
	s := New(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	checkpointDone := make(chan struct{}, 1)
	quotaDone := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runLocalAccountingWorker(ctx, "xray", time.Hour, time.Hour,
			func(context.Context) error {
				checkpointDone <- struct{}{}
				return fmt.Errorf("durable checkpoint unavailable")
			},
			func(context.Context) error {
				quotaDone <- struct{}{}
				return fmt.Errorf("quota enforcement unavailable")
			})
	}()
	select {
	case <-checkpointDone:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("initial accounting checkpoint did not run")
	}
	select {
	case <-quotaDone:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("policy enforcement was skipped after checkpoint failure")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	if got := s.localAccountingFailures["accounting:xray"]; got == "" {
		t.Fatalf("accounting degradation was not recorded: %v", s.localAccountingFailures)
	}
	if got := s.localPolicyFailures["policy:xray"]; got == "" {
		t.Fatalf("policy degradation was not recorded: %v", s.localPolicyFailures)
	}
	response, err := s.Health(context.Background(), &nodev1.HealthRequest{})
	if err != nil {
		t.Fatalf("health failed: %v", err)
	}
	capabilities := map[string]bool{}
	for _, capability := range response.GetRuntime().GetCapabilities() {
		capabilities[capability] = true
	}
	if !capabilities["accounting_degraded"] || !capabilities["policy_degraded"] {
		t.Fatalf("health capabilities=%v", capabilities)
	}
}

func offlineWireGuardFixture(t *testing.T, dir string, limit *int64) *Server {
	t.Helper()
	s := New(Config{DataDir: dir})
	if err := s.syncWireGuardUsageConfigs([]wireGuardRuntimeInbound{{
		Tag: "wg-offline", ListenPort: 51820,
		Settings: map[string]any{"accounting_enabled": true, "interface_name": "wg-offline0"},
		Peers:    []wireGuardRuntimePeer{{UserID: 10, PublicKey: "peer-offline", Status: "active", DataLimit: limit}},
	}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func mockOfflineWireGuardCounters(t *testing.T, counter *uint64) *int {
	t.Helper()
	previousAll, previousInterface := wireGuardDumpAll, wireGuardDumpInterface
	reads := new(int)
	peer := func() string {
		return fmt.Sprintf("peer-offline\t(none)\t198.51.100.2:1000\t10.69.0.2/32\t0\t%d\t0\t0\n", *counter)
	}
	wireGuardDumpAll = func(context.Context) ([]byte, error) {
		*reads++
		return []byte("wg-offline0\tprivate\tpublic\t51820\toff\n" + "wg-offline0\t" + peer()), nil
	}
	wireGuardDumpInterface = func(context.Context, string) ([]byte, error) {
		return []byte("private\tpublic\t51820\toff\n" + peer()), nil
	}
	t.Cleanup(func() {
		wireGuardDumpAll, wireGuardDumpInterface = previousAll, previousInterface
	})
	return reads
}

func TestWireGuardOfflineCheckpointsContinueWithPendingAndNodeRestart(t *testing.T) {
	mockWireGuardGenerationIdentity(t)
	ctx := context.Background()
	const mb = uint64(1024 * 1024)
	dir := t.TempDir()
	s := offlineWireGuardFixture(t, dir, nil)
	counter := 500 * mb
	mockOfflineWireGuardCounters(t, &counter)
	if err := s.wireGuardOfflineTick(ctx, false, true); err != nil {
		t.Fatal(err)
	}
	first, err := s.CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
	if err != nil || first.GetStats()[0].GetValue() != 500*mb {
		t.Fatalf("first pending = %v, %v", first, err)
	}
	counter = 600 * mb
	if err := s.wireGuardOfflineTick(ctx, false, true); err != nil {
		t.Fatal(err)
	}
	counter = 100 * mb
	if err := s.wireGuardOfflineTick(ctx, false, true); err != nil {
		t.Fatal(err)
	}
	s = New(Config{DataDir: dir})
	if err := s.wireGuardOfflineTick(ctx, false, true); err != nil {
		t.Fatal(err)
	}
	retry, err := s.CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
	if err != nil || retry.GetBatchId() != first.GetBatchId() || retry.GetStats()[0].GetValue() != 500*mb {
		t.Fatalf("offline restart changed pending = %v, %v", retry, err)
	}
	ack, err := s.AckUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: retry.GetBatchId()})
	if err != nil || !ack.GetAcknowledged() {
		t.Fatalf("ACK = %v, %v", ack, err)
	}
	remainder, err := s.CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
	if err != nil || remainder.GetStats()[0].GetValue() != 200*mb {
		t.Fatalf("durable offline remainder = %v, %v", remainder, err)
	}
}

func TestWireGuardOfflineQuota50MBCutsOffUsingPersistedPolicy(t *testing.T) {
	mockWireGuardGenerationIdentity(t)
	ctx := context.Background()
	const mb = uint64(1024 * 1024)
	limit := int64(50 * mb)
	dir := t.TempDir()
	_ = offlineWireGuardFixture(t, dir, &limit)
	// A fresh agent has no runtime/process map and no panel connection.
	s := New(Config{DataDir: dir})
	var counter uint64
	reads := mockOfflineWireGuardCounters(t, &counter)
	previousLookPath, previousRun := wireGuardRuntimeLookPath, wireGuardRuntimeRun
	removed := false
	wireGuardRuntimeLookPath = func(name string) (string, error) { return name, nil }
	wireGuardRuntimeRun = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "set wg-offline0 peer peer-offline remove" {
			removed = true
		}
		return nil, nil
	}
	t.Cleanup(func() { wireGuardRuntimeLookPath, wireGuardRuntimeRun = previousLookPath, previousRun })
	for i := 0; i < 20 && !removed; i++ {
		counter += 7 * mb
		if err := s.wireGuardOfflineTick(ctx, true, i == 0); err != nil {
			t.Fatal(err)
		}
	}
	if !removed || counter != 56*mb || *reads != 8 {
		t.Fatalf("quota fixture cutoff=%dMB removed=%v aggregate reads=%d", counter/mb, removed, *reads)
	}
	carry := s.wireGuardUsageCarry[wireGuardUsageBaselineKey("wg-offline", "wg-offline0", "peer-offline")]
	if carry.Value != counter {
		t.Fatalf("quota removal lost final traffic: %+v", carry)
	}
	t.Log("synthetic 7MB/tick workload: 50MB quota, 56MB cutoff, 6MB overshoot; no live timing claim")
}

func TestWireGuardNormalQuotaTicksDoNotWriteAccountingState(t *testing.T) {
	mockWireGuardGenerationIdentity(t)
	s := offlineWireGuardFixture(t, t.TempDir(), nil)
	counter := uint64(100)
	mockOfflineWireGuardCounters(t, &counter)
	if err := s.wireGuardOfflineTick(context.Background(), false, true); err != nil {
		t.Fatal(err)
	}
	original := wireGuardAccountingWrite
	writes := 0
	wireGuardAccountingWrite = func(path string, raw []byte) error { writes++; return original(path, raw) }
	t.Cleanup(func() { wireGuardAccountingWrite = original })
	for i := 0; i < 10; i++ {
		counter += 100
		if err := s.wireGuardOfflineTick(context.Background(), true, false); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 0 {
		t.Fatalf("normal quota ticks wrote disk %d times", writes)
	}
	if err := s.wireGuardOfflineTick(context.Background(), false, true); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("periodic checkpoint writes=%d", writes)
	}
}

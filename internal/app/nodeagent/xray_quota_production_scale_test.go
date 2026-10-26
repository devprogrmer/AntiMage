package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/platform/usagereflection"
)

// xrayQuotaProductionFixture reproduces the shape observed on AntiMage-s3:
// 176 users on each of two Xray inbounds (vless + shadowsocks), every enforced
// user carrying a real reflected-usage watermark. It is a scale fixture for
// profiling the local quota/checkpoint tick; it is not native acceptance.
func xrayQuotaProductionFixture(tb testing.TB, users int, inbounds []string) (*Server, []xrayStat, *xrayQuotaPolicyIndex) {
	return xrayQuotaProductionFixtureSized(tb, users, inbounds, 1)
}

// receiptOwners pads each user's reflected watermark with extra synthetic
// owners. A production receipt carries only the owners of that one user (its
// own protocol/inbound memberships, typically a handful), so this is a width
// scaling probe rather than a production shape: it answers "how does per-tick
// receipt decoding scale if a user's owner set grows", not "what is the
// deployed receipt width".
func xrayQuotaProductionFixtureSized(tb testing.TB, users int, inbounds []string, receiptOwners int) (*Server, []xrayStat, *xrayQuotaPolicyIndex) {
	tb.Helper()
	s := New(Config{DataDir: tb.TempDir()})
	s.xrayUsageLoaded = true
	s.xrayUsageBaseline = map[string]uint64{}
	s.localUsageLoaded = true
	s.localUsageV2 = &localUsageLedgerV2{Epoch: "prod", Latest: map[string]localUsageReceiptV2{}}

	policies := make([]xrayLocalUserPolicy, 0, users*len(inbounds))
	stats := make([]xrayStat, 0, users*len(inbounds)*2)
	for user := 1; user <= users; user++ {
		totals := map[string]uint64{}
		for _, tag := range inbounds {
			totals[usagereflection.Key("xray", tag)] = uint64(user * 1024)
			s.localUsageV2.Owners = append(s.localUsageV2.Owners, localUsageOwnerV2{
				Protocol: "xray", UserID: int64(user), InboundTag: tag, Total: uint64(user*1024 + 4096),
			})
		}
		for owner := 0; owner < receiptOwners; owner++ {
			totals[usagereflection.Key("xray", fmt.Sprintf("owner-%04d-inbound", owner))] = uint64(owner*8 + user)
		}
		raw, err := json.Marshal(usagereflection.Watermark{Version: 2, Epoch: "prod", Root: "combined-prod", Totals: totals})
		if err != nil {
			tb.Fatal(err)
		}
		for _, tag := range inbounds {
			email := fmt.Sprintf("%d.%s", user, tag)
			policies = append(policies, xrayLocalUserPolicy{
				UserID: int64(user), Email: email, InboundTag: tag, Protocol: "xray",
				nativeSessionUserPolicy: nativeSessionUserPolicy{
					Status: "active", DataLimit: 1 << 40,
					ReflectedUsageBatchID: "combined-prod", ReflectedUsageLedger: string(raw),
				},
			})
			for _, direction := range []string{"uplink", "downlink"} {
				stats = append(stats, xrayStat{
					Name:  "user>>>" + email + ">>>traffic>>>" + direction,
					Value: int64(user * 4096),
				})
			}
		}
	}
	payload, err := json.Marshal(nativeRuntimePayload{XrayPolicies: policies})
	if err != nil {
		tb.Fatal(err)
	}
	snapshot := &runtimePolicySnapshot{Revision: 10696, Checksum: "prod-checksum", NativeJSON: string(payload)}
	s.runtimePolicy = snapshot
	s.lastRuntime = &exec.Cmd{}
	s.xrayRuntimeGeneration = "prod-gen"
	index, err := s.xrayQuotaPolicyIndexFor(snapshot)
	if err != nil {
		tb.Fatal(err)
	}
	return s, stats, index
}

func TestXrayQuotaProductionScaleEvaluatesEveryEnforcedPolicy(t *testing.T) {
	s, stats, index := xrayQuotaProductionFixture(t, 176, []string{"vless-in", "ss-in"})
	if len(index.EnforcedPositions) != 352 {
		t.Fatalf("expected 352 enforced policies, got %d", len(index.EnforcedPositions))
	}
	if err := s.quotaCheckXrayOfflineWithStats(context.Background(), s.runtimePolicy, "prod-gen", index, stats); err != nil {
		t.Fatalf("quota check: %v", err)
	}
	if len(s.xrayQuotaSamples) != 352 {
		t.Fatalf("expected a quota sample per enforced policy, got %d", len(s.xrayQuotaSamples))
	}
}

// BenchmarkXrayQuotaProductionTick measures one enforce tick at the measured
// AntiMage-s3 scale. The stats sample grows monotonically so the adaptive
// interval, sampling and enforcement loops all take their steady-state path.
func BenchmarkXrayQuotaProductionTick(b *testing.B) {
	benchmarkXrayQuotaProductionTick(b, 176, 1)
}

// The controller embeds one ledger per user containing every reflected owner.
// A large deployment therefore decodes a receipt whose Totals map is far wider
// than the two inbounds the node enforces.
func BenchmarkXrayQuotaProductionTickWideReceipt(b *testing.B) {
	benchmarkXrayQuotaProductionTick(b, 176, 64)
}

func benchmarkXrayQuotaProductionTick(b *testing.B, users, receiptOwners int) {
	s, stats, index := xrayQuotaProductionFixtureSized(b, users, []string{"vless-in", "ss-in"}, receiptOwners)
	if err := s.quotaCheckXrayOfflineWithStats(context.Background(), s.runtimePolicy, "prod-gen", index, stats); err != nil {
		b.Fatal(err)
	}
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		delta := int64(i+1) * 512
		for j := range stats {
			stats[j].Value += delta
		}
		if err := s.quotaCheckXrayOfflineWithStats(context.Background(), s.runtimePolicy, "prod-gen", index, stats); err != nil {
			b.Fatal(err)
		}
	}
	_ = now
}

// BenchmarkXrayAccountingCheckpointProduction measures the durable checkpoint
// half of the same tick (publish + persist decision) at production scale.
func BenchmarkXrayAccountingCheckpointProduction(b *testing.B) {
	s, stats, _ := xrayQuotaProductionFixture(b, 176, []string{"vless-in", "ss-in"})
	if err := s.checkpointXrayAccountingSample(stats, "prod-gen"); err != nil {
		b.Fatal(err)
	}
	s.xrayUsageMu.Lock()
	locked := true
	unlock := func() {
		if locked {
			s.xrayUsageMu.Unlock()
			locked = false
		}
	}
	defer unlock()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		delta := int64(i+1) * 512
		for j := range stats {
			stats[j].Value += delta
		}
		if err := s.checkpointXrayGenerationLocked(stats, "prod-gen"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkXrayQuotaReceiptWidthScaling isolates how one quota tick scales with
// the number of cumulative owners in a decoded reflection receipt. Production
// receipts hold only their own user's owners, so this bounds the cost of a
// future wide-owner user; it is not evidence that deployment-wide receipts
// exist, and the 650/1300 rows must not be read as the deployed shape.
func BenchmarkXrayQuotaReceiptWidthScaling(b *testing.B) {
	for _, owners := range []int{1, 64, 256, 650, 1300} {
		b.Run(fmt.Sprintf("receipt-owners-%d", owners), func(b *testing.B) {
			benchmarkXrayQuotaProductionTick(b, 176, owners)
		})
	}
}

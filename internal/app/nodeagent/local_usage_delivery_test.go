package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"path/filepath"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestLocalDeliveryReflectionLostACKRestartAndCompaction(t *testing.T) {
	s := &Server{cfg: Config{DataDir: t.TempDir()}}
	deliveries := []localUsageDelivery{
		{BatchID: "combined-old", Rows: []localUsageDeliveryRow{{Protocol: "xray", ChildID: "xray-old", UserID: 10, InboundTag: "a", Raw: 100}, {Protocol: "wireguard", ChildID: "wireguard-old", UserID: 11, InboundTag: "b", Raw: 200}}},
		{BatchID: "combined-new", Rows: []localUsageDeliveryRow{{Protocol: "xray", ChildID: "xray-new", UserID: 10, InboundTag: "a", Raw: 50}}},
	}
	if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
		t.Fatal(err)
	}
	// Pending already contributes raw usage; its prepared receipt must not add it again.
	if got, err := s.localAwaitingReflectionUsage("xray", 10, "a", "xray-new", ""); err != nil || got != 100 {
		t.Fatalf("got=%d err=%v", got, err)
	}
	reflected, err := s.localPendingUsageReflected("xray", 10, "xray-new", "combined-new")
	if err != nil || !reflected {
		t.Fatalf("lost ACK reflection=%v err=%v", reflected, err)
	}
	if got, err := s.localAwaitingReflectionUsage("xray", 10, "a", "xray-new", "combined-new"); err != nil || got != 0 {
		t.Fatalf("got=%d err=%v", got, err)
	}
	restarted := &Server{cfg: s.cfg}
	reflected, err = restarted.localPendingUsageReflected("xray", 10, "xray-new", "combined-new")
	if err != nil || !reflected {
		t.Fatal("restart lost reflected pending identity")
	}
	if got, err := restarted.localAwaitingReflectionUsage("wireguard", 11, "b", "", ""); err != nil || got != 200 {
		t.Fatalf("another user incorrectly pruned: %d %v", got, err)
	}
	if _, err := restarted.localAwaitingReflectionUsage("xray", 10, "a", "", "combined-new"); err != nil {
		t.Fatal(err)
	}
	if len(restarted.localUsageDeliveries) != 2 {
		t.Fatal("quota read removed a delivery before the committed runtime could reclaim it")
	}
	if reflected, err := restarted.localPendingUsageReflected("xray", 10, "xray-new", "combined-new"); !reflected || err != nil {
		t.Fatalf("quota read removed the marker needed for lost ACK: %v, %v", reflected, err)
	}
}

func TestLocalDeliveryCorruptionPreserved(t *testing.T) {
	s := &Server{cfg: Config{DataDir: t.TempDir()}}
	path := filepath.Join(s.cfg.DataDir, "accounting", "deliveries.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.localAwaitingReflectionUsage("xray", 10, "a", "", ""); err == nil {
		t.Fatal("corruption silently ignored")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "corrupt" {
		t.Fatal("corrupt evidence overwritten")
	}
}

func TestFullLocalDeliveryRecoversFromPersistedReflection(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	s := &Server{cfg: cfg}
	deliveries := make([]localUsageDelivery, 0, maxLocalUsageDeliveries)
	for i := 0; i < maxLocalUsageDeliveries-2; i++ {
		id := fmt.Sprintf("xray-old-%d", i)
		deliveries = append(deliveries, localUsageDelivery{BatchID: id, Rows: []localUsageDeliveryRow{
			{Protocol: "xray", ChildID: id, UserID: 10, InboundTag: "a", Raw: 100},
		}})
	}
	// This batch contains unconfirmed usage, so the whole batch must survive.
	deliveries = append(deliveries, localUsageDelivery{BatchID: "xray-unconfirmed", Rows: []localUsageDeliveryRow{
		{Protocol: "xray", ChildID: "xray-unconfirmed", UserID: 11, InboundTag: "b", Raw: 200},
	}})
	deliveries = append(deliveries, localUsageDelivery{BatchID: "xray-marker", Rows: []localUsageDeliveryRow{
		{Protocol: "xray", ChildID: "xray-marker", UserID: 10, InboundTag: "a", Raw: 50},
	}})
	if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
		t.Fatal(err)
	}
	s.xrayUsagePending = &xrayUsagePendingBatch{
		BatchID: "xray-pending",
		Samples: []xrayUsageSample{{UserID: 10, InboundTag: "a", Value: 75}},
	}
	if err := s.persistXrayUsageStateLocked(); err != nil {
		t.Fatal(err)
	}

	s = &Server{cfg: cfg}
	if err := s.recordLocalUsageDelivery("xray-pending"); err == nil {
		t.Fatal("accepted another delivery without proof of reflection")
	}
	if len(s.localUsageDeliveries) != maxLocalUsageDeliveries {
		t.Fatal("changed the ledger without proof of reflection")
	}
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{
		ConfigJson:    "{}",
		OvRuntimeJson: `{"xray_policies":[{"user_id":10,"email":"10.a","used_traffic":0},{"user_id":11,"email":"11.b","used_traffic":0}],"wg_inbounds":[{"tag":"wg","peers":[{"user_id":11,"used_traffic":0}]}]}`,
	}); err != nil {
		t.Fatal(err)
	}

	s = &Server{cfg: cfg}
	if err := s.applyAccountingReflections([]*nodev1.AccountingReflection{
		{UserId: 11, BatchId: "xray-marker", UsedTraffic: 200},
		{UserId: 10, BatchId: "xray-marker", UsedTraffic: 409450},
	}); err != nil {
		t.Fatal(err)
	}
	s = &Server{cfg: cfg}
	snapshot, err := s.currentRuntimePolicy()
	if err != nil || snapshot == nil {
		t.Fatalf("read refreshed policy after restart: %v", err)
	}
	payload, err := snapshot.nativePayload()
	if err != nil || len(payload.XrayPolicies) != 2 ||
		payload.XrayPolicies[0].UsedTraffic != 409450 ||
		payload.XrayPolicies[0].ReflectedUsageBatchID != "xray-marker" ||
		payload.XrayPolicies[1].UsedTraffic != 0 || payload.XrayPolicies[1].ReflectedUsageBatchID != "" {
		t.Fatalf("usage and its reflection were not persisted together: %v", err)
	}
	if err := s.recordLocalUsageDelivery("xray-pending"); err != nil {
		t.Fatal(err)
	}
	s = &Server{cfg: cfg}
	if err := s.recordLocalUsageDelivery("xray-pending"); err != nil {
		t.Fatal(err)
	}
	got := s.localUsageDeliveries
	if len(got) != 3 || got[0].BatchID != "xray-unconfirmed" ||
		got[1].BatchID != "xray-marker" || got[2].BatchID != "xray-pending" {
		t.Fatalf("incorrect ledger after restart and retry: %+v", got)
	}
	if reflected, err := s.localPendingUsageReflected("xray", 10, "xray-marker", "xray-marker"); !reflected || err != nil {
		t.Fatalf("lost the retained ACK identity: reflected=%v err=%v", reflected, err)
	}
	if usage, err := s.localAwaitingReflectionUsage("xray", 11, "b", "", ""); usage != 200 || err != nil {
		t.Fatalf("unconfirmed usage=%d err=%v", usage, err)
	}
}

func TestAccountingRefreshAdvancesSharedXrayWireGuardPolicyAfterACK(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	s := &Server{cfg: cfg}
	const root = "combined-root"
	const latest = "xray-latest"
	const future = "wireguard-future"
	if err := s.persistLocalUsageDeliveriesLocked([]localUsageDelivery{
		{BatchID: root, Rows: []localUsageDeliveryRow{
			{Protocol: "xray", ChildID: "xray-child", UserID: 10, InboundTag: "x", Raw: 50},
			{Protocol: "wireguard", ChildID: "wireguard-child", UserID: 10, InboundTag: "wg", Raw: 100},
		}},
		{BatchID: latest, Rows: []localUsageDeliveryRow{
			{Protocol: "xray", ChildID: latest, UserID: 10, InboundTag: "x", Raw: 10},
		}},
		{BatchID: future, Rows: []localUsageDeliveryRow{
			{Protocol: "wireguard", ChildID: future, UserID: 10, InboundTag: "wg", Raw: 25},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{
		ConfigJson:    "{}",
		OvRuntimeJson: `{"xray_policies":[{"user_id":10,"email":"10.x"}],"wg_inbounds":[{"tag":"wg","peers":[{"user_id":10,"public_key":"peer"}]}]}`,
	}); err != nil {
		t.Fatal(err)
	}
	s.wireGuardUsagePending = &wireGuardUsagePendingBatch{
		BatchID: "wireguard-child",
		Samples: []wireGuardUsageSample{{UserID: 10, InboundTag: "wg", Value: 100}},
	}
	if err := s.persistWireGuardUsageStateLocked(); err != nil {
		t.Fatal(err)
	}
	proofs := []*nodev1.AccountingReflection{{UserId: 10, BatchId: latest, UsedTraffic: 160}}
	if err := s.applyAccountingReflections(proofs); err != nil {
		t.Fatal(err)
	}
	if s.runtimePolicy.native.XrayPolicies[0].UsedTraffic != 0 {
		t.Fatal("advanced a shared policy before WireGuard ACK was durable")
	}
	s.wireGuardUsagePending = &wireGuardUsagePendingBatch{
		BatchID: future, Samples: []wireGuardUsageSample{{UserID: 10, InboundTag: "wg", Value: 25}},
	}
	s.wireGuardUsageAwaitingReflection = []wireGuardUsageAwaitingReflectionBatch{
		{BatchID: root, Samples: []wireGuardUsageSample{{UserID: 10, InboundTag: "wg", Value: 100}}},
	}
	if err := s.persistWireGuardUsageStateLocked(); err != nil {
		t.Fatal(err)
	}
	s = &Server{cfg: cfg}
	if err := s.applyAccountingReflections(proofs); err != nil {
		t.Fatal(err)
	}
	s = &Server{cfg: cfg}
	snapshot, err := s.currentRuntimePolicy()
	if err != nil || snapshot == nil {
		t.Fatalf("load shared policy: %v", err)
	}
	if snapshot.ConfigJSON != "{}" || snapshot.native.XrayPolicies[0].UsedTraffic != 160 {
		t.Fatal("changed runtime configuration or lost the Xray accounting total")
	}
	old := wireGuardUsageRuntimeConfig{InboundTag: "wg", Peers: map[string]int64{"peer": 10},
		Policies: map[string]nativeSessionUserPolicy{"peer": {Status: "active", DataLimit: 200}}}
	refreshed := s.withRefreshedWireGuardAccounting(old)
	got := refreshed.Policies["peer"]
	if got.UsedTraffic != 160 || got.ReflectedUsageBatchID != latest || got.wireGuardReflectionThrough != root ||
		got.DataLimit != 200 || got.Status != "active" || old.Policies["peer"].UsedTraffic != 0 {
		t.Fatalf("incorrect helper accounting overlay: %+v", got)
	}
	if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	remaining, err := s.wireGuardLiveUnackedUsageLocked(refreshed, "wg0", nil, 10)
	if remaining != 25 || err != nil {
		t.Fatalf("WireGuard mixed confirmed bytes with the newer pending batch: %d, %v", remaining, err)
	}
}

func TestAccountingRefreshRecoversFullLedgerForAllProtocols(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	s := New(cfg)
	protocols := []string{"xray", "wireguard", "amneziawg", "openvpn", "l2tp", "pptp", "ikev2", "anyconnect"}
	rows := make([]localUsageDeliveryRow, 0, len(protocols))
	for _, protocol := range protocols {
		rows = append(rows, localUsageDeliveryRow{Protocol: protocol, UserID: 7, InboundTag: protocol, ChildID: protocol + "-old", Raw: 1})
	}
	deliveries := make([]localUsageDelivery, maxLocalUsageDeliveries)
	for i := range deliveries {
		deliveries[i] = localUsageDelivery{BatchID: fmt.Sprintf("combined-%d", i), Rows: rows}
	}
	marker := deliveries[len(deliveries)-1].BatchID
	if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
		t.Fatal(err)
	}
	request := &nodev1.RuntimeConfigRequest{
		ConfigJson: "{}", DesiredRevision: 42,
		OvRuntimeJson: `{
			"xray_policies":[{"user_id":7,"used_traffic":10}],
			"wg_inbounds":[{"tag":"wireguard","peers":[{"user_id":7,"public_key":"wg-key","used_traffic":10}]}],
			"awg_inbounds":[{"tag":"amneziawg","peers":[{"user_id":7,"used_traffic":10}]}],
			"inbounds":[{"tag":"openvpn","users":[{"user_id":7,"used_traffic":10}]}],
			"l2tp_inbounds":[{"tag":"l2tp","users":[{"user_id":7,"used_traffic":10}]}],
			"pptp_inbounds":[{"tag":"pptp","users":[{"user_id":7,"used_traffic":10}]}],
			"ikev2_inbounds":[{"tag":"ikev2","users":[{"user_id":7,"used_traffic":10}]}],
			"anyconnect_inbounds":[{"tag":"anyconnect","users":[{"user_id":7,"used_traffic":10}]}],
			"unknown_extension":{"preserve":true}
		}`,
	}
	if err := s.persistRuntimePolicy(request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CollectUserUsage(context.Background(), &nodev1.CollectUsageRequest{CanRefreshAccounting: true}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("missing recovery handshake before sampling: %v", err)
	}
	proof := &nodev1.AccountingReflection{UserId: 7, BatchId: marker, UsedTraffic: 40000}
	if err := s.applyAccountingReflections([]*nodev1.AccountingReflection{proof}); err != nil {
		t.Fatal(err)
	}
	restarted := New(cfg)
	snapshot, err := restarted.currentRuntimePolicy()
	if err != nil || snapshot == nil || snapshot.Revision != 42 || snapshot.ConfigJSON != "{}" {
		t.Fatalf("runtime changed or could not be reloaded: %v, %v", snapshot, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(snapshot.NativeJSON), &raw); err != nil || raw["unknown_extension"] == nil {
		t.Fatalf("runtime extension was lost: %v", err)
	}
	groups, err := nativeAccountingViews(raw)
	if err != nil || len(groups) != len(protocols) {
		t.Fatalf("runtime accounting views: %d, %v", len(groups), err)
	}
	for _, group := range groups {
		entry := group.entries[0]
		if entry.UsedTraffic != 40000 || entry.ReflectedUsageBatchID != marker {
			t.Fatalf("%s did not receive the complete receipt: %+v", group.field, entry)
		}
		if protocol := group.nativeProtocol(); protocol != "" {
			policy, err := restarted.currentNativeAccountingPolicy(protocol, 7, protocol, nativeSessionUserPolicy{DataLimit: 50000, UsedTraffic: 10})
			if err != nil || policy.UsedTraffic != 40000 || policy.ReflectedUsageBatchID != marker || policy.DataLimit != 50000 {
				t.Fatalf("%s lost helper policy or used stale accounting: %+v, %v", protocol, policy, err)
			}
		}
	}
	if err := restarted.ensureLocalUsageDeliveriesLocked(); err != nil || len(restarted.localUsageDeliveries) != 1 {
		t.Fatalf("confirmed ledger did not reclaim space: %d, %v", len(restarted.localUsageDeliveries), err)
	}
	for _, protocol := range protocols {
		matched, err := restarted.localPendingUsageReflected(protocol, 7, protocol+"-old", marker)
		if err != nil || !matched {
			t.Fatalf("%s lost the pending ACK identity: %v, %v", protocol, matched, err)
		}
	}
}

func TestAccountingRefreshRecoversMissingLegacyAnchorAndRetiredOwner(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	s := New(cfg)
	deliveries := make([]localUsageDelivery, 3893)
	for i := range deliveries {
		deliveries[i] = localUsageDelivery{
			BatchID: fmt.Sprintf("combined-legacy-%d", i),
			Rows: []localUsageDeliveryRow{
				{Protocol: "xray", UserID: 10, InboundTag: "x", ChildID: fmt.Sprintf("xray-a-%d", i), Raw: 10},
				{Protocol: "l2tp", UserID: 11, InboundTag: "retired", ChildID: fmt.Sprintf("l2tp-b-%d", i), Raw: 20},
			},
		}
	}
	deliveries[0].Rows = append(deliveries[0].Rows, localUsageDeliveryRow{
		Protocol: "xray", UserID: 12, InboundTag: "unconfirmed", ChildID: "xray-unconfirmed", Raw: 99,
	})
	if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
		t.Fatal(err)
	}
	latest := deliveries[len(deliveries)-1].BatchID
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{
		ConfigJson: "{}", DesiredRevision: 954,
		OvRuntimeJson: `{"xray_policies":[{"user_id":10,"used_traffic":100,"reflected_usage_batch_id":"xray-removed-by-legacy-pruning"}]}`,
	}); err != nil {
		t.Fatal(err)
	}
	proofs := []*nodev1.AccountingReflection{
		{UserId: 10, BatchId: latest, UsedTraffic: 50000},
		{UserId: 11, BatchId: latest, UsedTraffic: 80000},
		{UserId: 999, BatchId: latest, UsedTraffic: 100000},
	}
	if err := s.applyAccountingReflections(proofs); err != nil {
		t.Fatal(err)
	}
	restarted := New(cfg)
	snapshot, err := restarted.currentRuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := snapshot.nativePayload()
	if err != nil {
		t.Fatal(err)
	}
	if payload.XrayPolicies[0].UsedTraffic != 50000 || payload.XrayPolicies[0].ReflectedUsageBatchID != latest {
		t.Fatal("missing legacy anchor permanently blocked a newer committed receipt")
	}
	if len(payload.XrayPolicies) != 1 || payload.NativeAccountingRefreshed[11] == nil ||
		len(payload.NativeAccountingRefreshed[11].Inbounds) != 0 || payload.NativeAccountingRefreshed[999] != nil {
		t.Fatal("retired owner receipt lost or foreign owner accepted")
	}
	if snapshot.Revision != 954 || snapshot.ConfigJSON != "{}" {
		t.Fatal("accounting recovery changed the runtime")
	}
	if err := restarted.ensureLocalUsageDeliveriesLocked(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.localUsageDeliveries) != 2 {
		t.Fatalf("legacy backlog retained %d batches, want only latest receipt and unconfirmed batch", len(restarted.localUsageDeliveries))
	}
	unconfirmed, err := restarted.localAwaitingReflectionUsage("xray", 12, "unconfirmed", "", "")
	if err != nil || unconfirmed != 99 {
		t.Fatalf("unconfirmed bytes changed: %d, %v", unconfirmed, err)
	}
	// A repeated confirmed receipt must still repair a ledger after the policy
	// persisted successfully but a crash interrupted its subsequent rewrite.
	if err := restarted.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
		t.Fatal(err)
	}
	restarted = New(cfg)
	if err := restarted.applyAccountingReflections(proofs); err != nil {
		t.Fatal(err)
	}
	if len(restarted.localUsageDeliveries) != 2 {
		t.Fatalf("equal receipt did not recover interrupted compaction: %d", len(restarted.localUsageDeliveries))
	}
}

func TestAccountingRefreshRejectsRegressingAndForeignProofs(t *testing.T) {
	for _, proof := range []*nodev1.AccountingReflection{
		{UserId: 10, BatchId: "combined-latest", UsedTraffic: 99},
		{UserId: 10, BatchId: "combined-unknown", UsedTraffic: 200},
		{UserId: 11, BatchId: "combined-latest", UsedTraffic: 200},
	} {
		t.Run(fmt.Sprintf("%d-%s", proof.GetUserId(), proof.GetBatchId()), func(t *testing.T) {
			s := New(Config{DataDir: t.TempDir()})
			if err := s.persistLocalUsageDeliveriesLocked([]localUsageDelivery{
				{BatchID: "combined-latest", Rows: []localUsageDeliveryRow{{Protocol: "xray", UserID: 10, InboundTag: "x", ChildID: "xray-latest", Raw: 10}}},
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{
				ConfigJson: "{}", OvRuntimeJson: `{"xray_policies":[{"user_id":10,"used_traffic":100,"reflected_usage_batch_id":"legacy-missing"}]}`,
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.applyAccountingReflections([]*nodev1.AccountingReflection{proof}); err != nil {
				t.Fatal(err)
			}
			if s.runtimePolicy.native.XrayPolicies[0].UsedTraffic != 100 {
				t.Fatal("invalid proof changed quota")
			}
		})
	}
}

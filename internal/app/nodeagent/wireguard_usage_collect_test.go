package nodeagent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestWireGuardUsageCollectRetryAckAndCounterReset(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	ctx := context.Background()

	if err := server.syncWireGuardUsageConfigs(
		[]wireGuardRuntimeInbound{{
			Tag:        "wg-main",
			ListenPort: 51820,
			Settings: map[string]any{
				"accounting_enabled": true,
				"interface_name":     "wg-test0",
			},
			Peers: []wireGuardRuntimePeer{{
				UserID:    42,
				PublicKey: "peer-a",
				Status:    "active",
			}},
		}},
	); err != nil {
		t.Fatal(err)
	}

	dump := "priv\tserver-pub\t51820\toff\n" +
		"peer-a\t(none)\t198.51.100.4:20000\t10.69.0.2/32\t1700000000\t200\t100\t25\n"

	previousDump := wireGuardDumpInterface
	wireGuardDumpInterface = func(
		context.Context,
		string,
	) ([]byte, error) {
		return []byte(dump), nil
	}
	t.Cleanup(func() {
		wireGuardDumpInterface = previousDump
	})

	first, err := server.collectWireGuardUserUsage(
		ctx,
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.GetBatchId(), "wireguard-") {
		t.Fatalf("batch id = %q", first.GetBatchId())
	}
	if len(first.GetStats()) != 1 {
		t.Fatalf("stats = %d, want 1", len(first.GetStats()))
	}
	if first.GetStats()[0].GetUid() != "wireguard:42" {
		t.Fatalf("uid = %q", first.GetStats()[0].GetUid())
	}
	if first.GetStats()[0].GetValue() != 300 {
		t.Fatalf("value = %d, want 300", first.GetStats()[0].GetValue())
	}
	if first.GetStats()[0].GetInboundTag() != "wg-main" {
		t.Fatalf("tag = %q", first.GetStats()[0].GetInboundTag())
	}

	dump = "priv\tserver-pub\t51820\toff\n" +
		"peer-a\t(none)\t198.51.100.4:20000\t10.69.0.2/32\t1700000001\t350\t150\t25\n"

	retry, err := server.collectWireGuardUserUsage(
		ctx,
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if retry.GetBatchId() != first.GetBatchId() {
		t.Fatalf(
			"retry batch id changed: %q != %q",
			retry.GetBatchId(),
			first.GetBatchId(),
		)
	}
	if retry.GetStats()[0].GetValue() != 300 {
		t.Fatalf(
			"retry value = %d, want 300",
			retry.GetStats()[0].GetValue(),
		)
	}

	ack, err := server.ackWireGuardUserUsage(
		ctx,
		&nodev1.AckUsageRequest{BatchId: first.GetBatchId()},
	)
	if err != nil || !ack.GetAcknowledged() {
		t.Fatalf("first ACK failed: %v", err)
	}

	second, err := server.collectWireGuardUserUsage(
		ctx,
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.GetStats()) != 1 ||
		second.GetStats()[0].GetValue() != 200 {
		t.Fatalf("second delta = %#v, want 200", second.GetStats())
	}

	ack2, err := server.ackWireGuardUserUsage(
		ctx,
		&nodev1.AckUsageRequest{BatchId: second.GetBatchId()},
	)
	if err != nil || !ack2.GetAcknowledged() {
		t.Fatalf("second ACK failed: %v", err)
	}

	dump = "priv\tserver-pub\t51820\toff\n" +
		"peer-a\t(none)\t198.51.100.4:20000\t10.69.0.2/32\t1700000002\t30\t20\t25\n"

	resetBatch, err := server.collectWireGuardUserUsage(
		ctx,
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resetBatch.GetStats()) != 1 ||
		resetBatch.GetStats()[0].GetValue() != 50 {
		t.Fatalf("reset delta = %#v, want 50", resetBatch.GetStats())
	}
}

func TestWireGuardUsageACKPersistsAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	ctx := context.Background()

	server1 := New(Config{DataDir: dataDir})
	server1.wireGuardUsagePending = &wireGuardUsagePendingBatch{
		BatchID: "wireguard-persist",
		NextBaseline: map[string]uint64{
			"wg-main\x00wg-test0\x00peer-a": 900,
		},
	}
	server1.wireGuardUsageLoaded = true
	if err := server1.persistWireGuardUsageStateLocked(); err != nil {
		t.Fatal(err)
	}

	resp, err := server1.ackWireGuardUserUsage(
		ctx,
		&nodev1.AckUsageRequest{BatchId: "wireguard-persist"},
	)
	if err != nil || !resp.GetAcknowledged() {
		t.Fatalf("ACK failed: %v", err)
	}

	server2 := New(Config{DataDir: dataDir})
	if err := server2.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	if server2.wireGuardUsageLastAckedBatchID != "wireguard-persist" {
		t.Fatalf(
			"last acked = %q",
			server2.wireGuardUsageLastAckedBatchID,
		)
	}

	resp2, err := server2.ackWireGuardUserUsage(
		ctx,
		&nodev1.AckUsageRequest{BatchId: "wireguard-persist"},
	)
	if err != nil || !resp2.GetAcknowledged() {
		t.Fatalf(
			"idempotent ACK after restart failed: %v",
			err,
		)
	}
}

func TestWireGuardUsageResolvesInterfaceByListenPort(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	ctx := context.Background()

	if err := server.syncWireGuardUsageConfigs(
		[]wireGuardRuntimeInbound{{
			Tag:        "wg-main",
			ListenPort: 51820,
			Settings: map[string]any{
				"accounting_enabled": true,
			},
			Peers: []wireGuardRuntimePeer{{
				UserID:    42,
				PublicKey: "peer-a",
				Status:    "active",
			}},
		}},
	); err != nil {
		t.Fatal(err)
	}

	previousAll := wireGuardDumpAll
	wireGuardDumpAll = func(context.Context) ([]byte, error) {
		return []byte(
			"wg-real0\tpriv\tserver-pub\t51820\toff\n" +
				"wg-real0\tpeer-a\t(none)\t198.51.100.4:20000\t10.69.0.2/32\t1700000000\t120\t80\t25\n",
		), nil
	}
	t.Cleanup(func() {
		wireGuardDumpAll = previousAll
	})

	batch, err := server.collectWireGuardUserUsage(
		ctx,
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.GetStats()) != 1 {
		t.Fatalf("stats = %d, want 1", len(batch.GetStats()))
	}
	if batch.GetStats()[0].GetUid() != "wireguard:42" {
		t.Fatalf("uid = %q", batch.GetStats()[0].GetUid())
	}
	if batch.GetStats()[0].GetValue() != 200 {
		t.Fatalf("value = %d, want 200", batch.GetStats()[0].GetValue())
	}

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-real0",
		"peer-a",
	)
	if server.wireGuardUsagePending.NextBaseline[key] != 200 {
		t.Fatalf(
			"resolved baseline = %d, want 200",
			server.wireGuardUsagePending.NextBaseline[key],
		)
	}
}

func TestWireGuardUsageCollectionFailurePreservesBaseline(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	ctx := context.Background()

	if err := server.syncWireGuardUsageConfigs(
		[]wireGuardRuntimeInbound{{
			Tag:        "wg-main",
			ListenPort: 51820,
			Settings: map[string]any{
				"accounting_enabled": true,
				"interface_name":     "wg-test0",
			},
			Peers: []wireGuardRuntimePeer{{
				UserID:    42,
				PublicKey: "peer-a",
				Status:    "active",
			}},
		}},
	); err != nil {
		t.Fatal(err)
	}

	baselineKey := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)
	server.wireGuardUsageLoaded = true
	server.wireGuardUsageBaseline = map[string]uint64{
		baselineKey: 300,
	}

	previousDump := wireGuardDumpInterface
	failQuery := true
	wireGuardDumpInterface = func(
		context.Context,
		string,
	) ([]byte, error) {
		if failQuery {
			return nil, errors.New("temporary wg query failure")
		}
		return []byte(
			"priv\tserver-pub\t51820\toff\n" +
				"peer-a\t(none)\t198.51.100.4:20000\t10.69.0.2/32\t1700000001\t300\t200\t25\n",
		), nil
	}
	t.Cleanup(func() {
		wireGuardDumpInterface = previousDump
	})

	failedPass, err := server.collectWireGuardUserUsage(
		ctx,
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(failedPass.GetStats()) != 0 {
		t.Fatalf("failed scrape stats = %#v, want none", failedPass.GetStats())
	}
	if got := server.wireGuardUsageBaseline[baselineKey]; got != 300 {
		t.Fatalf("baseline after failed scrape = %d, want 300", got)
	}

	failQuery = false
	next, err := server.collectWireGuardUserUsage(
		ctx,
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.GetStats()) != 1 {
		t.Fatalf("stats = %d, want 1", len(next.GetStats()))
	}
	if got := next.GetStats()[0].GetValue(); got != 200 {
		t.Fatalf("delta after recovery = %d, want 200", got)
	}
}

func TestWireGuardUsageReportsHandshakeOnlineWithoutTraffic(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	ctx := context.Background()

	if err := server.syncWireGuardUsageConfigs(
		[]wireGuardRuntimeInbound{{
			Tag:        "wg-main",
			ListenPort: 51820,
			Settings: map[string]any{
				"accounting_enabled": true,
				"interface_name":     "wg-test0",
			},
			Peers: []wireGuardRuntimePeer{{
				UserID:    42,
				PublicKey: "peer-a",
				Status:    "active",
			}},
		}},
	); err != nil {
		t.Fatal(err)
	}

	baselineKey := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)
	server.wireGuardUsageLoaded = true
	server.wireGuardUsageBaseline = map[string]uint64{
		baselineKey: 300,
	}

	dump := "priv\tserver-pub\t51820\toff\n" +
		"peer-a\t(none)\t198.51.100.4:20000\t10.69.0.2/32\t" +
		strconv.FormatInt(time.Now().UTC().Unix(), 10) +
		"\t200\t100\t25\n"

	previousDump := wireGuardDumpInterface
	wireGuardDumpInterface = func(
		context.Context,
		string,
	) ([]byte, error) {
		return []byte(dump), nil
	}
	t.Cleanup(func() {
		wireGuardDumpInterface = previousDump
	})

	batch, err := server.collectWireGuardUserUsage(
		ctx,
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if batch.GetBatchId() != "" {
		t.Fatalf("batch id = %q, want empty for online-only presence", batch.GetBatchId())
	}
	if len(batch.GetStats()) != 1 {
		t.Fatalf("stats = %d, want 1", len(batch.GetStats()))
	}
	if batch.GetStats()[0].GetUid() != "online:wireguard:42" {
		t.Fatalf("uid = %q", batch.GetStats()[0].GetUid())
	}
	if batch.GetStats()[0].GetValue() != 0 {
		t.Fatalf("value = %d, want 0", batch.GetStats()[0].GetValue())
	}
}

func TestWireGuardHandshakeActiveRejectsStaleAndFutureValues(t *testing.T) {
	now := time.Now().UTC()

	if !wireGuardHandshakeActive(now.Unix(), now) {
		t.Fatal("current handshake should be active")
	}
	if wireGuardHandshakeActive(
		now.Add(-2*time.Minute).Unix(),
		now,
	) {
		t.Fatal("stale handshake should not be active")
	}
	if wireGuardHandshakeActive(
		now.Add(time.Minute).Unix(),
		now,
	) {
		t.Fatal("far-future handshake should not be active")
	}
}

func TestWireGuardAccountingDisabledStillReportsOnlineWithoutUsage(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	ctx := context.Background()

	if err := server.syncWireGuardUsageConfigs(
		[]wireGuardRuntimeInbound{{
			Tag:        "wg-main",
			ListenPort: 51820,
			Settings: map[string]any{
				"accounting_enabled": false,
				"interface_name":     "wg-test0",
			},
			Peers: []wireGuardRuntimePeer{{
				UserID:    42,
				PublicKey: "peer-a",
				Status:    "active",
				Address:   "10.69.0.42",
			}},
		}},
	); err != nil {
		t.Fatal(err)
	}

	previousDump := wireGuardDumpInterface
	wireGuardDumpInterface = func(
		context.Context,
		string,
	) ([]byte, error) {
		return []byte(
			"priv\tserver-pub\t51820\toff\n" +
				"peer-a\t(none)\t198.51.100.4:20000\t10.69.0.42/32\t" +
				strconv.FormatInt(time.Now().UTC().Unix(), 10) +
				"\t9000\t7000\t25\n",
		), nil
	}
	t.Cleanup(func() {
		wireGuardDumpInterface = previousDump
	})

	batch, err := server.collectWireGuardUserUsage(
		ctx,
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(batch.GetStats()) != 1 {
		t.Fatalf("stats = %#v, want one online-only sample", batch.GetStats())
	}
	if got := batch.GetStats()[0].GetUid(); got != "online:wireguard:42" {
		t.Fatalf("uid = %q, want online:wireguard:42", got)
	}
	if got := batch.GetStats()[0].GetValue(); got != 0 {
		t.Fatalf("online sample value = %d, want 0", got)
	}
	if server.wireGuardUsagePending != nil {
		t.Fatal("accounting-disabled online presence must not create a pending accounting batch")
	}

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)
	if _, exists := server.wireGuardUsageBaseline[key]; exists {
		t.Fatal("accounting-disabled peer must not retain a billing baseline")
	}
}

func TestWireGuardPendingBatchRefreshesHandshakePresence(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	ctx := context.Background()

	if err := server.syncWireGuardUsageConfigs(
		[]wireGuardRuntimeInbound{{
			Tag:        "wg-main",
			ListenPort: 51820,
			Settings: map[string]any{
				"accounting_enabled": true,
				"interface_name":     "wg-test0",
			},
			Peers: []wireGuardRuntimePeer{{
				UserID:    42,
				PublicKey: "peer-a",
				Status:    "active",
				Address:   "10.69.0.42",
			}},
		}},
	); err != nil {
		t.Fatal(err)
	}

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)
	server.wireGuardUsageLoaded = true
	server.wireGuardUsageBaseline = map[string]uint64{
		key: 100,
	}

	now := time.Now().UTC()
	dump := "priv\tserver-pub\t51820\toff\n" +
		"peer-a\t(none)\t198.51.100.4:20000\t10.69.0.42/32\t" +
		strconv.FormatInt(now.Unix(), 10) +
		"\t100\t50\t25\n"

	calls := 0
	previousDump := wireGuardDumpInterface
	wireGuardDumpInterface = func(
		context.Context,
		string,
	) ([]byte, error) {
		calls++
		return []byte(dump), nil
	}
	t.Cleanup(func() {
		wireGuardDumpInterface = previousDump
	})

	first, err := server.collectWireGuardUserUsage(
		ctx,
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.GetBatchId() == "" {
		t.Fatal("first usage batch id is empty")
	}

	firstOnline := false
	for _, sample := range first.GetStats() {
		if sample.GetUid() == "online:wireguard:42" {
			firstOnline = true
		}
	}
	if !firstOnline {
		t.Fatal("first batch must report fresh WireGuard online presence")
	}

	// Keep the accounting batch unacknowledged, but make the handshake stale.
	dump = "priv\tserver-pub\t51820\toff\n" +
		"peer-a\t(none)\t198.51.100.4:20000\t10.69.0.42/32\t" +
		strconv.FormatInt(now.Add(-2*time.Minute).Unix(), 10) +
		"\t100\t50\t25\n"

	second, err := server.collectWireGuardUserUsage(
		ctx,
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if second.GetBatchId() != first.GetBatchId() {
		t.Fatalf(
			"pending accounting batch changed: first=%q second=%q",
			first.GetBatchId(),
			second.GetBatchId(),
		)
	}

	for _, sample := range second.GetStats() {
		if sample.GetUid() == "online:wireguard:42" {
			t.Fatal("stale handshake was replayed as online from pending accounting state")
		}
	}

	if calls != 2 {
		t.Fatalf(
			"wireguard dump calls = %d, want 2 so pending accounting still refreshes runtime state",
			calls,
		)
	}
}
func TestWireGuardUsageCarryWithLivePeerDoesNotDoubleCount(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	ctx := context.Background()

	if err := server.syncWireGuardUsageConfigs(
		[]wireGuardRuntimeInbound{{
			Tag:        "wg-main",
			ListenPort: 51820,
			Settings: map[string]any{
				"accounting_enabled": true,
				"interface_name":     "wg-test0",
			},
			Peers: []wireGuardRuntimePeer{{
				UserID:    42,
				PublicKey: "peer-a",
				Status:    "active",
			}},
		}},
	); err != nil {
		t.Fatal(err)
	}

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server.wireGuardUsageLoaded = true
	server.wireGuardUsageBaseline = map[string]uint64{
		key: 100,
	}
	server.wireGuardUsageCarry[key] = wireGuardUsageCarry{
		UserID:       42,
		InboundTag:   "wg-main",
		Value:        20,
		NextBaseline: 120,
	}

	previousDump := wireGuardDumpInterface
	wireGuardDumpInterface = func(
		context.Context,
		string,
	) ([]byte, error) {
		return []byte(
			"priv\tserver-pub\t51820\toff\n" +
				"peer-a\t(none)\t198.51.100.4:20000\t10.69.0.42/32\t" +
				"1700000000\t100\t50\t25\n",
		), nil
	}
	t.Cleanup(func() {
		wireGuardDumpInterface = previousDump
	})

	batch, err := server.collectWireGuardUserUsage(
		ctx,
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if batch.GetBatchId() == "" {
		t.Fatal("accounting batch id is empty")
	}
	if len(batch.GetStats()) != 1 {
		t.Fatalf(
			"stats = %#v, want exactly one accounting sample",
			batch.GetStats(),
		)
	}
	if got := batch.GetStats()[0].GetValue(); got != 50 {
		t.Fatalf(
			"usage with carry = %d, want 50 (20 carry + 30 new)",
			got,
		)
	}

	if server.wireGuardUsagePending == nil {
		t.Fatal("pending batch missing")
	}
	if got := server.wireGuardUsagePending.CarryValues[key]; got != 20 {
		t.Fatalf("pending carry = %d, want 20", got)
	}
	if got := server.wireGuardUsagePending.NextBaseline[key]; got != 150 {
		t.Fatalf(
			"pending next baseline = %d, want 150",
			got,
		)
	}
	if got := server.wireGuardUsageCarry[key].Value; got != 20 {
		t.Fatalf(
			"carry mutated before ACK = %d, want 20",
			got,
		)
	}
}

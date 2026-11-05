package nodeagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
)

// Advance only the display sampling window, without sleeping or changing the
// accounting clock, persistent baselines, pending receipts or native counters.
func ageNativeRateWindows(s *Server) {
	s.nativeLiveMu.Lock()
	defer s.nativeLiveMu.Unlock()
	for _, scope := range s.nativeLiveRates {
		scope.at = time.Now().Add(-2 * time.Second)
		for id, baseline := range scope.sources {
			baseline.seen = scope.at
			scope.sources[id] = baseline
		}
	}
}

func assertNativeDirectionalRate(t *testing.T, s *Server, protocol string, userID int64) {
	t.Helper()
	rates := s.nativeTrafficSpeeds(time.Now())
	uid := fmt.Sprintf("%s:%d", protocol, userID)
	for _, rate := range rates {
		if rate.Uid != uid {
			continue
		}
		// Each integration fixture adds 1000 bytes received / 500 sent, over
		// just over two seconds (the small I/O time is deliberately included).
		if rate.Upload < 400 || rate.Upload > 500 || rate.Download < 200 || rate.Download > 250 {
			t.Fatalf("direction/rate: %v", rate)
		}
		return
	}
	t.Fatalf("missing native rate %s: %v", uid, rates)
}

func TestNativeLiveRatesOpenVPNCollectorRetriedBatch(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	root := filepath.Join(s.cfg.DataDir, "openvpn", "vpn")
	if err := offlineDurableJSON(filepath.Join(root, "usage-helper.json"), openVPNUsageRuntimeConfig{InboundTag: "vpn", Users: map[string]int64{"alice": 7}}); err != nil {
		t.Fatal(err)
	}
	writeStatus := func(up, down uint64) {
		t.Helper()
		raw := fmt.Sprintf("HEADER\tCLIENT_LIST\tUsername\tClient ID\tConnected Since (time_t)\tBytes Received\tBytes Sent\nCLIENT_LIST\talice\t7\t100\t%d\t%d\n", up, down)
		if err := os.WriteFile(filepath.Join(root, "status.tsv"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeStatus(100, 200)
	first, err := s.collectOpenVPNUserUsage(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	before := proto.Clone(first)
	ageNativeRateWindows(s)
	writeStatus(1100, 700)
	retry, err := s.collectOpenVPNUserUsage(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(before, retry) {
		t.Fatal("unacknowledged accounting batch changed")
	}
	assertNativeDirectionalRate(t, s, "openvpn", 7)
	restarted := New(Config{DataDir: s.cfg.DataDir})
	replay, err := restarted.collectOpenVPNUserUsage(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(before, replay) {
		t.Fatal("restart changed pending accounting batch")
	}
	for _, rate := range restarted.nativeTrafficSpeeds(time.Now()) {
		if rate.Upload != 0 || rate.Download != 0 {
			t.Fatalf("restart reused stale display sample: %v", rate)
		}
	}
}

func TestNativeLiveRatesPPPCollectors(t *testing.T) {
	oldQuery, oldCounter, oldIdentity := pppOfflineQuery, pppOfflineReadCounter, pppOfflineReadIdentity
	t.Cleanup(func() {
		pppOfflineQuery, pppOfflineReadCounter, pppOfflineReadIdentity = oldQuery, oldCounter, oldIdentity
	})
	pppOfflineQuery = func(context.Context) ([]byte, error) {
		return []byte("20: ppp0 inet 10.0.0.1 peer 10.0.0.2/32 scope global ppp0"), nil
	}
	pppOfflineReadIdentity = func(string) (string, error) { return "boot:1", nil }
	for _, protocol := range []string{"l2tp", "pptp"} {
		t.Run(protocol, func(t *testing.T) {
			s := New(Config{DataDir: t.TempDir()})
			root := filepath.Join(s.cfg.DataDir, protocol, "vpn")
			if err := offlineDurableJSON(filepath.Join(root, "usage-helper.json"), map[string]any{"inbound_tag": "vpn", "users": map[string]int64{"10.0.0.2": 7}}); err != nil {
				t.Fatal(err)
			}
			up, down := uint64(100), uint64(200)
			pppOfflineReadCounter = func(_ string, name string) (uint64, error) {
				if name == "rx_bytes" {
					return up, nil
				}
				return down, nil
			}
			collect := s.collectL2TPUserUsage
			if protocol == "pptp" {
				collect = s.collectPPTPUserUsage
			}
			first, err := collect(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			before := proto.Clone(first)
			ageNativeRateWindows(s)
			up += 1000
			down += 500
			retry, err := collect(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(before, retry) {
				t.Fatal("unacknowledged PPP batch changed")
			}
			assertNativeDirectionalRate(t, s, protocol, 7)
		})
	}
}

func TestNativeLiveRatesWireGuardCheckpoint(t *testing.T) {
	s, counter, _ := wireGuardGenerationFixture(t)
	if err := s.checkpointWireGuardOfflineGeneration(context.Background()); err != nil {
		t.Fatal(err)
	}
	ageNativeRateWindows(s)
	*counter += 1000
	if err := s.checkpointWireGuardOfflineGeneration(context.Background()); err != nil {
		t.Fatal(err)
	}
	rates := s.nativeTrafficSpeeds(time.Now())
	if len(rates) != 1 || rates[0].Uid != "wireguard:42" || rates[0].Upload < 400 || rates[0].Upload > 500 || rates[0].Download != 0 {
		t.Fatalf("WireGuard directional rate: %v", rates)
	}
}

func TestNativeLiveRatesAmneziaWGCheckpointDoesNotWriteForDisplay(t *testing.T) {
	oldAll, oldSnapshot, oldIdentity, oldWrite := amneziaWGSnapshotAll, amneziaWGSnapshot, amneziaWGInterfaceIdentity, amneziaWGOfflineWrite
	t.Cleanup(func() {
		amneziaWGSnapshotAll, amneziaWGSnapshot, amneziaWGInterfaceIdentity, amneziaWGOfflineWrite = oldAll, oldSnapshot, oldIdentity, oldWrite
	})
	amneziaWGSnapshotAll = nil
	amneziaWGInterfaceIdentity = func(string) (string, error) { return "boot:1", nil }
	key := awgTestKey('r')
	up, down := uint64(100), uint64(200)
	amneziaWGSnapshot = func(string) ([]wireGuardPeerCounters, error) {
		return []wireGuardPeerCounters{{PublicKey: key, ReceivedBytes: up, SentBytes: down}}, nil
	}
	s := New(Config{DataDir: t.TempDir()})
	writeAWGUsageConfig(t, s.cfg.DataDir, amneziaWGUsageRuntimeConfig{InboundTag: "vpn", InterfaceName: "awg0", Peers: map[string]int64{key: 7}, AccountingEnabled: true})
	writes := 0
	amneziaWGOfflineWrite = func(path string, raw []byte) error { writes++; return oldWrite(path, raw) }
	if err := s.checkpointAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	ageNativeRateWindows(s)
	up += 1000
	down += 500
	if err := s.checkpointAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertNativeDirectionalRate(t, s, "amneziawg", 7)
	before := writes
	ageNativeRateWindows(s)
	if err := s.checkpointAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writes != before {
		t.Fatal("display sample caused durable rewrite despite unchanged counters")
	}
}

func TestNativeLiveRatesIKEv2Checkpoint(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	oldBoot := offlineReadBootID
	offlineReadBootID = func() (string, error) { return "boot", nil }
	t.Cleanup(func() { offlineReadBootID = oldBoot })
	in := ikev2RuntimeInbound{Tag: "vpn", Users: []ikev2RuntimeUser{{UserID: 7, Username: "alice"}}}
	sa := ikev2RawSA{ConnectionName: ikev2ConnectionName("vpn"), UniqueID: "1", InitiatorSPI: "aa", ResponderSPI: "bb", RemoteID: "alice", State: "ESTABLISHED", Children: []ikev2RawChildSA{{State: "INSTALLED", UniqueID: "1", SPIIn: "11", SPIOut: "22", BytesIn: 100, BytesOut: 200}}}
	runtimes := map[string]ikev2RuntimeInbound{sa.ConnectionName: in}
	next, err := s.previewIKEv2OfflineLocked(runtimes, []ikev2RawSA{sa})
	if err != nil {
		t.Fatal(err)
	}
	s.ikev2UsageBaseline = next
	ageNativeRateWindows(s)
	sa.Children[0].BytesIn += 1000
	sa.Children[0].BytesOut += 500
	if _, err := s.previewIKEv2OfflineLocked(runtimes, []ikev2RawSA{sa}); err != nil {
		t.Fatal(err)
	}
	assertNativeDirectionalRate(t, s, "ikev2", 7)
}

func TestNativeLiveRatesDirectionsAndAggregation(t *testing.T) {
	s := &Server{}
	at := time.Now()
	s.observeNativeTraffic("wireguard", "a", at, []nativeTrafficCounter{{"peer", "boot1", 7, 100, 200}})
	s.observeNativeTraffic("wireguard", "a", at.Add(2*time.Second), []nativeTrafficCounter{{"peer", "boot1", 7, 500, 1000}, {"new-peer", "boot1", 7, 9000, 10000}})
	s.observeNativeTraffic("wireguard", "b", at, []nativeTrafficCounter{{"peer", "boot1", 7, 0, 0}})
	s.observeNativeTraffic("wireguard", "b", at.Add(2*time.Second), []nativeTrafficCounter{{"peer", "boot1", 7, 100, 200}})
	rates := s.nativeTrafficSpeeds(at.Add(2 * time.Second))
	if len(rates) != 1 || rates[0].Uid != "wireguard:7" || rates[0].Upload != 250 || rates[0].Download != 500 {
		t.Fatalf("directional bytes/sec across inbounds: %v", rates)
	}
}

func TestNativeLiveRatesResetReassignmentAndStaleness(t *testing.T) {
	for _, tc := range []struct {
		name, generation string
		uid              int64
		up, down         uint64
	}{
		{"generation", "new", 7, 9000, 10000},
		{"counter-reset", "old", 7, 1, 2},
		{"owner-change", "old", 8, 9000, 10000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			at := time.Now()
			s.observeNativeTraffic("openvpn", "a", at, []nativeTrafficCounter{{"session", "old", 7, 100, 200}})
			s.observeNativeTraffic("openvpn", "a", at.Add(time.Second), []nativeTrafficCounter{{"session", tc.generation, tc.uid, tc.up, tc.down}})
			rates := s.nativeTrafficSpeeds(at.Add(time.Second))
			if len(rates) != 1 || rates[0].Upload != 0 || rates[0].Download != 0 {
				t.Fatalf("reset produced a spike: %v", rates)
			}
			if rates := s.nativeTrafficSpeeds(at.Add(10 * time.Second)); len(rates) != 0 {
				t.Fatalf("stale rates: %v", rates)
			}
			if len(s.nativeLiveRates) != 0 {
				t.Fatal("stale scopes not pruned")
			}
		})
	}
}

func TestNativeLiveRatesEmptySnapshotAndShortPolling(t *testing.T) {
	s := &Server{}
	at := time.Now()
	s.observeNativeTraffic("l2tp", "a", at, []nativeTrafficCounter{{"ppp0", "one", 7, 0, 0}})
	s.observeNativeTraffic("l2tp", "a", at.Add(100*time.Millisecond), []nativeTrafficCounter{{"ppp0", "one", 7, 80, 40}})
	s.observeNativeTraffic("l2tp", "a", at.Add(time.Second), []nativeTrafficCounter{{"ppp0", "one", 7, 800, 400}})
	rates := s.nativeTrafficSpeeds(at.Add(time.Second))
	if len(rates) != 1 || rates[0].Upload != 800 || rates[0].Download != 400 {
		t.Fatalf("short tick changed sampling window: %v", rates)
	}
	s.observeNativeTraffic("l2tp", "a", at.Add(2*time.Second), nil)
	if rates := s.nativeTrafficSpeeds(at.Add(2 * time.Second)); len(rates) != 0 {
		t.Fatalf("disconnected sessions retained: %v", rates)
	}
	// Delayed observations must not rewind the baseline.
	s.observeNativeTraffic("l2tp", "a", at, []nativeTrafficCounter{{"ppp0", "one", 7, 90000, 90000}})
	if rates := s.nativeTrafficSpeeds(at.Add(2 * time.Second)); len(rates) != 0 {
		t.Fatalf("out-of-order snapshot accepted: %v", rates)
	}
}

func TestNativeLiveRatesLeaveAccountingBatchImmutable(t *testing.T) {
	s := &Server{}
	at := time.Now()
	batch := &nodev1.UserUsageBatch{BatchId: "retry-batch", Stats: []*nodev1.UserUsageSample{{Uid: "wireguard:7", Value: 400}}, Speeds: []*nodev1.UserTrafficSpeed{{Uid: "7.alice", Upload: 12}, {Uid: "wireguard:7", Upload: 999}}}
	before := proto.Clone(batch).(*nodev1.UserUsageBatch)
	s.observeNativeTraffic("wireguard", "a", at, []nativeTrafficCounter{{"p", "g", 7, 0, 0}})
	s.observeNativeTraffic("wireguard", "a", at.Add(time.Second), []nativeTrafficCounter{{"p", "g", 7, 200, 300}})
	result := s.withNativeTrafficSpeeds(batch, at.Add(time.Second))
	if !proto.Equal(batch, before) {
		t.Fatal("pending accounting batch mutated")
	}
	if result.BatchId != batch.BatchId || !proto.Equal(result.Stats[0], batch.Stats[0]) {
		t.Fatal("accounting identity/value changed")
	}
	if len(result.Speeds) != 2 || result.Speeds[0].Upload != 12 || result.Speeds[1].Upload != 200 {
		t.Fatalf("unexpected speeds: %v", result.Speeds)
	}
	// A lost ACK keeps the accounting batch, but must not replay old live rates.
	stale := s.withNativeTrafficSpeeds(batch, at.Add(10*time.Second))
	if len(stale.Speeds) != 1 || stale.Speeds[0].Uid != "7.alice" {
		t.Fatalf("replayed stale native speed: %v", stale.Speeds)
	}
}

func TestNativeLiveRatesConcurrent(t *testing.T) {
	s := &Server{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				now := time.Now()
				s.observeNativeTraffic("pptp", "a", now, []nativeTrafficCounter{{"p", "g", 7, uint64(j), uint64(j)}})
				s.nativeTrafficSpeeds(now)
			}
		}()
	}
	wg.Wait()
}

func TestNativeLiveRatesOverflowAndLongGap(t *testing.T) {
	if got := nativeBytesPerSecond(^uint64(0), time.Second); got != ^uint64(0) {
		t.Fatalf("overflow: %d", got)
	}
	if got := nativeRateAdd(^uint64(0)-1, 10); got != ^uint64(0) {
		t.Fatalf("sum wrapped: %d", got)
	}
	s := &Server{}
	at := time.Now()
	s.observeNativeTraffic("wireguard", "a", at, []nativeTrafficCounter{{"p", "g", 7, 1, 2}})
	s.observeNativeTraffic("wireguard", "a", at.Add(10*time.Second), []nativeTrafficCounter{{"p", "g", 7, 9000, 10000}})
	rates := s.nativeTrafficSpeeds(at.Add(10 * time.Second))
	if len(rates) != 1 || rates[0].Upload != 0 || rates[0].Download != 0 {
		t.Fatalf("old window reported as live: %v", rates)
	}
}

func BenchmarkNativeLiveRates1000Users(b *testing.B) {
	counters := make([]nativeTrafficCounter, 1000)
	for i := range counters {
		counters[i] = nativeTrafficCounter{fmt.Sprint(i), "boot", int64(i + 1), 100, 200}
	}
	b.Run("observe", func(b *testing.B) {
		s := &Server{}
		at := time.Now()
		s.observeNativeTraffic("wireguard", "a", at, counters)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			at = at.Add(time.Second)
			for j := range counters {
				counters[j].Upload += 1000
				counters[j].Download += 500
			}
			s.observeNativeTraffic("wireguard", "a", at, counters)
		}
	})
	b.Run("quota_tick_not_due", func(b *testing.B) {
		s := &Server{}
		at := time.Now()
		s.observeNativeTraffic("wireguard", "a", at, counters)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			s.nativeTrafficDue("wireguard", "a", at.Add(100*time.Millisecond))
		}
	})
}

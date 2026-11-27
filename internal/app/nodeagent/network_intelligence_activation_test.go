package nodeagent

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestNetworkIntelligenceUDPDetectionKeepsUnverifiedQualityUnavailable(t *testing.T) {
	now := time.Now().UTC()
	signatures := filepath.Join(t.TempDir(), "signatures.json")
	database, err := json.Marshal(ni.SignatureDatabase{
		Version: 1, Revision: "isolated-udp-fixture-v1", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
		Rules: []ni.ApplicationRule{{Application: "Fixture UDP App", DestinationPrefix: "192.0.2.0/24", Protocol: "udp", Ports: []uint16{27051}, Source: "isolated test fixture; not a production game signature"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signatures, database, 0600); err != nil {
		t.Fatal(err)
	}
	agent := New(Config{NetworkIntelligenceEnabled: true, NetworkIntelligenceSignatures: signatures, DataDir: t.TempDir()})
	defer agent.stopNetworkIntelligence()

	tracker := agent.networkIntelligenceTracker
	source := netip.MustParseAddr("10.0.0.2")
	binding := ni.Binding{NodeID: 7, UserID: 42, Interface: "wg-test", Address: source, Session: "fixture", Generation: 1, Since: now.Add(-time.Second)}
	if err := tracker.Bind(binding); err != nil {
		t.Fatal(err)
	}
	for sequence, destination := range []string{"192.0.2.7:27051", "198.51.100.7:27051"} {
		if !tracker.Observe(ni.Observation{CollectorEpoch: tracker.Epoch(), Binding: binding, Sequence: uint64(sequence + 1), At: now.Add(time.Duration(sequence) * time.Millisecond), Tuple: ni.Tuple{Source: netip.AddrPortFrom(source, uint16(40000+sequence)), Destination: netip.MustParseAddrPort(destination), Protocol: "udp"}, UploadPackets: 3, DownloadPackets: 2, UploadBytes: 300, DownloadBytes: 200}, now.Add(time.Second)) {
			t.Fatalf("failed to seed UDP observation %s", destination)
		}
	}
	var snapshot struct {
		Flows []struct {
			Destination string `json:"destination"`
			Protocol    string `json:"protocol"`
			TCPQuality  *any   `json:"tcp_quality"`
			Application struct {
				Application string `json:"application"`
				Confidence  string `json:"confidence"`
			} `json:"application"`
		} `json:"flows"`
	}
	if err := json.Unmarshal([]byte(agent.networkIntelligenceSnapshot()), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Flows) != 2 {
		t.Fatalf("flows=%+v", snapshot.Flows)
	}
	for _, flow := range snapshot.Flows {
		if flow.Protocol != "udp" || flow.TCPQuality != nil {
			t.Fatalf("UDP exposed unsupported TCP/UDP quality: %+v", flow)
		}
		switch flow.Destination {
		case "192.0.2.7:27051":
			if flow.Application.Application != "Fixture UDP App" || flow.Application.Confidence != "probable" {
				t.Fatalf("signature match=%+v", flow.Application)
			}
		case "198.51.100.7:27051":
			if flow.Application.Application != "Unknown" || flow.Application.Confidence != "unknown" {
				t.Fatalf("port-only match was not Unknown: %+v", flow.Application)
			}
		default:
			t.Fatalf("unexpected destination: %s", flow.Destination)
		}
	}
}

func TestNetworkIntelligenceSnapshotKeepsNewestFlowsAtLimit(t *testing.T) {
	agent := New(Config{NetworkIntelligenceEnabled: true, DataDir: t.TempDir()})
	defer agent.stopNetworkIntelligence()
	tracker := agent.networkIntelligenceTracker
	now := time.Now().UTC()
	source := netip.MustParseAddr("192.0.2.1")
	target := netip.MustParseAddr("192.0.2.2")
	binding := ni.Binding{NodeID: 7, UserID: 42, Interface: "wg-test", Address: source, Session: "fixture", Generation: 1, Since: now.Add(-time.Second)}
	if err := tracker.Bind(binding); err != nil {
		t.Fatal(err)
	}
	for port := uint16(1); port <= 251; port++ {
		if !tracker.Observe(ni.Observation{
			CollectorEpoch: tracker.Epoch(), Binding: binding, Sequence: 1,
			At:    now.Add(time.Duration(int(port)-251) * time.Millisecond),
			Tuple: ni.Tuple{Source: netip.AddrPortFrom(source, 1000), Destination: netip.AddrPortFrom(target, port), Protocol: "udp"},
		}, now) {
			t.Fatalf("failed to seed flow for port %d", port)
		}
	}
	var snapshot struct {
		Truncated bool
		Flows     []struct{ Destination string }
	}
	if err := json.Unmarshal([]byte(agent.networkIntelligenceSnapshot()), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Truncated || len(snapshot.Flows) != 250 {
		t.Fatalf("truncated=%t flows=%d", snapshot.Truncated, len(snapshot.Flows))
	}
	if snapshot.Flows[0].Destination != "192.0.2.2:251" || snapshot.Flows[249].Destination != "192.0.2.2:2" {
		t.Fatal("snapshot did not keep the newest flows in order")
	}
}

func TestNetworkIntelligenceMetricsWireOptIn(t *testing.T) {
	agent := New(Config{NetworkIntelligenceEnabled: true})
	defer agent.stopNetworkIntelligence()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	nodev1.RegisterNodeRuntimeServiceServer(server, agent)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	conn, err := grpc.NewClient("passthrough:///node-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := nodev1.NewNodeRuntimeServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, enabled := range []bool{false, true} {
		response, err := client.Metrics(ctx, &nodev1.MetricsRequest{IncludeRuntime: true, IncludeNetworkIntelligence: enabled})
		if err != nil || response.GetRuntime() == nil || (response.GetNetworkIntelligenceJson() != "") != enabled {
			t.Fatalf("requested=%t response=%v err=%v", enabled, response, err)
		}
	}
}

func TestNetworkIntelligenceActivationOptInAndSnapshot(t *testing.T) {
	disabled := New(Config{})
	if disabled.networkIntelligenceLifecycle != nil || disabled.networkIntelligenceSnapshot() != "" {
		t.Fatal("default must not initialize capture")
	}
	enabled := New(Config{NetworkIntelligenceEnabled: true})
	defer enabled.stopNetworkIntelligence()
	if enabled.networkIntelligenceLifecycle == nil || !enabled.networkIntelligenceLifecycle.Enabled() {
		t.Fatal("opt-in unavailable")
	}
	var snapshot struct {
		Version int
		Flows   []any
	}
	if err := json.Unmarshal([]byte(enabled.networkIntelligenceSnapshot()), &snapshot); err != nil || snapshot.Version != 1 || len(snapshot.Flows) != 0 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	enabled.stopNetworkIntelligence()
	if enabled.networkIntelligenceLifecycle.Enabled() {
		t.Fatal("kill switch did not disable")
	}
}

func TestNetworkIntelligencePublishRequiresAuthoritativeNodeIdentity(t *testing.T) {
	s := New(Config{NetworkIntelligenceEnabled: true})
	defer s.stopNetworkIntelligence()
	if err := s.publishNetworkIntelligence(preparedWireGuardRuntime{}); err == nil {
		t.Fatal("missing policy admitted")
	}
	if len(s.networkIntelligenceTracker.Snapshot(s.startedAt)) != 0 {
		t.Fatal("failed verification published flows")
	}
}

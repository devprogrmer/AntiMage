package nodeagent

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

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

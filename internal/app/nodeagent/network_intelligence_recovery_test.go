package nodeagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	nodeapp "github.com/antimage/antimage/internal/app/node"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type recoveryNodeConfig struct{ Address, Dir, Cert, Key, Mode string }

type stalledRecoveryNode struct{ *Server }

func (s *stalledRecoveryNode) Metrics(ctx context.Context, _ *nodev1.MetricsRequest) (*nodev1.MetricsResponse, error) {
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

// A test-only subprocess entry point: actual Node Agent RPC implementation,
// optional transport fault injection, and portable socket/session adapters.
func TestNetworkRecoveryNodeProcess(t *testing.T) {
	path := os.Getenv("ANTIMAGE_NI_RECOVERY_CHILD")
	if path == "" {
		t.Skip("owned subprocess fixture")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg recoveryNodeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	agent := New(Config{NetworkIntelligenceEnabled: true, DataDir: cfg.Dir})
	defer agent.stopNetworkIntelligence()
	if cfg.Mode != "empty" {
		binding := ni.Binding{NodeID: 7, UserID: 42, Interface: "local-recovery", Address: netip.MustParseAddr("127.0.0.1"), Session: "old-private-peer", Generation: 1, Since: time.Now().UTC().Add(-time.Second)}
		if err := agent.networkIntelligenceTracker.Bind(binding); err != nil {
			t.Fatal(err)
		}
		capture, err := ni.NewSessionCapture(agent.networkIntelligenceTracker, []ni.Binding{binding}, 16)
		if err != nil {
			t.Fatal(err)
		}
		server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		localPanelUDP(t, capture, net.ParseIP(binding.Address.String()), 1, server)
		if cfg.Mode == "reassigned" || cfg.Mode == "new-ip" {
			capture.Stop()
			agent.networkIntelligenceTracker.Unbind(binding)
			binding.Generation++
			binding.Session = "new-private-peer"
			binding.Since = time.Now().UTC()
			if cfg.Mode == "reassigned" {
				binding.UserID = 43
			} else {
				binding.Address = netip.MustParseAddr("127.0.0.2")
			}
			if err := agent.networkIntelligenceTracker.Bind(binding); err != nil {
				t.Fatal(err)
			}
			capture, err = ni.NewSessionCapture(agent.networkIntelligenceTracker, []ni.Binding{binding}, 16)
			if err != nil {
				t.Fatal(err)
			}
			late := ni.CapturedPacket{Metadata: ni.PacketMetadata{Tuple: ni.Tuple{Source: netip.MustParseAddrPort("127.0.0.1:3478"), Destination: netip.AddrPortFrom(binding.Address, 45000), Protocol: "udp"}, WireLength: 48}, Outgoing: true, At: time.Now().UTC()}
			if capture.Consume(late) {
				t.Fatal("new owner accepted unsolicited old response")
			}
			localPanelUDP(t, capture, net.ParseIP(binding.Address.String()), 1, server)
		}
		defer capture.Stop()
	}
	pair, err := tls.LoadX509KeyPair(cfg.Cert, cfg.Key)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := nodeControlTLS(Config{CertFile: cfg.Cert, MasterCertFile: cfg.Cert}, pair)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", cfg.Address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	rpc := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)))
	defer rpc.Stop()
	nodev1.RegisterNodeControlServiceServer(rpc, agent)
	if cfg.Mode == "stall" {
		nodev1.RegisterNodeRuntimeServiceServer(rpc, &stalledRecoveryNode{agent})
	} else {
		nodev1.RegisterNodeRuntimeServiceServer(rpc, agent)
	}
	if err := rpc.Serve(listener); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkIntelligenceProcessRecoveryAndVersionRollback(t *testing.T) {
	baseline, current := os.Getenv("ANTIMAGE_NI_BASELINE_NODE"), os.Getenv("ANTIMAGE_NI_CURRENT_NODE")
	if baseline == "" || current == "" {
		t.Skip("requires locally built current and verified baseline node binaries")
	}
	dir := t.TempDir()
	cert, key, err := nodeapp.GenerateCertificate("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "cert.key")
	if err := os.WriteFile(certPath, []byte(cert), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(key), 0600); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(cert))
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reservation.Addr().String()
	port := reservation.Addr().(*net.TCPAddr).Port
	reservation.Close()
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, Certificates: []tls.Certificate{pair}})))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	control, runtime := nodev1.NewNodeControlServiceClient(connection), nodev1.NewNodeRuntimeServiceClient(connection)
	var active *exec.Cmd
	var log bytes.Buffer
	stop := func() {
		if active != nil {
			_ = active.Process.Kill()
			_ = active.Wait()
			active = nil
		}
	}
	defer stop()
	start := func(mode, binary string, enabled bool) {
		t.Helper()
		stop()
		log.Reset()
		if binary == "" {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(dir, "child.json")
			raw, _ := json.Marshal(recoveryNodeConfig{address, filepath.Join(dir, "agent"), certPath, keyPath, mode})
			if err := os.WriteFile(configPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			active = exec.Command(executable, "-test.run", "^TestNetworkRecoveryNodeProcess$", "-test.v")
			active.Env = append(os.Environ(), "ANTIMAGE_NI_RECOVERY_CHILD="+configPath)
		} else {
			active = exec.Command(binary)
			active.Env = append(os.Environ(), "SERVICE_HOST=127.0.0.1", "SERVICE_PORT="+strconv.Itoa(port), "ANTIMAGE_NODE_NAME=local-recovery-node", "ANTIMAGE_NODE_DATA_DIR="+filepath.Join(dir, "binary-agent"), "ANTIMAGE_DATA_DIR="+filepath.Join(dir, "binary-agent"), "ANTIMAGE_NODE_APP_DIR="+filepath.Join(dir, "binary-app"), "SSL_CERT_FILE="+certPath, "SSL_KEY_FILE="+keyPath, "ANTIMAGE_NODE_MASTER_CERT_FILE="+certPath, "ANTIMAGE_NETWORK_INTELLIGENCE_ENABLED="+strconv.FormatBool(enabled), "ANTIMAGE_NETWORK_OPTIMIZATION_ENABLED=false")
		}
		active.Stdout = &log
		active.Stderr = &log
		if err := active.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			_, err := control.Hello(ctx, &nodev1.HelloRequest{})
			cancel()
			if err == nil {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		stop()
		t.Fatalf("node %s startup failed: %s", mode, log.String())
	}
	read := func() string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		response, err := runtime.Metrics(ctx, &nodev1.MetricsRequest{IncludeRuntime: true, IncludeNetworkIntelligence: true})
		if err != nil {
			t.Fatal(err)
		}
		return response.GetNetworkIntelligenceJson()
	}
	assertOwner := func(raw string, user int64, source string) {
		t.Helper()
		var snapshot struct {
			Flows []struct {
				UserID  int64              `json:"user_id"`
				Source  string             `json:"source"`
				Quality *ni.UDPFlowQuality `json:"udp_quality"`
				Upload  uint64             `json:"upload_packets"`
			} `json:"flows"`
		}
		if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Flows) != 1 || snapshot.Flows[0].UserID != user || netip.MustParseAddrPort(snapshot.Flows[0].Source).Addr().String() != source || snapshot.Flows[0].Quality == nil || snapshot.Flows[0].Quality.UploadExchange.Samples != 2 || snapshot.Flows[0].Upload != 2 {
			t.Fatalf("wrong owner/fresh counters: %s", raw)
		}
	}
	start("seed", "", true)
	assertOwner(read(), 42, "127.0.0.1")
	stop()
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	_, err = runtime.Metrics(ctx, &nodev1.MetricsRequest{IncludeNetworkIntelligence: true})
	cancel()
	if err == nil {
		t.Fatal("dead process returned telemetry")
	}
	t.Log("process disconnect rejected stale telemetry")
	start("stall", "", true)
	ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
	_, err = runtime.Metrics(ctx, &nodev1.MetricsRequest{IncludeNetworkIntelligence: true})
	cancel()
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expected actual RPC deadline: %v", err)
	}
	t.Log("actual gRPC DeadlineExceeded verified")
	start("reassigned", "", true)
	assertOwner(read(), 43, "127.0.0.1")
	t.Log("reused address assigned to new peer/account without inherited counters")
	start("new-ip", "", true)
	assertOwner(read(), 42, "127.0.0.2")
	t.Log("new IP/peer replaced old ownership")
	start("empty", "", true)
	var empty struct {
		Flows []any `json:"flows"`
	}
	if err := json.Unmarshal([]byte(read()), &empty); err != nil || len(empty.Flows) != 0 {
		t.Fatal("restart resurrected flows", err)
	}
	// Run the actual current binary, then the archived baseline binary, then
	// upgrade again on the same disposable data directory and endpoint.
	start("current-binary", current, true)
	if raw := read(); raw == "" {
		t.Fatal("current enabled binary lacks snapshot")
	}
	start("baseline-rollback", baseline, false)
	if raw := read(); raw != "" {
		t.Fatal("baseline rollback with flags off exposed telemetry")
	}
	start("current-reupgrade", current, true)
	if raw := read(); raw == "" {
		t.Fatal("re-upgrade failed")
	}
	t.Log("PASS: current binary → baseline binary (flags off) → current binary; same isolated data and mTLS endpoint")
}

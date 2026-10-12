//go:build linux

package nodecontroller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/migrations"
	"github.com/antimage/antimage/internal/app/nodeagent"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func TestControllerStartupConsumesActualAgentRestoreEvidenceOverTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	app := t.TempDir()
	t.Setenv("ANTIMAGE_NODE_APP_DIR", app)
	t.Setenv("ANTIMAGE_NODE_APP_NAME", "isolated-node")
	if err := os.MkdirAll(filepath.Join(app, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(app, "bin", "antimage-node")
	old, err := os.ReadFile("/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, old, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, ".binary-release.json"), []byte(`{"tag":"v1.0.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "antimage", "managed-backup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(action string) {
		t.Helper()
		output, err := exec.CommandContext(ctx, "bash", "-c", `source "$1"; managed_binary_backup "$2" "$3" restore-op node isolated-node "$4"`, "fixture", helper, action, app, binary).CombinedOutput()
		if err != nil {
			t.Fatalf("production %s helper: %v %s", action, err, output)
		}
	}
	invoke("create")
	target, err := os.ReadFile("/bin/false")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, target, 0755); err != nil {
		t.Fatal(err)
	}
	invoke("restore") // The restore response is deliberately not given to the controller.
	identity, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	restartBoundary := time.Now().Add(-time.Second)
	xray := filepath.Join(app, "runtime-worker")
	if err := os.WriteFile(xray, []byte("#!/bin/sh\nif [ \"$1\" = -version ]; then echo 'Xray 1.0.0'; exit 0; fi\nexec sleep 600\n"), 0755); err != nil {
		t.Fatal(err)
	}
	agent := nodeagent.New(nodeagent.Config{Name: "isolated-node", DataDir: filepath.Join(app, "data"), XrayPath: xray, InstallMode: "script", Version: "v1.0.0"})
	startFence := &nodev1.DestructiveFence{OperationId: "restore-op", CommandId: "runtime-after-restore", ResourceId: "1", ResourceGeneration: 1, LeaseGeneration: 1}
	if _, err := agent.StartRuntime(ctx, &nodev1.RuntimeConfigRequest{OperationId: "restore-op", ConfigJson: `{"inbounds":[],"outbounds":[]}`, Fence: startFence}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop := *startFence
		stop.CommandId = "cleanup-runtime"
		_, _ = agent.StopRuntime(context.Background(), &nodev1.StopRuntimeRequest{OperationId: "restore-op", Fence: &stop})
	})

	tlsFixture := httptest.NewTLSServer(nil)
	certificate := tlsFixture.TLS.Certificates[0]
	tlsFixture.Close()
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}))
	keyBytes, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var destructiveRPCs atomic.Int32
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})), grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == nodev1.NodeRuntimeService_UpdateService_FullMethodName {
			destructiveRPCs.Add(1)
		}
		return handler(ctx, req)
	}))
	nodev1.RegisterNodeControlServiceServer(server, agent)
	nodev1.RegisterNodeRuntimeServiceServer(server, agent)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	db, err := sql.Open("sqlite", filepath.Join(app, "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migrations.RunMigrations(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err := db.Exec(`INSERT INTO nodes(id,name,address,port,api_port,status,certificate,certificate_key,node_capabilities) VALUES (1,'isolated-node','127.0.0.1',?,?,'connected',?,?,'["shared_fencing_v1","command_idempotency_v1"]')`, port, port, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO tls(certificate,`key`) VALUES (?,?)", certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	c := NewController(NewRepository(db, "sqlite"))
	op := NodeUpdateOperation{ID: "restore-op", NodeID: 1, Action: "rollback", BackupIdentity: "restore-op", DesiredVersion: "v1.0.0", PreviousVersion: "v2.0.0", Phase: "rollback_verifying", StartedAt: restartBoundary, RestartRequestedAt: restartBoundary, ReconnectDeadline: time.Now().Add(20 * time.Second), HealthDeadline: time.Now().Add(20 * time.Second)}
	if err := c.repo.StartNodeUpdate(ctx, op); err != nil {
		t.Fatal(err)
	}
	// A reconstructed controller discovers the persisted operation and observes
	// production agent files/process state through the actual TLS gRPC transport.
	reconstructed := NewController(NewRepository(db, "sqlite"))
	if err := reconstructed.RecoverRollouts(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		stored, err := operationapp.Get(ctx, db, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if operationapp.Terminal(stored.State) {
			if stored.State != "rolled_back" {
				t.Fatalf("unverified recovery: %+v", stored)
			}
			break
		}
		if stored.Phase == "manual_recovery_required" {
			t.Fatalf("actual agent evidence rejected: %+v", stored)
		}
		if ctx.Err() != nil {
			t.Fatalf("startup recovery did not finish: %+v", stored)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if destructiveRPCs.Load() != 0 {
		t.Fatal("completed restore was redispatched")
	}
	after, err := os.Stat(binary)
	if err != nil || !os.SameFile(identity, after) {
		t.Fatal("controller repeated production replacement")
	}
	var locks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operation_locks WHERE operation_id=?`, op.ID).Scan(&locks); err != nil || locks != 0 {
		t.Fatalf("verified rollback retained lock: %d %v", locks, err)
	}
	var recoveryEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operation_events WHERE operation_id=? AND event_type='recovery.started'`, op.ID).Scan(&recoveryEvents); err != nil || recoveryEvents != 1 {
		t.Fatalf("startup recovery audit missing or duplicated: %d %v", recoveryEvents, err)
	}
}

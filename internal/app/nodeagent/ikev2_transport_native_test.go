package nodeagent

// These helpers are deliberately opt-in and are the process boundary used by
// scripts/ci/native-ikev2-driver.py.  The driver starts this test as a real
// WSL process; it must not be replaced by an in-process fake or a batch replay.

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/nodeclient"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
)

func TestIKEv2NativeNodeTransportProcess(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("ANTIMAGE_IKEV2_NATIVE_NODE_PROCESS") != "1" {
		t.Skip("requires the WSL native IKEv2 process harness")
	}
	dataDir := os.Getenv("ANTIMAGE_IKEV2_NATIVE_STATE")
	if dataDir == "" {
		t.Fatal("ANTIMAGE_IKEV2_NATIVE_STATE is required")
	}
	cfg := LoadConfig()
	cfg.DataDir = dataDir
	if value := os.Getenv("ANTIMAGE_IKEV2_NATIVE_NODE_CERT"); value != "" {
		cfg.CertFile = value
	}
	if value := os.Getenv("ANTIMAGE_IKEV2_NATIVE_NODE_KEY"); value != "" {
		cfg.KeyFile = value
	}
	if value := os.Getenv("ANTIMAGE_IKEV2_NATIVE_NODE_ADDR"); value != "" {
		cfg.ListenHost = value
	}
	if value := os.Getenv("ANTIMAGE_IKEV2_NATIVE_NODE_PORT"); value != "" {
		cfg.ServicePort = envInt("ANTIMAGE_IKEV2_NATIVE_NODE_PORT", cfg.ServicePort)
	}
	cfg.Name = "native-ikev2-node"
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	server := New(cfg)
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx) }()
	address := net.JoinHostPort(cfg.ListenHost, fmt.Sprintf("%d", cfg.ServicePort))
	if err := waitNativeNodeListener(address, cfg.CertFile, cfg.KeyFile); err != nil {
		t.Fatal(err)
	}
	ready := os.Getenv("ANTIMAGE_IKEV2_NATIVE_NODE_READY")
	if ready == "" {
		ready = filepath.Join(dataDir, "native-node-ready")
	}
	if err := os.WriteFile(ready, []byte(address+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runErr:
		if ctx.Err() == nil && err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		if err := <-runErr; err != nil {
			t.Logf("node stopped: %v", err)
		}
	}
}

func waitNativeNodeListener(address, certFile, keyFile string) error {
	tlsConfig, err := nodeclient.LoadClientTLS(nodeclient.TLSConfig{ClientCertFile: certFile, ClientKeyFile: keyFile, ServerCertFile: certFile})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := nodeclient.Dial(ctx, address, tlsConfig, grpc.WithBlock())
	if err != nil {
		return err
	}
	defer client.Close()
	_, err = client.Control().Hello(ctx, &nodev1.HelloRequest{})
	return err
}

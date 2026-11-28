package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	nodeapp "github.com/antimage/antimage/internal/app/node"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Test-only authenticated fault controller drives owned node subprocesses.
// The panel API and browser receive actual mTLS responses/failures, not mocks.
func TestNetworkIntelligenceBrowserRecoversFromNodeFailures(t *testing.T) {
	dashboard, helper := os.Getenv("ANTIMAGE_NI_LOCAL_DASHBOARD"), os.Getenv("ANTIMAGE_NI_NODE_TEST_BINARY")
	if dashboard == "" || helper == "" {
		t.Skip("requires built dashboard and local node test executable")
	}
	dir := t.TempDir()
	t.Setenv("ANTIMAGE_DATA_DIR", filepath.Join(dir, "data"))
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
	controlClient := nodev1.NewNodeControlServiceClient(connection)
	var mu sync.Mutex
	var active *exec.Cmd
	var logs bytes.Buffer
	stop := func() {
		if active != nil {
			_ = active.Process.Kill()
			_ = active.Wait()
			active = nil
		}
	}
	defer func() { mu.Lock(); defer mu.Unlock(); stop() }()
	action := func(mode string) error {
		mu.Lock()
		defer mu.Unlock()
		stop()
		logs.Reset()
		if mode == "disconnect" {
			return nil
		}
		if mode != "seed" && mode != "stall" && mode != "reassigned" && mode != "empty" {
			return fmt.Errorf("unsupported test action")
		}
		configuration := filepath.Join(dir, "node.json")
		raw, _ := json.Marshal(map[string]string{"Address": address, "Dir": filepath.Join(dir, "node-data"), "Cert": certPath, "Key": keyPath, "Mode": mode})
		if err := os.WriteFile(configuration, raw, 0600); err != nil {
			return err
		}
		active = exec.Command(helper, "-test.run", "^TestNetworkRecoveryNodeProcess$", "-test.v")
		active.Env = append(os.Environ(), "ANTIMAGE_NI_RECOVERY_CHILD="+configuration)
		active.Stdout = &logs
		active.Stderr = &logs
		if err := active.Start(); err != nil {
			active = nil
			return err
		}
		for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); {
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			_, err := controlClient.Hello(ctx, &nodev1.HelloRequest{})
			cancel()
			if err == nil {
				return nil
			}
			time.Sleep(30 * time.Millisecond)
		}
		stop()
		return fmt.Errorf("owned node failed startup: %s", logs.String())
	}
	if err := action("seed"); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Database: "sqlite:///" + filepath.ToSlash(filepath.Join(dir, "panel.sqlite")), CertificateBase: filepath.Join(dir, "certificates"), NetworkIntelligenceEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if _, err := s.db.Exec("INSERT INTO tls (certificate,`key`) VALUES (?,?)", cert, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO nodes (id,name,address,port,api_port,status,certificate,certificate_key,xray_config_mode,usage_coefficient) VALUES (7,'recovery-node','127.0.0.1',?,?,'connected',?,?,'custom',1)`, port, port, cert, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO users (id,username) VALUES (42,'original-account'),(43,'replacement-account'); INSERT INTO user_online_ips (node_id,user_id,protocol,ip,last_seen_at) VALUES (7,42,'wireguard','127.0.0.1',CURRENT_TIMESTAMP),(7,43,'wireguard','127.0.0.1',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	hash, err := adminapp.HashPassword("local-recovery-password")
	if err != nil {
		t.Fatal(err)
	}
	permissions, _ := json.Marshal(adminapp.RoleDefaultPermissions(adminapp.RoleFullAccess))
	if _, err := s.db.Exec(`INSERT INTO admins (username,hashed_password,role,permissions,status,subscription_settings,users_usage,lifetime_usage,created_traffic,deleted_users_usage) VALUES ('recovery-admin',?,'full_access',?,'active','{}',0,0,0,0)`, hash, string(permissions)); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", s.Handler())
	mux.Handle("/statics/", http.FileServer(http.Dir(filepath.Join(dashboard, "build"))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(dashboard, "build", "index.html"))
	})
	panel := httptest.NewServer(mux)
	defer panel.Close()
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(nonce[:])
	faults := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "denied", 403)
			return
		}
		if err := action(r.URL.Query().Get("action")); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	}))
	defer faults.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "e2e/network-intelligence-recovery-live.mjs", panel.URL)
	cmd.Dir = dashboard
	cmd.Env = append(os.Environ(), "ANTIMAGE_NI_FAULT_ORIGIN="+faults.URL, "ANTIMAGE_NI_FAULT_TOKEN="+token)
	output, err := cmd.CombinedOutput()
	t.Logf("actual browser recovery: %s", output)
	if err != nil {
		t.Fatal("browser recovery failed", err)
	}
}

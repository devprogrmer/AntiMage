package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	"github.com/antimage/antimage/internal/app/migrations"
	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	nodeapp "github.com/antimage/antimage/internal/app/node"
	"github.com/antimage/antimage/internal/app/nodecontroller"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type historyTransportAgent struct {
	nodev1.UnimplementedNodeControlServiceServer
	nodev1.UnimplementedNodeRuntimeServiceServer
	snapshot string
}

func (*historyTransportAgent) Hello(context.Context, *nodev1.HelloRequest) (*nodev1.HelloResponse, error) {
	return &nodev1.HelloResponse{}, nil
}

func (a *historyTransportAgent) Metrics(_ context.Context, request *nodev1.MetricsRequest) (*nodev1.MetricsResponse, error) {
	if !request.GetIncludeNetworkIntelligence() {
		return &nodev1.MetricsResponse{}, nil
	}
	return &nodev1.MetricsResponse{NetworkIntelligenceJson: a.snapshot}, nil
}

func TestNetworkHistoryWorkerPersistsThroughMTLSAndDatabaseReopen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	at := now.Add(-time.Second)
	rtt := 42.5
	snapshot, err := json.Marshal(map[string]any{"history": map[string]any{
		"points": []nodecontroller.NetworkHistoryPoint{{NodeID: 7, UserID: 42, At: at, RTT: &rtt}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cert, key, err := nodeapp.GenerateCertificate("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(cert)) {
		t.Fatal("invalid fixture certificate")
	}
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	rpc := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots,
	})))
	defer rpc.Stop()
	agent := &historyTransportAgent{snapshot: string(snapshot)}
	nodev1.RegisterNodeControlServiceServer(rpc, agent)
	nodev1.RegisterNodeRuntimeServiceServer(rpc, agent)
	go func() { _ = rpc.Serve(listener) }()

	path := filepath.Join(t.TempDir(), "panel.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrations.RunMigrations(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO tls (certificate,`key`) VALUES (?,?)", cert, key); err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	nodeAddress := "127.31.0.2"
	if os.Getenv("ANTIMAGE_DISPOSABLE_GUEST") != "1" {
		nodeAddress = "127.0.0.1"
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes (id,name,address,port,api_port,status,certificate,certificate_key,xray_config_mode,usage_coefficient) VALUES (7,'history-fixture',?,?,?,'connected',?,?,'custom',1)`, nodeAddress, port, port, cert, key); err != nil {
		t.Fatal(err)
	}
	controller := nodecontroller.NewController(nodecontroller.NewRepository(db, "sqlite"))
	if _, err := controller.NetworkIntelligence(ctx, 7); err != nil {
		t.Fatalf("fixture node is unreachable: %v", err)
	}
	ticks := make(chan time.Time, 2)
	ticks <- now
	ticks <- now
	close(ticks)
	nodecontroller.RunNetworkHistoryCollector(ctx, ticks, controller, &ni.HistoryRepository{DB: db}, func() time.Time { return now })
	nodeRequest := httptest.NewRequest(http.MethodGet, "/api/network-intelligence/node?node_id=7&diagnose=true", nil)
	nodeRequest = nodeRequest.WithContext(context.WithValue(nodeRequest.Context(), adminContextKey, adminPrincipal{Role: "full_access"}))
	nodeResponse := httptest.NewRecorder()
	(&Server{nodeController: controller}).handleNetworkIntelligenceNode(nodeResponse, nodeRequest)
	var diagnostics struct {
		Results []struct {
			Scope    string
			Findings []ni.Finding
		}
	}
	if err := json.Unmarshal(nodeResponse.Body.Bytes(), &diagnostics); err != nil || nodeResponse.Code != http.StatusOK ||
		len(diagnostics.Results) != 1 || diagnostics.Results[0].Scope != "node" ||
		len(diagnostics.Results[0].Findings) != 1 || diagnostics.Results[0].Findings[0].Code != "stale_telemetry" {
		t.Fatalf("missing node telemetry must be reported: status=%d body=%s err=%v", nodeResponse.Code, nodeResponse.Body, err)
	}
	rpc.Stop()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	browserDashboard := os.Getenv("ANTIMAGE_NI_BROWSER_DASHBOARD")
	panel := startNetworkHistoryBrowserPanel(t, path, browserDashboard)
	client := panel.Client()
	client.Timeout = 5 * time.Second
	client.Jar, err = cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	historyURL := panel.URL + "/api/network-intelligence/history?node_id=7&user_id=42"
	unauthenticated, err := client.Get(historyURL)
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated history returned %d", unauthenticated.StatusCode)
	}
	login, err := http.NewRequest(http.MethodPost, panel.URL+"/api/auth/login", strings.NewReader(`{"username":"history-admin","password":"fixture-admin-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	login.Header.Set("Origin", panel.URL)
	login.Header.Set("Content-Type", "application/json")
	session, err := client.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	session.Body.Close()
	if session.StatusCode != http.StatusOK || len(session.Cookies()) == 0 {
		t.Fatalf("login returned %d", session.StatusCode)
	}
	res, err := client.Get(historyURL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var history networkHistoryResponse
	if err := json.NewDecoder(res.Body).Decode(&history); err != nil || res.StatusCode != http.StatusOK || history.Storage != "durable" || len(history.Points) != 1 {
		t.Fatalf("history after database reopen: status=%d history=%+v error=%v", res.StatusCode, history, err)
	}
	var point nodecontroller.NetworkHistoryPoint
	if err := json.Unmarshal(history.Points[0], &point); err != nil || point.NodeID != 7 || point.UserID != 42 || !point.At.Equal(at) || point.RTT == nil || *point.RTT != rtt {
		t.Fatalf("wrong persisted point: %s, error=%v", history.Points[0], err)
	}
	if browserDashboard != "" {
		runNetworkHistoryBrowser(t, browserDashboard, panel.URL)
	}
}

func TestNetworkHistoryBrowserAgainstWireGuard(t *testing.T) {
	path := os.Getenv("ANTIMAGE_NI_BROWSER_WIREGUARD_DB")
	if path == "" {
		t.Skip("requires a database owned by the disposable live WireGuard fixture")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	dashboard := os.Getenv("ANTIMAGE_NI_BROWSER_DASHBOARD")
	if dashboard == "" {
		t.Fatal("wireguard browser fixture requires the built dashboard")
	}
	panel := startNetworkHistoryBrowserPanel(t, path, dashboard)
	runNetworkHistoryBrowser(t, dashboard, panel.URL, "--real-wireguard")
}

func startNetworkHistoryBrowserPanel(t *testing.T, path, browserDashboard string) *httptest.Server {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("ANTIMAGE_DATA_DIR", dataDir)
	s, err := New(Config{
		Database: "sqlite:///" + filepath.ToSlash(path), CertificateBase: filepath.Join(dataDir, "certificates"),
		NetworkIntelligenceEnabled: true, NetworkHistoryPersistence: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.db.Close() })
	hash, err := adminapp.HashPassword("fixture-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	perms, err := json.Marshal(adminapp.RoleDefaultPermissions(adminapp.RoleFullAccess))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO admins (
		username, hashed_password, role, permissions, status, subscription_settings,
		users_usage, lifetime_usage, created_traffic, deleted_users_usage
	) VALUES ('history-admin', ?, 'full_access', ?, 'active', '{}', 0, 0, 0, 0)`, hash, string(perms)); err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()

	if browserDashboard != "" {
		if _, err := s.db.Exec(`INSERT INTO users (id, username) VALUES (42, 'history-user');
			INSERT INTO user_online_ips (node_id, user_id, protocol, ip, last_seen_at)
			VALUES (7, 42, 'wireguard', '192.0.2.42', CURRENT_TIMESTAMP)`); err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		mux.Handle("/api/", handler)
		mux.Handle("/statics/", http.FileServer(http.Dir(filepath.Join(browserDashboard, "build"))))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, filepath.Join(browserDashboard, "build", "index.html"))
		})
		handler = mux
	}
	panel := httptest.NewServer(handler)
	t.Cleanup(panel.Close)
	return panel
}

func runNetworkHistoryBrowser(t *testing.T, dashboard, origin string, mode ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	args := append([]string{"e2e/network-intelligence-live.mjs", origin}, mode...)
	browser := exec.CommandContext(ctx, "node", args...)
	browser.Dir = dashboard
	if output, err := browser.CombinedOutput(); err != nil {
		t.Fatalf("real dashboard browser test failed: %v\n%s", err, output)
	}
}

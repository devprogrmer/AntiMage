package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Started by the opt-in Node Agent test while its actual mTLS RPC server is
// running. No node transport or HTTP route is mocked/intercepted here.
func TestNetworkLiveBrowserAgainstLocalNode(t *testing.T) {
	path, dashboard := os.Getenv("ANTIMAGE_NI_LOCAL_DB"), os.Getenv("ANTIMAGE_NI_LOCAL_DASHBOARD")
	if path == "" || dashboard == "" {
		t.Skip("requires parent local socket/Node Agent fixture")
	}
	dir := t.TempDir()
	t.Setenv("ANTIMAGE_DATA_DIR", dir)
	s, err := New(Config{Database: "sqlite:///" + filepath.ToSlash(path), CertificateBase: filepath.Join(dir, "certificates"), NetworkIntelligenceEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	mux := http.NewServeMux()
	mux.Handle("/api/", s.Handler())
	mux.Handle("/statics/", http.FileServer(http.Dir(filepath.Join(dashboard, "build"))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(dashboard, "build", "index.html"))
	})
	panel := httptest.NewServer(mux)
	defer panel.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "e2e/network-intelligence-local-live.mjs", panel.URL)
	cmd.Dir = dashboard
	output, err := cmd.CombinedOutput()
	t.Logf("authenticated local browser: %s", output)
	if err != nil {
		t.Fatalf("browser/API/Node Agent integration failed: %v", err)
	}
}

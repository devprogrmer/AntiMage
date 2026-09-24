package nodeagent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestMaintenanceVersionAndServiceNameValidation(t *testing.T) {
	for _, version := range []string{"latest", "v26.7.11", "26.7.11"} {
		if !xrayReleasePattern.MatchString(version) {
			t.Fatalf("valid Xray release rejected: %q", version)
		}
	}
	for _, version := range []string{"", "v1;reboot", "../v1", "dev"} {
		if xrayReleasePattern.MatchString(version) {
			t.Fatalf("unsafe Xray release accepted: %q", version)
		}
	}
	server := New(Config{Name: "antimage-node"})
	t.Setenv("ANTIMAGE_NODE_APP_NAME", "node-1")
	if unit, err := server.serviceUnit(); err != nil || unit != "node-1.service" {
		t.Fatalf("service unit = %q, %v", unit, err)
	}
	t.Setenv("ANTIMAGE_NODE_APP_NAME", "node/other")
	if _, err := server.serviceUnit(); err == nil {
		t.Fatal("unsafe service name accepted")
	}
}

func TestMaintenanceRejectsDockerMode(t *testing.T) {
	server := New(Config{InstallMode: "docker", Name: "antimage-node"})
	if _, err := server.UpdateRuntime(context.Background(), &nodev1.RuntimeUpdateRequest{Version: "latest"}); err == nil || !strings.Contains(err.Error(), "binary-mode") {
		t.Fatalf("Docker core update error = %v", err)
	}
	if _, err := server.UpdateGeo(context.Background(), &nodev1.GeoUpdateRequest{}); err == nil || !strings.Contains(err.Error(), "binary-mode") {
		t.Fatalf("Docker geo update error = %v", err)
	}
	if _, err := server.RestartService(context.Background(), &nodev1.ServiceRestartRequest{}); err == nil || !strings.Contains(err.Error(), "binary-mode") {
		t.Fatalf("Docker restart error = %v", err)
	}
	if _, err := server.UpdateService(context.Background(), &nodev1.ServiceUpdateRequest{}); err == nil || !strings.Contains(err.Error(), "binary-mode") {
		t.Fatalf("Docker service update error = %v", err)
	}
	if _, err := server.RebootHost(context.Background(), &nodev1.HostRebootRequest{}); err == nil || !strings.Contains(err.Error(), "binary-mode") {
		t.Fatalf("Docker reboot error = %v", err)
	}
}

func TestMaintenanceRejectsNonRootLinux(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Skip("requires a non-root Linux test process")
	}
	server := New(Config{InstallMode: "binary", Name: "antimage-node"})
	if err := server.requireBinaryMaintenance(); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("non-root maintenance error = %v", err)
	}
}

func TestGeoUpdateStagesFilesBeforeReplacingExistingAssets(t *testing.T) {
	previousGOOS, previousEUID := maintenanceGOOS, maintenanceEUID
	previousLookup, previousClient := geoLookupIP, geoHTTPClient
	maintenanceGOOS = "linux"
	maintenanceEUID = func() int { return 0 }
	geoLookupIP = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("1.1.1.1")}, nil }
	t.Cleanup(func() {
		maintenanceGOOS, maintenanceEUID = previousGOOS, previousEUID
		geoLookupIP, geoHTTPClient = previousLookup, previousClient
	})
	assetsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(assetsDir, "geoip.dat"), []byte("old-ip"), 0644); err != nil {
		t.Fatal(err)
	}
	server := New(Config{InstallMode: "binary", XrayAssetsDir: assetsDir})
	failed := false
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/geosite.dat" && failed {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("new-" + r.URL.Path[1:]))
	}))
	defer tlsServer.Close()
	geoHTTPClient = tlsServer.Client
	files := []*nodev1.GeoFile{
		{Name: "geoip.dat", Url: tlsServer.URL + "/geoip.dat"},
		{Name: "geosite.dat", Url: tlsServer.URL + "/geosite.dat"},
	}
	failed = true
	if _, err := server.UpdateGeo(context.Background(), &nodev1.GeoUpdateRequest{Files: files}); err == nil {
		t.Fatal("failed second download was reported as success")
	}
	if current, err := os.ReadFile(filepath.Join(assetsDir, "geoip.dat")); err != nil || string(current) != "old-ip" {
		t.Fatalf("existing geo asset changed after failed staging: %q, %v", current, err)
	}
	failed = false
	if _, err := server.UpdateGeo(context.Background(), &nodev1.GeoUpdateRequest{Files: files}); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"geoip.dat", "geosite.dat"} {
		current, err := os.ReadFile(filepath.Join(assetsDir, filename))
		if err != nil || string(current) != "new-"+filename {
			t.Fatalf("updated geo asset %s = %q, %v", filename, current, err)
		}
	}
	if _, err := server.UpdateGeo(context.Background(), &nodev1.GeoUpdateRequest{Files: []*nodev1.GeoFile{{Name: "../escape.dat", Url: tlsServer.URL}}}); err == nil {
		t.Fatal("unsafe geo filename accepted")
	}
}

func TestGeoUpdateRejectsPrivateRedirect(t *testing.T) {
	previousGOOS, previousEUID := maintenanceGOOS, maintenanceEUID
	previousLookup, previousClient := geoLookupIP, geoHTTPClient
	maintenanceGOOS = "linux"
	maintenanceEUID = func() int { return 0 }
	geoLookupIP = func(host string) ([]net.IP, error) {
		if host == "private.example" {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return []net.IP{net.ParseIP("1.1.1.1")}, nil
	}
	t.Cleanup(func() {
		maintenanceGOOS, maintenanceEUID = previousGOOS, previousEUID
		geoLookupIP, geoHTTPClient = previousLookup, previousClient
	})
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://private.example/geoip.dat", http.StatusFound)
	}))
	defer tlsServer.Close()
	geoHTTPClient = tlsServer.Client
	server := New(Config{InstallMode: "binary", XrayAssetsDir: t.TempDir()})
	if _, err := server.UpdateGeo(context.Background(), &nodev1.GeoUpdateRequest{Files: []*nodev1.GeoFile{{Name: "geoip.dat", Url: tlsServer.URL}}}); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("private redirect was not rejected: %v", err)
	}
}

func TestHostActionsScheduleExpectedCommands(t *testing.T) {
	previousGOOS, previousEUID := maintenanceGOOS, maintenanceEUID
	previousCommand := maintenanceCommandContext
	maintenanceGOOS = "linux"
	maintenanceEUID = func() int { return 0 }
	var commands [][]string
	maintenanceCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		commands = append(commands, append([]string{name}, args...))
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestMaintenanceHelperProcess")
	}
	t.Setenv("ANTIMAGE_NODE_APP_NAME", "node-1")
	t.Setenv("GO_WANT_MAINTENANCE_HELPER_PROCESS", "1")
	t.Cleanup(func() {
		maintenanceGOOS, maintenanceEUID = previousGOOS, previousEUID
		maintenanceCommandContext = previousCommand
	})
	server := New(Config{InstallMode: "binary", Name: "display-name"})
	ctx := context.Background()
	if _, err := server.RestartService(ctx, &nodev1.ServiceRestartRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.UpdateService(ctx, &nodev1.ServiceUpdateRequest{Channel: "dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.RebootHost(ctx, &nodev1.HostRebootRequest{}); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 3 {
		t.Fatalf("scheduled commands = %d", len(commands))
	}
	for i, expected := range []string{"systemctl restart node-1.service", "/usr/local/bin/node-1 update --dev", "systemctl reboot"} {
		joined := strings.Join(commands[i], " ")
		if !strings.Contains(joined, expected) || !strings.Contains(joined, "--setenv=ANTIMAGE_NODE_APP_NAME=node-1") {
			t.Fatalf("unexpected scheduled command: %s", joined)
		}
	}
	if _, err := server.UpdateService(ctx, &nodev1.ServiceUpdateRequest{Version: "v1;reboot"}); err == nil {
		t.Fatal("unsafe update version accepted")
	}
}

func TestMaintenanceHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MAINTENANCE_HELPER_PROCESS") != "1" {
		return
	}
	os.Exit(0)
}

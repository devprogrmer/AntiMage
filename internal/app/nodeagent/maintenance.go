package nodeagent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	maintenanceCommandContext = exec.CommandContext
	maintenanceGOOS           = runtime.GOOS
	maintenanceEUID           = os.Geteuid
	geoLookupIP               = net.LookupIP
	geoHTTPClient             = func() *http.Client { return &http.Client{Timeout: 2 * time.Minute} }
)

var (
	xrayReleasePattern = regexp.MustCompile(`^(?:latest|v?[0-9]+(?:\.[0-9]+){1,3})$`)
	serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
)

func (s *Server) requireBinaryMaintenance() error {
	if maintenanceGOOS != "linux" || !strings.EqualFold(s.cfg.InstallMode, "binary") {
		return status.Error(codes.FailedPrecondition, "host maintenance requires a Linux binary-mode node")
	}
	if maintenanceEUID() != 0 {
		return status.Error(codes.PermissionDenied, "host maintenance requires root privileges")
	}
	return nil
}

func (s *Server) UpdateRuntime(ctx context.Context, req *nodev1.RuntimeUpdateRequest) (*nodev1.RuntimeActionResponse, error) {
	if err := s.requireBinaryMaintenance(); err != nil {
		return nil, err
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	version := strings.TrimSpace(req.GetVersion())
	if !xrayReleasePattern.MatchString(version) {
		return nil, status.Error(codes.InvalidArgument, "invalid Xray release version")
	}
	appDir := strings.TrimSpace(os.Getenv("ANTIMAGE_NODE_APP_DIR"))
	if appDir == "" {
		return nil, status.Error(codes.FailedPrecondition, "node installer directory is not configured")
	}
	script := filepath.Join(appDir, "scripts", "install_latest_xray.sh")
	if _, err := os.Stat(script); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "Xray installer is unavailable: %v", err)
	}
	cmd := maintenanceCommandContext(ctx, "bash", script, version)
	cmd.Env = append(os.Environ(), "ANTIMAGE_DATA_DIR="+s.cfg.DataDir, "XRAY_INSTALL_DIR="+filepath.Dir(s.cfg.XrayPath), "XRAY_ASSETS_DIR="+s.cfg.XrayAssetsDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, status.Errorf(codes.Internal, "Xray installation failed: %v: %s", err, strings.TrimSpace(string(output)))
	}
	installedVersion := xrayVersion(s.cfg.XrayPath)
	if installedVersion == "" {
		return nil, status.Error(codes.Internal, "Xray installer did not produce a usable binary")
	}
	if version != "latest" && installedVersion != strings.TrimPrefix(version, "v") {
		return nil, status.Errorf(codes.Internal, "Xray version mismatch: requested %s, installed %s", version, installedVersion)
	}
	s.mu.Lock()
	configPath := s.lastConfig
	s.mu.Unlock()
	if configPath != "" {
		if err := s.startXray(ctx, configPath); err != nil {
			return nil, status.Errorf(codes.Internal, "Xray installed but runtime restart failed: %v", err)
		}
	}
	return s.action(req.GetOperationId(), "Xray core updated to "+installedVersion), nil
}

func (s *Server) UpdateGeo(ctx context.Context, req *nodev1.GeoUpdateRequest) (*nodev1.RuntimeActionResponse, error) {
	if err := s.requireBinaryMaintenance(); err != nil {
		return nil, err
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if len(req.GetFiles()) == 0 || len(req.GetFiles()) > 2 {
		return nil, status.Error(codes.InvalidArgument, "one or two geo files are required")
	}
	if err := os.MkdirAll(s.cfg.XrayAssetsDir, 0755); err != nil {
		return nil, status.Errorf(codes.Internal, "create Xray assets directory: %v", err)
	}
	staged := make(map[string]string, len(req.GetFiles()))
	defer func() {
		for _, path := range staged {
			_ = os.Remove(path)
		}
	}()
	client := geoHTTPClient()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many geo download redirects")
		}
		return validateGeoDownloadURL(req.URL)
	}
	for _, file := range req.GetFiles() {
		name := strings.TrimSpace(file.GetName())
		if name != "geoip.dat" && name != "geosite.dat" {
			return nil, status.Error(codes.InvalidArgument, "unsupported geo filename")
		}
		if _, duplicate := staged[name]; duplicate {
			return nil, status.Error(codes.InvalidArgument, "duplicate geo filename")
		}
		parsed, parseErr := url.Parse(strings.TrimSpace(file.GetUrl()))
		if parseErr != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid geo download URL")
		}
		if err := validateGeoDownloadURL(parsed); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, file.GetUrl(), nil)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid geo URL: %v", err)
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "download %s: %v", name, err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, status.Errorf(codes.Unavailable, "download %s: HTTP %d", name, response.StatusCode)
		}
		tmp, err := os.CreateTemp(s.cfg.XrayAssetsDir, "."+name+"-*")
		if err != nil {
			response.Body.Close()
			return nil, status.Errorf(codes.Internal, "stage %s: %v", name, err)
		}
		staged[name] = tmp.Name()
		bytesWritten, copyErr := io.CopyN(tmp, response.Body, 128<<20+1)
		closeErr := tmp.Close()
		response.Body.Close()
		if copyErr != nil && copyErr != io.EOF {
			return nil, status.Errorf(codes.Internal, "write %s: %v", name, copyErr)
		}
		if closeErr != nil || bytesWritten == 0 || bytesWritten > 128<<20 {
			return nil, status.Errorf(codes.InvalidArgument, "invalid %s size or write failure", name)
		}
	}
	for name, path := range staged {
		if err := os.Chmod(path, 0644); err != nil {
			return nil, status.Errorf(codes.Internal, "chmod %s: %v", name, err)
		}
		if err := os.Rename(path, filepath.Join(s.cfg.XrayAssetsDir, name)); err != nil {
			return nil, status.Errorf(codes.Internal, "install %s: %v", name, err)
		}
	}
	s.mu.Lock()
	configPath := s.lastConfig
	s.mu.Unlock()
	if configPath != "" {
		if err := s.startXray(ctx, configPath); err != nil {
			return nil, status.Errorf(codes.Internal, "geo files installed but runtime restart failed: %v", err)
		}
	}
	return s.action(req.GetOperationId(), "Xray geo files updated"), nil
}

func validateGeoDownloadURL(parsed *url.URL) error {
	if (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("geo download URL must use HTTP or HTTPS without credentials")
	}
	addresses, err := geoLookupIP(parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return fmt.Errorf("geo download host could not be resolved")
	}
	for _, address := range addresses {
		if address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
			return fmt.Errorf("geo download host resolves to a private or reserved address")
		}
	}
	return nil
}

func (s *Server) scheduleHostAction(ctx context.Context, action string, command string, args ...string) (*nodev1.RuntimeActionResponse, error) {
	if err := s.requireBinaryMaintenance(); err != nil {
		return nil, err
	}
	name, err := s.installedServiceName()
	if err != nil {
		return nil, err
	}
	unit := fmt.Sprintf("antimage-%s-%d", action, time.Now().UnixNano())
	commandArgs := append([]string{"--unit=" + unit, "--on-active=3s", "--collect", "--setenv=ANTIMAGE_NODE_APP_NAME=" + name, command}, args...)
	output, err := maintenanceCommandContext(ctx, "systemd-run", commandArgs...).CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "schedule %s: %v: %s", action, err, strings.TrimSpace(string(output)))
	}
	return s.action("", action+" scheduled; check node status for completion"), nil
}

func (s *Server) installedServiceName() (string, error) {
	name := strings.TrimSpace(os.Getenv("ANTIMAGE_NODE_APP_NAME"))
	if name == "" {
		name = s.cfg.Name
	}
	if !serviceNamePattern.MatchString(name) {
		return "", status.Error(codes.FailedPrecondition, "invalid node service name")
	}
	return name, nil
}

func (s *Server) serviceUnit() (string, error) {
	name, err := s.installedServiceName()
	if err != nil {
		return "", err
	}
	return name + ".service", nil
}

func (s *Server) RestartService(ctx context.Context, req *nodev1.ServiceRestartRequest) (*nodev1.RuntimeActionResponse, error) {
	unit, err := s.serviceUnit()
	if err != nil {
		return nil, err
	}
	result, err := s.scheduleHostAction(ctx, "restart", "systemctl", "restart", unit)
	if result != nil {
		result.OperationId = req.GetOperationId()
	}
	return result, err
}

func (s *Server) UpdateService(ctx context.Context, req *nodev1.ServiceUpdateRequest) (*nodev1.RuntimeActionResponse, error) {
	name, err := s.installedServiceName()
	if err != nil {
		return nil, err
	}
	channel := strings.ToLower(strings.TrimSpace(req.GetChannel()))
	if channel != "" && channel != "stable" && channel != "dev" {
		return nil, status.Error(codes.InvalidArgument, "invalid node update channel")
	}
	version := strings.TrimSpace(req.GetVersion())
	if version != "" && !regexp.MustCompile(`^(?:v[0-9]+(?:\.[0-9]+){1,3}|dev-[a-f0-9]{7,40})$`).MatchString(version) {
		return nil, status.Error(codes.InvalidArgument, "invalid node update version")
	}
	if channel == "dev" && version != "" {
		return nil, status.Error(codes.InvalidArgument, "dev channel and explicit version cannot be combined")
	}
	args := []string{"update"}
	if channel == "dev" {
		args = append(args, "--dev")
	} else if version != "" {
		args = append(args, "--version", version)
	}
	result, err := s.scheduleHostAction(ctx, "update", path.Join("/usr/local/bin", name), args...)
	if result != nil {
		result.OperationId = req.GetOperationId()
	}
	return result, err
}

func (s *Server) RebootHost(ctx context.Context, req *nodev1.HostRebootRequest) (*nodev1.RuntimeActionResponse, error) {
	result, err := s.scheduleHostAction(ctx, "reboot", "systemctl", "reboot")
	if result != nil {
		result.OperationId = req.GetOperationId()
	}
	return result, err
}

package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	systemapp "github.com/antimage/antimage/internal/app/system"
	managedprocess "github.com/antimage/antimage/internal/platform/process"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	maintenanceCommandContext = managedprocess.CommandContext
	maintenanceGOOS           = runtime.GOOS
	maintenanceEUID           = os.Geteuid
	geoLookupIP               = func(ctx context.Context, host string) ([]net.IP, error) {
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		result := make([]net.IP, 0, len(addresses))
		for _, address := range addresses {
			result = append(result, address.IP)
		}
		return result, nil
	}
	geoHTTPClient = func() *http.Client { return &http.Client{Timeout: 2 * time.Minute} }
)

func (s *Server) verifiedInstallerAvailable() bool {
	if !strings.EqualFold(s.cfg.InstallMode, "binary") || maintenanceGOOS != "linux" {
		return false
	}
	name, err := s.installedServiceName()
	if err != nil {
		return false
	}
	file, err := os.Open(path.Join("/usr/local/bin", name))
	if err != nil {
		return false
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		return false
	}
	text := string(payload)
	return strings.Contains(text, "download_resolved_build()") && strings.Contains(text, "rollback_command()") && strings.Contains(text, "start_node_rollback_watchdog()") && strings.Contains(text, "commit_node_update_command()") && strings.Contains(text, "node_command_fence()") && strings.Contains(text, "--lease-generation")
}

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

func (s *Server) updateRuntime(ctx context.Context, req *nodev1.RuntimeUpdateRequest) (*nodev1.RuntimeActionResponse, error) {
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
	installedVersion := xrayVersionContext(ctx, s.cfg.XrayPath)
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

func (s *Server) updateGeo(ctx context.Context, req *nodev1.GeoUpdateRequest) (*nodev1.RuntimeActionResponse, error) {
	if err := s.requireBinaryMaintenance(); err != nil {
		return nil, err
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	var transactionPath string
	var transaction geoTransaction
	if req.GetOperationId() != "" {
		var err error
		transactionPath, err = geoTransactionPath(os.Getenv("ANTIMAGE_NODE_APP_DIR"), req.OperationId)
		if err != nil {
			return nil, err
		}
		if req.ResumeReloadOnly {
			transaction, err = readGeoTransaction(transactionPath)
			if err != nil {
				return nil, err
			}
			if transaction.OperationID != req.OperationId {
				return nil, fmt.Errorf("Geo recovery identity mismatch")
			}
			return s.reloadCommittedGeo(ctx, transactionPath, transaction)
		}
		if _, err := os.Lstat(transactionPath); err == nil || !os.IsNotExist(err) {
			return nil, fmt.Errorf("Geo transaction already dispatched; inspect actual files before recovery")
		}
		if req.Fence == nil {
			return nil, fmt.Errorf("Geo transaction requires a fence")
		}
		payload, err := json.Marshal(req.Files)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(payload)
		transaction = geoTransaction{OperationID: req.OperationId, CommandID: req.Fence.CommandId, ResourceGeneration: req.Fence.ResourceGeneration, RequestSHA256: fmt.Sprintf("%x", digest), Phase: "downloading", Deadline: time.Now().Add(3 * time.Minute).UnixNano(), Files: map[string]string{}}
		if deadline, ok := ctx.Deadline(); ok && deadline.UnixNano() < transaction.Deadline {
			transaction.Deadline = deadline.UnixNano()
		}
		if err := writeGeoTransaction(transactionPath, transaction); err != nil {
			return nil, err
		}
	}
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
		return validateGeoDownloadURL(req.Context(), req.URL)
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
		if err := validateGeoDownloadURL(ctx, parsed); err != nil {
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
		syncErr := tmp.Sync()
		closeErr := tmp.Close()
		response.Body.Close()
		if copyErr != nil && copyErr != io.EOF {
			return nil, status.Errorf(codes.Internal, "write %s: %v", name, copyErr)
		}
		if syncErr != nil || closeErr != nil || bytesWritten == 0 || bytesWritten > 128<<20 {
			return nil, status.Errorf(codes.InvalidArgument, "invalid %s size or write failure", name)
		}
	}
	if transactionPath != "" {
		for name, path := range staged {
			hash, err := geoFileHash(path)
			if err != nil {
				return nil, err
			}
			transaction.Files[name] = hash
		}
		transaction.Phase = "replacement_ready"
		if err := writeGeoTransaction(transactionPath, transaction); err != nil {
			return nil, err
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
	if runtime.GOOS == "linux" {
		directory, err := os.Open(s.cfg.XrayAssetsDir)
		if err != nil {
			return nil, err
		}
		err = directory.Sync()
		_ = directory.Close()
		if err != nil {
			return nil, err
		}
	}
	if transactionPath != "" {
		transaction.Phase = "files_committed"
		if err := writeGeoTransaction(transactionPath, transaction); err != nil {
			return nil, err
		}
		return s.reloadCommittedGeo(ctx, transactionPath, transaction)
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

func validateGeoDownloadURL(ctx context.Context, parsed *url.URL) error {
	if (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("geo download URL must use HTTP or HTTPS without credentials")
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := geoLookupIP(lookup, parsed.Hostname())
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
	if action == "update" || action == "rollback" || action == "fenced-restart" || action == "fenced-reboot" {
		for index, arg := range args {
			if arg == "--command-id" && index+1 < len(args) {
				digest := sha256.Sum256([]byte(strings.Join(args, "\x00")))
				unit = fmt.Sprintf("antimage-%s-%x", action, digest[:16])
				break
			}
		}
	}
	commandArgs := []string{"--unit=" + unit, "--on-active=3s", "--collect", "--property=RuntimeMaxSec=180s", "--property=TimeoutStopSec=10s", "--property=KillMode=control-group", "--setenv=ANTIMAGE_NODE_APP_NAME=" + name}
	if app := os.Getenv("ANTIMAGE_NODE_APP_DIR"); app != "" {
		commandArgs = append(commandArgs, "--setenv=ANTIMAGE_NODE_APP_DIR="+app)
	}
	commandArgs = append(commandArgs, command)
	commandArgs = append(commandArgs, args...)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
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
	return s.scheduleFencedHostAction(ctx, req.GetOperationId(), req.GetFence(), "fenced-restart")
}

func (s *Server) UpdateService(ctx context.Context, req *nodev1.ServiceUpdateRequest) (response *nodev1.RuntimeActionResponse, err error) {
	if err := s.requireBinaryMaintenance(); err != nil {
		return nil, err
	}
	name, err := s.installedServiceName()
	if err != nil {
		return nil, err
	}
	if req.Action == "resume_install" || req.Action == "resume_restore" || req.Action == "resume_restart" {
		restore := req.BackupIdentity != ""
		evidence, err := systemapp.InspectBinaryTransaction(os.Getenv("ANTIMAGE_NODE_APP_DIR"), req.OperationId, req.BackupIdentity, "node", name, runtime.GOARCH, restore)
		if err != nil || !evidence.BackupValid || evidence.NextAction == "manual_recovery_required" || evidence.TargetVersion != req.Version {
			return nil, status.Error(codes.FailedPrecondition, "transaction recovery lacks verified files/artifact/backup identity")
		}
		if req.Action == "resume_install" && evidence.NextAction != "resume_install" || req.Action == "resume_restore" && evidence.NextAction != "resume_restore" || req.Action == "resume_restart" && evidence.NextAction != "restart_only" {
			return nil, status.Error(codes.FailedPrecondition, "requested recovery step differs from actual transaction state")
		}
		if !restore {
			var target systemapp.ResolvedInstall
			if json.Unmarshal([]byte(req.ResolvedBuildJson), &target) != nil || target.SHA256 != evidence.ArtifactSHA256 || target.Version != evidence.TargetVersion {
				return nil, status.Error(codes.FailedPrecondition, "immutable transaction artifact differs from recovery target")
			}
		}
		if req.Action == "resume_restart" {
			fenceArgs, err := s.acceptFencedServiceCommand(ctx, name, req)
			if err != nil {
				return nil, err
			}
			response, err := s.scheduleHostAction(ctx, "fenced-restart", path.Join("/usr/local/bin", name), append([]string{"fenced-restart"}, fenceArgs...)...)
			if response != nil {
				response.OperationId = req.OperationId
				response.CommandId = req.CommandId
				response.ResourceId = req.ResourceId
				response.LeaseGeneration = req.LeaseGeneration
				response.ResourceGeneration = req.ResourceGeneration
			}
			return response, err
		}
	}

	defer func() {
		if response != nil && req.GetAction() != "validate_backup" {
			response.OperationId = req.OperationId
			response.CommandId = req.CommandId
			response.LeaseGeneration = req.LeaseGeneration
			response.ResourceGeneration = req.ResourceGeneration
			response.ResourceId = req.ResourceId
		}
	}()
	if req.GetAction() == "commit_update" {
		state := s.runtimeState("")
		var target systemapp.ResolvedInstall
		if err := json.Unmarshal([]byte(req.GetResolvedBuildJson()), &target); err != nil {
			return nil, status.Error(codes.InvalidArgument, "verified immutable target is required")
		}
		running, requested := state.GetNodeVersion(), req.GetVersion()
		matches := running == requested || (nodeDevCommitVersionMatches(running, requested))
		if requested == "" || target.Version != requested || target.Commit == "" || target.Commit != state.GetCommitSha() || target.OS != state.GetOperatingSystem() || target.Architecture != state.GetArchitecture() || !matches || !state.GetConnected() || !state.GetStarted() {
			return nil, status.Error(codes.FailedPrecondition, "current runtime does not match verified update target")
		}
		if err := installedBinaryMatchesTarget(ctx, os.Getenv("ANTIMAGE_NODE_APP_DIR"), target); err != nil {
			return nil, status.Error(codes.FailedPrecondition, "installed binary identity does not match verified update target")
		}
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`).MatchString(req.GetOperationId()) {
			return nil, status.Error(codes.InvalidArgument, "invalid verified update operation identity")
		}
		fenceArgs, err := s.acceptFencedServiceCommand(ctx, name, req)
		if err != nil {
			return nil, err
		}
		args := append([]string{"update-commit", "--operation-id", req.GetOperationId()}, fenceArgs...)
		command := maintenanceCommandContext(ctx, path.Join("/usr/local/bin", name), args...)
		command.Env = append(os.Environ(), "ANTIMAGE_NODE_APP_NAME="+name)
		if _, err := command.CombinedOutput(); err != nil {
			return nil, status.Error(codes.FailedPrecondition, "node recovery watchdog could not be cancelled safely")
		}
		return s.action(req.GetOperationId(), "Verified update committed; backup retained"), nil
	}
	if req.GetAction() == "rollback" || req.GetAction() == "validate_backup" || req.GetAction() == "resume_restore" {
		backup, err := systemapp.ReadBinaryBackup(os.Getenv("ANTIMAGE_NODE_APP_DIR"), req.GetBackupIdentity(), "node", name, runtime.GOARCH)
		if err != nil || backup.Version != req.GetVersion() {
			return nil, status.Error(codes.FailedPrecondition, "rollback backup is unavailable or failed identity, checksum or architecture verification")
		}
		if req.GetAction() == "validate_backup" {
			backup.CurrentRunningVersion = s.runtimeState("").GetNodeVersion()
			payload, err := json.Marshal(backup)
			if err != nil {
				return nil, status.Error(codes.Internal, "unable to serialize backup metadata")
			}
			return &nodev1.RuntimeActionResponse{Accepted: true, BackupJson: string(payload)}, nil
		}
		fenceArgs, err := s.acceptFencedServiceCommand(ctx, name, req)
		if err != nil {
			return nil, err
		}
		args := append([]string{"rollback", "--backup-id", backup.Identity, "--version", backup.Version, "--operation-id", req.GetOperationId()}, fenceArgs...)
		result, err := s.scheduleHostAction(ctx, "rollback", path.Join("/usr/local/bin", name), args...)
		if result != nil {
			result.OperationId = req.GetOperationId()
		}
		return result, err
	}
	if req.GetAction() != "" && req.GetAction() != "update" && req.GetAction() != "resume_install" {
		return nil, status.Error(codes.InvalidArgument, "unsupported service maintenance action")
	}
	channel := strings.ToLower(strings.TrimSpace(req.GetChannel()))
	if channel == "latest" {
		channel = "stable"
	}
	if channel != "" && channel != "stable" && channel != "dev" {
		return nil, status.Error(codes.InvalidArgument, "invalid node update channel")
	}
	version := strings.TrimSpace(req.GetVersion())
	if version != "" && !regexp.MustCompile(`^(?:v[0-9]+(?:\.[0-9]+){1,3}|dev-[a-f0-9]{7,40})$`).MatchString(version) {
		return nil, status.Error(codes.InvalidArgument, "invalid node update version")
	}
	if channel == "dev" && version != "" && !strings.HasPrefix(version, "dev-") {
		return nil, status.Error(codes.InvalidArgument, "dev channel requires a dev-SHA version")
	}
	if channel != "dev" && strings.HasPrefix(version, "dev-") {
		return nil, status.Error(codes.InvalidArgument, "dev version requires the dev channel")
	}
	args := []string{"update"}
	if req.GetResolvedBuildJson() != "" {
		var target systemapp.ResolvedInstall
		if err := json.Unmarshal([]byte(req.GetResolvedBuildJson()), &target); err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid resolved artifact")
		}
		catalog := systemapp.VersionCatalog{}
		if target.Channel == "dev" {
			catalog.Dev = []systemapp.BuildCatalogEntry{target.BuildCatalogEntry}
		} else {
			catalog.Stable = []systemapp.BuildCatalogEntry{target.BuildCatalogEntry}
		}
		if _, err := systemapp.ResolveBuild(catalog, channel, "pinned", version, maintenanceGOOS, runtime.GOARCH); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	} else {
		return nil, status.Error(codes.FailedPrecondition, "verified node update requires resolved artifact identity")
	}
	if channel == "dev" {
		if version == "" {
			args = append(args, "--dev")
		} else {
			args = append(args, "--version", version)
		}
	} else if version != "" {
		args = append(args, "--version", version)
	}
	args = append(args, "--resolved-build", req.GetResolvedBuildJson())
	args = append(args, "--operation-id", req.GetOperationId())
	fenceArgs, err := s.acceptFencedServiceCommand(ctx, name, req)
	if err != nil {
		return nil, err
	}
	args = append(args, fenceArgs...)
	result, err := s.scheduleHostAction(ctx, "update", path.Join("/usr/local/bin", name), args...)
	if result != nil {
		result.OperationId = req.GetOperationId()
	}
	return result, err
}

func (s *Server) acceptFencedServiceCommand(ctx context.Context, name string, req *nodev1.ServiceUpdateRequest) ([]string, error) {
	app := strings.TrimSpace(os.Getenv("ANTIMAGE_NODE_APP_DIR"))
	if app == "" || !filepath.IsAbs(app) {
		return nil, status.Error(codes.FailedPrecondition, "managed node installer directory is required for persistent fencing")
	}
	identity := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`)
	if req.LeaseGeneration <= 0 || req.ResourceGeneration <= 0 || !identity.MatchString(req.ResourceId) || !identity.MatchString(req.FenceOperationId) || !identity.MatchString(req.CommandId) {
		return nil, status.Error(codes.FailedPrecondition, "persistent command fencing identity and generation are required")
	}
	args := []string{"--lease-generation", fmt.Sprint(req.LeaseGeneration), "--fence-operation-id", req.FenceOperationId, "--command-id", req.CommandId}
	args = append(args, "--resource-generation", fmt.Sprint(req.ResourceGeneration), "--resource-id", req.ResourceId)
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := maintenanceCommandContext(bounded, path.Join("/usr/local/bin", name), append([]string{"fence-accept"}, args...)...)
	command.Env = append(os.Environ(), "ANTIMAGE_NODE_APP_NAME="+name)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Command generation verified") {
		return nil, status.Error(codes.FailedPrecondition, "node command generation was rejected or installed CLI fencing is unavailable")
	}
	return args, nil
}

func nodeDevCommitVersionMatches(running, requested string) bool {
	return regexp.MustCompile(`^dev-[a-f0-9]{7,40}$`).MatchString(requested) && regexp.MustCompile(`^dev-[a-f0-9]{7,40}$`).MatchString(running) && strings.HasPrefix(running, requested)
}

func (s *Server) RebootHost(ctx context.Context, req *nodev1.HostRebootRequest) (*nodev1.RuntimeActionResponse, error) {
	return s.scheduleFencedHostAction(ctx, req.GetOperationId(), req.GetFence(), "fenced-reboot")
}

func (s *Server) scheduleFencedHostAction(ctx context.Context, id string, fence *nodev1.DestructiveFence, action string) (*nodev1.RuntimeActionResponse, error) {
	if err := s.requireBinaryMaintenance(); err != nil {
		return nil, err
	}
	if fence == nil || fence.OperationId != id {
		return nil, status.Error(codes.FailedPrecondition, "shared destructive fencing is required")
	}
	name, err := s.installedServiceName()
	if err != nil {
		return nil, err
	}
	args, err := s.acceptFencedServiceCommand(ctx, name, &nodev1.ServiceUpdateRequest{OperationId: id, FenceOperationId: id, CommandId: fence.CommandId, ResourceId: fence.ResourceId, ResourceGeneration: fence.ResourceGeneration, LeaseGeneration: fence.LeaseGeneration})
	if err != nil {
		return nil, err
	}
	response, err := s.scheduleHostAction(ctx, action, path.Join("/usr/local/bin", name), append([]string{action}, args...)...)
	if response != nil {
		response.OperationId, response.CommandId, response.ResourceId = id, fence.CommandId, fence.ResourceId
		response.ResourceGeneration, response.LeaseGeneration = fence.ResourceGeneration, fence.LeaseGeneration
	}
	return response, err
}

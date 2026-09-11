package nodeagent

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type Server struct {
	nodev1.UnimplementedNodeControlServiceServer
	nodev1.UnimplementedNodeRuntimeServiceServer
	nodev1.UnimplementedNodeUsageServiceServer
	nodev1.UnimplementedNodeLogsServiceServer

	cfg                            Config
	xrayAPIPortFallback            int
	mu                             sync.Mutex
	startedAt                      time.Time
	lastConfig                     string
	lastRuntime                    *exec.Cmd
	openVPNRuntimes                map[string]*openVPNProcess
	openVPNTProxySpecs             map[string]openVPNTProxySpec
	openVPNTProxyStartupReconciled bool
	openVPNUsageMu                 sync.Mutex
	openVPNUsageBaseline           map[string]uint64
	openVPNUsagePending            *openVPNUsagePendingBatch
	openVPNUsageLoaded             bool
	openVPNUsageLastAckedBatchID   string
	wireGuardUsageMu               sync.Mutex
	wireGuardUsageBaseline         map[string]uint64
	wireGuardUsagePending          *wireGuardUsagePendingBatch
	wireGuardUsageLoaded           bool
	wireGuardUsageLastAckedBatchID string
	xrayUsageMu                    sync.Mutex
	xrayUsageBaseline              map[string]uint64
	xrayUsagePending               *xrayUsagePendingBatch
	xrayUsageLoaded                bool
	xrayUsageLastAckedBatchID      string
	xrayOutboundUsageMu            sync.Mutex
	xrayOutboundUsageBaseline      map[string]uint64
	xrayOutboundUsagePending       *xrayOutboundUsagePendingBatch
	xrayOutboundUsageLoaded        bool
	mergedUsageMu                  sync.Mutex
	mergedUsagePending             *mergedUsagePendingBatch
	mergedUsageLoaded              bool
	mergedUsageLastAckedBatchID    string
	combinedUsageMu                sync.Mutex
	combinedUsagePending           *combinedUsagePendingBatch
	combinedUsageLoaded            bool
	combinedUsageLastAckedBatchID  string
	torProxies                     map[uint32]*exec.Cmd
	appliedRev                     uint64
	logs                           []string
}

var (
	torCommandContext  = exec.CommandContext
	torLookPath        = exec.LookPath
	xrayCommandContext = exec.CommandContext
)

func New(cfg Config) *Server {
	return &Server{
		cfg:                       cfg,
		xrayAPIPortFallback:       cfg.XrayAPIPort,
		startedAt:                 time.Now(),
		openVPNRuntimes:           make(map[string]*openVPNProcess),
		openVPNTProxySpecs:        make(map[string]openVPNTProxySpec),
		openVPNUsageBaseline:      make(map[string]uint64),
		wireGuardUsageBaseline:    make(map[string]uint64),
		xrayUsageBaseline:         make(map[string]uint64),
		xrayOutboundUsageBaseline: make(map[string]uint64),
		torProxies:                make(map[uint32]*exec.Cmd),
		logs:                      []string{"AntiMage-node agent initialized"},
	}
}

func (s *Server) Run(ctx context.Context) error {
	cert, err := tls.LoadX509KeyPair(s.cfg.CertFile, s.cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("load node certificate: %w", err)
	}
	addr := net.JoinHostPort(s.cfg.ListenHost, fmt.Sprintf("%d", s.cfg.ServicePort))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	defer func() {
		s.stopAllOpenVPNRuntimes()
		s.stopAllOpenVPNTProxySpecs()
		_ = s.stopRuntime()
	}()

	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS12,
	})))
	nodev1.RegisterNodeControlServiceServer(grpcServer, s)
	nodev1.RegisterNodeRuntimeServiceServer(grpcServer, s)
	nodev1.RegisterNodeUsageServiceServer(grpcServer, s)
	nodev1.RegisterNodeLogsServiceServer(grpcServer, s)

	errCh := make(chan error, 1)
	go func() {
		errCh <- grpcServer.Serve(listener)
	}()
	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Server) Hello(context.Context, *nodev1.HelloRequest) (*nodev1.HelloResponse, error) {
	return &nodev1.HelloResponse{
		NodeId:        s.cfg.Name,
		NodeName:      s.cfg.Name,
		NodeVersion:   s.cfg.Version,
		InstallMode:   s.cfg.InstallMode,
		UpdateChannel: s.cfg.UpdateChannel,
		Runtime:       s.runtimeState("ready"),
	}, nil
}

func (s *Server) Connect(context.Context, *nodev1.ConnectRequest) (*nodev1.ConnectResponse, error) {
	s.appendLog("master connected")
	return &nodev1.ConnectResponse{
		ConnectionId: fmt.Sprintf("%s-%d", s.cfg.Name, time.Now().Unix()),
		Runtime:      s.runtimeState("connected"),
	}, nil
}

func (s *Server) Health(context.Context, *nodev1.HealthRequest) (*nodev1.HealthResponse, error) {
	return &nodev1.HealthResponse{Runtime: s.runtimeState("healthy"), Metrics: s.metrics(true)}, nil
}

func (s *Server) StartRuntime(ctx context.Context, req *nodev1.RuntimeConfigRequest) (*nodev1.RuntimeActionResponse, error) {
	return s.applyConfig(ctx, req, "started")
}

func (s *Server) RestartRuntime(
	ctx context.Context,
	req *nodev1.RuntimeConfigRequest,
) (*nodev1.RuntimeActionResponse, error) {
	s.stopAllOpenVPNRuntimes()
	s.stopAllOpenVPNTProxySpecs()
	_ = s.stopRuntime()

	return s.applyConfig(ctx, req, "restarted")
}

func (s *Server) StopRuntime(
	context.Context,
	*nodev1.StopRuntimeRequest,
) (*nodev1.RuntimeActionResponse, error) {
	s.stopAllOpenVPNRuntimes()
	s.stopAllOpenVPNTProxySpecs()
	_ = s.stopRuntime()

	return s.action("", "stopped"), nil
}

func (s *Server) SyncConfig(ctx context.Context, req *nodev1.RuntimeConfigRequest) (*nodev1.RuntimeActionResponse, error) {
	return s.applyConfig(ctx, req, "config synced")
}

func (s *Server) AddUser(context.Context, *nodev1.InboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	return s.action("", "user operation queued through full config sync"), nil
}

func (s *Server) UpdateUser(context.Context, *nodev1.InboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	return s.action("", "user operation queued through full config sync"), nil
}

func (s *Server) RemoveUser(context.Context, *nodev1.RemoveInboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	return s.action("", "user operation queued through full config sync"), nil
}

func (s *Server) Metrics(context.Context, *nodev1.MetricsRequest) (*nodev1.MetricsResponse, error) {
	return s.metrics(true), nil
}

func (s *Server) PublicIPs(context.Context, *nodev1.PublicIPsRequest) (*nodev1.PublicIPsResponse, error) {
	return &nodev1.PublicIPsResponse{}, nil
}

func (s *Server) RestartService(context.Context, *nodev1.ServiceRestartRequest) (*nodev1.RuntimeActionResponse, error) {
	return s.action("", "service restart acknowledged"), nil
}

func (s *Server) ApplyTorProxy(ctx context.Context, req *nodev1.TorProxyRequest) (*nodev1.RuntimeActionResponse, error) {
	port := req.GetSocksPort()
	if port < 1024 || port > 65535 {
		return nil, status.Error(codes.InvalidArgument, "socks_port must be between 1024 and 65535")
	}
	country := strings.ToLower(strings.TrimSpace(req.GetExitCountry()))
	if country != "" && len(country) != 2 {
		return nil, status.Error(codes.InvalidArgument, "exit_country must be a two-letter ISO code")
	}
	torPath, err := torLookPath("tor")
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "tor is not installed on this node")
	}
	proxyDir := filepath.Join(s.cfg.DataDir, "tor", strconv.FormatUint(uint64(port), 10))
	if err := os.MkdirAll(filepath.Join(proxyDir, "data"), 0700); err != nil {
		return nil, err
	}
	torrc := torConfig(port, country, req.GetStrictExit(), filepath.Join(proxyDir, "data"))
	torrcPath := filepath.Join(proxyDir, "torrc")
	if err := os.WriteFile(torrcPath, []byte(torrc), 0600); err != nil {
		return nil, err
	}

	cmd := torCommandContext(context.Background(), torPath, "-f", torrcPath)
	cmd.Stdout = logWriter{server: s}
	cmd.Stderr = logWriter{server: s}
	if err := cmd.Start(); err != nil {
		s.appendLog("failed to start tor proxy: " + err.Error())
		return nil, err
	}

	s.mu.Lock()
	if previous := s.torProxies[port]; previous != nil && previous.Process != nil {
		_ = previous.Process.Kill()
	}
	s.torProxies[port] = cmd
	s.mu.Unlock()

	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		if s.torProxies[port] == cmd {
			delete(s.torProxies, port)
		}
		s.mu.Unlock()
		if err != nil {
			s.appendLog("tor proxy stopped: " + err.Error())
		} else {
			s.appendLog("tor proxy stopped")
		}
	}()

	message := fmt.Sprintf("tor proxy started on 127.0.0.1:%d", port)
	if country != "" {
		message += " exit=" + country
	}
	return s.action(req.GetOperationId(), message), nil
}

func (s *Server) UpdateRuntime(context.Context, *nodev1.RuntimeUpdateRequest) (*nodev1.RuntimeActionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "runtime update is managed by the installer")
}

func (s *Server) UpdateGeo(context.Context, *nodev1.GeoUpdateRequest) (*nodev1.RuntimeActionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "geo update is managed by the installer")
}

func (s *Server) UpdateService(context.Context, *nodev1.ServiceUpdateRequest) (*nodev1.RuntimeActionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "service update is managed by the installer")
}

func (s *Server) RebootHost(context.Context, *nodev1.HostRebootRequest) (*nodev1.RuntimeActionResponse, error) {
	return nil, status.Error(codes.PermissionDenied, "host reboot is disabled by default")
}

func (s *Server) CollectOnlineUsers(context.Context, *nodev1.Empty) (*nodev1.OnlineUsersResponse, error) {
	return &nodev1.OnlineUsersResponse{}, nil
}

func (s *Server) CollectUserUsage(
	ctx context.Context,
	req *nodev1.CollectUsageRequest,
) (*nodev1.UserUsageBatch, error) {
	return s.collectUserUsageWithWireGuard(ctx, req)
}

func (s *Server) AckUserUsage(
	ctx context.Context,
	req *nodev1.AckUsageRequest,
) (*nodev1.AckUsageResponse, error) {
	batchID := strings.TrimSpace(req.GetBatchId())
	if batchID == "" {
		return &nodev1.AckUsageResponse{Acknowledged: false}, nil
	}

	// Try OpenVPN ACK
	if strings.HasPrefix(batchID, "openvpn-") {
		return s.ackOpenVPNUserUsage(ctx, req)
	}

	// Try Xray ACK
	if strings.HasPrefix(batchID, "xray-") {
		return s.ackXrayUserUsage(ctx, req)
	}

	// Try WireGuard ACK
	if strings.HasPrefix(batchID, "wireguard-") {
		return s.ackWireGuardUserUsage(ctx, req)
	}

	// Try combined OpenVPN/Xray + WireGuard ACK
	if strings.HasPrefix(batchID, "combined-") {
		return s.ackCombinedUserUsage(ctx, req)
	}

	// Try merged batch ACK
	if strings.HasPrefix(batchID, "merged-") {
		return s.ackMergedUserUsage(ctx, req)
	}

	// Unknown batch ID format
	return &nodev1.AckUsageResponse{Acknowledged: false}, nil
}

func (s *Server) CollectOutboundUsage(ctx context.Context, req *nodev1.CollectUsageRequest) (*nodev1.OutboundUsageBatch, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.xrayOutboundUsageMu.Lock()
	defer s.xrayOutboundUsageMu.Unlock()

	if err := s.ensureXrayOutboundUsageStateLoadedLocked(); err != nil {
		return nil, err
	}

	// If there's a pending batch, return it (waiting for ACK)
	if s.xrayOutboundUsagePending != nil {
		return xrayOutboundUsageBatchProto(s.xrayOutboundUsagePending), nil
	}

	// Check if Xray runtime is running
	s.mu.Lock()
	xrayRunning := s.lastRuntime != nil
	xrayPath := s.cfg.XrayPath
	xrayAPIPort := s.cfg.XrayAPIPort
	s.mu.Unlock()

	if !xrayRunning {
		return &nodev1.OutboundUsageBatch{
			BatchId: fmt.Sprintf("outbound-%d", time.Now().Unix()),
		}, nil
	}

	// Query Xray stats via CLI with outbound pattern
	client := newXrayStatsClient(xrayPath, xrayAPIPort)

	stats, err := client.queryStats(ctx, "outbound>>>", false)
	if err != nil {
		s.appendLog("xray outbound stats query failed: " + err.Error())
		return &nodev1.OutboundUsageBatch{
			BatchId: fmt.Sprintf("outbound-%d", time.Now().Unix()),
		}, nil
	}

	if s.xrayOutboundUsageBaseline == nil {
		s.xrayOutboundUsageBaseline = make(map[string]uint64)
	}

	nextBaseline := make(map[string]uint64, len(s.xrayOutboundUsageBaseline))
	for key, value := range s.xrayOutboundUsageBaseline {
		nextBaseline[key] = value
	}

	type outboundTraffic struct {
		upDelta   uint64
		downDelta uint64
	}

	byTag := make(map[string]*outboundTraffic)

	for _, stat := range stats {
		statName, ok := parseXrayStatName(stat.Name)
		if !ok || statName.Type != "outbound" {
			continue
		}

		tag := statName.Tag
		if tag == "" {
			continue
		}

		// Build baseline key: tag:direction
		baselineKey := tag + ":" + statName.Direction

		baseline, exists := s.xrayOutboundUsageBaseline[baselineKey]

		currentValue := uint64(0)
		if stat.Value >= 0 {
			currentValue = uint64(stat.Value)
		}

		delta := currentValue
		if exists && currentValue >= baseline {
			delta = currentValue - baseline
		}

		// Counter reset detection
		if exists && currentValue < baseline {
			delta = currentValue
		}

		nextBaseline[baselineKey] = currentValue

		traffic := byTag[tag]
		if traffic == nil {
			traffic = &outboundTraffic{}
			byTag[tag] = traffic
		}

		switch statName.Direction {
		case "uplink":
			traffic.upDelta = delta
		case "downlink":
			traffic.downDelta = delta
		}
	}

	if len(byTag) == 0 {
		return &nodev1.OutboundUsageBatch{
			BatchId: fmt.Sprintf("outbound-%d", time.Now().Unix()),
		}, nil
	}

	samples := make([]xrayOutboundUsageSample, 0, len(byTag))
	for tag, traffic := range byTag {
		if traffic.upDelta == 0 && traffic.downDelta == 0 {
			continue
		}
		samples = append(samples, xrayOutboundUsageSample{
			Tag:  tag,
			Up:   traffic.upDelta,
			Down: traffic.downDelta,
		})
	}

	pending := &xrayOutboundUsagePendingBatch{
		BatchID:      fmt.Sprintf("outbound-%d", time.Now().UTC().UnixNano()),
		Samples:      samples,
		NextBaseline: nextBaseline,
	}

	s.xrayOutboundUsagePending = pending

	if err := s.persistXrayOutboundUsageStateLocked(); err != nil {
		s.xrayOutboundUsagePending = nil
		return nil, err
	}

	return xrayOutboundUsageBatchProto(pending), nil
}

func (s *Server) AckOutboundUsage(ctx context.Context, req *nodev1.AckUsageRequest) (*nodev1.AckUsageResponse, error) {
	batchID := strings.TrimSpace(req.GetBatchId())
	if batchID == "" {
		return &nodev1.AckUsageResponse{Acknowledged: false}, nil
	}

	s.xrayOutboundUsageMu.Lock()
	defer s.xrayOutboundUsageMu.Unlock()

	if err := s.ensureXrayOutboundUsageStateLoadedLocked(); err != nil {
		return nil, err
	}

	pending := s.xrayOutboundUsagePending

	if pending == nil || pending.BatchID != batchID {
		return &nodev1.AckUsageResponse{Acknowledged: false}, nil
	}

	previousBaseline := s.xrayOutboundUsageBaseline

	s.xrayOutboundUsageBaseline = pending.NextBaseline
	s.xrayOutboundUsagePending = nil

	if err := s.persistXrayOutboundUsageStateLocked(); err != nil {
		s.xrayOutboundUsageBaseline = previousBaseline
		s.xrayOutboundUsagePending = pending
		return nil, err
	}

	return &nodev1.AckUsageResponse{Acknowledged: true}, nil
}

func xrayOutboundUsageBatchProto(pending *xrayOutboundUsagePendingBatch) *nodev1.OutboundUsageBatch {
	if pending == nil {
		return &nodev1.OutboundUsageBatch{}
	}

	stats := make([]*nodev1.OutboundUsageSample, 0, len(pending.Samples))
	for _, sample := range pending.Samples {
		stats = append(stats, &nodev1.OutboundUsageSample{
			Tag:  sample.Tag,
			Up:   sample.Up,
			Down: sample.Down,
		})
	}

	return &nodev1.OutboundUsageBatch{
		BatchId: pending.BatchID,
		Stats:   stats,
	}
}

func (s *Server) StreamLogs(req *nodev1.StreamLogsRequest, stream grpc.ServerStreamingServer[nodev1.LogLine]) error {
	lines := s.snapshotLogs()
	max := int(req.GetMaxLines())
	if max <= 0 || max > len(lines) {
		max = len(lines)
	}
	lines = lines[len(lines)-max:]
	for _, line := range lines {
		if err := stream.Send(&nodev1.LogLine{
			StreamId:      req.GetStreamId(),
			Line:          line,
			EmittedAtUnix: time.Now().Unix(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) applyConfig(ctx context.Context, req *nodev1.RuntimeConfigRequest, message string) (*nodev1.RuntimeActionResponse, error) {
	configJSON := req.GetConfigJson()
	if strings.TrimSpace(configJSON) == "" {
		return nil, status.Error(codes.InvalidArgument, "config_json is required")
	}

	// Validate the API endpoint in the incoming runtime config before touching
	// disk or runtime state. This must not change the effective collector port.
	if _, _, err := xrayAPIPortFromRuntimeConfig(configJSON); err != nil {
		return nil, status.Error(codes.InvalidArgument, "xray api port: "+err.Error())
	}

	if err := os.MkdirAll(s.cfg.DataDir, 0755); err != nil {
		return nil, err
	}

	configPath := filepath.Join(s.cfg.DataDir, "xray-config.json")
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		return nil, err
	}

	// Only after the exact runtime config has been persisted may its
	// API_INBOUND port become authoritative for stats/online collectors.
	if _, _, err := s.syncXrayAPIPortFromRuntimeConfig(configJSON); err != nil {
		return nil, status.Error(codes.InvalidArgument, "xray api port: "+err.Error())
	}

	s.mu.Lock()
	s.lastConfig = configPath
	if req.GetDesiredRevision() > s.appliedRev {
		s.appliedRev = req.GetDesiredRevision()
	}
	s.mu.Unlock()

	if _, err := os.Stat(s.cfg.XrayPath); err == nil {
		if err := s.startXray(ctx, configPath); err != nil {
			return nil, status.Error(codes.Internal, "start xray: "+err.Error())
		}
		message += " and runtime started"
	} else {
		message += "; xray binary is not installed yet"
	}

	if err := s.applyNativeRuntime(req.GetOvRuntimeJson()); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}

	return s.action(req.GetOperationId(), message), nil
}
func (s *Server) startXray(ctx context.Context, configPath string) error {
	_ = s.stopRuntime()
	cmd := xrayCommandContext(context.Background(), s.cfg.XrayPath, "run", "-config", configPath)
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+s.cfg.XrayAssetsDir)
	cmd.Stdout = logWriter{server: s}
	cmd.Stderr = logWriter{server: s}
	if err := cmd.Start(); err != nil {
		s.appendLog("failed to start xray: " + err.Error())
		return err
	}
	s.mu.Lock()
	s.lastRuntime = cmd
	s.mu.Unlock()
	s.appendLog("xray runtime started")
	go func() {
		err := cmd.Wait()
		if err != nil {
			s.appendLog("xray runtime stopped: " + err.Error())
		} else {
			s.appendLog("xray runtime stopped")
		}
	}()
	return nil
}

func (s *Server) stopRuntime() error {
	s.mu.Lock()
	cmd := s.lastRuntime
	s.lastRuntime = nil
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func (s *Server) action(operationID, message string) *nodev1.RuntimeActionResponse {
	s.appendLog(message)
	return &nodev1.RuntimeActionResponse{
		OperationId: operationID,
		Accepted:    true,
		Runtime:     s.runtimeState(message),
		Message:     message,
	}
}

func (s *Server) runtimeState(message string) *nodev1.RuntimeState {
	s.mu.Lock()
	started := s.lastRuntime != nil
	applied := s.appliedRev
	torProxyCount := len(s.torProxies)
	s.mu.Unlock()
	capabilities := []string{"config_revision", "logs", "metrics", "full_config_sync", "tor_proxy"}
	if torProxyCount > 0 {
		capabilities = append(capabilities, "tor_proxy_running")
	}
	return &nodev1.RuntimeState{
		Connected:       true,
		Started:         started,
		CoreVersion:     xrayVersion(s.cfg.XrayPath),
		NodeVersion:     s.cfg.Version,
		InstallMode:     s.cfg.InstallMode,
		UpdateChannel:   s.cfg.UpdateChannel,
		Message:         message,
		Capabilities:    capabilities,
		AppliedRevision: applied,
	}
}

func torConfig(port uint32, country string, strict bool, dataDir string) string {
	lines := []string{
		"SocksPort 127.0.0.1:" + strconv.FormatUint(uint64(port), 10),
		"DataDirectory " + dataDir,
		"Log notice stdout",
	}
	if country != "" {
		lines = append(lines, "ExitNodes {"+country+"}")
		if strict {
			lines = append(lines, "StrictNodes 1")
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func (s *Server) metrics(includeRuntime bool) *nodev1.MetricsResponse {
	system := &nodev1.SystemMetrics{CpuCores: int32(runtime.NumCPU()), UptimeSeconds: uint64(time.Since(s.startedAt).Seconds())}
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		system.CpuFrequencyHz = infos[0].Mhz * 1000 * 1000
	}
	if percents, err := cpu.Percent(0, false); err == nil && len(percents) > 0 {
		system.CpuUsagePercent = percents[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		system.MemoryUsed = vm.Used
		system.MemoryTotal = vm.Total
		system.MemoryUsagePercent = vm.UsedPercent
	}
	if uptime, err := host.Uptime(); err == nil {
		system.UptimeSeconds = uptime
	}
	res := &nodev1.MetricsResponse{System: system, Transfer: &nodev1.TransferMetrics{}, SampledAtUnix: time.Now().Unix()}
	if includeRuntime {
		res.Runtime = s.runtimeState("metrics collected")
	}
	return res
}

func xrayVersion(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	out, err := exec.Command(path, "-version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) >= 2 && strings.EqualFold(fields[0], "Xray") {
		return fields[1]
	}
	return ""
}

func (s *Server) appendLog(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, time.Now().Format(time.RFC3339)+" "+line)
	if len(s.logs) > 1000 {
		s.logs = append([]string(nil), s.logs[len(s.logs)-1000:]...)
	}
}

func (s *Server) snapshotLogs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.logs...)
}

type logWriter struct {
	server *Server
}

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		w.server.appendLog(line)
	}
	return len(p), nil
}

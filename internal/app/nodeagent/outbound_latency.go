package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"golang.org/x/net/proxy"
)

const outboundLatencyDefaultURL = "https://www.google.com/generate_204"

func (s *Server) testOutboundLatency(
	ctx context.Context,
	tag string,
	outbounds []map[string]any,
	rawURL string,
	testType string,
) *nodev1.OutboundTestResponse {
	result := &nodev1.OutboundTestResponse{
		TestType: testType,
	}

	testURL := strings.TrimSpace(rawURL)
	if testURL == "" {
		testURL = outboundLatencyDefaultURL
	}

	host, port, err := outboundLatencyTarget(testURL)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Address = host
	result.Port = int32(port)

	xrayPath := strings.TrimSpace(s.cfg.XrayPath)
	if xrayPath == "" {
		result.Error = "Xray executable path is not configured"
		return result
	}

	if _, err := os.Stat(xrayPath); err != nil {
		result.Error = fmt.Sprintf("Xray executable is unavailable: %v", err)
		return result
	}

	socksPort, err := freeLoopbackPort()
	if err != nil {
		result.Error = fmt.Sprintf("allocate probe port: %v", err)
		return result
	}

	configData, err := buildOutboundLatencyConfig(tag, socksPort, outbounds)
	if err != nil {
		result.Error = fmt.Sprintf("build Xray probe config: %v", err)
		return result
	}

	baseDir := strings.TrimSpace(s.cfg.DataDir)
	if baseDir != "" {
		if err := os.MkdirAll(baseDir, 0755); err != nil {
			result.Error = fmt.Sprintf("prepare probe data directory: %v", err)
			return result
		}
	}

	tempDir, err := os.MkdirTemp(baseDir, "antimage-outbound-probe-*")
	if err != nil {
		result.Error = fmt.Sprintf("create probe directory: %v", err)
		return result
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "xray-probe.json")
	if err := os.WriteFile(configPath, configData, 0600); err != nil {
		result.Error = fmt.Sprintf("write Xray probe config: %v", err)
		return result
	}

	validateCtx, cancelValidate := context.WithTimeout(ctx, 10*time.Second)
	validateCmd := probeCommandContext(
		validateCtx,
		xrayPath,
		"run",
		"-test",
		"-config",
		configPath,
	)
	validateCmd.Env = append(
		os.Environ(),
		"XRAY_LOCATION_ASSET="+s.cfg.XrayAssetsDir,
	)

	validateOutput, validateErr := validateCmd.CombinedOutput()
	cancelValidate()

	if validateErr != nil {
		detail := strings.TrimSpace(string(validateOutput))
		if detail == "" {
			detail = validateErr.Error()
		}
		result.Error = "Xray rejected outbound probe config: " + detail
		return result
	}

	runCtx, cancelRun := context.WithCancel(ctx)

	cmd := probeCommandContext(
		runCtx,
		xrayPath,
		"run",
		"-config",
		configPath,
	)
	cmd.Env = append(
		os.Environ(),
		"XRAY_LOCATION_ASSET="+s.cfg.XrayAssetsDir,
	)

	var processOutput bytes.Buffer
	cmd.Stdout = &processOutput
	cmd.Stderr = &processOutput

	if err := cmd.Start(); err != nil {
		cancelRun()
		result.Error = fmt.Sprintf("start Xray outbound probe: %v", err)
		return result
	}

	processDone := make(chan error, 1)
	go func() {
		processDone <- cmd.Wait()
	}()

	proxyAddress := net.JoinHostPort(
		"127.0.0.1",
		strconv.Itoa(socksPort),
	)

	if err := waitForProbeListener(
		ctx,
		proxyAddress,
		processDone,
		5*time.Second,
	); err != nil {
		stopProbeProcess(cancelRun, cmd, processDone)

		detail := strings.TrimSpace(processOutput.String())
		if detail != "" {
			result.Error = fmt.Sprintf("%v: %s", err, detail)
		} else {
			result.Error = err.Error()
		}
		return result
	}

	defer stopProbeProcess(cancelRun, cmd, processDone)

	socksDialer, err := proxy.SOCKS5(
		"tcp",
		proxyAddress,
		nil,
		&net.Dialer{Timeout: 10 * time.Second},
	)
	if err != nil {
		result.Error = fmt.Sprintf("create SOCKS probe client: %v", err)
		return result
	}

	transport := &http.Transport{
		DialContext: func(
			requestCtx context.Context,
			network string,
			address string,
		) (net.Conn, error) {
			if contextDialer, ok := socksDialer.(proxy.ContextDialer); ok {
				return contextDialer.DialContext(
					requestCtx,
					network,
					address,
				)
			}
			return socksDialer.Dial(network, address)
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	defer transport.CloseIdleConnections()

	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		testURL,
		nil,
	)
	if err != nil {
		result.Error = fmt.Sprintf("create HTTP probe request: %v", err)
		return result
	}

	httpRequest.Header.Set(
		"User-Agent",
		"AntiMage-node outbound probe",
	)

	started := time.Now()

	response, err := (&http.Client{
		Transport: transport,
	}).Do(httpRequest)

	result.Delay = time.Since(started).Milliseconds()

	if err != nil {
		result.Error = fmt.Sprintf(
			"HTTP probe through outbound %q failed: %v",
			tag,
			err,
		)
		return result
	}
	defer response.Body.Close()

	_, _ = io.Copy(
		io.Discard,
		io.LimitReader(response.Body, 64*1024),
	)

	result.Success = true
	result.StatusCode = int32(response.StatusCode)
	result.Output = fmt.Sprintf(
		"HTTP %d %s via outbound %s",
		response.StatusCode,
		http.StatusText(response.StatusCode),
		tag,
	)

	return result
}

func buildOutboundLatencyConfig(
	tag string,
	socksPort int,
	outbounds []map[string]any,
) ([]byte, error) {
	config := map[string]any{
		"log": map[string]any{
			"loglevel": "warning",
		},
		"inbounds": []any{
			map[string]any{
				"tag":      "antimage-outbound-probe",
				"listen":   "127.0.0.1",
				"port":     socksPort,
				"protocol": "socks",
				"settings": map[string]any{
					"auth": "noauth",
					"udp":  false,
				},
			},
		},
		"outbounds": outbounds,
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules": []any{
				map[string]any{
					"type":        "field",
					"inboundTag":  []string{"antimage-outbound-probe"},
					"outboundTag": tag,
				},
			},
		},
	}

	return json.Marshal(config)
}

func outboundLatencyTarget(rawURL string) (string, int, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil {
		return "", 0, fmt.Errorf(
			"test URL must be a valid HTTP or HTTPS URL",
		)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", 0, fmt.Errorf(
			"test URL must use HTTP or HTTPS",
		)
	}

	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return "", 0, fmt.Errorf("test URL has no hostname")
	}

	port := 80
	if scheme == "https" {
		port = 443
	}

	if rawPort := parsed.Port(); rawPort != "" {
		parsedPort, err := strconv.Atoi(rawPort)
		if err != nil || parsedPort <= 0 || parsedPort > 65535 {
			return "", 0, fmt.Errorf(
				"test URL contains an invalid port",
			)
		}
		port = parsedPort
	}

	return host, port, nil
}

func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}

	port := listener.Addr().(*net.TCPAddr).Port

	if err := listener.Close(); err != nil {
		return 0, err
	}

	return port, nil
}

func waitForProbeListener(
	ctx context.Context,
	address string,
	processDone <-chan error,
	timeout time.Duration,
) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		conn, err := (&net.Dialer{
			Timeout: 200 * time.Millisecond,
		}).DialContext(ctx, "tcp", address)

		if err == nil {
			_ = conn.Close()
			return nil
		}

		select {
		case err := <-processDone:
			if err == nil {
				return fmt.Errorf(
					"Xray outbound probe exited before becoming ready",
				)
			}

			return fmt.Errorf(
				"Xray outbound probe exited before becoming ready: %w",
				err,
			)

		case <-ctx.Done():
			return ctx.Err()

		case <-timer.C:
			return fmt.Errorf(
				"timed out waiting for Xray outbound probe on %s",
				address,
			)

		case <-ticker.C:
		}
	}
}

func stopProbeProcess(
	cancel context.CancelFunc,
	cmd *exec.Cmd,
	processDone <-chan error,
) {
	cancel()

	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return
	}

	select {
	case <-processDone:
		return
	case <-time.After(2 * time.Second):
	}

	_ = cmd.Process.Kill()

	select {
	case <-processDone:
	case <-time.After(2 * time.Second):
	}
}

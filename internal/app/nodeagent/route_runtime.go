package nodeagent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"golang.org/x/net/proxy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) TestRoute(
	ctx context.Context,
	req *nodev1.RouteTestRequest,
) (*nodev1.RouteTestResponse, error) {
	if req == nil {
		return nil, status.Error(
			codes.InvalidArgument,
			"request is required",
		)
	}

	configJSON := strings.TrimSpace(req.GetConfigJson())
	if configJSON == "" {
		return nil, status.Error(
			codes.InvalidArgument,
			"config_json is required",
		)
	}

	testURL := strings.TrimSpace(req.GetTestUrl())
	if testURL == "" {
		return nil, status.Error(
			codes.InvalidArgument,
			"test_url is required",
		)
	}

	host, port, err := outboundLatencyTarget(testURL)
	if err != nil {
		return nil, status.Error(
			codes.InvalidArgument,
			err.Error(),
		)
	}

	xrayPath := strings.TrimSpace(s.cfg.XrayPath)
	if xrayPath == "" {
		return &nodev1.RouteTestResponse{
			Success: false,
			Error:   "Xray executable path is not configured",
		}, nil
	}

	if _, err := os.Stat(xrayPath); err != nil {
		return &nodev1.RouteTestResponse{
			Success: false,
			Error: fmt.Sprintf(
				"Xray executable is unavailable: %v",
				err,
			),
		}, nil
	}

	socksPort, err := freeLoopbackPort()
	if err != nil {
		return &nodev1.RouteTestResponse{
			Success: false,
			Error: fmt.Sprintf(
				"allocate route probe port: %v",
				err,
			),
		}, nil
	}

	apiPort, err := freeLoopbackPort()
	if err != nil {
		return &nodev1.RouteTestResponse{
			Success: false,
			Error: fmt.Sprintf(
				"allocate route API port: %v",
				err,
			),
		}, nil
	}

	if apiPort == socksPort {
		apiPort, err = freeLoopbackPort()
		if err != nil || apiPort == socksPort {
			return &nodev1.RouteTestResponse{
				Success: false,
				Error:   "could not allocate distinct route probe ports",
			}, nil
		}
	}

	configData, _, err := buildRouteProbeConfig(
		configJSON,
		req.GetInboundTag(),
		socksPort,
		apiPort,
	)
	if err != nil {
		return &nodev1.RouteTestResponse{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	baseDir := strings.TrimSpace(s.cfg.DataDir)
	if baseDir != "" {
		if err := os.MkdirAll(baseDir, 0755); err != nil {
			return &nodev1.RouteTestResponse{
				Success: false,
				Error: fmt.Sprintf(
					"prepare route probe data directory: %v",
					err,
				),
			}, nil
		}
	}

	tempDir, err := os.MkdirTemp(
		baseDir,
		"antimage-route-probe-*",
	)
	if err != nil {
		return &nodev1.RouteTestResponse{
			Success: false,
			Error: fmt.Sprintf(
				"create route probe directory: %v",
				err,
			),
		}, nil
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(
		tempDir,
		"xray-route-probe.json",
	)

	if err := os.WriteFile(
		configPath,
		configData,
		0600,
	); err != nil {
		return &nodev1.RouteTestResponse{
			Success: false,
			Error: fmt.Sprintf(
				"write route probe config: %v",
				err,
			),
		}, nil
	}

	validateCtx, cancelValidate := context.WithTimeout(
		ctx,
		10*time.Second,
	)

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
		detail := strings.TrimSpace(
			string(validateOutput),
		)
		if detail == "" {
			detail = validateErr.Error()
		}

		return &nodev1.RouteTestResponse{
			Success: false,
			Error: "Xray rejected route probe config: " +
				detail,
		}, nil
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

		return &nodev1.RouteTestResponse{
			Success: false,
			Error: fmt.Sprintf(
				"start Xray route probe: %v",
				err,
			),
		}, nil
	}

	processDone := make(chan error, 1)
	go func() {
		processDone <- cmd.Wait()
	}()

	socksAddress := net.JoinHostPort(
		"127.0.0.1",
		strconv.Itoa(socksPort),
	)

	if err := waitForProbeListener(
		ctx,
		socksAddress,
		processDone,
		5*time.Second,
	); err != nil {
		stopProbeProcess(
			cancelRun,
			cmd,
			processDone,
		)

		detail := strings.TrimSpace(
			processOutput.String(),
		)

		if detail != "" {
			err = fmt.Errorf(
				"%v: %s",
				err,
				detail,
			)
		}

		return &nodev1.RouteTestResponse{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	defer stopProbeProcess(
		cancelRun,
		cmd,
		processDone,
	)

	socksDialer, err := proxy.SOCKS5(
		"tcp",
		socksAddress,
		nil,
		&net.Dialer{
			Timeout: 10 * time.Second,
		},
	)
	if err != nil {
		return &nodev1.RouteTestResponse{
			Success: false,
			Error: fmt.Sprintf(
				"create route SOCKS client: %v",
				err,
			),
		}, nil
	}

	transport := &http.Transport{
		DialContext: func(
			requestCtx context.Context,
			network string,
			address string,
		) (net.Conn, error) {
			if contextDialer, ok :=
				socksDialer.(proxy.ContextDialer); ok {
				return contextDialer.DialContext(
					requestCtx,
					network,
					address,
				)
			}

			return socksDialer.Dial(
				network,
				address,
			)
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
		return &nodev1.RouteTestResponse{
			Success: false,
			Error: fmt.Sprintf(
				"create route test request: %v",
				err,
			),
		}, nil
	}

	httpRequest.Header.Set(
		"User-Agent",
		"AntiMage-node route probe",
	)

	started := time.Now()

	httpResponse, err := (&http.Client{
		Transport: transport,
	}).Do(httpRequest)

	delay := time.Since(started).Milliseconds()

	if err != nil {
		return &nodev1.RouteTestResponse{
			Success: false,
			Delay:   delay,
			Error: fmt.Sprintf(
				"route HTTP probe to %s:%d failed: %v",
				host,
				port,
				err,
			),
		}, nil
	}
	defer httpResponse.Body.Close()

	_, _ = io.Copy(
		io.Discard,
		io.LimitReader(
			httpResponse.Body,
			64*1024,
		),
	)

	outboundTag, groupTags, traffic, err :=
		s.queryRouteStatsWithRetry(
			ctx,
			xrayPath,
			apiPort,
		)

	if err != nil {
		return &nodev1.RouteTestResponse{
			Success:    false,
			Delay:      delay,
			StatusCode: int32(httpResponse.StatusCode),
			Error: fmt.Sprintf(
				"query route statistics: %v",
				err,
			),
		}, nil
	}

	if outboundTag == "" {
		return &nodev1.RouteTestResponse{
			Success:    false,
			Matched:    false,
			Delay:      delay,
			StatusCode: int32(httpResponse.StatusCode),
			Error: "route probe completed but no outbound " +
				"traffic counter was recorded",
		}, nil
	}

	return &nodev1.RouteTestResponse{
		Matched:         true,
		OutboundTag:     outboundTag,
		GroupTags:       groupTags,
		Success:         true,
		Delay:           delay,
		StatusCode:      int32(httpResponse.StatusCode),
		OutboundTraffic: traffic,
	}, nil
}

func (s *Server) queryRouteStatsWithRetry(
	ctx context.Context,
	xrayPath string,
	apiPort int,
) (
	string,
	[]string,
	[]*nodev1.RouteTestTraffic,
	error,
) {
	deadline := time.Now().Add(2 * time.Second)

	var lastErr error

	for {
		tag, groups, traffic, err := queryRouteStats(
			ctx,
			xrayPath,
			apiPort,
		)

		if err == nil && tag != "" {
			return tag, groups, traffic, nil
		}

		if err != nil {
			lastErr = err
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return nilRouteStats(lastErr)
			}

			return "", nil, nil, nil
		}

		select {
		case <-ctx.Done():
			return nilRouteStats(ctx.Err())

		case <-time.After(100 * time.Millisecond):
		}
	}
}

func nilRouteStats(
	err error,
) (
	string,
	[]string,
	[]*nodev1.RouteTestTraffic,
	error,
) {
	return "", nil, nil, err
}

func queryRouteStats(
	ctx context.Context,
	xrayPath string,
	apiPort int,
) (
	string,
	[]string,
	[]*nodev1.RouteTestTraffic,
	error,
) {
	queryCtx, cancel := context.WithTimeout(
		ctx,
		5*time.Second,
	)
	defer cancel()

	server := net.JoinHostPort(
		"127.0.0.1",
		strconv.Itoa(apiPort),
	)

	cmd := probeCommandContext(
		queryCtx,
		xrayPath,
		"api",
		"statsquery",
		"--server="+server,
		"-pattern",
		"outbound>>>",
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(
			stderr.String(),
		)

		var exitErr *exec.ExitError
		if detail == "" &&
			errorAsExitError(err, &exitErr) &&
			len(exitErr.Stderr) > 0 {
			detail = strings.TrimSpace(
				string(exitErr.Stderr),
			)
		}

		if detail == "" {
			detail = err.Error()
		}

		return "", nil, nil, fmt.Errorf(
			"Xray statsquery failed: %s",
			detail,
		)
	}

	return parseRouteOutboundStats(output)
}

func errorAsExitError(
	err error,
	target **exec.ExitError,
) bool {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}

	*target = exitErr
	return true
}

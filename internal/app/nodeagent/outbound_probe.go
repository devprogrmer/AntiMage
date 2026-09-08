package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var probeCommandContext = exec.CommandContext

func (s *Server) TestOutbound(ctx context.Context, req *nodev1.OutboundTestRequest) (*nodev1.OutboundTestResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}

	tag := strings.TrimSpace(req.GetOutboundTag())
	if tag == "" {
		return nil, status.Error(codes.InvalidArgument, "outbound_tag is required")
	}

	outbounds, err := decodeOutboundList(req.GetAllOutboundsJson())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	outbound, ok := findOutboundByTag(outbounds, tag)
	if !ok {
		return &nodev1.OutboundTestResponse{
			Success:  false,
			Error:    "selected outbound was not found",
			TestType: normalizeNodeOutboundTestType(req.GetTestType()),
		}, nil
	}

	protocol := strings.ToLower(strings.TrimSpace(stringValue(outbound["protocol"])))
	if protocol == "blackhole" || strings.EqualFold(tag, "blocked") {
		return &nodev1.OutboundTestResponse{
			Success:  false,
			Error:    "blocked/blackhole outbound cannot be tested",
			TestType: normalizeNodeOutboundTestType(req.GetTestType()),
		}, nil
	}

	testType := normalizeNodeOutboundTestType(req.GetTestType())

	switch testType {
	case "tcp":
		return testOutboundTCP(ctx, outbound, testType), nil
	case "icmp":
		return testOutboundICMP(ctx, outbound, testType), nil
	case "latency":
		return s.testOutboundLatency(ctx, tag, outbounds, req.GetTestUrl(), testType), nil
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unsupported outbound test type %q", testType)
	}
}

func normalizeNodeOutboundTestType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "http", "latency":
		return "latency"
	case "tcp":
		return "tcp"
	case "icmp", "ping":
		return "icmp"
	default:
		return "latency"
	}
}

func decodeOutboundList(raw string) ([]map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("all_outbounds_json is required")
	}

	var outbounds []map[string]any
	if err := json.Unmarshal([]byte(raw), &outbounds); err != nil {
		return nil, fmt.Errorf("invalid all_outbounds_json: %w", err)
	}

	if len(outbounds) == 0 {
		return nil, fmt.Errorf("all_outbounds_json must not be empty")
	}

	return outbounds, nil
}

func findOutboundByTag(outbounds []map[string]any, tag string) (map[string]any, bool) {
	for _, outbound := range outbounds {
		if strings.TrimSpace(stringValue(outbound["tag"])) == tag {
			return outbound, true
		}
	}
	return nil, false
}

func testOutboundTCP(ctx context.Context, outbound map[string]any, testType string) *nodev1.OutboundTestResponse {
	host, port, err := outboundProbeEndpoint(outbound)
	result := &nodev1.OutboundTestResponse{
		TestType: testType,
		Address:  host,
		Port:     int32(port),
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if port <= 0 {
		result.Error = "selected outbound does not provide a TCP port"
		return result
	}

	address := net.JoinHostPort(host, strconv.Itoa(port))
	started := time.Now()

	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	result.Delay = time.Since(started).Milliseconds()

	if err != nil {
		result.Error = err.Error()
		return result
	}
	_ = conn.Close()

	result.Success = true
	result.Output = "TCP connection established to " + address
	return result
}

func testOutboundICMP(ctx context.Context, outbound map[string]any, testType string) *nodev1.OutboundTestResponse {
	host, port, err := outboundProbeEndpoint(outbound)
	result := &nodev1.OutboundTestResponse{
		TestType: testType,
		Address:  host,
		Port:     int32(port),
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}

	name, args := pingCommand(host)

	started := time.Now()
	output, err := probeCommandContext(ctx, name, args...).CombinedOutput()
	result.Delay = time.Since(started).Milliseconds()
	result.Output = strings.TrimSpace(string(output))

	if err != nil {
		if result.Output != "" {
			result.Error = result.Output
		} else {
			result.Error = err.Error()
		}
		return result
	}

	result.Success = true
	return result
}

func pingCommand(host string) (string, []string) {
	switch runtime.GOOS {
	case "windows":
		return "ping", []string{"-n", "1", "-w", "5000", host}
	case "darwin":
		return "ping", []string{"-c", "1", "-W", "5000", host}
	default:
		return "ping", []string{"-c", "1", "-W", "5", host}
	}
}

func outboundProbeEndpoint(outbound map[string]any) (string, int, error) {
	protocol := strings.ToLower(strings.TrimSpace(stringValue(outbound["protocol"])))
	settings := objectValue(outbound["settings"])

	switch protocol {
	case "vmess":
		return endpointFromServerList(settings["vnext"])

	case "vless":
		if address := strings.TrimSpace(stringValue(settings["address"])); address != "" {
			return address, intValue(settings["port"]), nil
		}
		return endpointFromServerList(settings["vnext"])

	case "http", "socks", "shadowsocks", "trojan":
		return endpointFromServerList(settings["servers"])

	case "dns":
		host := strings.TrimSpace(stringValue(settings["address"]))
		if host == "" {
			return "", 0, fmt.Errorf("selected outbound does not provide an address")
		}
		port := intValue(settings["port"])
		if port == 0 {
			port = 53
		}
		return host, port, nil

	case "wireguard":
		peers := arrayValue(settings["peers"])
		if len(peers) == 0 {
			return "", 0, fmt.Errorf("selected WireGuard outbound has no peers")
		}

		peer := objectValue(peers[0])
		endpoint := strings.TrimSpace(stringValue(peer["endpoint"]))
		if endpoint == "" {
			return "", 0, fmt.Errorf("selected WireGuard peer has no endpoint")
		}

		host, rawPort, err := net.SplitHostPort(endpoint)
		if err != nil {
			return endpoint, 0, nil
		}

		port, _ := strconv.Atoi(rawPort)
		return strings.Trim(host, "[]"), port, nil

	default:
		return "", 0, fmt.Errorf("selected outbound does not provide a probe endpoint")
	}
}

func endpointFromServerList(value any) (string, int, error) {
	servers := arrayValue(value)
	if len(servers) == 0 {
		return "", 0, fmt.Errorf("selected outbound does not provide a server")
	}

	server := objectValue(servers[0])
	host := strings.TrimSpace(stringValue(server["address"]))
	if host == "" {
		return "", 0, fmt.Errorf("selected outbound server has no address")
	}

	return host, intValue(server["port"]), nil
}

func objectValue(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return map[string]any{}
}

func arrayValue(value any) []any {
	if result, ok := value.([]any); ok {
		return result
	}
	return nil
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return ""
	}
}

func intValue(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case float32:
		return int(v)
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case json.Number:
		n, _ := strconv.Atoi(v.String())
		return n
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}

package nodeagent

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestOutboundProbeEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		outbound map[string]any
		wantHost string
		wantPort int
	}{
		{
			name: "vless vnext",
			outbound: map[string]any{
				"protocol": "vless",
				"settings": map[string]any{
					"vnext": []any{
						map[string]any{
							"address": "example.com",
							"port":    float64(443),
						},
					},
				},
			},
			wantHost: "example.com",
			wantPort: 443,
		},
		{
			name: "flat vless",
			outbound: map[string]any{
				"protocol": "vless",
				"settings": map[string]any{
					"address": "1.2.3.4",
					"port":    float64(8443),
				},
			},
			wantHost: "1.2.3.4",
			wantPort: 8443,
		},
		{
			name: "wireguard",
			outbound: map[string]any{
				"protocol": "wireguard",
				"settings": map[string]any{
					"peers": []any{
						map[string]any{
							"endpoint": "1.1.1.1:2408",
						},
					},
				},
			},
			wantHost: "1.1.1.1",
			wantPort: 2408,
		},
		{
			name: "dns default port",
			outbound: map[string]any{
				"protocol": "dns",
				"settings": map[string]any{
					"address": "8.8.8.8",
				},
			},
			wantHost: "8.8.8.8",
			wantPort: 53,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host, port, err := outboundProbeEndpoint(tc.outbound)
			if err != nil {
				t.Fatalf("outboundProbeEndpoint returned error: %v", err)
			}
			if host != tc.wantHost || port != tc.wantPort {
				t.Fatalf(
					"endpoint=%s:%d want=%s:%d",
					host,
					port,
					tc.wantHost,
					tc.wantPort,
				)
			}
		})
	}
}

func TestOutboundTCPConnectsToSelectedServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port

	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
		accepted <- err
	}()

	s := New(Config{})

	allOutbounds := fmt.Sprintf(
		`[{"tag":"test-vless","protocol":"vless","settings":{"vnext":[{"address":"127.0.0.1","port":%d}]}}]`,
		port,
	)

	res, err := s.TestOutbound(context.Background(), &nodev1.OutboundTestRequest{
		OutboundTag:      "test-vless",
		OutboundProtocol: "vless",
		AllOutboundsJson: allOutbounds,
		TestType:         "tcp",
	})
	if err != nil {
		t.Fatalf("TestOutbound returned error: %v", err)
	}
	if !res.GetSuccess() {
		t.Fatalf("TCP test failed: %s", res.GetError())
	}
	if res.GetAddress() != "127.0.0.1" {
		t.Fatalf("address=%q", res.GetAddress())
	}
	if res.GetPort() != int32(port) {
		t.Fatalf("port=%d want=%d", res.GetPort(), port)
	}
	if res.GetTestType() != "tcp" {
		t.Fatalf("test type=%q", res.GetTestType())
	}
	if !strings.Contains(res.GetOutput(), strconv.Itoa(port)) {
		t.Fatalf("unexpected output=%q", res.GetOutput())
	}

	if err := <-accepted; err != nil {
		t.Fatalf("server accept failed: %v", err)
	}
}

func TestOutboundICMPRunsPingProbe(t *testing.T) {
	previous := probeCommandContext
	t.Cleanup(func() {
		probeCommandContext = previous
	})

	var commandName string
	var commandArgs []string

	probeCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		commandName = name
		commandArgs = append([]string(nil), args...)

		return exec.CommandContext(
			ctx,
			os.Args[0],
			"-test.run=TestNodeAgentHelperProcess",
			"--",
			"runtime-exit",
		)
	}

	s := New(Config{})

	res, err := s.TestOutbound(context.Background(), &nodev1.OutboundTestRequest{
		OutboundTag: "dns-out",
		AllOutboundsJson: `[
{
"tag":"dns-out",
"protocol":"dns",
"settings":{"address":"8.8.8.8","port":53}
}
]`,
		TestType: "icmp",
	})
	if err != nil {
		t.Fatalf("TestOutbound returned error: %v", err)
	}
	if !res.GetSuccess() {
		t.Fatalf("ICMP test failed: %s", res.GetError())
	}
	if commandName != "ping" {
		t.Fatalf("command=%q want ping", commandName)
	}

	foundHost := false
	for _, arg := range commandArgs {
		if arg == "8.8.8.8" {
			foundHost = true
			break
		}
	}
	if !foundHost {
		t.Fatalf("ping args do not contain target host: %#v", commandArgs)
	}
}

func TestOutboundRejectsUnknownTag(t *testing.T) {
	s := New(Config{})

	res, err := s.TestOutbound(context.Background(), &nodev1.OutboundTestRequest{
		OutboundTag:      "missing",
		AllOutboundsJson: `[{"tag":"direct","protocol":"freedom"}]`,
		TestType:         "tcp",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if res.GetSuccess() {
		t.Fatal("expected unknown outbound to fail")
	}
	if !strings.Contains(res.GetError(), "not found") {
		t.Fatalf("unexpected error=%q", res.GetError())
	}
}

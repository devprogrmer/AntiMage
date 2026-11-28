//go:build linux

package nodeagent

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNetworkIntelligenceOptimizationApplyRollbackInDisposableGuest(t *testing.T) {
	if os.Getenv("ANTIMAGE_WIREGUARD_NI_E2E") != "1" {
		t.Skip("requires the isolated Linux network guest")
	}
	if _, err := os.Stat("/run/antimage-disposable-network-lab"); err != nil {
		t.Fatal("refusing network optimization test outside marked disposable guest")
	}
	if os.Geteuid() != 0 {
		t.Fatal("isolated optimization fixture requires root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func(command string, args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v failed: %v: %s", command, args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	const (
		iface       = "nioptwg0"
		nodeID      = int64(71401)
		userID      = int64(42401)
		operationID = "1234567890abcdef1234567890abcdef"
		target      = "127.0.0.1"
	)
	if _, err := net.InterfaceByName(iface); err == nil {
		t.Fatalf("fixture interface %s already exists", iface)
	}
	run("ip", "link", "add", iface, "type", "wireguard")
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", iface).Run() })
	run("ip", "link", "set", "dev", iface, "mtu", "1420")
	run("ip", "addr", "add", "10.77.0.2/32", "dev", iface)
	run("ip", "addr", "add", "10.77.0.3/32", "dev", iface)
	run("ip", "link", "set", "dev", iface, "up")

	agent := New(Config{DataDir: t.TempDir(), NetworkIntelligenceEnabled: true, NetworkOptimizationEnabled: true})
	defer agent.stopNetworkIntelligence()
	binding := ni.Binding{
		NodeID: nodeID, UserID: userID, Interface: iface, Address: netip.MustParseAddr("10.77.0.2"),
		Session: "isolated-active-peer", Generation: 1, Since: time.Now().UTC().Add(-time.Second),
	}
	if err := agent.networkIntelligenceTracker.Bind(binding); err != nil {
		t.Fatal(err)
	}
	request := &nodev1.NetworkOptimizationRequest{
		Action: nodev1.NetworkOptimizationRequest_APPLY_TCP_MSS, NodeId: nodeID, UserId: userID,
		OperationId: operationID, TcpMss: 1340,
	}
	t.Cleanup(func() {
		_, _ = agent.ManageNetworkOptimization(context.Background(), &nodev1.NetworkOptimizationRequest{
			Action: nodev1.NetworkOptimizationRequest_ROLLBACK, NodeId: nodeID, UserId: userID, OperationId: operationID,
		})
	})
	if _, err := agent.ManageNetworkOptimization(ctx, &nodev1.NetworkOptimizationRequest{
		Action: nodev1.NetworkOptimizationRequest_APPLY_TCP_MSS, NodeId: nodeID, UserId: userID + 1,
		OperationId: "2234567890abcdef1234567890abcdef", TcpMss: 1340,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unbound account applied an optimization: %v", err)
	}
	if _, err := agent.ManageNetworkOptimization(ctx, request); err != nil {
		t.Fatal(err)
	}
	state, _, err := loadNetworkOptimizationState(agent.cfg.DataDir)
	if err != nil || state.Operations[operationID].State != "active" {
		t.Fatalf("durable active state=%+v err=%v", state.Operations[operationID], err)
	}
	rule := state.Operations[operationID].Rules[0]
	present, err := networkOptimizationRulePresent(ctx, rule)
	if err != nil || !present {
		t.Fatalf("scoped MSS rule absent: present=%t err=%v", present, err)
	}

	optimizedMSS := captureLoopbackSYNMSS(t, ctx, target, "10.77.0.2")
	if optimizedMSS != 1340 {
		t.Fatalf("authenticated user's TCP SYN MSS=%d, want 1340", optimizedMSS)
	}
	unmodifiedMSS := captureLoopbackSYNMSS(t, ctx, target, "10.77.0.3")
	if unmodifiedMSS == 1340 {
		t.Fatal("unbound control address inherited another user's MSS setting")
	}

	// Reassignment/unbind must not prevent an administrator from removing the
	// exact recorded rule; the node validates operation ownership from its ledger.
	agent.networkIntelligenceTracker.Unbind(binding)
	result, err := agent.ManageNetworkOptimization(ctx, &nodev1.NetworkOptimizationRequest{
		Action: nodev1.NetworkOptimizationRequest_ROLLBACK, NodeId: nodeID, UserId: userID, OperationId: operationID,
	})
	if err != nil || result.GetApplied() || result.GetState() != "rolled_back" {
		t.Fatalf("rollback result=%+v err=%v", result, err)
	}
	present, err = networkOptimizationRulePresent(ctx, rule)
	if err != nil || present {
		t.Fatalf("rollback left MSS rule installed: present=%t err=%v", present, err)
	}
	restarted := New(Config{DataDir: agent.cfg.DataDir})
	result, err = restarted.ManageNetworkOptimization(ctx, &nodev1.NetworkOptimizationRequest{
		Action: nodev1.NetworkOptimizationRequest_ROLLBACK, NodeId: nodeID, UserId: userID, OperationId: operationID,
	})
	if err != nil || result.GetApplied() || result.GetState() != "rolled_back" {
		t.Fatalf("post-restart idempotent rollback=%+v err=%v", result, err)
	}

	// Existing terminal mangle verdicts can bypass appended rules; they must
	// cause a safe refusal and be left untouched.
	if err := agent.networkIntelligenceTracker.Bind(binding); err != nil {
		t.Fatal(err)
	}
	run("iptables", "-w", "-t", "mangle", "-I", "POSTROUTING", "1", "-s", "10.77.0.2", "-j", "ACCEPT")
	defer func() {
		_ = exec.Command("iptables", "-w", "-t", "mangle", "-D", "POSTROUTING", "-s", "10.77.0.2", "-j", "ACCEPT").Run()
	}()
	if _, err := agent.ManageNetworkOptimization(ctx, &nodev1.NetworkOptimizationRequest{
		Action: nodev1.NetworkOptimizationRequest_APPLY_TCP_MSS, NodeId: nodeID, UserId: userID,
		OperationId: "3234567890abcdef1234567890abcdef", TcpMss: 1340,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("conflicting terminal rule was not rejected: %v", err)
	}
}

func captureLoopbackSYNMSS(t *testing.T, ctx context.Context, destination, source string) int {
	t.Helper()
	listener, err := net.Listen("tcp4", net.JoinHostPort(destination, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	accepted := make(chan struct{})
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = connection.Close()
			close(accepted)
		}
	}()
	filter := fmt.Sprintf("tcp and src host %s and dst port %d and (tcp[13] & 2 != 0)", source, port)
	capture := exec.CommandContext(ctx, "tcpdump", "-l", "-nn", "-vv", "-c", "1", "-i", "lo", filter)
	output := make(chan []byte, 1)
	go func() {
		data, _ := capture.CombinedOutput()
		output <- data
	}()
	time.Sleep(100 * time.Millisecond)
	dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(source)}}
	connection, err := dialer.DialContext(ctx, "tcp4", net.JoinHostPort(destination, strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	select {
	case <-accepted:
	case <-ctx.Done():
		t.Fatal("loopback TCP listener did not accept optimization probe")
	}
	select {
	case data := <-output:
		text := string(data)
		marker := "mss "
		index := strings.Index(text, marker)
		if index < 0 {
			t.Fatalf("tcpdump did not expose SYN MSS: %s", text)
		}
		end := index + len(marker)
		for end < len(text) && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		mss, err := strconv.Atoi(text[index+len(marker) : end])
		if err != nil {
			t.Fatalf("invalid tcpdump MSS in %q: %v", text, err)
		}
		return mss
	case <-ctx.Done():
		_ = capture.Process.Kill()
		t.Fatal("tcpdump did not capture TCP SYN")
		return 0
	}
}

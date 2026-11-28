//go:build linux

package nodeagent

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestNetworkInfrastructureFaultInjectionInDisposableGuest exercises real
// kernel GRE, MTU/PMTU, IPv4 fragmentation, qdisc loss, and route failure. It
// owns only a dedicated veth, GRE link, and namespace in the disposable CI VM.
func TestNetworkInfrastructureFaultInjectionInDisposableGuest(t *testing.T) {
	if os.Getenv("ANTIMAGE_WIREGUARD_NI_E2E") != "1" {
		t.Skip("requires the disposable Network Intelligence Linux guest")
	}
	if _, err := os.Stat("/run/antimage-disposable-network-lab"); err != nil {
		t.Fatal("refusing network fault injection outside the explicitly marked disposable guest")
	}
	if os.Geteuid() != 0 {
		t.Fatal("isolated network fault test requires root inside its disposable guest")
	}
	for _, command := range []string{"ip", "tc", "ping"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Fatalf("required isolated-test tool %q is unavailable: %v", command, err)
		}
	}
	for _, name := range []string{"nif0", "nif1", "nigr0"} {
		if _, err := net.InterfaceByName(name); err == nil {
			t.Fatalf("refusing to reuse existing interface %q", name)
		}
	}
	if err := exec.Command("ip", "netns", "exec", "ni-fault-peer", "true").Run(); err == nil {
		t.Fatal("refusing to reuse existing namespace ni-fault-peer")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func(allowFailure bool, command string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.CommandContext(ctx, command, args...)
		output, err := cmd.CombinedOutput()
		if err != nil && !allowFailure {
			t.Fatalf("isolated command %s %s failed: %v: %s", command, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
		return strings.TrimSpace(string(output)), err
	}
	run(false, "ip", "netns", "add", "ni-fault-peer")
	run(false, "ip", "link", "add", "nif0", "type", "veth", "peer", "name", "nif1")
	run(false, "ip", "link", "set", "nif1", "netns", "ni-fault-peer")
	t.Cleanup(func() {
		_ = exec.Command("tc", "qdisc", "del", "dev", "nif0", "root").Run()
		_ = exec.Command("ip", "link", "del", "nigr0").Run()
		_ = exec.Command("ip", "netns", "del", "ni-fault-peer").Run()
		_ = exec.Command("ip", "link", "del", "nif0").Run()
	})
	run(false, "ip", "addr", "add", "198.18.0.1/30", "dev", "nif0")
	run(false, "ip", "link", "set", "nif0", "up")
	run(false, "ip", "-n", "ni-fault-peer", "addr", "add", "198.18.0.2/30", "dev", "nif1")
	run(false, "ip", "-n", "ni-fault-peer", "link", "set", "lo", "up")
	run(false, "ip", "-n", "ni-fault-peer", "link", "set", "nif1", "up")
	run(false, "ip", "tunnel", "add", "nigr0", "mode", "gre", "local", "198.18.0.1", "remote", "198.18.0.2", "ttl", "64")
	run(false, "ip", "addr", "add", "192.0.2.1/30", "dev", "nigr0")
	run(false, "ip", "link", "set", "nigr0", "mtu", "1200")
	run(false, "ip", "link", "set", "nigr0", "up")
	run(false, "ip", "-n", "ni-fault-peer", "tunnel", "add", "nigr1", "mode", "gre", "local", "198.18.0.2", "remote", "198.18.0.1", "ttl", "64")
	run(false, "ip", "-n", "ni-fault-peer", "addr", "add", "192.0.2.2/30", "dev", "nigr1")
	run(false, "ip", "-n", "ni-fault-peer", "link", "set", "nigr1", "mtu", "1200")
	run(false, "ip", "-n", "ni-fault-peer", "link", "set", "nigr1", "up")

	// Establish baseline tunnel reachability and confirm the production sampler
	// sees the GRE MTU rather than presenting cumulative counters as rates.
	run(false, "ping", "-I", "nigr0", "-M", "do", "-s", "1100", "-c", "2", "-W", "2", "192.0.2.2")
	sampler := networkInfrastructureSampler{}
	beforeCounters, beforeInterfaces, err := readNetworkInfrastructure()
	if err != nil {
		t.Fatal(err)
	}
	before := sampler.observe(time.Now(), beforeCounters, beforeInterfaces)
	if before.Rates != nil {
		t.Fatal("first infrastructure sample unexpectedly reported counter rates")
	}
	foundGRE := false
	for _, item := range before.Interfaces {
		if item.Name == "nigr0" && item.MTU == 1200 {
			foundGRE = true
		}
	}
	if !foundGRE {
		t.Fatalf("sample did not expose isolated GRE interface and MTU: %+v", before.Interfaces)
	}

	// A larger inner packet with DF set than the receiver tunnel MTU must fail;
	// a packet within both MTUs must still pass after restoration.
	run(false, "ip", "-n", "ni-fault-peer", "link", "set", "nigr1", "mtu", "1000")
	if _, err := run(true, "ip", "netns", "exec", "ni-fault-peer", "ping", "-I", "nigr1", "-M", "do", "-s", "1100", "-c", "1", "-W", "1", "192.0.2.1"); err == nil {
		t.Fatal("DF probe unexpectedly passed the reduced GRE MTU")
	}
	run(false, "ip", "-n", "ni-fault-peer", "link", "set", "nigr1", "mtu", "1200")
	run(false, "ping", "-I", "nigr0", "-M", "do", "-s", "1100", "-c", "1", "-W", "2", "192.0.2.2")

	// Force real IPv4 fragmentation on the dedicated underlay veth and validate
	// that the kernel's interval counter increases after the observed probe.
	run(false, "ip", "link", "set", "nif0", "mtu", "1300")
	run(false, "ip", "-n", "ni-fault-peer", "link", "set", "nif1", "mtu", "1300")
	fragmentBefore, _, err := readNetworkInfrastructure()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fragmentBefore["ip.FragCreates"]; !ok {
		t.Fatal("kernel fragmentation counter is unavailable")
	}
	run(false, "ping", "-I", "nif0", "-M", "dont", "-s", "1400", "-c", "2", "-W", "2", "198.18.0.2")
	fragmentAfter, interfacesAfter, err := readNetworkInfrastructure()
	if err != nil {
		t.Fatal(err)
	}
	if fragmentAfter["ip.FragCreates"] <= fragmentBefore["ip.FragCreates"] {
		t.Fatalf("oversized DF-clear probes did not increment FragCreates: before=%d after=%d", fragmentBefore["ip.FragCreates"], fragmentAfter["ip.FragCreates"])
	}
	fragmentRate := sampler.observe(time.Now().Add(time.Second), fragmentAfter, interfacesAfter)
	if fragmentRate.Rates["ip.FragCreates"] <= 0 {
		t.Fatalf("sampler did not convert cumulative fragmentation counter to a positive interval rate: %+v", fragmentRate.Rates)
	}

	// Isolated qdisc loss verifies impairment evidence and recovery without
	// changing a system/production interface.
	run(false, "tc", "qdisc", "add", "dev", "nif0", "root", "netem", "loss", "100%")
	if _, err := run(true, "ping", "-I", "nif0", "-c", "1", "-W", "1", "198.18.0.2"); err == nil {
		t.Fatal("ping unexpectedly survived 100% loss on isolated qdisc")
	}
	qdisc, _ := run(false, "tc", "-s", "qdisc", "show", "dev", "nif0")
	if !strings.Contains(qdisc, "dropped") {
		t.Fatalf("qdisc output has no drop evidence: %s", qdisc)
	}
	run(false, "tc", "qdisc", "del", "dev", "nif0", "root")
	run(false, "ping", "-I", "nif0", "-c", "1", "-W", "2", "198.18.0.2")

	// A blackhole route models a routing fault and must fail, then recover when
	// the fixture-only route is removed.
	run(false, "ip", "route", "add", "blackhole", "198.18.0.2/32")
	if _, err := run(true, "ping", "-I", "nigr0", "-c", "1", "-W", "1", "192.0.2.2"); err == nil {
		t.Fatal("probe unexpectedly survived isolated blackhole route")
	}
	run(false, "ip", "route", "del", "blackhole", "198.18.0.2/32")
	run(false, "ping", "-I", "nigr0", "-c", "1", "-W", "2", "192.0.2.2")
}

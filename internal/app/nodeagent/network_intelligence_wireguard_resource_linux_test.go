//go:build linux

package nodeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	"golang.org/x/sys/unix"
)

// TestNetworkIntelligenceWireGuardForwardingResourceComparison measures a
// real encrypted client -> node -> downstream UDP echo path with capture off
// and on. It runs only in the disposable CI guest and uses fixture-owned links.
func TestNetworkIntelligenceWireGuardForwardingResourceComparison(t *testing.T) {
	if os.Getenv("ANTIMAGE_NI_WG_RESOURCE_E2E") != "1" {
		t.Skip("opt-in isolated WireGuard forwarding benchmark")
	}
	if os.Getenv("ANTIMAGE_WIREGUARD_NI_E2E") != "1" {
		t.Fatal("WireGuard forwarding benchmark requires the isolated network fixture")
	}
	if _, err := os.Stat("/run/antimage-disposable-network-lab"); err != nil {
		t.Fatal("refusing WireGuard benchmark outside the explicitly marked disposable guest")
	}
	if os.Geteuid() != 0 {
		t.Fatal("isolated WireGuard benchmark requires root inside its disposable guest")
	}
	for _, name := range []string{"niwc0", "niwc1", "niwd0", "niwd1", "niuwg"} {
		if _, err := net.InterfaceByName(name); err == nil {
			t.Fatalf("refusing to reuse existing interface %q", name)
		}
	}
	for _, name := range []string{"ni-wg-load-client", "ni-wg-load-dest"} {
		if err := exec.Command("ip", "netns", "exec", name, "true").Run(); err == nil {
			t.Fatalf("refusing to reuse existing namespace %q", name)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run := func(input, command string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Stdin = strings.NewReader(input)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated benchmark command %s %s failed: %v: %s", command, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
		return strings.TrimSpace(string(output))
	}
	run("", "ip", "netns", "add", "ni-wg-load-client")
	run("", "ip", "netns", "add", "ni-wg-load-dest")
	run("", "ip", "link", "add", "niwc0", "type", "veth", "peer", "name", "niwc1")
	run("", "ip", "link", "set", "niwc1", "netns", "ni-wg-load-client")
	run("", "ip", "link", "add", "niwd0", "type", "veth", "peer", "name", "niwd1")
	run("", "ip", "link", "set", "niwd1", "netns", "ni-wg-load-dest")
	run("", "ip", "addr", "add", "172.31.111.1/30", "dev", "niwc0")
	run("", "ip", "link", "set", "niwc0", "up")
	run("", "ip", "-n", "ni-wg-load-client", "addr", "add", "172.31.111.2/30", "dev", "niwc1")
	run("", "ip", "-n", "ni-wg-load-client", "link", "set", "lo", "up")
	run("", "ip", "-n", "ni-wg-load-client", "link", "set", "niwc1", "up")
	run("", "ip", "addr", "add", "198.18.111.1/30", "dev", "niwd0")
	run("", "ip", "link", "set", "niwd0", "up")
	run("", "ip", "-n", "ni-wg-load-dest", "addr", "add", "198.18.111.2/30", "dev", "niwd1")
	run("", "ip", "-n", "ni-wg-load-dest", "link", "set", "lo", "up")
	run("", "ip", "-n", "ni-wg-load-dest", "link", "set", "niwd1", "up")
	t.Cleanup(func() {
		_ = exec.Command("ip", "link", "del", "niuwg").Run()
		_ = exec.Command("ip", "netns", "del", "ni-wg-load-client").Run()
		_ = exec.Command("ip", "netns", "del", "ni-wg-load-dest").Run()
		_ = exec.Command("ip", "link", "del", "niwc0").Run()
		_ = exec.Command("ip", "link", "del", "niwd0").Run()
	})

	oldForwarding := run("", "sysctl", "-n", "net.ipv4.ip_forward")
	run("", "sysctl", "-w", "net.ipv4.ip_forward=1")
	t.Cleanup(func() { _ = exec.Command("sysctl", "-w", "net.ipv4.ip_forward="+oldForwarding).Run() })
	run("", "iptables", "-I", "FORWARD", "1", "-i", "niuwg", "-o", "niwd0", "-j", "ACCEPT")
	run("", "iptables", "-I", "FORWARD", "1", "-i", "niwd0", "-o", "niuwg", "-j", "ACCEPT")
	t.Cleanup(func() {
		_ = exec.Command("iptables", "-D", "FORWARD", "-i", "niuwg", "-o", "niwd0", "-j", "ACCEPT").Run()
		_ = exec.Command("iptables", "-D", "FORWARD", "-i", "niwd0", "-o", "niuwg", "-j", "ACCEPT").Run()
	})

	serverPrivate := run("", "wg", "genkey")
	serverPublic := run(serverPrivate, "wg", "pubkey")
	clientPrivate := run("", "wg", "genkey")
	clientPublic := run(clientPrivate, "wg", "pubkey")
	portProbe, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenPort := portProbe.LocalAddr().(*net.UDPAddr).Port
	portProbe.Close()
	run("", "ip", "link", "add", "niuwg", "type", "wireguard")
	run("", "ip", "addr", "add", "10.211.111.1/24", "dev", "niuwg")
	run(serverPrivate, "wg", "set", "niuwg", "private-key", "/dev/stdin", "listen-port", strconv.Itoa(listenPort), "peer", clientPublic, "allowed-ips", "10.211.111.2/32")
	run("", "ip", "link", "set", "niuwg", "up")
	run("", "ip", "-n", "ni-wg-load-client", "link", "add", "niuwgc", "type", "wireguard")
	run("", "ip", "-n", "ni-wg-load-client", "addr", "add", "10.211.111.2/24", "dev", "niuwgc")
	run(clientPrivate, "ip", "netns", "exec", "ni-wg-load-client", "wg", "set", "niuwgc", "private-key", "/dev/stdin", "peer", serverPublic, "allowed-ips", "198.18.111.0/24", "endpoint", "172.31.111.1:"+strconv.Itoa(listenPort), "persistent-keepalive", "1")
	run("", "ip", "-n", "ni-wg-load-client", "link", "set", "niuwgc", "up")
	run("", "ip", "-n", "ni-wg-load-dest", "route", "add", "10.211.111.0/24", "via", "198.18.111.1")
	// A more specific client route ensures workload packets enter the tunnel.
	run("", "ip", "-n", "ni-wg-load-client", "route", "add", "198.18.111.0/24", "dev", "niuwgc")

	readyFile := filepath.Join(t.TempDir(), "udp-echo-ready")
	serverUsageFile := filepath.Join(t.TempDir(), "udp-echo-usage.json")
	serverCode := `import json,os,resource,signal,socket
baseline=resource.getrusage(resource.RUSAGE_SELF)
def report(_signal,_frame):
 global baseline
 current=resource.getrusage(resource.RUSAGE_SELF)
 sample={'cpu_ms':(current.ru_utime+current.ru_stime-baseline.ru_utime-baseline.ru_stime)*1000,'maxrss_kb':current.ru_maxrss}
 path=os.environ['ANTIMAGE_NI_WG_RESOURCE_USAGE']
 with open(path+'.tmp','w') as f: json.dump(sample,f)
 os.replace(path+'.tmp',path)
 baseline=current
signal.signal(signal.SIGUSR1,report)
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
s.setsockopt(socket.SOL_SOCKET,socket.SO_RCVBUF,16777216)
s.bind(('198.18.111.2',int(os.environ['ANTIMAGE_NI_WG_RESOURCE_PORT'])))
open(os.environ['ANTIMAGE_NI_WG_RESOURCE_READY'],'w').write('ready')
while True:
 d,a=s.recvfrom(65535); s.sendto(d,a)
`
	echo := exec.CommandContext(ctx, "ip", "netns", "exec", "ni-wg-load-dest", "python3", "-u", "-c", serverCode)
	echo.Env = append(os.Environ(), "ANTIMAGE_NI_WG_RESOURCE_PORT=39117", "ANTIMAGE_NI_WG_RESOURCE_READY="+readyFile, "ANTIMAGE_NI_WG_RESOURCE_USAGE="+serverUsageFile)
	if err := echo.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = echo.Process.Kill()
		_ = echo.Wait()
	})
	readyDeadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("isolated UDP echo server did not become ready")
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}

	clientCode := `import json,resource,socket,struct,sys,time
n=int(sys.argv[1]); port=int(sys.argv[2]); s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
s.setsockopt(socket.SOL_SOCKET,socket.SO_RCVBUF,16777216); start=time.perf_counter(); seen=set()
for first in range(0,n,64):
 target=set(range(first,min(n,first+64)))
 for i in target: s.sendto(struct.pack('!I',i)+b'x'*1020,('198.18.111.2',port))
 deadline=time.perf_counter()+0.05
 while not target.issubset(seen):
  remaining=deadline-time.perf_counter()
  if remaining<=0: break
  s.settimeout(remaining)
  try: data,_=s.recvfrom(2048)
  except socket.timeout: break
  if len(data)>=4: seen.add(struct.unpack('!I',data[:4])[0])
elapsed=time.perf_counter()-start; usage=resource.getrusage(resource.RUSAGE_SELF)
wire_bytes=(n+len(seen))*1024
print(json.dumps({'sent':n,'received':len(seen),'lost':n-len(seen),'loss_pct':100.0*(n-len(seen))/n,'elapsed_ms':elapsed*1000,'wire_packet_rate_pps':2*len(seen)/elapsed,'wire_throughput_mbps':wire_bytes*8/(elapsed*1000000),'wire_bytes_both_directions':wire_bytes,'client_cpu_ms':(usage.ru_utime+usage.ru_stime)*1000,'client_maxrss_kb':usage.ru_maxrss}))
`
	type trial struct {
		Monitoring               bool               `json:"monitoring"`
		Sent                     int                `json:"sent"`
		Received                 int                `json:"received"`
		Lost                     int                `json:"lost"`
		LossPct                  float64            `json:"loss_pct"`
		ElapsedMS                float64            `json:"elapsed_ms"`
		PacketsPerSecond         float64            `json:"wire_packet_rate_pps"`
		ThroughputMbps           float64            `json:"wire_throughput_mbps"`
		WireBytesBothDirections  int                `json:"wire_bytes_both_directions"`
		ClientCPUMS              float64            `json:"client_cpu_ms"`
		ClientMaxRSSKB           int64              `json:"client_maxrss_kb"`
		EchoServerCPUMS          float64            `json:"echo_server_cpu_ms"`
		EchoServerMaxRSSKB       int64              `json:"echo_server_maxrss_kb"`
		AgentProcessCPUMS        float64            `json:"agent_process_cpu_ms"`
		AgentHeapBeforeBytes     uint64             `json:"agent_heap_before_bytes"`
		AgentHeapAfterBytes      uint64             `json:"agent_heap_after_bytes"`
		AgentRSSBeforeBytes      uint64             `json:"agent_rss_before_bytes"`
		AgentRSSAfterBytes       uint64             `json:"agent_rss_after_bytes"`
		AttributedFlows          int                `json:"attributed_flows"`
		FlowObservedPackets      uint64             `json:"flow_observed_packets"`
		CaptureReceivedPackets   uint64             `json:"capture_received_packets"`
		CollectorKernelDrops     uint64             `json:"collector_kernel_drops"`
		CollectorStatisticsValid bool               `json:"collector_statistics_available"`
		InterfaceDropDeltas      map[string]uint64  `json:"interface_drop_deltas"`
		SystemCPUPercent         float64            `json:"system_cpu_percent_guest_total"`
		SystemSoftIRQPercent     float64            `json:"system_softirq_percent_guest_total"`
		SystemCPUPercentByCore   map[string]float64 `json:"system_cpu_percent_by_core"`
		MemAvailableBeforeBytes  uint64             `json:"guest_mem_available_before_bytes"`
		MemAvailableAfterBytes   uint64             `json:"guest_mem_available_after_bytes"`
		MemTotalBytes            uint64             `json:"guest_mem_total_bytes"`
	}

	measure := func(enabled bool) trial {
		t.Helper()
		readServerUsage := func() (float64, int64) {
			t.Helper()
			_ = os.Remove(serverUsageFile)
			if err := echo.Process.Signal(syscall.SIGUSR1); err != nil {
				t.Fatalf("request isolated echo-server resource sample: %v", err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				if data, err := os.ReadFile(serverUsageFile); err == nil {
					var sample struct {
						CPUMS  float64 `json:"cpu_ms"`
						MaxRSS int64   `json:"maxrss_kb"`
					}
					if err := json.Unmarshal(data, &sample); err != nil {
						t.Fatalf("decode echo-server resource sample: %v", err)
					}
					return sample.CPUMS, sample.MaxRSS
				}
				if time.Now().After(deadline) {
					t.Fatal("isolated echo-server resource sample timed out")
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		_, _ = readServerUsage() // Exclude warm-up work from this trial.
		tracker, _ := ni.New(128, time.Minute)
		var capture *ni.SessionCapture
		var statsMu sync.Mutex
		var captureStats ni.CaptureStatistics
		if enabled {
			binding := ni.Binding{NodeID: 701, Interface: "niuwg", Address: netip.MustParseAddr("10.211.111.2"), UserID: 42, Session: "isolated-wg-resource-peer", Generation: 1, Since: time.Now().UTC().Add(-time.Second)}
			if err := tracker.Bind(binding); err != nil {
				t.Fatal(err)
			}
			capture, err = ni.NewSessionCapture(tracker, []ni.Binding{binding}, 128)
			if err != nil {
				t.Fatal(err)
			}
			if err := capture.Start(ctx, ni.CaptureConfig{Interface: "niuwg", MaxPacketsPerSecond: 50000, Statistics: func(s ni.CaptureStatistics) { statsMu.Lock(); captureStats = s; statsMu.Unlock() }}); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := capture.Stop(); err != nil && !errors.Is(err, context.Canceled) {
					t.Errorf("capture stop: %v", err)
				}
			}()
		}
		beforeCounters, _, err := readNetworkInfrastructure()
		if err != nil {
			t.Fatal(err)
		}
		cpuBefore := readBenchmarkSystemCPU(t)
		memBeforeGuest := readBenchmarkSystemMemory(t)
		runtime.GC()
		var memBefore, memAfter runtime.MemStats
		runtime.ReadMemStats(&memBefore)
		beforeRSS := benchmarkProcessRSS(t)
		var usageBefore, usageAfter unix.Rusage
		if err := unix.Getrusage(unix.RUSAGE_SELF, &usageBefore); err != nil {
			t.Fatal(err)
		}
		client := exec.CommandContext(ctx, "ip", "netns", "exec", "ni-wg-load-client", "python3", "-c", clientCode, "5000", "39117")
		output, err := client.CombinedOutput()
		if err != nil {
			t.Fatalf("WireGuard UDP forwarding workload failed: %v: %s", err, strings.TrimSpace(string(output)))
		}
		var result trial
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatalf("invalid forwarding workload result %q: %v", output, err)
		}
		result.EchoServerCPUMS, result.EchoServerMaxRSSKB = readServerUsage()
		var flows []ni.Flow
		if enabled {
			deadline := time.Now().Add(2 * time.Second)
			for {
				flows = tracker.Snapshot(time.Now().UTC())
				var observed uint64
				for _, flow := range flows {
					observed += flow.UploadPackets + flow.DownloadPackets
				}
				if observed >= uint64(result.Sent+result.Received) || time.Now().After(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			result.AttributedFlows = len(flows)
			for _, flow := range flows {
				result.FlowObservedPackets += flow.UploadPackets + flow.DownloadPackets
			}
			// Stop reports final socket counters. Save flows before Stop revokes them.
			if err := capture.Stop(); err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			statsMu.Lock()
			result.CollectorKernelDrops = captureStats.KernelDrops
			result.CollectorStatisticsValid = captureStats.KernelStatisticsAvailable
			result.CaptureReceivedPackets = captureStats.ReceivedPackets
			statsMu.Unlock()
			if result.AttributedFlows == 0 || result.FlowObservedPackets == 0 || result.CaptureReceivedPackets == 0 {
				t.Fatalf("enabled collector did not observe forwarded WireGuard traffic: %+v", result)
			}
			if expected := uint64(result.Sent + result.Received); result.FlowObservedPackets != expected || result.CaptureReceivedPackets != expected || result.CollectorKernelDrops != 0 {
				t.Fatalf("capture did not publish every workload packet exactly once: expected=%d result=%+v", expected, result)
			}
		}
		if err := unix.Getrusage(unix.RUSAGE_SELF, &usageAfter); err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&memAfter)
		afterRSS := benchmarkProcessRSS(t)
		afterCounters, _, err := readNetworkInfrastructure()
		if err != nil {
			t.Fatal(err)
		}
		cpuDuration := func(r unix.Rusage) time.Duration {
			return time.Duration(r.Utime.Sec+r.Stime.Sec)*time.Second + time.Duration(r.Utime.Usec+r.Stime.Usec)*time.Microsecond
		}
		result.Monitoring = enabled
		result.AgentProcessCPUMS = float64(cpuDuration(usageAfter)-cpuDuration(usageBefore)) / float64(time.Millisecond)
		result.AgentHeapBeforeBytes = memBefore.HeapAlloc
		result.AgentHeapAfterBytes = memAfter.HeapAlloc
		result.AgentRSSBeforeBytes, result.AgentRSSAfterBytes = beforeRSS, afterRSS
		cpuAfter := readBenchmarkSystemCPU(t)
		result.SystemCPUPercent, result.SystemSoftIRQPercent, result.SystemCPUPercentByCore = benchmarkSystemCPUPercent(cpuBefore, cpuAfter)
		memAfterGuest := readBenchmarkSystemMemory(t)
		result.MemAvailableBeforeBytes = memBeforeGuest["MemAvailable"]
		result.MemAvailableAfterBytes = memAfterGuest["MemAvailable"]
		result.MemTotalBytes = memBeforeGuest["MemTotal"]
		for _, counter := range []string{"rx_dropped", "tx_dropped"} {
			for _, name := range []string{"niuwg", "niwd0"} {
				key := "interface." + name + "." + counter
				if after, ok := afterCounters[key]; ok && after >= beforeCounters[key] {
					if result.InterfaceDropDeltas == nil {
						result.InterfaceDropDeltas = make(map[string]uint64)
					}
					result.InterfaceDropDeltas[key] = after - beforeCounters[key]
				}
			}
		}
		if result.Lost != 0 {
			t.Fatalf("isolated tunnel workload lost %d/%d echo responses (%.3f%%): %+v", result.Lost, result.Sent, result.LossPct, result)
		}
		if result.ElapsedMS <= 0 || result.Sent != 5000 || result.Received != result.Sent {
			t.Fatalf("invalid forwarding benchmark result: %+v", result)
		}
		encoded, _ := json.Marshal(result)
		t.Log(string(encoded))
		return result
	}

	// Send a short warm-up first to establish the encrypted peer handshake and
	// populate route/neighbour state outside the measured paired trials.
	warmup := exec.CommandContext(ctx, "ip", "netns", "exec", "ni-wg-load-client", "python3", "-c", clientCode, "100", "39117")
	output, err := warmup.CombinedOutput()
	if err != nil {
		t.Fatalf("WireGuard warm-up failed: %v: %s", err, output)
	}
	var warmupResult struct {
		Sent int `json:"sent"`
		Lost int `json:"lost"`
	}
	if err := json.Unmarshal(output, &warmupResult); err != nil || warmupResult.Sent != 100 || warmupResult.Lost != 0 {
		t.Fatalf("WireGuard echo path did not warm up cleanly: result=%s err=%v", output, err)
	}
	handshakes := strings.Fields(run("", "wg", "show", "niuwg", "latest-handshakes"))
	if len(handshakes) < 2 {
		t.Fatalf("WireGuard handshake missing: %v", handshakes)
	}
	stamp, err := strconv.ParseInt(handshakes[1], 10, 64)
	if err != nil || stamp == 0 {
		t.Fatalf("WireGuard handshake is not live: %v", handshakes)
	}

	offTrials, onTrials := make([]trial, 0, 3), make([]trial, 0, 3)
	// Profile a separate, longer observation workload. Keep profiler overhead
	// out of the paired OFF/ON performance measurements below.
	if os.Getenv("ANTIMAGE_NI_WG_PROFILE") == "1" {
		for range 30 {
			measure(true)
		}
		return
	}
	for range 3 {
		offTrials = append(offTrials, measure(false))
		onTrials = append(onTrials, measure(true))
	}
	median := func(samples []trial, value func(trial) float64) float64 {
		values := make([]float64, len(samples))
		for i := range samples {
			values[i] = value(samples[i])
		}
		sort.Float64s(values)
		return values[len(values)/2]
	}
	offPPS := median(offTrials, func(v trial) float64 { return v.PacketsPerSecond })
	onPPS := median(onTrials, func(v trial) float64 { return v.PacketsPerSecond })
	offMbps := median(offTrials, func(v trial) float64 { return v.ThroughputMbps })
	onMbps := median(onTrials, func(v trial) float64 { return v.ThroughputMbps })
	offCPU := median(offTrials, func(v trial) float64 { return v.AgentProcessCPUMS })
	onCPU := median(onTrials, func(v trial) float64 { return v.AgentProcessCPUMS })
	if onPPS <= 0 || offPPS <= 0 || offMbps <= 0 {
		t.Fatalf("invalid throughput comparison: off pps=%f mbps=%f, on pps=%f mbps=%f", offPPS, offMbps, onPPS, onMbps)
	}
	processCPUOverheadPct := (onCPU - offCPU) / offCPU * 100
	if offCPU <= 0 {
		processCPUOverheadPct = 0
	}
	throughputChangePct := (onMbps - offMbps) / offMbps * 100
	summary, _ := json.Marshal(map[string]any{
		"benchmark":                      "real_encrypted_wireguard_forwarding",
		"baseline_off_trials":            offTrials,
		"monitoring_on_trials":           onTrials,
		"median_baseline_wire_pps":       offPPS,
		"median_monitoring_wire_pps":     onPPS,
		"median_baseline_mbps":           offMbps,
		"median_monitoring_mbps":         onMbps,
		"median_baseline_agent_cpu_ms":   offCPU,
		"median_monitoring_agent_cpu_ms": onCPU,
		"agent_process_cpu_overhead_pct": processCPUOverheadPct,
		"wire_throughput_change_pct":     throughputChangePct,
		"packet_loss_delta_pct_points":   median(onTrials, func(v trial) float64 { return v.LossPct }) - median(offTrials, func(v trial) float64 { return v.LossPct }),
	})
	t.Log(string(summary))
}

func benchmarkProcessRSS(t *testing.T) uint64 {
	t.Helper()
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				t.Fatalf("invalid VmRSS line %q", line)
			}
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return value * 1024
		}
	}
	t.Fatal("VmRSS unavailable in /proc/self/status")
	return 0
}

func readBenchmarkSystemCPU(t *testing.T) map[string][]uint64 {
	t.Helper()
	file, err := os.Open("/proc/stat")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result := make(map[string][]uint64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 9 || !strings.HasPrefix(fields[0], "cpu") {
			if len(result) > 0 {
				break
			}
			continue
		}
		values := make([]uint64, len(fields)-1)
		for i, value := range fields[1:] {
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				t.Fatalf("invalid /proc/stat CPU counter: %v", err)
			}
			values[i] = parsed
		}
		result[fields[0]] = values
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(result) == 0 {
		t.Fatal("/proc/stat CPU counters unavailable")
	}
	return result
}

func benchmarkSystemCPUPercent(before, after map[string][]uint64) (float64, float64, map[string]float64) {
	calculate := func(old, current []uint64) (busyPct, softIRQPct float64, ok bool) {
		if len(old) < 8 || len(current) != len(old) {
			return 0, 0, false
		}
		var total, idle, softIRQ uint64
		for i, value := range current {
			if value < old[i] {
				return 0, 0, false
			}
			delta := value - old[i]
			total += delta
			if i == 3 || i == 4 {
				idle += delta
			}
			if i == 6 {
				softIRQ = delta
			}
		}
		if total == 0 || idle > total {
			return 0, 0, false
		}
		return float64(total-idle) / float64(total) * 100, float64(softIRQ) / float64(total) * 100, true
	}
	overall, softIRQ, ok := calculate(before["cpu"], after["cpu"])
	if !ok {
		overall, softIRQ = 0, 0
	}
	perCore := make(map[string]float64)
	for name, current := range after {
		if name == "cpu" {
			continue
		}
		if value, _, valid := calculate(before[name], current); valid {
			perCore[name] = value
		}
	}
	return overall, softIRQ, perCore
}

func readBenchmarkSystemMemory(t *testing.T) map[string]uint64 {
	t.Helper()
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result := make(map[string]uint64, 2)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || (fields[0] != "MemTotal:" && fields[0] != "MemAvailable:") {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatalf("invalid /proc/meminfo counter: %v", err)
		}
		result[strings.TrimSuffix(fields[0], ":")] = value * 1024
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if result["MemTotal"] == 0 || result["MemAvailable"] == 0 {
		t.Fatalf("guest memory counters unavailable: %v", result)
	}
	return result
}

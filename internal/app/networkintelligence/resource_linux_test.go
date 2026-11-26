//go:build linux

package networkintelligence

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// This is an actual socket-load comparison on loopback, not a VPN capacity
// estimate. Socket clients share a fixture identity. Loopback duplicates and
// shared kernel resources prevent interpreting these results as user capacity.
func TestCaptureResourceComparison(t *testing.T) {
	if os.Getenv("ANTIMAGE_NI_RESOURCE_E2E") != "1" {
		t.Skip("opt-in isolated resource fixture")
	}
	for _, clients := range []int{10, 50, 100, 250, 500} {
		for _, enabled := range []bool{false, true} {
			resourceTrial(t, clients, enabled)
		}
	}
}

func resourceTrial(t *testing.T, clients int, enabled bool) {
	t.Helper()
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 1024)
		for {
			n, addr, err := server.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = server.WriteToUDP(buffer[:n], addr)
		}
	}()
	defer func() { server.Close(); <-done }()
	tracker, _ := New(4096, time.Minute)
	var capture *SessionCapture
	var statistics CaptureStatistics
	if enabled {
		binding := Binding{NodeID: 1, Interface: "lo", Address: netip.MustParseAddr("127.0.0.1"), UserID: 1, Session: "resource-fixture", Generation: 1, Since: time.Now()}
		if err := tracker.Bind(binding); err != nil {
			t.Fatal(err)
		}
		capture, err = NewSessionCapture(tracker, []Binding{binding}, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := capture.Start(context.Background(), CaptureConfig{Interface: "lo", Statistics: func(s CaptureStatistics) { statistics = s }}); err != nil {
			t.Fatal(err)
		}
		defer capture.Stop()
	}
	sockets := make([]*net.UDPConn, clients)
	for i := range sockets {
		sockets[i], err = net.DialUDP("udp4", nil, server.LocalAddr().(*net.UDPAddr))
		if err != nil {
			t.Fatal(err)
		}
		defer sockets[i].Close()
	}
	runtime.GC()
	var memBefore, memAfter runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	var before, after unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &before); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	rounds := 100
	paced := os.Getenv("ANTIMAGE_NI_RESOURCE_PACED") == "1"
	if paced {
		rounds = 20
	}
	payload := make([]byte, 256)
	response := make([]byte, 256)
	for range rounds {
		for _, socket := range sockets {
			if paced {
				time.Sleep(time.Millisecond)
			}
			if err := socket.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := socket.Write(payload); err != nil {
				t.Fatal(err)
			}
			if n, err := socket.Read(response); err != nil || n != len(payload) {
				t.Fatalf("echo n=%d err=%v", n, err)
			}
		}
	}
	elapsed := time.Since(start)
	if err := unix.Getrusage(unix.RUSAGE_SELF, &after); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&memAfter)
	if capture != nil {
		_ = capture.Stop()
	}
	flows := tracker.Snapshot(time.Now())
	telemetry, err := json.Marshal(flows)
	if err != nil {
		t.Fatal(err)
	}
	cpu := func(r unix.Rusage) time.Duration {
		return time.Duration(r.Utime.Sec+r.Stime.Sec)*time.Second + time.Duration(r.Utime.Usec+r.Stime.Usec)*time.Microsecond
	}
	result := map[string]any{"socket_clients": clients, "monitoring": enabled, "echoes": clients * rounds, "application_bytes_each_direction": clients * rounds * len(payload), "elapsed_ms": float64(elapsed) / float64(time.Millisecond), "echoes_per_second": float64(clients*rounds) / elapsed.Seconds(), "mean_echo_us": float64(elapsed) / float64(time.Microsecond) / float64(clients*rounds), "process_cpu_ms": float64(cpu(after)-cpu(before)) / float64(time.Millisecond), "heap_before_bytes": memBefore.HeapAlloc, "heap_after_bytes": memAfter.HeapAlloc, "tracked_flows": len(flows), "snapshot_json_bytes": len(telemetry), "collector_received_packets": statistics.ReceivedPackets, "collector_kernel_drops": statistics.KernelDrops, "collector_statistics_available": statistics.KernelStatisticsAvailable, "timed_out_echoes": 0}
	raw, _ := json.Marshal(result)
	t.Log(string(raw))
}

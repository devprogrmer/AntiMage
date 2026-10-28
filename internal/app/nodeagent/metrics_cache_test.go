package nodeagent

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestCachedSystemMetricsFansOutConcurrentRequests(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	var samples atomic.Int32
	server.metricsSystemSampler = func() *nodev1.SystemMetrics {
		return &nodev1.SystemMetrics{CpuUsagePercent: float64(samples.Add(1) * 10)}
	}

	const callers = 32
	type result struct {
		metrics *nodev1.SystemMetrics
		sampled time.Time
	}
	results := make(chan result, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			metrics, sampled := server.cachedSystemMetrics()
			results <- result{metrics: metrics, sampled: sampled}
		}()
	}
	group.Wait()
	close(results)

	var sampleTime time.Time
	var first *nodev1.SystemMetrics
	for current := range results {
		if current.metrics.GetCpuUsagePercent() != 10 {
			t.Fatalf("concurrent caller got CPU sample %v, want 10", current.metrics.GetCpuUsagePercent())
		}
		if !sampleTime.IsZero() && !current.sampled.Equal(sampleTime) {
			t.Fatalf("concurrent callers got sample times %v and %v", sampleTime, current.sampled)
		}
		sampleTime = current.sampled
		first = current.metrics
	}
	if got := samples.Load(); got != 1 {
		t.Fatalf("system metrics sampled %d times for concurrent fanout, want 1", got)
	}

	first.CpuUsagePercent = 99
	cached, cachedAt := server.cachedSystemMetrics()
	if cached.GetCpuUsagePercent() != 10 || !cachedAt.Equal(sampleTime) {
		t.Fatalf("caller mutation escaped into cache: metrics=%v sampled=%v", cached.GetCpuUsagePercent(), cachedAt)
	}

	server.metricsMu.Lock()
	server.metricsSystemCacheAt = time.Now().Add(-metricsSystemCacheTTL)
	server.metricsMu.Unlock()
	refreshed, refreshedAt := server.cachedSystemMetrics()
	if got := samples.Load(); got != 2 || refreshed.GetCpuUsagePercent() != 20 || !refreshedAt.After(sampleTime) {
		t.Fatalf("expired cache was not refreshed: samples=%d cpu=%v at=%v", got, refreshed.GetCpuUsagePercent(), refreshedAt)
	}
}

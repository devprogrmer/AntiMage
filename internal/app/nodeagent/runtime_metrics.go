package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"
)

// runtimeMetricsFileName is a read-only, best-effort snapshot of the Go runtime
// state. It exists so an operator-side, read-only collector can attribute CPU
// and memory pressure without a profiling listener and without changing any
// accounting, quota, or runtime behavior. A missing or stale file is never an
// error: nothing in NodeAgent depends on reading it back.
const runtimeMetricsFileName = "runtime-metrics.json"

const runtimeMetricsInterval = 5 * time.Second

// runtimeMetricsCounters are monotonically increasing call counts for the
// long-lived worker loops. They are only observations; the snapshot writer
// copies them into the periodic document.
type runtimeMetricsCounters struct {
	accountingTicks  atomic.Uint64
	quotaEvaluations atomic.Uint64
	reflectionCalls  atomic.Uint64
	deliveryCalls    atomic.Uint64
}

// runtimeMetrics is the on-disk shape read by scripts/performance. Field names
// are stable and additive: the collector treats absent fields as unavailable.
type runtimeMetrics struct {
	SampledAtUnix    int64  `json:"sampled_at_unix"`
	ProcessStartedAt int64  `json:"process_started_at_unix"`
	UptimeSeconds    uint64 `json:"uptime_seconds"`
	GoroutineCount   int    `json:"goroutine_count"`
	HeapBytes        uint64 `json:"heap_bytes"`
	HeapObjectsBytes uint64 `json:"heap_objects_bytes"`
	StackBytes       uint64 `json:"stack_bytes"`
	TotalAllocBytes  uint64 `json:"total_alloc_bytes"`
	AllocBytes       uint64 `json:"alloc_bytes"`
	GCCycles         uint32 `json:"gc_cycles"`
	GCCPUSeconds     uint64 `json:"gc_cpu_seconds_micro"`
	HeapReleased     uint64 `json:"heap_released_bytes"`
	AccountingTicks  uint64 `json:"accounting_ticks"`
	QuotaEvaluations uint64 `json:"quota_evaluations"`
	ReflectionCalls  uint64 `json:"reflection_calls"`
	DeliveryCalls    uint64 `json:"delivery_calls"`
}

func (s *Server) runtimeMetricsSnapshot() runtimeMetrics {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return runtimeMetrics{
		SampledAtUnix:    time.Now().Unix(),
		ProcessStartedAt: s.startedAt.Unix(),
		UptimeSeconds:    uint64(time.Since(s.startedAt).Seconds()),
		GoroutineCount:   runtime.NumGoroutine(),
		HeapBytes:        stats.HeapAlloc,
		HeapObjectsBytes: stats.HeapObjects,
		StackBytes:       stats.StackInuse,
		TotalAllocBytes:  stats.TotalAlloc,
		AllocBytes:       stats.Alloc,
		GCCycles:         stats.NumGC,
		GCCPUSeconds:     stats.PauseTotalNs / 1e6,
		HeapReleased:     stats.HeapReleased,
		AccountingTicks:  s.runtimeMetricsCounters.accountingTicks.Load(),
		QuotaEvaluations: s.runtimeMetricsCounters.quotaEvaluations.Load(),
		ReflectionCalls:  s.runtimeMetricsCounters.reflectionCalls.Load(),
		DeliveryCalls:    s.runtimeMetricsCounters.deliveryCalls.Load(),
	}
}

func (s *Server) runtimeMetricsPath() string {
	return filepath.Join(s.cfg.DataDir, runtimeMetricsFileName)
}

// writeRuntimeMetricsSnapshot is best-effort: an unwritable data directory
// (for example a read-only mount) must never stop the node from running.
func (s *Server) writeRuntimeMetricsSnapshot() {
	if s.cfg.DataDir == "" {
		return
	}
	path := s.runtimeMetricsPath()
	payload, err := json.Marshal(s.runtimeMetricsSnapshot())
	if err != nil {
		return
	}
	payload = append(payload, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-metrics-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmpName, path)
}

func (s *Server) runRuntimeMetricsWriter(ctx context.Context) {
	if s.cfg.DataDir == "" {
		return
	}
	s.writeRuntimeMetricsSnapshot()
	ticker := time.NewTicker(runtimeMetricsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.writeRuntimeMetricsSnapshot()
		}
	}
}

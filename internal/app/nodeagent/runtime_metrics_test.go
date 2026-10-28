package nodeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeMetricsSnapshotWritesReadableDocument(t *testing.T) {
	dir := t.TempDir()
	server := &Server{cfg: Config{DataDir: dir}}
	server.runtimeMetricsCounters.accountingTicks.Add(3)
	server.runtimeMetricsCounters.quotaEvaluations.Add(7)
	server.runtimeMetricsCounters.reflectionCalls.Add(2)
	server.runtimeMetricsCounters.deliveryCalls.Add(1)

	server.writeRuntimeMetricsSnapshot()

	raw, err := os.ReadFile(filepath.Join(dir, runtimeMetricsFileName))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var decoded runtimeMetrics
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if decoded.GoroutineCount <= 0 {
		t.Fatalf("goroutine count = %d, want > 0", decoded.GoroutineCount)
	}
	if decoded.HeapBytes <= 0 {
		t.Fatalf("heap bytes = %d, want > 0", decoded.HeapBytes)
	}
	if decoded.AccountingTicks != 3 || decoded.QuotaEvaluations != 7 {
		t.Fatalf("tick counters = %d/%d, want 3/7", decoded.AccountingTicks, decoded.QuotaEvaluations)
	}
	if decoded.ReflectionCalls != 2 || decoded.DeliveryCalls != 1 {
		t.Fatalf("call counters = %d/%d, want 2/1", decoded.ReflectionCalls, decoded.DeliveryCalls)
	}
	if decoded.SampledAtUnix <= 0 {
		t.Fatalf("sampled_at_unix = %d, want > 0", decoded.SampledAtUnix)
	}
}

func TestRuntimeMetricsSnapshotSkipsEmptyDataDir(t *testing.T) {
	server := &Server{}
	server.writeRuntimeMetricsSnapshot()
	if _, err := os.Stat(runtimeMetricsFileName); err == nil {
		t.Fatalf("snapshot unexpectedly written for empty data dir")
	}
}

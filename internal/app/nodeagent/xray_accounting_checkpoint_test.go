package nodeagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestOfflineXrayCheckpointRestartAndLostAck(t *testing.T) {
	const megabyte = uint64(1024 * 1024)
	const key = "42.alice:uplink"
	dir := t.TempDir()
	s := New(Config{DataDir: dir})
	if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	sample := func(server *Server, bytes uint64) {
		t.Helper()
		if err := server.checkpointXrayStatsLocked([]xrayStat{{Name: "user>>>42.alice>>>traffic>>>uplink", Value: int64(bytes)}}); err != nil {
			t.Fatal(err)
		}
	}
	sample(s, 500*megabyte)
	s.xrayUsagePending = &xrayUsagePendingBatch{
		BatchID:      "xray-offline-500",
		Samples:      []xrayUsageSample{{UserID: 42, InboundTag: "test", Value: 500 * megabyte}},
		NextBaseline: map[string]uint64{key: 500 * megabyte},
	}
	if err := s.persistXrayUsageStateLocked(); err != nil {
		t.Fatal(err)
	}
	// Traffic continues with an outstanding immutable batch, then resets.
	sample(s, 600*megabyte)
	sample(s, 100*megabyte)
	s = New(Config{DataDir: dir})
	if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	sample(s, 200*megabyte)
	if got := s.xrayAccountingCounters[key].Total; got != 800*megabyte {
		t.Fatalf("durable total after runtime and node restart = %d", got)
	}
	for i := 0; i < 2; i++ {
		batch, err := s.collectXrayUserUsage(context.Background(), &nodev1.CollectUsageRequest{})
		if err != nil || batch.GetBatchId() != "xray-offline-500" || batch.GetStats()[0].GetValue() != 500*megabyte {
			t.Fatalf("lost ACK retry changed batch: %v, %v", batch, err)
		}
	}
	for i := 0; i < 2; i++ {
		ack, err := s.ackXrayUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: "xray-offline-500"})
		if err != nil || !ack.GetAcknowledged() {
			t.Fatalf("ACK %d: %v, %v", i, ack, err)
		}
	}
	s = New(Config{DataDir: dir})
	if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	if got := s.xrayAccountingCounters[key].Total - s.xrayUsageBaseline[key]; got != 300*megabyte {
		t.Fatalf("unreflected bytes after ACK/restart = %d", got)
	}
	batch, err := s.collectXrayUserUsage(context.Background(), &nodev1.CollectUsageRequest{})
	if err != nil || len(batch.GetStats()) != 1 || batch.GetStats()[0].GetValue() != 300*megabyte {
		t.Fatalf("reconnect with runtime stopped lost durable remainder: %v, %v", batch, err)
	}
	ack, err := s.ackXrayUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: batch.GetBatchId()})
	if err != nil || !ack.GetAcknowledged() {
		t.Fatalf("remainder ACK: %v, %v", ack, err)
	}
	if got := s.xrayUsageBaseline[key]; got != 800*megabyte {
		t.Fatalf("final reflected total = %d", got)
	}
}

func TestXrayCheckpointMigratesPendingNativeBaseline(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	key := "42.alice:uplink"
	s.xrayUsageBaseline[key] = 100
	s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: "xray-legacy", NextBaseline: map[string]uint64{key: 500}}
	if err := s.checkpointXrayStatsLocked([]xrayStat{{Name: "user>>>42.alice>>>traffic>>>uplink", Value: 20}}); err != nil {
		t.Fatal(err)
	}
	if got := s.xrayAccountingCounters[key].Total; got != 520 {
		t.Fatalf("legacy pending reset lost bytes: %d", got)
	}
}

func TestXrayCheckpointPersistenceFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{DataDir: blocked})
	if err := s.checkpointXrayStatsLocked([]xrayStat{{Name: "user>>>42.alice>>>traffic>>>uplink", Value: 100}}); err == nil {
		t.Fatal("expected persistence failure")
	}
	if len(s.xrayAccountingCounters) != 0 {
		t.Fatal("failed checkpoint advanced memory")
	}
}

func TestAccountingCounterRejectsOverflowAndCorruption(t *testing.T) {
	for _, previous := range []map[string]accountingCounter{
		{"key": {Native: 10, Total: 9}},
		{"key": {Native: 0, Total: ^uint64(0)}},
	} {
		if _, err := advanceAccountingCounters(previous, map[string]uint64{"key": 1}, nil); err == nil {
			t.Fatal("unsafe counter accepted")
		}
	}
}

func TestXrayCheckpointCorruptionIsNotResetToEmpty(t *testing.T) {
	for _, content := range []string{
		`{"counters":`,
		`{"counters":{"42.alice:uplink":{"native":100,"total":50}}}`,
		`{"counters":{"broken":{"native":1,"total":1}}}`,
	} {
		s := New(Config{DataDir: t.TempDir()})
		path := s.xrayUsageStatePath()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := s.ensureXrayUsageStateLoadedLocked(); err == nil || s.xrayUsageLoaded {
			t.Fatal("corrupt state was accepted")
		}
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != content {
			t.Fatal("corrupt state silently overwritten")
		}
	}
}

func TestAccountingSeriesCapacityDoesNotPruneUnreflectedUsage(t *testing.T) {
	previous := make(map[string]accountingCounter, maxAccountingCounterSeries)
	for i := 0; i < maxAccountingCounterSeries; i++ {
		previous[fmt.Sprint(i)] = accountingCounter{Native: 1, Total: 1}
	}
	if _, err := advanceAccountingCounters(previous, map[string]uint64{"new": 1}, nil); err == nil {
		t.Fatal("series limit was not enforced")
	}
	if len(previous) != maxAccountingCounterSeries {
		t.Fatal("capacity failure pruned original counters")
	}
}

func TestAccountingCheckpointInterval(t *testing.T) {
	for _, input := range []string{"100ms", "0", "off", "2m", "garbage"} {
		if _, err := accountingCheckpointInterval(input); err == nil {
			t.Fatalf("unsafe interval accepted: %q", input)
		}
	}
	for _, input := range []string{"", "1s", "1m"} {
		interval, err := accountingCheckpointInterval(input)
		if err != nil || interval < time.Second {
			t.Fatalf("valid interval rejected: %q: %v", input, err)
		}
	}
}

func TestXrayGenerationDetectsResetThatOvertakesOldCounter(t *testing.T) {
	const gigabyte = uint64(1024 * 1024 * 1024)
	key := "42.alice:uplink"
	dir := t.TempDir()
	s := New(Config{DataDir: dir})
	if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	sample := func(server *Server, value uint64, generation string) {
		t.Helper()
		if err := server.checkpointXrayGenerationLocked([]xrayStat{{Name: "user>>>42.alice>>>traffic>>>uplink", Value: int64(value)}}, generation); err != nil {
			t.Fatal(err)
		}
	}
	sample(s, 10*gigabyte, "runtime-A")
	s = New(Config{DataDir: dir})
	if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	sample(s, 11*gigabyte, "runtime-B")
	sample(s, 12*gigabyte, "runtime-B")
	if got := s.xrayAccountingCounters[key].Total; got != 22*gigabyte {
		t.Fatalf("generation reset total = %d, want 22GB", got)
	}
	batch, err := s.CollectUserUsage(context.Background(), &nodev1.CollectUsageRequest{})
	if err != nil || len(batch.GetStats()) != 1 || batch.GetStats()[0].GetValue() != 22*gigabyte {
		t.Fatalf("generation reconnect batch = %v, %v", batch, err)
	}
}

//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSampleRuntimeIncludesRequiredMetrics(t *testing.T) {
	sample, err := sampleRuntime(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(sample.Metrics) != len(metricNames) || sample.UnixSeconds <= 0 {
		t.Fatalf("incomplete sample: %+v", sample)
	}
	for name, value := range sample.Metrics {
		if value < 0 {
			t.Fatalf("negative metric %s: %f", name, value)
		}
	}
}

func TestCaptureCancellationFlushesPrivateProfiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evidence")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready, done := make(chan error, 1), make(chan error, 1)
	go func() { done <- capture(ctx, dir, time.Millisecond, "", ready) }()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := os.Stat(filepath.Join(dir, "runtime.jsonl"))
		if err == nil && info.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("capture did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("capture failed to stop")
	}
	for _, name := range []string{"build.json", "runtime.jsonl", "cpu.pprof", "heap.pprof", "allocs.pprof", "goroutine.pprof"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() == 0 || info.Mode().Perm() != 0600 {
			t.Fatalf("invalid evidence file %s: %+v", name, info)
		}
	}
	var meta struct {
		PID int `json:"pid"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "build.json"))
	if err != nil || json.Unmarshal(raw, &meta) != nil || meta.PID != os.Getpid() {
		t.Fatalf("wrong process identity: %s %v", raw, err)
	}
}

func TestCaptureDoesNotOverwriteExistingEvidence(t *testing.T) {
	ready := make(chan error, 1)
	if err := capture(context.Background(), t.TempDir(), time.Millisecond, "", ready); err == nil {
		t.Fatal("existing evidence accepted")
	}
	if err := <-ready; err == nil {
		t.Fatal("startup failure was not reported")
	}
}

func TestCaptureWaitsForWorkloadBeforeProfiling(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evidence")
	marker := filepath.Join(dir, "measurement-start")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready, done := make(chan error, 1), make(chan error, 1)
	go func() { done <- capture(ctx, dir, time.Millisecond, marker, ready) }()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "cpu.pprof")); !os.IsNotExist(err) {
		t.Fatalf("profiling started before workload: %v", err)
	}
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "runtime.jsonl")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("profiling did not follow workload start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("capture did not stop")
	}
}

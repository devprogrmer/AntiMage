//go:build linux

// antimage_node_perf runs the production NodeAgent with file-only profiling.
// It deliberately has no profiling HTTP listener and no test/fake collectors.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"strconv"
	"syscall"
	"time"

	"github.com/antimage/antimage/internal/app/nodeagent"
)

var metricNames = []string{
	"/gc/heap/allocs:bytes", "/gc/heap/frees:bytes",
	"/gc/heap/live:bytes", "/memory/classes/heap/objects:bytes",
	"/memory/classes/total:bytes", "/memory/classes/heap/released:bytes",
	"/gc/cycles/total:gc-cycles", "/cpu/classes/gc/total:cpu-seconds",
	"/cpu/classes/gc/mark/assist:cpu-seconds", "/sched/goroutines:goroutines",
}

type runtimeSample struct {
	At          time.Time          `json:"at"`
	UnixSeconds float64            `json:"unix_seconds"`
	Elapsed     float64            `json:"elapsed_seconds"`
	Metrics     map[string]float64 `json:"metrics"`
}

func sampleRuntime(start time.Time) (runtimeSample, error) {
	samples := make([]metrics.Sample, len(metricNames))
	for i, name := range metricNames {
		samples[i].Name = name
	}
	metrics.Read(samples)
	out := runtimeSample{At: time.Now().UTC(), UnixSeconds: float64(time.Now().UnixNano()) / 1e9, Elapsed: time.Since(start).Seconds(), Metrics: make(map[string]float64, len(samples))}
	for _, sample := range samples {
		switch sample.Value.Kind() {
		case metrics.KindUint64:
			out.Metrics[sample.Name] = float64(sample.Value.Uint64())
		case metrics.KindFloat64:
			out.Metrics[sample.Name] = sample.Value.Float64()
		default:
			return runtimeSample{}, fmt.Errorf("required runtime metric unavailable: %s", sample.Name)
		}
	}
	return out, nil
}

func capture(ctx context.Context, dir string, warmup time.Duration, startFile string, ready chan<- error) (err error) {
	// A unique directory prevents accidental replacement of baseline evidence.
	if err = os.Mkdir(dir, 0700); err != nil {
		ready <- err
		return err
	}
	meta, err := os.OpenFile(filepath.Join(dir, "build.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		ready <- err
		return err
	}
	info, _ := debug.ReadBuildInfo()
	err = json.NewEncoder(meta).Encode(struct {
		PID        int              `json:"pid"`
		GoVersion  string           `json:"go_version"`
		GOMAXPROCS int              `json:"gomaxprocs"`
		Warmup     float64          `json:"warmup_seconds"`
		Build      *debug.BuildInfo `json:"build"`
	}{os.Getpid(), runtime.Version(), runtime.GOMAXPROCS(0), warmup.Seconds(), info})
	err = errors.Join(err, meta.Close())
	if err != nil {
		ready <- err
		return err
	}
	ready <- nil
	if startFile != "" {
		poll := time.NewTicker(100 * time.Millisecond)
		defer poll.Stop()
		for {
			if _, statErr := os.Stat(startFile); statErr == nil {
				break
			} else if !os.IsNotExist(statErr) {
				return statErr
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("capture stopped before workload start")
			case <-poll.C:
			}
		}
	}
	timer := time.NewTimer(warmup)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("capture stopped before warmup completed")
	case <-timer.C:
	}
	cpu, err := os.OpenFile(filepath.Join(dir, "cpu.pprof"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err = pprof.StartCPUProfile(cpu); err != nil {
		return errors.Join(err, cpu.Close())
	}
	defer func() {
		pprof.StopCPUProfile()
		err = errors.Join(err, cpu.Close())
		for _, name := range []string{"heap", "allocs", "goroutine"} {
			file, openErr := os.OpenFile(filepath.Join(dir, name+".pprof"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if openErr != nil {
				err = errors.Join(err, openErr)
				continue
			}
			err = errors.Join(err, pprof.Lookup(name).WriteTo(file, 0), file.Close())
		}
	}()
	file, err := os.OpenFile(filepath.Join(dir, "runtime.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	encoder := json.NewEncoder(file)
	start := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		sample, sampleErr := sampleRuntime(start)
		if sampleErr != nil {
			return sampleErr
		}
		if err = encoder.Encode(sample); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "session-event" {
		return nodeagent.RunNativeSessionEventHelper(os.Args[2:])
	}
	dir := os.Getenv("ANTIMAGE_PERF_OUTPUT")
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("ANTIMAGE_PERF_OUTPUT must name a new absolute evidence directory")
	}
	warmupSeconds, err := strconv.Atoi(os.Getenv("ANTIMAGE_PERF_WARMUP_SECONDS"))
	if err != nil || warmupSeconds < 1 {
		return fmt.Errorf("ANTIMAGE_PERF_WARMUP_SECONDS must be positive")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ready, done := make(chan error, 1), make(chan error, 1)
	go func() {
		captureErr := capture(ctx, dir, time.Duration(warmupSeconds)*time.Second, os.Getenv("ANTIMAGE_PERF_START_FILE"), ready)
		done <- captureErr
		if captureErr != nil {
			stop()
		}
	}()
	if err := <-ready; err != nil {
		return err
	}
	err = nodeagent.New(nodeagent.LoadConfig()).Run(ctx)
	stop()
	return errors.Join(err, <-done)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

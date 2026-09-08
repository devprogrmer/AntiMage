package nodeagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	nodev1 "github.com/devprogrmer/AntiMage/internal/proto/node/v1"
)

func TestApplyConfigValidationFailurePreservesLastGoodConfigAndRevision(t *testing.T) {
	withRuntimeCommand(t, "validate-fail")

	dataDir := t.TempDir()
	xrayPath := filepath.Join(dataDir, "xray")
	if err := os.WriteFile(xrayPath, []byte("placeholder"), 0755); err != nil {
		t.Fatalf("create fake xray: %v", err)
	}

	configPath := filepath.Join(dataDir, "xray-config.json")
	const lastGood = `{"log":{"loglevel":"warning"}}`
	if err := os.WriteFile(configPath, []byte(lastGood), 0644); err != nil {
		t.Fatalf("write last-good config: %v", err)
	}

	s := New(Config{
		DataDir:       dataDir,
		XrayPath:      xrayPath,
		XrayAssetsDir: dataDir,
	})
	s.lastConfig = configPath
	s.appliedRev = 7

	_, err := s.applyConfig(context.Background(), &nodev1.RuntimeConfigRequest{
		ConfigJson:      `{"broken":true}`,
		DesiredRevision: 99,
	}, "config synced")
	if err == nil {
		t.Fatal("expected invalid config to fail")
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read last-good config: %v", err)
	}
	if string(got) != lastGood {
		t.Fatalf("last-good config was overwritten: %s", got)
	}

	if s.appliedRev != 7 {
		t.Fatalf("applied revision advanced after rejected config: got %d want 7", s.appliedRev)
	}
}

func TestApplyConfigStartFailureRestoresPreviousRuntime(t *testing.T) {
	dataDir := t.TempDir()
	xrayPath := filepath.Join(dataDir, "xray")

	if err := os.WriteFile(xrayPath, []byte("placeholder"), 0755); err != nil {
		t.Fatalf("create fake xray: %v", err)
	}

	configPath := filepath.Join(dataDir, "xray-config.json")
	const lastGood = `{"log":{"loglevel":"warning"}}`
	if err := os.WriteFile(configPath, []byte(lastGood), 0644); err != nil {
		t.Fatalf("write last-good config: %v", err)
	}

	s := New(Config{
		DataDir:       dataDir,
		XrayPath:      xrayPath,
		XrayAssetsDir: dataDir,
	})
	s.lastConfig = configPath
	s.appliedRev = 7

	oldCmd := exec.Command(
		os.Args[0],
		"-test.run=TestNodeAgentHelperProcess",
		"--",
		"runtime-wait",
	)
	if err := oldCmd.Start(); err != nil {
		t.Fatalf("start previous runtime helper: %v", err)
	}
	go func() {
		_ = oldCmd.Wait()
	}()

	s.mu.Lock()
	s.lastRuntime = oldCmd
	s.mu.Unlock()

	t.Cleanup(func() {
		_ = s.stopRuntime()
	})

	previousRuntimeCommand := runtimeCommand
	startCalls := 0

	runtimeCommand = func(_ string, args ...string) *exec.Cmd {
		for _, arg := range args {
			if arg == "-test" {
				return exec.Command(
					os.Args[0],
					"-test.run=TestNodeAgentHelperProcess",
					"--",
					"runtime-exit",
				)
			}
		}

		startCalls++
		if startCalls == 1 {
			return exec.Command(filepath.Join(dataDir, "missing-runtime"))
		}

		return exec.Command(
			os.Args[0],
			"-test.run=TestNodeAgentHelperProcess",
			"--",
			"runtime-wait",
		)
	}

	t.Cleanup(func() {
		runtimeCommand = previousRuntimeCommand
	})

	_, err := s.applyConfig(context.Background(), &nodev1.RuntimeConfigRequest{
		ConfigJson:      `{"new":true}`,
		DesiredRevision: 99,
	}, "config synced")
	if err == nil {
		t.Fatal("expected new runtime start to fail")
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}
	if string(got) != lastGood {
		t.Fatalf("previous config was not restored: %s", got)
	}

	if s.appliedRev != 7 {
		t.Fatalf(
			"applied revision advanced after failed runtime start: got %d want 7",
			s.appliedRev,
		)
	}

	s.mu.Lock()
	restoredRuntime := s.lastRuntime
	s.mu.Unlock()

	if restoredRuntime == nil {
		t.Fatal("previous runtime was not restarted")
	}
	if restoredRuntime == oldCmd {
		t.Fatal("runtime state still points at the killed previous process")
	}
	if startCalls != 2 {
		t.Fatalf("unexpected runtime start attempts: got %d want 2", startCalls)
	}
}

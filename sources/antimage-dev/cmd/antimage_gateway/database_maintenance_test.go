package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunManagedDatabaseMaintenance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper is not available on Windows")
	}

	t.Setenv("ANTIMAGE_INSTALL_MODE", "binary")
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "antimage")
	contents := "#!/bin/sh\nprintf '%s' \"$*\" > \"" + argsFile + "\"\n"
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}

	previousPath := antimageScriptPath
	antimageScriptPath = script
	t.Cleanup(func() { antimageScriptPath = previousPath })

	runManagedDatabaseMaintenance(context.Background())
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "database-maintenance" {
		t.Fatalf("unexpected arguments: %q", got)
	}
}

func TestRunManagedDatabaseMaintenanceSkipsDocker(t *testing.T) {
	t.Setenv("ANTIMAGE_INSTALL_MODE", "docker")
	previousPath := antimageScriptPath
	antimageScriptPath = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { antimageScriptPath = previousPath })

	runManagedDatabaseMaintenance(context.Background())
}

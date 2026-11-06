package nodeagent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// Check with the installed core before publishing a policy that offline
// recovery would restore or replacing the current runtime/configuration.
func (s *Server) validateXrayRuntimeConfig(ctx context.Context, configJSON string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	running, appliedPath := s.lastRuntime != nil, s.lastConfig
	s.mu.Unlock()
	if running && appliedPath != "" {
		if applied, err := os.ReadFile(appliedPath); err == nil && bytes.Equal(applied, []byte(configJSON)) {
			return nil // Reconciliation/retries need no new validation process.
		}
	}
	if _, err := os.Stat(s.cfg.XrayPath); err != nil {
		if os.IsNotExist(err) {
			return nil // Native-only nodes may not have a core installed yet.
		}
		return fmt.Errorf("inspect target Xray: %w", err)
	}
	if err := os.MkdirAll(s.cfg.DataDir, 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.cfg.DataDir, ".xray-config-check-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(configJSON); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := xrayCommandContext(checkCtx, s.cfg.XrayPath, "run", "-test", "-config", file.Name())
	cmd.Env = append(cmd.Environ(), "XRAY_LOCATION_ASSET="+s.cfg.XrayAssetsDir)
	output := &xrayConfigCheckOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil {
		if checkCtx.Err() != nil {
			return fmt.Errorf("target Xray config check: %w", checkCtx.Err())
		}
		return fmt.Errorf("target Xray rejected config: %w: %s", err, strings.TrimSpace(output.String()))
	}
	return nil
}

// Do not retain unbounded diagnostics from a failing executable.
type xrayConfigCheckOutput struct{ bytes.Buffer }

func (out *xrayConfigCheckOutput) Write(raw []byte) (int, error) {
	const capacity = 4096
	if remaining := capacity - out.Len(); remaining > 0 {
		out.Buffer.Write(raw[:min(remaining, len(raw))])
	}
	return len(raw), nil
}

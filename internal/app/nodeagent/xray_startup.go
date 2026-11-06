package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// err is published once before done is closed. Readers wait on done first.
type xrayRuntimeExit struct {
	done chan struct{}
	err  error
}

func xrayStartupRPC(configPath string) (int, bool, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return 0, false, err
	}
	port, present, err := xrayAPIPortFromRuntimeConfig(string(raw))
	if err != nil || !present {
		return 0, false, err
	}
	var cfg struct {
		API struct {
			Services []string `json:"services"`
		} `json:"api"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return 0, false, err
	}
	for _, service := range cfg.API.Services {
		if service == "StatsService" {
			return port, true, nil
		}
	}
	return 0, false, nil
}

// Starting an executable alone does not prove its listeners started. Detect
// early exits and, for managed API configs, require a non-resetting Stats RPC.
// Legacy configs without StatsService retain bounded process-liveness checks.
func (s *Server) waitXrayStartup(ctx context.Context, exit *xrayRuntimeExit, port int, checkRPC bool) error {
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stableAt := time.Now().Add(250 * time.Millisecond)
	var client *xrayStatsClient
	if checkRPC {
		client = newXrayStatsClient(s.cfg.XrayPath, port)
		defer client.rpc.close()
	}
	ready := !checkRPC
	var lastErr error
	for {
		select {
		case <-exit.done:
			return fmt.Errorf("Xray exited before startup confirmation: %v", exit.err)
		case <-startup.Done():
			return fmt.Errorf("Xray startup confirmation: %w (last RPC error: %v)", startup.Err(), lastErr)
		default:
		}
		if !ready {
			probe, cancel := context.WithTimeout(startup, 200*time.Millisecond)
			_, lastErr = client.queryStats(probe, "antimage-startup-confirmation", false)
			cancel()
			ready = lastErr == nil
		}
		if ready && !time.Now().Before(stableAt) {
			select {
			case <-exit.done:
				return fmt.Errorf("Xray exited during startup confirmation: %v", exit.err)
			default:
				return nil
			}
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-timer.C:
		case <-exit.done:
			timer.Stop()
		case <-startup.Done():
			timer.Stop()
		}
	}
}

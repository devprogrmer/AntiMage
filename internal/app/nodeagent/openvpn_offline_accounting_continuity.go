package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

var openVPNOfflineReadStatus = os.ReadFile

func (s *Server) openVPNOfflineGeneration(tag, root string, durable bool) (string, error) {
	s.mu.Lock()
	process := s.openVPNRuntimes[tag]
	pid := 0
	if process != nil && process.cmd != nil && process.cmd.Process != nil {
		pid = process.cmd.Process.Pid
	}
	s.mu.Unlock()
	path := filepath.Join(root, "accounting-generation.json")
	if pid > 0 {
		identity, err := offlineProcessIdentity(strconv.Itoa(pid))
		if err != nil {
			return "", err
		}
		if raw, err := os.ReadFile(path); err == nil {
			var recorded string
			if json.Unmarshal(raw, &recorded) == nil && (recorded == identity || strings.HasPrefix(recorded, identity+":daemon:")) {
				return recorded, nil
			}
		}
		if !durable {
			return identity, nil
		}
		if err := offlineDurableJSON(path, identity); err != nil {
			return "", err
		}
		return identity, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	} // Legacy helper: connected-since remains mandatory provenance.
	if err != nil {
		return "", err
	}
	var identity string
	if err := json.Unmarshal(raw, &identity); err != nil {
		return "", err
	}
	if identity == "" {
		return "", fmt.Errorf("empty OpenVPN generation")
	}
	return identity, nil
}

func (s *Server) checkpointOpenVPNOffline(ctx context.Context) error {
	ctx = context.WithValue(ctx, offlineCheckpointContextKey{}, true)
	_, err := s.collectOpenVPNUserUsage(ctx, nil)
	return err
}

func (s *Server) previewOpenVPNOffline(ctx context.Context) (*offlineAccountingPreview, error) {
	preview := &offlineAccountingPreview{}
	_, err := s.collectOpenVPNUserUsage(context.WithValue(ctx, offlinePreviewContextKey{}, preview), nil)
	return preview, err
}

func (s *Server) quotaCheckOpenVPNOffline(ctx context.Context) error {
	preview, err := s.previewOpenVPNOffline(ctx)
	if err != nil {
		return err
	}
	roots, err := offlineHelperRoots(s.cfg.DataDir, "openvpn")
	if err != nil {
		return err
	}
	var once sync.Once
	var checkpointErr error
	beforeDisconnect := func() error {
		once.Do(func() { checkpointErr = s.checkpointOpenVPNOffline(ctx) })
		return checkpointErr
	}
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, err := os.ReadFile(filepath.Join(root, "session-helper.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var cfg nativeSessionHelperConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		cfg, err = s.openVPNOfflinePolicySnapshot(cfg, preview)
		if err != nil {
			return err
		}
		cfg.OfflinePolicyOnly = true
		cfg.OfflineClientsReady = true
		cfg.OfflineClients = preview.OpenVPNClients[root]
		cfg.OfflineBeforeDisconnect = beforeDisconnect
		if err := s.refreshOpenVPNSessions(cfg.InboundTag, root, cfg); err != nil {
			return err
		}
	}
	return checkpointErr
}

func (s *Server) openVPNOfflinePolicySnapshot(cfg nativeSessionHelperConfig, preview *offlineAccountingPreview) (nativeSessionHelperConfig, error) {
	cfg.OfflineRawUsage = make(map[int64]uint64)
	for username, uid := range cfg.Users {
		policy := cfg.Policies[username]
		pendingRaw, err := offlinePreviewPendingRaw(preview, uid, cfg.InboundTag)
		if err != nil {
			return cfg, err
		}
		usage, err := s.offlinePolicyRaw(preview.Baseline, "openvpn", uid, cfg.InboundTag, preview.PendingID, policy.ReflectedUsageBatchID, pendingRaw)
		if err != nil {
			return cfg, err
		}
		cfg.OfflineRawUsage[uid] = usage
	}
	return cfg, nil
}

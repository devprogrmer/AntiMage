package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Server) quotaCheckPPPOffline(ctx context.Context) error {
	previews, err := s.previewPPPOffline(ctx)
	if err != nil {
		return err
	}
	checkpointed := false
	for _, protocol := range []string{"l2tp", "pptp"} {
		roots, err := offlineHelperRoots(s.cfg.DataDir, protocol)
		if err != nil {
			return err
		}
		for _, root := range roots {
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
			paths, err := filepath.Glob(filepath.Join(root, "ppp-accounting", "active", "*.json"))
			if err != nil {
				return err
			}
			liveSessions := make([]pppOfflineSession, 0, len(paths))
			for _, path := range paths {
				if err := ctx.Err(); err != nil {
					return err
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				var record pppOfflineSession
				if err := json.Unmarshal(raw, &record); err != nil {
					return err
				}
				// Ignore ended sessions and replaced interfaces; never signal a reused PID.
				live, err := pppOfflineActiveSession(root, record.Interface, record.PeerIP, record.UserID)
				if err != nil {
					continue
				}
				parts := strings.Split(live.Process, ":")
				if len(parts) != 3 {
					return fmt.Errorf("invalid PPP process identity")
				}
				if _, finalErr := os.Stat(filepath.Join(root, "ppp-accounting", "final", live.ID+".json")); finalErr == nil {
					continue
				} else if !os.IsNotExist(finalErr) {
					return finalErr
				}
				identity, err := pppOfflineReadProcess(parts[1])
				if err != nil || identity != live.Process {
					continue
				}
				if live.InboundTag != cfg.InboundTag {
					return fmt.Errorf("PPP policy inbound mismatch")
				}
				liveSessions = append(liveSessions, live)
			}
			ipLimits := map[int64]int64{}
			deviceLimits := map[int64]int64{}
			for username, uid := range cfg.Users {
				ipLimits[uid] = cfg.Policies[username].IPLimit
				deviceLimits[uid] = cfg.Policies[username].DeviceLimit
			}
			observations := make([]nativeOfflineIPObservation, 0, len(liveSessions))
			for _, live := range liveSessions {
				observations = append(observations, nativeOfflineIPObservation{live.ID, live.UserID, live.ClientIP})
			}
			deniedIPs := nativeOfflineIPLimitDenied(observations, ipLimits)
			deniedDevices := nativeOfflineDeviceLimitDenied(observations, deviceLimits)
			for _, live := range liveSessions {
				for username, uid := range cfg.Users {
					if uid != live.UserID {
						continue
					}
					policy, ok := cfg.Policies[username]
					if !ok {
						continue
					}
					preview := previews[protocol]
					pendingRaw, err := offlinePreviewPendingRaw(preview, uid, cfg.InboundTag)
					if err != nil {
						return err
					}
					usage, err := s.offlinePolicyRaw(preview.Baseline, protocol, uid, cfg.InboundTag, preview.PendingID, policy.ReflectedUsageBatchID, pendingRaw)
					if err != nil {
						return err
					}
					allowed, reason := s.localQuotaAllowed(protocol, uid, cfg.InboundTag, policy, usage, time.Now().UTC())
					if allowed && !deniedIPs[live.ID] && !deniedDevices[live.ID] {
						continue
					}
					if deniedIPs[live.ID] {
						reason = "IP limit reached"
					} else if deniedDevices[live.ID] {
						reason = "device limit reached"
					}
					marker := filepath.Join(root, "ppp-accounting", "deny-"+live.ID+".json")
					if info, err := os.Stat(marker); err == nil && time.Since(info.ModTime()) < 30*time.Second {
						continue
					}
					if !checkpointed {
						if err := s.checkpointPPPOffline(ctx); err != nil {
							return err
						}
						checkpointed = true
					}
					effective := nativeSessionEffectiveLiveUsage(policy, usage)
					s.appendLog(fmt.Sprintf("local %s quota cutoff at=%s user_id=%d username=%q peer_ip=%s interface=%s pppd=%s raw_bytes=%d effective_bytes=%d data_limit=%d reason=%q action=SIGTERM", protocol, time.Now().UTC().Format(time.RFC3339Nano), live.UserID, username, live.PeerIP, live.Interface, live.Process, usage, effective, policy.DataLimit, reason))
					if err := pppOfflineTerminateSession(ctx, live.Process); err != nil {
						s.appendLog(fmt.Sprintf("local %s quota disconnect failed: user_id=%d interface=%s pppd=%s error=%v", protocol, live.UserID, live.Interface, live.Process, err))
						return err
					}
					s.appendLog(fmt.Sprintf("local %s quota disconnect complete: user_id=%d interface=%s pppd=%s process_exit=confirmed", protocol, live.UserID, live.Interface, live.Process))
					if err := offlineDurableJSON(marker, live.ID); err != nil {
						return err
					}
					break
				}
			}
		}
	}
	return nil
}

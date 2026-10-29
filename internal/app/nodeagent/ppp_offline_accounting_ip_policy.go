package nodeagent

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type nativeOfflineIPObservation struct {
	SessionID string
	UserID    int64
	RealIP    string
}

// pppOfflineAdmissionLimit checks durable, process-verified sessions before
// pppd brings a newly authenticated peer up.
func pppOfflineAdmissionLimit(root string, uid int64, clientIP, excludeProcess string, policy nativeSessionUserPolicy) (bool, string, error) {
	if policy.DeviceLimit <= 0 && policy.IPLimit <= 0 {
		return false, "", nil
	}
	paths := make([]string, 0)
	activeDirs := make(map[string]string)
	for _, protocol := range []string{"l2tp", "pptp"} {
		dirs, err := filepath.Glob(filepath.Join(root, protocol, "*", "ppp-accounting", "active"))
		if err != nil {
			return false, "", err
		}
		for _, activeDir := range dirs {
			files, err := filepath.Glob(filepath.Join(activeDir, "*.json"))
			if err != nil {
				return false, "", err
			}
			for _, path := range files {
				paths = append(paths, path)
				activeDirs[path] = activeDir
			}
		}
	}
	// Unit callers and pre-upgrade layouts can provide a single runtime root.
	legacyActiveDir := filepath.Join(root, "ppp-accounting", "active")
	if info, err := os.Stat(legacyActiveDir); err == nil && info.IsDir() {
		files, err := filepath.Glob(filepath.Join(legacyActiveDir, "*.json"))
		if err != nil {
			return false, "", err
		}
		for _, path := range files {
			paths = append(paths, path)
			activeDirs[path] = legacyActiveDir
		}
	} else if err != nil && !os.IsNotExist(err) {
		return false, "", err
	}
	live := make([]pppOfflineSession, 0, len(paths))
	seenProcesses := map[string]struct{}{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false, "", err
		}
		var record pppOfflineSession
		if err := json.Unmarshal(raw, &record); err != nil {
			return false, "", fmt.Errorf("decode active PPP session: %w", err)
		}
		if record.UserID != uid || record.Final || record.Process == excludeProcess {
			continue
		}
		runtimeRoot := filepath.Dir(filepath.Dir(activeDirs[path]))
		verified, err := pppOfflineActiveSession(runtimeRoot, record.Interface, record.PeerIP, uid)
		if err != nil {
			continue
		}
		parts := strings.Split(verified.Process, ":")
		if len(parts) != 3 {
			continue
		}
		identity, err := pppOfflineReadProcess(parts[1])
		if err != nil || identity != verified.Process {
			continue
		}
		live = append(live, verified)
		seenProcesses[verified.Process] = struct{}{}
	}
	pendingPaths, err := filepath.Glob(filepath.Join(root, "ppp-admission", "pending", "*.json"))
	if err != nil {
		return false, "", err
	}
	for _, path := range pendingPaths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false, "", err
		}
		var record pppOfflineSession
		if err := json.Unmarshal(raw, &record); err != nil {
			return false, "", fmt.Errorf("decode pending PPP admission: %w", err)
		}
		if record.UserID != uid || record.Process == excludeProcess {
			continue
		}
		if _, exists := seenProcesses[record.Process]; exists {
			continue
		}
		parts := strings.Split(record.Process, ":")
		if len(parts) != 3 {
			return false, "", fmt.Errorf("invalid pending PPP process identity")
		}
		identity, identityErr := pppOfflineReadProcess(parts[1])
		if identityErr != nil || identity != record.Process {
			if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
				return false, "", removeErr
			}
			continue
		}
		live = append(live, record)
		seenProcesses[record.Process] = struct{}{}
	}
	if policy.DeviceLimit > 0 && int64(len(live)) >= policy.DeviceLimit {
		return true, "device limit reached", nil
	}
	if policy.IPLimit > 0 {
		candidate, err := netip.ParseAddr(strings.TrimSpace(clientIP))
		if err != nil || candidate.IsUnspecified() {
			return true, "real client IP unavailable for IP limit", nil
		}
		candidate = candidate.Unmap()
		ips := map[string]struct{}{}
		for _, session := range live {
			address, err := netip.ParseAddr(session.ClientIP)
			if err != nil || address.IsUnspecified() {
				return true, "active session has no reliable real client IP", nil
			}
			ips[address.Unmap().String()] = struct{}{}
		}
		if _, exists := ips[candidate.String()]; !exists && int64(len(ips)) >= policy.IPLimit {
			return true, "IP limit reached", nil
		}
	}
	return false, "", nil
}

// nativeOfflineDeviceLimitDenied enforces the configured concurrent-session
// cap using durable PPP session IDs. PPP exposes a session identity, not a
// hardware identity, so this limits authenticated sessions rather than
// claiming to identify a physical device.
func nativeOfflineDeviceLimitDenied(observations []nativeOfflineIPObservation, limits map[int64]int64) map[string]bool {
	byUser := map[int64][]nativeOfflineIPObservation{}
	for _, observation := range observations {
		if limits[observation.UserID] > 0 && observation.SessionID != "" {
			byUser[observation.UserID] = append(byUser[observation.UserID], observation)
		}
	}
	denied := map[string]bool{}
	for uid, sessions := range byUser {
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].SessionID < sessions[j].SessionID })
		limit := limits[uid]
		for i := limit; i < int64(len(sessions)); i++ {
			denied[sessions[i].SessionID] = true
		}
	}
	return denied
}

// Session authentication and source IP are observable; physical device identity
// is not. Missing/malformed real addresses disable IP enforcement for that user.
func nativeOfflineIPLimitDenied(observations []nativeOfflineIPObservation, limits map[int64]int64) map[string]bool {
	byUser := map[int64][]nativeOfflineIPObservation{}
	unreliable := map[int64]bool{}
	for _, observation := range observations {
		if limits[observation.UserID] <= 0 {
			continue
		}
		address, err := netip.ParseAddr(observation.RealIP)
		if err != nil || address.IsUnspecified() || observation.SessionID == "" {
			unreliable[observation.UserID] = true
			continue
		}
		observation.RealIP = address.Unmap().String()
		byUser[observation.UserID] = append(byUser[observation.UserID], observation)
	}
	denied := map[string]bool{}
	for uid, sessions := range byUser {
		if unreliable[uid] {
			continue
		}
		sort.Slice(sessions, func(i, j int) bool {
			if sessions[i].RealIP == sessions[j].RealIP {
				return sessions[i].SessionID < sessions[j].SessionID
			}
			return sessions[i].RealIP < sessions[j].RealIP
		})
		allowedIPs := map[string]bool{}
		for _, session := range sessions {
			if !allowedIPs[session.RealIP] {
				if int64(len(allowedIPs)) >= limits[uid] {
					denied[session.SessionID] = true
					continue
				}
				allowedIPs[session.RealIP] = true
			}
		}
	}
	return denied
}

func openVPNOfflineIPDenied(cfg nativeSessionHelperConfig, clients []openVPNStatusClient) map[string]bool {
	limits := map[int64]int64{}
	observations := make([]nativeOfflineIPObservation, 0, len(clients))
	for _, client := range clients {
		username := client.Username
		uid := cfg.Users[username]
		if uid <= 0 {
			username = client.CommonName
			uid = cfg.Users[username]
		}
		if uid <= 0 {
			continue
		}
		policy := cfg.Policies[username]
		limits[uid] = policy.IPLimit
		realIP, _ := openVPNStatusRealAddressParts(client.RealAddress)
		observations = append(observations, nativeOfflineIPObservation{client.ClientID, uid, realIP})
	}
	return nativeOfflineIPLimitDenied(observations, limits)
}

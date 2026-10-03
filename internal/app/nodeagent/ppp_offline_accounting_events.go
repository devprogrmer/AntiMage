package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type pppOfflineSession struct {
	ID         string
	UserID     int64
	InboundTag string
	Interface  string
	PeerIP     string
	ClientIP   string
	Identity   string
	Process    string
	Total      uint64
	Final      bool
}

var pppOfflineReadIdentity = pppOfflineInterfaceIdentity
var pppOfflineReadProcess = offlineProcessIdentity
var pppOfflineReadCounter = readL2TPInterfaceCounter

func offlineDurableJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, raw, 0600); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		defer dir.Close()
		return dir.Sync()
	}
	return nil
}

func offlineProcessIdentity(pid string) (string, error) {
	if _, err := strconv.ParseUint(pid, 10, 32); err != nil {
		return "", err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
	if err != nil {
		return "", err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return "", fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(raw)[end+1:])
	if len(fields) < 20 {
		return "", fmt.Errorf("short process stat")
	}
	return strings.TrimSpace(string(boot)) + ":" + pid + ":" + fields[19], nil
}

func pppOfflineSessionEvent(root, event string, record pppOfflineSession) error {
	path := filepath.Join(root, "ppp-accounting", "active", record.Interface+".json")
	if !l2TPPPPInterfacePattern.MatchString(record.Interface) {
		return fmt.Errorf("invalid PPP interface")
	}
	if event == "start" {
		return offlineDurableJSON(path, record)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("missing trusted PPP start metadata: %w", err)
	}
	var start pppOfflineSession
	if err := json.Unmarshal(raw, &start); err != nil {
		return err
	}
	if start.UserID != record.UserID || start.InboundTag != record.InboundTag || start.PeerIP != record.PeerIP || start.Process != record.Process {
		return fmt.Errorf("PPP stop metadata does not match start")
	}
	start.Total = record.Total
	start.Final = true
	// Retain active metadata for retried stop hooks. A new start replaces it.
	return offlineDurableJSON(filepath.Join(root, "ppp-accounting", "final", start.ID+".json"), start)
}

func pppOfflineActiveSession(root, iface, peer string, uid int64) (pppOfflineSession, error) {
	raw, err := os.ReadFile(filepath.Join(root, "ppp-accounting", "active", iface+".json"))
	if err != nil {
		return pppOfflineSession{}, err
	}
	var record pppOfflineSession
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, err
	}
	identity, err := pppOfflineReadIdentity(iface)
	if err != nil {
		return record, err
	}
	if record.ID == "" || record.UserID != uid || record.PeerIP != peer || record.Identity != identity {
		return record, fmt.Errorf("PPP session identity mismatch")
	}
	return record, nil
}

func pppOfflineFinalRecords(root string) ([]pppOfflineSession, error) {
	paths, err := filepath.Glob(filepath.Join(root, "ppp-accounting", "final", "*.json"))
	if err != nil {
		return nil, err
	}
	if len(paths) > maxAccountingCounterSeries {
		return nil, fmt.Errorf("PPP final spool capacity exceeded")
	}
	records := make([]pppOfflineSession, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var record pppOfflineSession
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		if !record.Final || record.ID == "" || record.UserID <= 0 || !l2TPPPPInterfacePattern.MatchString(record.Interface) {
			return nil, fmt.Errorf("invalid PPP final record")
		}
		records = append(records, record)
	}
	return records, nil
}

// Final counters and interface counters may differ in framing overhead. Charge
// the larger cumulative value once, never the sum of both observations.
func pppOfflineAdvanceFinal(baseline map[string]uint64, record pppOfflineSession) (uint64, error) {
	key := l2TPUsageBaselineKey(record.InboundTag, record.Interface, record.PeerIP) + "\x00session:" + record.ID
	previous := baseline[key]
	if record.Total <= previous {
		return 0, nil
	}
	delta := record.Total - previous
	owner := offlineAccountingTotalKey(record.UserID, record.InboundTag)
	if ^uint64(0)-baseline[owner] < delta {
		return 0, fmt.Errorf("PPP accounting overflow")
	}
	baseline[key] = record.Total
	baseline[owner] += delta
	return delta, nil
}

func offlineHelperRoots(dataDir, protocol string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, protocol))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	roots := []string{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		root := filepath.Join(dataDir, protocol, entry.Name())
		if _, err := os.Stat(filepath.Join(root, "usage-helper.json")); err == nil {
			roots = append(roots, root)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return roots, nil
}

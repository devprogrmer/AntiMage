package nodeagent

import (
	"crypto/sha256"
	"encoding/hex"
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
var pppOfflineDetectProtocol = pppOfflineProcessProtocol
var pppOfflineReadCounter = readL2TPInterfaceCounter
var pppSessionHelperExecutable = os.Executable

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

// pppOfflineProcessProtocol resolves a pppd process to the VPN daemon that
// owns its transport. Both protocols pass their live transport peer through
// pppd's ipparam; ancestry prevents the two global pre-up hooks from applying
// policy to the other protocol.
func pppOfflineProcessProtocol(pid string) (string, error) {
	if _, err := strconv.ParseUint(pid, 10, 32); err != nil {
		return "", fmt.Errorf("invalid PPPD_PID: %w", err)
	}
	current := pid
	for depth := 0; depth < 5; depth++ {
		comm, err := os.ReadFile(filepath.Join("/proc", current, "comm"))
		if err != nil {
			return "", err
		}
		switch strings.TrimSpace(string(comm)) {
		case "pptpd", "pptpctrl":
			return "pptp", nil
		case "xl2tpd":
			return "l2tp", nil
		}
		stat, err := os.ReadFile(filepath.Join("/proc", current, "stat"))
		if err != nil {
			return "", err
		}
		end := strings.LastIndexByte(string(stat), ')')
		if end < 0 {
			return "", fmt.Errorf("invalid process stat for pid %s", current)
		}
		fields := strings.Fields(string(stat)[end+1:])
		if len(fields) < 2 || fields[1] == "0" || fields[1] == current {
			break
		}
		current = fields[1]
	}
	// Unrelated PPP consumers also run the global ip-pre-up dispatcher. They
	// are outside these protocol hooks and must pass through untouched.
	return "", nil
}

func pppOfflineSessionEvent(root, event string, record pppOfflineSession) error {
	if !l2TPPPPInterfacePattern.MatchString(record.Interface) {
		return fmt.Errorf("invalid PPP interface")
	}
	if strings.TrimSpace(record.Process) == "" {
		return fmt.Errorf("missing PPP process identity")
	}
	path := pppOfflineSessionActivePath(root, record.Interface, record.Process)
	if event == "start" {
		return offlineDurableJSON(path, record)
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Read pre-upgrade records so a runtime restart can still finish a
		// session that was active before per-process PPP state was introduced.
		legacyPath := filepath.Join(root, "ppp-accounting", "active", record.Interface+".json")
		raw, err = os.ReadFile(legacyPath)
	}
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

func pppOfflineSessionActivePath(root, iface, process string) string {
	sum := sha256.Sum256([]byte(process))
	return filepath.Join(root, "ppp-accounting", "active", iface+"-"+hex.EncodeToString(sum[:12])+".json")
}

func pppOfflineAdmissionReservationPath(root, process string) string {
	sum := sha256.Sum256([]byte(process))
	return filepath.Join(root, "ppp-admission", "pending", hex.EncodeToString(sum[:16])+".json")
}

func pppOfflineWriteAdmissionReservation(root string, uid int64, inbound, process, clientIP string) error {
	if uid <= 0 || strings.TrimSpace(inbound) == "" || strings.TrimSpace(process) == "" {
		return fmt.Errorf("incomplete PPP admission reservation identity")
	}
	return offlineDurableJSON(pppOfflineAdmissionReservationPath(root, process), pppOfflineSession{
		UserID: uid, InboundTag: inbound, Process: process, ClientIP: clientIP,
	})
}

func pppOfflineRemoveAdmissionReservation(root, process string) error {
	if strings.TrimSpace(process) == "" {
		return nil
	}
	err := os.Remove(pppOfflineAdmissionReservationPath(root, process))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func pppOfflineFindActiveSession(root, iface, process string) (pppOfflineSession, error) {
	if !l2TPPPPInterfacePattern.MatchString(iface) || strings.TrimSpace(process) == "" {
		return pppOfflineSession{}, fmt.Errorf("invalid PPP session identity")
	}
	paths := []string{pppOfflineSessionActivePath(root, iface, process)}
	paths = append(paths, filepath.Join(root, "ppp-accounting", "active", iface+".json"))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return pppOfflineSession{}, err
		}
		var record pppOfflineSession
		if err := json.Unmarshal(raw, &record); err != nil {
			return pppOfflineSession{}, err
		}
		if record.Interface == iface && record.Process == process {
			return record, nil
		}
	}
	return pppOfflineSession{}, os.ErrNotExist
}

func pppOfflineActiveSession(root, iface, peer string, uid int64) (pppOfflineSession, error) {
	if !l2TPPPPInterfacePattern.MatchString(iface) {
		return pppOfflineSession{}, fmt.Errorf("invalid PPP interface")
	}
	identity, err := pppOfflineReadIdentity(iface)
	if err != nil {
		return pppOfflineSession{}, err
	}
	paths, err := filepath.Glob(filepath.Join(root, "ppp-accounting", "active", iface+"-*.json"))
	if err != nil {
		return pppOfflineSession{}, err
	}
	paths = append(paths, filepath.Join(root, "ppp-accounting", "active", iface+".json"))
	var mismatch bool
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return pppOfflineSession{}, err
		}
		var record pppOfflineSession
		if err := json.Unmarshal(raw, &record); err != nil {
			return record, err
		}
		if record.ID != "" && record.UserID == uid && record.PeerIP == peer && record.Interface == iface && record.Identity == identity {
			return record, nil
		}
		mismatch = true
	}
	if mismatch {
		return pppOfflineSession{}, fmt.Errorf("PPP session identity mismatch")
	}
	return pppOfflineSession{}, os.ErrNotExist
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

package nodeagent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type nativeSessionHelperConfig struct {
	OfflineIPDenied         map[string]bool                    `json:"-"`
	OfflineClientsReady     bool                               `json:"-"`
	OfflineClients          []openVPNStatusClient              `json:"-"`
	OfflineBeforeDisconnect func() error                       `json:"-"`
	OfflineRuntimeRoot      string                             `json:"-"`
	OfflineRawUsage         map[int64]uint64                   `json:"-"`
	OfflinePolicyOnly       bool                               `json:"-"`
	Callback                nativeRuntimeSessionCallback       `json:"callback"`
	InboundTag              string                             `json:"inbound_tag"`
	Protocol                string                             `json:"protocol,omitempty"`
	Users                   map[string]int64                   `json:"users"`
	Policies                map[string]nativeSessionUserPolicy `json:"policies,omitempty"`
	StateDir                string                             `json:"state_dir"`
	ManagementNetwork       string                             `json:"management_network,omitempty"`
	ManagementAddress       string                             `json:"management_address,omitempty"`
}

type nativeSessionUserPolicy struct {
	DeviceLimit           int64   `json:"device_limit,omitempty"`
	IPLimit               int64   `json:"ip_limit,omitempty"`
	Status                string  `json:"status"`
	UsedTraffic           int64   `json:"used_traffic"`
	DataLimit             int64   `json:"data_limit"`
	Expire                int64   `json:"expire"`
	ReflectedUsageBatchID string  `json:"reflected_usage_batch_id,omitempty"`
	UploadSpeedLimit      int64   `json:"upload_speed_limit,omitempty"`
	DownloadSpeedLimit    int64   `json:"download_speed_limit,omitempty"`
	UsageCoefficient      float64 `json:"usage_coefficient,omitempty"`
	InboundCoefficient    float64 `json:"inbound_coefficient,omitempty"`
}

var nativePPPProcessSignal = pppOfflineSignalSession

func nativeSessionUserPolicyAllowed(
	policy nativeSessionUserPolicy,
	now time.Time,
) (bool, string) {
	status := strings.ToLower(strings.TrimSpace(policy.Status))

	switch status {
	case "on_hold":
		return true, ""

	case "active":
		if policy.DataLimit > 0 &&
			policy.UsedTraffic >= policy.DataLimit {
			return false, "data limit reached"
		}

		if policy.Expire > 0 &&
			policy.Expire <= now.Unix() {
			return false, "expired"
		}

		return true, ""

	default:
		if status == "" {
			return false, "missing status"
		}
		return false, "status " + status
	}
}
func RunNativeSessionEventHelper(args []string) error {
	if len(args) < 2 || len(args) > 3 {
		return fmt.Errorf(
			"usage: antimage-node session-event <config> <pre-up|start|seen|stop> [transport-peer-ip]",
		)
	}

	configPath := strings.TrimSpace(args[0])
	eventName := strings.ToLower(strings.TrimSpace(args[1]))

	switch eventName {
	case "pre-up", "start", "seen", "stop":
	default:
		return fmt.Errorf("unsupported session event %q", eventName)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read session helper config: %w", err)
	}

	var cfg nativeSessionHelperConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse session helper config: %w", err)
	}

	protocol := strings.ToLower(strings.TrimSpace(cfg.Protocol))
	if protocol == "" {
		protocol = "ov"
	}
	if eventName == "pre-up" {
		if protocol != "pptp" && protocol != "l2tp" {
			return fmt.Errorf("pre-up admission is unsupported for %s", protocol)
		}
		actualProtocol, protocolErr := pppOfflineDetectProtocol(firstNonEmptyEnv("PPPD_PID"))
		if protocolErr != nil {
			return fmt.Errorf("resolve PPP transport protocol: %w", protocolErr)
		}
		if actualProtocol == "" || actualProtocol != protocol {
			// Both managed hooks run from pppd's global dispatcher. Process
			// ancestry selects this runtime's own session before parsing ipparam.
			return nil
		}
	}
	clientIP := firstNonEmptyIPEnv("trusted_ip", "trusted_ip6", "IPPARAM", "REMOTENUMBER", "IP_REAL")
	if len(args) == 3 && nativeSpeedIsPPPProtocol(protocol) {
		address, parseErr := netip.ParseAddr(strings.TrimSpace(args[2]))
		if parseErr != nil || address.IsUnspecified() {
			return fmt.Errorf("invalid PPP transport peer IP %q", args[2])
		}
		clientIP = address.Unmap().String()
	}
	if eventName == "pre-up" {
		if clientIP == "" {
			return fmt.Errorf("%s transport peer IP is missing from pppd ipparam", protocol)
		}
	}

	commonName := firstNonEmptyEnv("common_name", "PEERNAME", "USERNAME")
	if commonName == "" {
		return fmt.Errorf("%s session username is missing", protocol)
	}

	userID := cfg.Users[commonName]
	pppProcess := ""
	if nativeSpeedIsPPPProtocol(protocol) && (eventName == "start" || eventName == "stop") {
		pppProcess, err = pppOfflineReadProcess(firstNonEmptyEnv("PPPD_PID"))
		if err != nil {
			return fmt.Errorf("PPP process identity: %w", err)
		}
	}
	if eventName == "stop" && nativeSpeedIsPPPProtocol(protocol) {
		iface := firstNonEmptyEnv("IFNAME", "DEVICE")
		record, readErr := pppOfflineFindActiveSession(filepath.Dir(configPath), iface, pppProcess)
		if readErr != nil {
			return readErr
		}
		if record.InboundTag != cfg.InboundTag {
			return fmt.Errorf("PPP stop inbound mismatch")
		}
		userID = record.UserID
	}
	if userID <= 0 {
		return fmt.Errorf(
			"%s user %q is not present in runtime state",
			protocol,
			commonName,
		)
	}
	policy := cfg.Policies[commonName]
	if eventName == "pre-up" {
		return rejectNativePPPAdmission(configPath, cfg, commonName, userID, "", clientIP, true)
	}
	// pppd can invoke ip-up after a concurrent pre-up rejection has already
	// signalled its process. Recheck durable policy at the session boundary so
	// the rejected reconnect cannot leave a Panel session marked online.
	if eventName == "start" && nativeSpeedIsPPPProtocol(protocol) {
		if err := rejectNativePPPAdmission(configPath, cfg, commonName, userID, pppProcess, clientIP, false); err != nil {
			return err
		}
	}

	assignedIP := firstNonEmptyEnv(
		"ifconfig_pool_remote_ip",
		"IPREMOTE",
		"PPP_REMOTE",
		"IP_REMOTE",
	)

	interfaceName := firstNonEmptyEnv("IFNAME", "DEVICE")

	trustedPort := firstNonEmptyEnv("trusted_port")

	// A reconnect must consult the checkpoint retained before client-kill.
	// Static credentials alone cannot represent traffic accrued offline.
	if eventName == "start" && (protocol == "ov" || protocol == "openvpn" || protocol == "anyconnect") {
		dataDir := filepath.Dir(filepath.Dir(filepath.Dir(configPath)))
		node := New(Config{DataDir: dataDir})
		accountingProtocol := "openvpn"
		if protocol == "anyconnect" {
			accountingProtocol = "anyconnect"
		}
		raw, err := node.durablePolicyRaw(accountingProtocol, userID, cfg.InboundTag, policy.ReflectedUsageBatchID)
		if err != nil {
			return fmt.Errorf("%s admission accounting: %w", accountingProtocol, err)
		}
		if allowed, reason := nativeSessionUserPolicyAllowedWithLiveUsage(policy, raw, time.Now()); !allowed {
			return fmt.Errorf("%s admission denied: %s", accountingProtocol, reason)
		}
	}

	stateDir := strings.TrimSpace(cfg.StateDir)
	if stateDir == "" {
		stateDir = filepath.Join(
			filepath.Dir(configPath),
			"sessions",
		)
	}

	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return fmt.Errorf("create session state directory: %w", err)
	}

	stateKey := nativeSessionStateKey(
		cfg.InboundTag,
		userID,
		assignedIP,
		clientIP,
		trustedPort,
	)
	if pppProcess != "" {
		sum := sha256.Sum256([]byte(stateKey + "\x00" + pppProcess))
		stateKey = hex.EncodeToString(sum[:16])
	}

	statePath := filepath.Join(
		stateDir,
		stateKey+".session",
	)

	if (protocol == "ov" || protocol == "openvpn") && eventName == "start" {
		if pid := firstNonEmptyEnv("daemon_pid"); pid != "" {
			identity, err := offlineProcessIdentity(pid)
			if err != nil {
				return fmt.Errorf("OpenVPN daemon identity: %w", err)
			}
			if start := firstNonEmptyEnv("daemon_start_time"); start != "" {
				identity += ":daemon:" + start
			}
			if err := offlineDurableJSON(filepath.Join(filepath.Dir(configPath), "accounting-generation.json"), identity); err != nil {
				return err
			}
		}
	}

	if protocol == "l2tp" || protocol == "pptp" {
		record := pppOfflineSession{UserID: userID, InboundTag: cfg.InboundTag, Interface: interfaceName, PeerIP: assignedIP, Process: pppProcess, ClientIP: clientIP}
		if eventName == "start" {
			record.ID, err = newNativeSessionID()
			if err != nil {
				return err
			}
			record.Identity, err = pppOfflineReadIdentity(interfaceName)
			if err != nil {
				return err
			}
		} else if eventName == "stop" {
			sent, parseErr := strconv.ParseUint(firstNonEmptyEnv("BYTES_SENT"), 10, 64)
			if parseErr != nil {
				return fmt.Errorf("PPP BYTES_SENT: %w", parseErr)
			}
			received, parseErr := strconv.ParseUint(firstNonEmptyEnv("BYTES_RCVD"), 10, 64)
			if parseErr != nil {
				return fmt.Errorf("PPP BYTES_RCVD: %w", parseErr)
			}
			if ^uint64(0)-sent < received {
				return fmt.Errorf("PPP final counter overflow")
			}
			record.Total = sent + received
		}
		if eventName != "seen" {
			if err := pppOfflineSessionEvent(filepath.Dir(configPath), eventName, record); err != nil {
				return err
			}
			if eventName == "start" {
				dataDir := filepath.Dir(filepath.Dir(filepath.Dir(configPath)))
				if err := pppOfflineRemoveAdmissionReservation(dataDir, pppProcess); err != nil {
					return err
				}
			}
		}
	}

	if eventName == "stop" {
		if _, err := nativeSpeedHandlePPPSessionEvent(
			protocol,
			eventName,
			interfaceName,
			assignedIP,
			userID,
			policy,
		); err != nil {
			return err
		}
	}

	sessionID := ""

	switch eventName {
	case "start":
		sessionID, err = newNativeSessionID()
		if err != nil {
			return err
		}

		if err := os.WriteFile(
			statePath,
			[]byte(sessionID+"\n"),
			0600,
		); err != nil {
			return fmt.Errorf("write session state: %w", err)
		}

	case "seen", "stop":
		rawSessionID, readErr := os.ReadFile(statePath)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				return nil
			}
			return fmt.Errorf("read session state: %w", readErr)
		}

		sessionID = strings.TrimSpace(string(rawSessionID))
		if sessionID == "" {
			return fmt.Errorf("empty session state")
		}
	}

	shaped := false

	if eventName == "start" {
		shaped, err = nativeSpeedHandlePPPSessionEvent(
			protocol,
			eventName,
			interfaceName,
			assignedIP,
			userID,
			policy,
		)
		if err != nil {
			_ = os.Remove(statePath)
			return err
		}
	}

	node := New(Config{DataDir: filepath.Dir(filepath.Dir(filepath.Dir(configPath)))})

	if strings.TrimSpace(cfg.Callback.URL) == "" {
		return nil
	}

	err = node.sendNativeSessionEventOfflineSafe(
		context.Background(),
		cfg.Callback,
		nativeSessionEvent{
			UserID:     userID,
			Protocol:   protocol,
			InboundTag: strings.TrimSpace(cfg.InboundTag),
			SessionID:  sessionID,
			AssignedIP: assignedIP,
			ClientIP:   clientIP,
			DeviceID:   nativeSessionDeviceID(protocol, sessionID),
			DeviceType: nativeSessionDeviceType(protocol),
			ClientName: nativeSessionClientName(protocol),
			Platform:   "Unknown",
			Event:      eventName,
		},
	)

	if err != nil {
		if eventName == "start" {
			if shaped && interfaceName != "" {
				nativeSpeedClearInterface(interfaceName)
			}
			_ = os.Remove(statePath)
		}
		return err
	}

	if eventName == "stop" {
		_ = os.Remove(statePath)
	}

	return nil
}

func rejectNativePPPAdmission(
	configPath string,
	cfg nativeSessionHelperConfig,
	username string,
	userID int64,
	process string,
	clientIP string,
	reserve bool,
) error {
	protocol := strings.ToLower(strings.TrimSpace(cfg.Protocol))
	dataDir := filepath.Dir(filepath.Dir(filepath.Dir(configPath)))
	root := filepath.Dir(configPath)
	return withPPPAdmissionLock(dataDir, func() error {
		node := New(Config{DataDir: dataDir})
		policy := cfg.Policies[username]
		raw, err := node.durablePolicyRaw(protocol, userID, cfg.InboundTag, policy.ReflectedUsageBatchID)
		if err != nil {
			return fmt.Errorf("%s admission accounting: %w", protocol, err)
		}
		allowed, reason := nativeSessionUserPolicyAllowedWithLiveUsage(policy, raw, time.Now())
		if strings.TrimSpace(process) == "" {
			process, err = pppOfflineReadProcess(firstNonEmptyEnv("PPPD_PID"))
			if err != nil {
				return fmt.Errorf("%s admission denied (%s); resolve pppd identity: %w", protocol, reason, err)
			}
		}
		if allowed {
			limitDenied, limitReason, limitErr := pppOfflineAdmissionLimit(dataDir, userID, clientIP, process, policy)
			if limitErr != nil {
				return fmt.Errorf("%s session-limit admission: %w", protocol, limitErr)
			}
			if !limitDenied {
				if reserve && (policy.DeviceLimit > 0 || policy.IPLimit > 0) {
					if err := pppOfflineWriteAdmissionReservation(dataDir, userID, cfg.InboundTag, process, clientIP); err != nil {
						return fmt.Errorf("%s session-limit reservation: %w", protocol, err)
					}
				}
				return nil
			}
			reason = limitReason
		}
		denial := struct {
			Protocol  string    `json:"protocol"`
			Inbound   string    `json:"inbound_tag"`
			UserID    int64     `json:"user_id"`
			Username  string    `json:"username"`
			PeerIP    string    `json:"peer_ip"`
			ClientIP  string    `json:"client_ip,omitempty"`
			Process   string    `json:"process"`
			Reason    string    `json:"reason"`
			Timestamp time.Time `json:"timestamp"`
		}{protocol, cfg.InboundTag, userID, username, firstNonEmptyEnv("ifconfig_pool_remote_ip", "IPREMOTE", "PPP_REMOTE", "IP_REMOTE"), clientIP, process, reason, time.Now().UTC()}
		denialPath := filepath.Join(root, "ppp-accounting", "admission-denials", strconv.FormatInt(userID, 10)+".json")
		if err := offlineDurableJSON(denialPath, denial); err != nil {
			return fmt.Errorf("%s admission denied (%s); persist denial evidence: %w", protocol, reason, err)
		}
		if err := nativePPPProcessSignal(process); err != nil {
			return fmt.Errorf("%s admission denied (%s); disconnect pppd: %w", protocol, reason, err)
		}
		return fmt.Errorf("%s admission denied: %s", protocol, reason)
	})
}

func nativeSessionDeviceID(protocol string, sessionID string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch protocol {
	case "ov", "openvpn":
		sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID)))
		return "ov-" + hex.EncodeToString(sum[:8])
	default:
		return ""
	}
}

func nativeSessionDeviceType(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "ov", "openvpn":
		return "VPN Session"
	default:
		return "Unknown"
	}
}

func nativeSessionClientName(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "ov", "openvpn":
		return "OpenVPN"
	case "l2tp":
		return "L2TP"
	case "pptp":
		return "PPTP"
	default:
		return "Unknown"
	}
}
func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyIPEnv(keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			continue
		}
		address, err := netip.ParseAddr(value)
		if err == nil {
			return address.Unmap().String()
		}
	}
	return ""
}

func nativeSessionStateKey(
	inboundTag string,
	userID int64,
	assignedIP,
	clientIP,
	trustedPort string,
) string {
	value := fmt.Sprintf(
		"%s\x00%d\x00%s\x00%s\x00%s",
		strings.TrimSpace(inboundTag),
		userID,
		strings.TrimSpace(assignedIP),
		strings.TrimSpace(clientIP),
		strings.TrimSpace(trustedPort),
	)

	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

func newNativeSessionID() (string, error) {
	var value [16]byte

	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}

	return "ov-" + hex.EncodeToString(value[:]), nil
}

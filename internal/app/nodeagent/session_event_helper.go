package nodeagent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type nativeSessionHelperConfig struct {
	Callback          nativeRuntimeSessionCallback       `json:"callback"`
	InboundTag        string                             `json:"inbound_tag"`
	Protocol          string                             `json:"protocol,omitempty"`
	Users             map[string]int64                   `json:"users"`
	Policies          map[string]nativeSessionUserPolicy `json:"policies,omitempty"`
	StateDir          string                             `json:"state_dir"`
	ManagementNetwork string                             `json:"management_network,omitempty"`
	ManagementAddress string                             `json:"management_address,omitempty"`
}

type nativeSessionUserPolicy struct {
	Status                string `json:"status"`
	UsedTraffic           int64  `json:"used_traffic"`
	DataLimit             int64  `json:"data_limit"`
	Expire                int64  `json:"expire"`
	ReflectedUsageBatchID string `json:"reflected_usage_batch_id,omitempty"`
}

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
	if len(args) != 2 {
		return fmt.Errorf(
			"usage: antimage-node session-event <config> <start|seen|stop>",
		)
	}

	configPath := strings.TrimSpace(args[0])
	eventName := strings.ToLower(strings.TrimSpace(args[1]))

	switch eventName {
	case "start", "seen", "stop":
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

	commonName := firstNonEmptyEnv("common_name", "PEERNAME")
	if commonName == "" {
		return fmt.Errorf("%s session username is missing", protocol)
	}

	userID, ok := cfg.Users[commonName]
	if !ok || userID <= 0 {
		return fmt.Errorf(
			"%s user %q is not present in runtime state",
			protocol,
			commonName,
		)
	}

	assignedIP := firstNonEmptyEnv(
		"ifconfig_pool_remote_ip",
		"IPREMOTE",
		"PPP_REMOTE",
	)

	clientIP := firstNonEmptyEnv("trusted_ip", "trusted_ip6", "CALLING_NUMBER")

	trustedPort := firstNonEmptyEnv("trusted_port")

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

	statePath := filepath.Join(
		stateDir,
		stateKey+".session",
	)

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

	node := &Server{}

	err = node.sendNativeSessionEvent(
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
			_ = os.Remove(statePath)
		}
		return err
	}

	if eventName == "stop" {
		_ = os.Remove(statePath)
	}

	return nil
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

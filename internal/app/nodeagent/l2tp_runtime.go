package nodeagent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultL2TPPoolCIDR = "10.67.0.0/16"
	defaultL2TPPort     = 1701
	defaultL2TPTunnel   = 1702
)

type l2TPRuntimeInbound struct {
	Tag        string            `json:"tag"`
	TunnelTag  string            `json:"tunnel_tag"`
	Port       int               `json:"port"`
	TunnelPort int               `json:"tunnel_port"`
	Settings   map[string]any    `json:"settings"`
	Users      []l2TPRuntimeUser `json:"users"`
}

type l2TPRuntimeUser struct {
	UserID      int64  `json:"user_id"`
	Username    string `json:"username"`
	VPNUsername string `json:"vpn_username"`
	Password    string `json:"password"`
	IPv4Address string `json:"ipv4_address"`
	Status      string `json:"status"`
	UsedTraffic int64  `json:"used_traffic"`
	DataLimit   *int64 `json:"data_limit,omitempty"`
	Expire      *int64 `json:"expire,omitempty"`
	DeviceLimit int64  `json:"device_limit,omitempty"`
}

type l2TPRuntimeFiles struct {
	IPSecConfig   string
	IPSecSecrets  string
	XL2TPConfig   string
	PPPOptions    string
	CHAPSecrets   string
	IPUpScript    string
	IPDownScript  string
	SessionConfig string
}

func (s *Server) prepareL2TPInbound(inbound l2TPRuntimeInbound, callback nativeRuntimeSessionCallback) (l2TPRuntimeFiles, error) {
	tag := strings.TrimSpace(inbound.Tag)
	if tag == "" {
		return l2TPRuntimeFiles{}, fmt.Errorf("l2tp inbound tag is required")
	}
	if inbound.Port != 0 && inbound.Port != defaultL2TPPort {
		return l2TPRuntimeFiles{}, fmt.Errorf("l2tp %q: port must be %d", tag, defaultL2TPPort)
	}
	if strings.TrimSpace(l2TPStringSetting(inbound.Settings, "ipsec_psk", "")) == "" {
		return l2TPRuntimeFiles{}, fmt.Errorf("l2tp %q: ipsec_psk is required", tag)
	}

	prefix, err := l2TPPoolPrefix(inbound)
	if err != nil {
		return l2TPRuntimeFiles{}, err
	}
	localIP := prefix.Addr().Next().String()
	remoteRange, err := l2TPRemoteIPRange(prefix)
	if err != nil {
		return l2TPRuntimeFiles{}, fmt.Errorf("l2tp %q: %w", tag, err)
	}

	root := filepath.Join(s.cfg.DataDir, "l2tp", l2TPRuntimeDirName(tag))
	if err := os.MkdirAll(root, 0700); err != nil {
		return l2TPRuntimeFiles{}, err
	}

	files := l2TPRuntimeFiles{
		IPSecConfig:   filepath.Join(root, "ipsec.conf"),
		IPSecSecrets:  filepath.Join(root, "ipsec.secrets"),
		XL2TPConfig:   filepath.Join(root, "xl2tpd.conf"),
		PPPOptions:    filepath.Join(root, "ppp-options"),
		CHAPSecrets:   filepath.Join(root, "chap-secrets"),
		IPUpScript:    filepath.Join(root, "ip-up.sh"),
		IPDownScript:  filepath.Join(root, "ip-down.sh"),
		SessionConfig: filepath.Join(root, "session-helper.json"),
	}

	if err := os.WriteFile(files.IPSecConfig, []byte(renderL2TPIPSecConfig()), 0600); err != nil {
		return l2TPRuntimeFiles{}, err
	}
	if err := os.WriteFile(files.IPSecSecrets, []byte(renderL2TPIPSecSecrets(l2TPStringSetting(inbound.Settings, "ipsec_psk", ""))), 0600); err != nil {
		return l2TPRuntimeFiles{}, err
	}
	if err := os.WriteFile(files.CHAPSecrets, []byte(renderL2TPCHAPSecrets(inbound.Users)), 0600); err != nil {
		return l2TPRuntimeFiles{}, err
	}

	if strings.TrimSpace(callback.URL) == "" {
		files.IPUpScript = ""
		files.IPDownScript = ""
		files.SessionConfig = ""
	}

	if err := os.WriteFile(files.PPPOptions, []byte(renderL2TPPPPOptions(inbound, files, localIP)), 0600); err != nil {
		return l2TPRuntimeFiles{}, err
	}
	if err := os.WriteFile(files.XL2TPConfig, []byte(renderL2TPXL2TPDConfig(files, localIP, remoteRange)), 0600); err != nil {
		return l2TPRuntimeFiles{}, err
	}

	if strings.TrimSpace(callback.URL) != "" {
		executable, err := os.Executable()
		if err != nil {
			return l2TPRuntimeFiles{}, fmt.Errorf("l2tp %q: resolve node executable: %w", tag, err)
		}
		executable, err = filepath.Abs(executable)
		if err != nil {
			return l2TPRuntimeFiles{}, fmt.Errorf("l2tp %q: resolve absolute node executable: %w", tag, err)
		}
		sessionUsers := map[string]int64{}
		for _, user := range inbound.Users {
			username := l2TPRuntimeUsername(user)
			if username == "" {
				return l2TPRuntimeFiles{}, fmt.Errorf("l2tp %q: invalid credentials for user %d", tag, user.UserID)
			}
			if user.UserID <= 0 {
				return l2TPRuntimeFiles{}, fmt.Errorf("l2tp %q: invalid user id %d for session callback", tag, user.UserID)
			}
			sessionUsers[username] = user.UserID
		}
		config := nativeSessionHelperConfig{
			Callback:   callback,
			InboundTag: tag,
			Protocol:   "l2tp",
			Users:      sessionUsers,
			Policies:   buildNativeSessionUserPolicies(l2TPUsersAsOpenVPNUsers(inbound.Users)),
			StateDir:   filepath.Join(root, "sessions"),
		}
		rawConfig, err := json.Marshal(config)
		if err != nil {
			return l2TPRuntimeFiles{}, fmt.Errorf("l2tp %q: marshal session helper config: %w", tag, err)
		}
		if err := os.MkdirAll(config.StateDir, 0700); err != nil {
			return l2TPRuntimeFiles{}, fmt.Errorf("l2tp %q: create session state directory: %w", tag, err)
		}
		if err := os.WriteFile(files.SessionConfig, rawConfig, 0600); err != nil {
			return l2TPRuntimeFiles{}, fmt.Errorf("l2tp %q: write session helper config: %w", tag, err)
		}
		connect := "#!/bin/sh\nset -eu\nexec " + shellSingleQuote(executable) + " session-event " + shellSingleQuote(files.SessionConfig) + " start\n"
		disconnect := "#!/bin/sh\nset -eu\nexec " + shellSingleQuote(executable) + " session-event " + shellSingleQuote(files.SessionConfig) + " stop\n"
		if err := os.WriteFile(files.IPUpScript, []byte(connect), 0700); err != nil {
			return l2TPRuntimeFiles{}, err
		}
		if err := os.WriteFile(files.IPDownScript, []byte(disconnect), 0700); err != nil {
			return l2TPRuntimeFiles{}, err
		}
	}

	return files, nil
}

func renderL2TPIPSecConfig() string {
	return strings.TrimLeft(`
conn antimage-l2tp
    auto=add
    keyexchange=ikev1
    authby=secret
    type=transport
    left=%any
    leftprotoport=17/1701
    right=%any
    rightprotoport=17/%any
    rekey=no
    forceencaps=yes
    fragmentation=yes
    dpddelay=30
    dpdtimeout=120
    dpdaction=clear
`, "\n")
}
func renderL2TPIPSecSecrets(psk string) string {
	return `%any %any : PSK ` + l2TPConfigQuote(strings.TrimSpace(psk)) + "\n"
}

func renderL2TPXL2TPDConfig(files l2TPRuntimeFiles, localIP, remoteRange string) string {
	return fmt.Sprintf(`[global]
listen-addr = 0.0.0.0

[lns default]
ip range = %s
local ip = %s
require chap = yes
refuse pap = yes
require authentication = yes
name = antimage-l2tp
ppp debug = no
pppoptfile = %s
length bit = yes
`, remoteRange, localIP, filepath.ToSlash(files.PPPOptions))
}

func renderL2TPPPPOptions(inbound l2TPRuntimeInbound, files l2TPRuntimeFiles, localIP string) string {
	var b strings.Builder
	line := func(value string) {
		b.WriteString(value)
		b.WriteByte('\n')
	}
	line("auth")
	line("name antimage-l2tp")
	line("chap-secrets " + filepath.ToSlash(files.CHAPSecrets))
	line("ms-dns " + strings.Join(l2TPStringListSetting(inbound.Settings, "dns_servers", []string{"1.1.1.1", "8.8.8.8"}), "\nms-dns "))
	line("mtu " + strconv.Itoa(l2TPIntSetting(inbound.Settings, "mtu", 1410)))
	line("mru " + strconv.Itoa(l2TPIntSetting(inbound.Settings, "mru", 1410)))
	line("lcp-echo-interval " + strconv.Itoa(l2TPIntSetting(inbound.Settings, "lcp_echo_interval", 30)))
	line("lcp-echo-failure " + strconv.Itoa(l2TPIntSetting(inbound.Settings, "lcp_echo_failure", 4)))
	line("proxyarp")
	line("nodefaultroute")
	line("lock")
	line("hide-password")
	line("connect-delay 5000")
	line("ipcp-accept-local")
	line("ipcp-accept-remote")
	line(localIP + ":")
	if files.IPUpScript != "" {
		line("ip-up-script " + filepath.ToSlash(files.IPUpScript))
	}
	if files.IPDownScript != "" {
		line("ip-down-script " + filepath.ToSlash(files.IPDownScript))
	}
	return b.String()
}

func renderL2TPCHAPSecrets(users []l2TPRuntimeUser) string {
	var b strings.Builder
	for _, user := range users {
		if !l2TPUserAllowed(user) {
			continue
		}
		username := l2TPRuntimeUsername(user)
		if username == "" || user.Password == "" {
			continue
		}
		address := strings.TrimSpace(user.IPv4Address)
		if address == "" {
			address = "*"
		}
		fmt.Fprintf(&b, "%s\t*\t%s\t%s\n", l2TPConfigQuote(username), l2TPConfigQuote(user.Password), address)
	}
	return b.String()
}

func l2TPConfigQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func l2TPPoolPrefix(inbound l2TPRuntimeInbound) (netip.Prefix, error) {
	raw := strings.TrimSpace(l2TPStringSetting(inbound.Settings, "ipv4_pool_cidr", defaultL2TPPoolCIDR))
	prefix, err := netip.ParsePrefix(raw)
	if err != nil || !prefix.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("l2tp %q: invalid IPv4 pool CIDR %q", inbound.Tag, raw)
	}
	return prefix.Masked(), nil
}

func l2TPRemoteIPRange(prefix netip.Prefix) (string, error) {
	start := prefix.Addr().Next().Next()
	bits := 32 - prefix.Bits()
	if bits <= 1 || bits > 16 {
		return "", fmt.Errorf("IPv4 pool CIDR must leave between 2 and 65534 client addresses")
	}
	total := 1 << bits
	end := prefix.Addr()
	for i := 0; i < total-2; i++ {
		end = end.Next()
	}
	return start.String() + "-" + end.String(), nil
}

func l2TPRuntimeDirName(tag string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tag)))
	return fmt.Sprintf("%x", sum[:8])
}

func l2TPRuntimeUsername(user l2TPRuntimeUser) string {
	username := strings.TrimSpace(user.VPNUsername)
	if username == "" {
		username = strings.TrimSpace(user.Username)
	}
	return username
}

func l2TPUserAllowed(user l2TPRuntimeUser) bool {
	status := strings.ToLower(strings.TrimSpace(user.Status))
	return status == "" || status == "active" || status == "on_hold"
}

func l2TPUsersAsOpenVPNUsers(users []l2TPRuntimeUser) []openVPNRuntimeUser {
	result := make([]openVPNRuntimeUser, 0, len(users))
	for _, user := range users {
		result = append(result, openVPNRuntimeUser{
			UserID:      user.UserID,
			Username:    user.Username,
			VPNUsername: user.VPNUsername,
			Password:    user.Password,
			IPv4Address: user.IPv4Address,
			Status:      user.Status,
			UsedTraffic: user.UsedTraffic,
			DataLimit:   user.DataLimit,
			Expire:      user.Expire,
			DeviceLimit: user.DeviceLimit,
		})
	}
	return result
}

func l2TPStringSetting(settings map[string]any, key, fallback string) string {
	return openVPNStringSetting(settings, key, fallback)
}

func l2TPIntSetting(settings map[string]any, key string, fallback int) int {
	if value := openVPNIntSetting(settings, key); value > 0 {
		return value
	}
	return fallback
}

func l2TPStringListSetting(settings map[string]any, key string, fallback []string) []string {
	values := openVPNStringListSetting(settings, key)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}

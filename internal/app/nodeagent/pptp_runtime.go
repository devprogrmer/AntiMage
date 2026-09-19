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
	defaultPPTPPoolCIDR = "10.68.0.0/24"
	defaultPPTPPort     = 1723
	defaultPPTPTunnel   = 41942
)

type pptpRuntimeInbound struct {
	Tag        string            `json:"tag"`
	TunnelTag  string            `json:"tunnel_tag"`
	Port       int               `json:"port"`
	TunnelPort int               `json:"tunnel_port"`
	Settings   map[string]any    `json:"settings"`
	Users      []pptpRuntimeUser `json:"users"`
}

type pptpRuntimeUser struct {
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

type pptpRuntimeFiles struct {
	Config        string
	PPPOptions    string
	CHAPSecrets   string
	IPUpScript    string
	IPDownScript  string
	SessionConfig string
	UsageConfig   string
}

func (s *Server) preparePPTPInbound(inbound pptpRuntimeInbound, callback nativeRuntimeSessionCallback) (string, error) {
	tag := strings.TrimSpace(inbound.Tag)
	if tag == "" {
		return "", fmt.Errorf("pptp inbound tag is required")
	}
	if inbound.Port != 0 && inbound.Port != defaultPPTPPort {
		return "", fmt.Errorf("pptp %q: port must be %d", tag, defaultPPTPPort)
	}
	prefix, err := pptpPoolPrefix(inbound)
	if err != nil {
		return "", err
	}
	localIP := prefix.Addr().Next().String()
	remoteRange, err := pptpRemoteIPRange(prefix)
	if err != nil {
		return "", fmt.Errorf("pptp %q: %w", tag, err)
	}

	root := filepath.Join(s.cfg.DataDir, "pptp", pptpRuntimeDirName(tag))
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	files := pptpRuntimeFiles{
		Config:        filepath.Join(root, "pptpd.conf"),
		PPPOptions:    filepath.Join(root, "ppp-options"),
		CHAPSecrets:   filepath.Join(root, "chap-secrets"),
		IPUpScript:    filepath.Join(root, "ip-up.sh"),
		IPDownScript:  filepath.Join(root, "ip-down.sh"),
		SessionConfig: filepath.Join(root, "session-helper.json"),
		UsageConfig:   filepath.Join(root, "usage-helper.json"),
	}

	if err := os.WriteFile(files.CHAPSecrets, []byte(renderPPTPCHAPSecrets(inbound.Users)), 0600); err != nil {
		return "", err
	}

	usageUsers := make(map[string]int64)
	for _, user := range inbound.Users {
		if !pptpUserAllowed(user) || user.UserID <= 0 {
			continue
		}
		rawIP := strings.TrimSpace(user.IPv4Address)
		if rawIP == "" {
			continue
		}
		addr, err := netip.ParseAddr(rawIP)
		if err != nil || !addr.Is4() {
			continue
		}
		usageUsers[addr.String()] = user.UserID
	}
	usageConfig := pptpUsageRuntimeConfig{
		InboundTag: tag,
		Users:      usageUsers,
	}
	rawUsageConfig, err := json.Marshal(usageConfig)
	if err != nil {
		return "", fmt.Errorf(
			"pptp %q: marshal usage config: %w",
			tag,
			err,
		)
	}
	if err := os.WriteFile(files.UsageConfig, rawUsageConfig, 0600); err != nil {
		return "", fmt.Errorf(
			"pptp %q: write usage config: %w",
			tag,
			err,
		)
	}

	// Accounting/online detection no longer depends on PPP callbacks.
	// Do not render callback scripts into PPP options when callback is absent.
	if strings.TrimSpace(callback.URL) == "" {
		files.IPUpScript = ""
		files.IPDownScript = ""
		files.SessionConfig = ""
	}

	if err := os.WriteFile(files.PPPOptions, []byte(renderPPTPPPPOptions(inbound, files, localIP)), 0600); err != nil {
		return "", err
	}
	if err := os.WriteFile(files.Config, []byte(renderPPTPDConfig(files, localIP, remoteRange)), 0600); err != nil {
		return "", err
	}

	if strings.TrimSpace(callback.URL) != "" {
		executable, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("pptp %q: resolve node executable: %w", tag, err)
		}
		executable, err = filepath.Abs(executable)
		if err != nil {
			return "", fmt.Errorf("pptp %q: resolve absolute node executable: %w", tag, err)
		}
		sessionUsers := map[string]int64{}
		for _, user := range inbound.Users {
			username := pptpRuntimeUsername(user)
			if username == "" {
				return "", fmt.Errorf("pptp %q: invalid credentials for user %d", tag, user.UserID)
			}
			if user.UserID <= 0 {
				return "", fmt.Errorf("pptp %q: invalid user id %d for session callback", tag, user.UserID)
			}
			sessionUsers[username] = user.UserID
		}
		config := nativeSessionHelperConfig{
			Callback:   callback,
			InboundTag: tag,
			Protocol:   "pptp",
			Users:      sessionUsers,
			Policies:   buildNativeSessionUserPolicies(pptpUsersAsOpenVPNUsers(inbound.Users)),
			StateDir:   filepath.Join(root, "sessions"),
		}
		rawConfig, err := json.Marshal(config)
		if err != nil {
			return "", fmt.Errorf("pptp %q: marshal session helper config: %w", tag, err)
		}
		if err := os.MkdirAll(config.StateDir, 0700); err != nil {
			return "", fmt.Errorf("pptp %q: create session state directory: %w", tag, err)
		}
		if err := os.WriteFile(files.SessionConfig, rawConfig, 0600); err != nil {
			return "", fmt.Errorf("pptp %q: write session helper config: %w", tag, err)
		}
		connect := "#!/bin/sh\nset -eu\nexec " + shellSingleQuote(executable) + " session-event " + shellSingleQuote(files.SessionConfig) + " start\n"
		disconnect := "#!/bin/sh\nset -eu\nexec " + shellSingleQuote(executable) + " session-event " + shellSingleQuote(files.SessionConfig) + " stop\n"
		if err := os.WriteFile(files.IPUpScript, []byte(connect), 0700); err != nil {
			return "", err
		}
		if err := os.WriteFile(files.IPDownScript, []byte(disconnect), 0700); err != nil {
			return "", err
		}
	}

	return files.Config, nil
}

func renderPPTPDConfig(files pptpRuntimeFiles, localIP, remoteRange string) string {
	return fmt.Sprintf(`option %s
localip %s
remoteip %s
`, filepath.ToSlash(files.PPPOptions), localIP, remoteRange)
}

func pptpRemoteIPRange(prefix netip.Prefix) (string, error) {
	prefix = prefix.Masked()
	if !prefix.Addr().Is4() {
		return "", fmt.Errorf("PPTP pool must be IPv4")
	}
	if prefix.Bits() < 24 {
		return "", fmt.Errorf("PPTP pool must be /24 or narrower")
	}

	base := prefix.Addr().As4()

	start := prefix.Addr().Next().Next()
	if !prefix.Contains(start) {
		return "", fmt.Errorf("PPTP pool has no usable remote addresses")
	}

	last := netip.AddrFrom4([4]byte{
		base[0],
		base[1],
		base[2],
		254,
	})

	if !prefix.Contains(last) || start.Compare(last) > 0 {
		return "", fmt.Errorf("PPTP pool has no usable remote range")
	}

	start4 := start.As4()
	last4 := last.As4()

	return fmt.Sprintf(
		"%d.%d.%d.%d-%d",
		start4[0],
		start4[1],
		start4[2],
		start4[3],
		last4[3],
	), nil
}

func renderPPTPPPPOptions(inbound pptpRuntimeInbound, files pptpRuntimeFiles, localIP string) string {
	var b strings.Builder
	line := func(value string) {
		b.WriteString(value)
		b.WriteByte('\n')
	}
	line("name antimage-pptp")
	line("auth")
	line("refuse-pap")
	line("refuse-chap")
	line("refuse-mschap")
	line("require-mschap-v2")
	line("require-mppe-128")
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
	line(localIP + ":")
	if files.IPUpScript != "" {
		line("ip-up-script " + filepath.ToSlash(files.IPUpScript))
	}
	if files.IPDownScript != "" {
		line("ip-down-script " + filepath.ToSlash(files.IPDownScript))
	}
	return b.String()
}

func renderPPTPCHAPSecrets(users []pptpRuntimeUser) string {
	var b strings.Builder
	for _, user := range users {
		if !pptpUserAllowed(user) {
			continue
		}
		username := pptpRuntimeUsername(user)
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

func pptpPoolPrefix(inbound pptpRuntimeInbound) (netip.Prefix, error) {
	raw := strings.TrimSpace(openVPNStringSetting(inbound.Settings, "ipv4_pool_cidr", defaultPPTPPoolCIDR))
	prefix, err := netip.ParsePrefix(raw)
	if err != nil || !prefix.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("pptp %q: invalid IPv4 pool CIDR %q", inbound.Tag, raw)
	}
	prefix = prefix.Masked()
	if prefix.Bits() < 24 {
		return netip.Prefix{}, fmt.Errorf("pptp %q: IPv4 pool must be /24 or narrower", inbound.Tag)
	}
	return prefix, nil
}

func pptpRuntimeDirName(tag string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tag)))
	return fmt.Sprintf("%x", sum[:8])
}

func pptpRuntimeUsername(user pptpRuntimeUser) string {
	username := strings.TrimSpace(user.VPNUsername)
	if username == "" {
		username = strings.TrimSpace(user.Username)
	}
	return username
}

func pptpUserAllowed(user pptpRuntimeUser) bool {
	status := strings.ToLower(strings.TrimSpace(user.Status))
	return status == "" || status == "active" || status == "on_hold"
}

func pptpUsersAsOpenVPNUsers(users []pptpRuntimeUser) []openVPNRuntimeUser {
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

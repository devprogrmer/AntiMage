package nodeagent

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

type anyConnectRuntimeInbound struct {
	Tag        string                  `json:"tag"`
	TunnelTag  string                  `json:"tunnel_tag"`
	Port       int                     `json:"port"`
	TunnelPort int                     `json:"tunnel_port"`
	Settings   map[string]any          `json:"settings"`
	Users      []anyConnectRuntimeUser `json:"users"`
}

type anyConnectRuntimeUser struct {
	UserID             int64  `json:"user_id"`
	Username           string `json:"username"`
	Password           string `json:"password"`
	IPv4Address        string `json:"ipv4_address"`
	Status             string `json:"status"`
	UsedTraffic        int64  `json:"used_traffic"`
	DataLimit          *int64 `json:"data_limit,omitempty"`
	Expire             *int64 `json:"expire,omitempty"`
	DeviceLimit        int64  `json:"device_limit,omitempty"`
	UploadSpeedLimit   int64  `json:"upload_speed_limit"`
	DownloadSpeedLimit int64  `json:"download_speed_limit"`
}

type anyConnectRuntimeFiles struct {
	ConfigPath       string
	PasswordFile     string
	ServerCert       string
	ServerKey        string
	CACert           string
	UserConfigDir    string
	ControlSocket    string
	PIDFile          string
	ConnectScript    string
	DisconnectScript string
	UsageConfig      string
}

type preparedAnyConnectRuntime struct {
	Tag     string
	Files   anyConnectRuntimeFiles
	Inbound anyConnectRuntimeInbound
	TProxy  openVPNTProxySpec
	NAT     openVPNNATSpec
}

func renderAnyConnectConfig(inbound anyConnectRuntimeInbound, files anyConnectRuntimeFiles) (string, error) {
	poolText := openVPNStringSetting(inbound.Settings, "ipv4_pool_cidr", "10.71.0.0/24")
	pool, err := netip.ParsePrefix(poolText)
	if err != nil || !pool.Addr().Is4() {
		return "", fmt.Errorf("anyconnect %q: invalid IPv4 pool %q", inbound.Tag, poolText)
	}
	pool = pool.Masked()
	mask := net.CIDRMask(pool.Bits(), 32)
	if len(mask) != net.IPv4len {
		return "", fmt.Errorf("anyconnect %q: invalid IPv4 mask", inbound.Tag)
	}
	port := inbound.Port
	if port <= 0 {
		port = 443
	}
	maxClients := openVPNIntSetting(inbound.Settings, "max_clients")
	if maxClients <= 0 {
		maxClients = 1024
	}

	lines := []string{
		`auth = "plain[passwd=` + files.PasswordFile + `]"`,
		"tcp-port = " + strconv.Itoa(port),
		"server-cert = " + files.ServerCert,
		"server-key = " + files.ServerKey,
		"device = " + anyConnectDeviceName(inbound.Tag),
		"socket-file = " + files.ControlSocket,
		"pid-file = " + files.PIDFile,
		"use-occtl = true",
		"ipv4-network = " + pool.Addr().String(),
		"ipv4-netmask = " + net.IP(mask).String(),
		"predictable-ips = true",
		"config-per-user = " + files.UserConfigDir,
		"max-clients = " + strconv.Itoa(maxClients),
		"max-same-clients = " + strconv.Itoa(max(1, openVPNIntSetting(inbound.Settings, "max_same_clients"))),
		"isolate-workers = true",
		"keepalive = " + strconv.Itoa(max(1, openVPNIntSetting(inbound.Settings, "keepalive"))),
		"dpd = " + strconv.Itoa(max(1, openVPNIntSetting(inbound.Settings, "dpd"))),
		"mobile-dpd = " + strconv.Itoa(max(1, openVPNIntSetting(inbound.Settings, "mobile_dpd"))),
		"mtu = " + strconv.Itoa(max(576, openVPNIntSetting(inbound.Settings, "mtu"))),
	}
	if files.ConnectScript != "" {
		lines = append(lines, "connect-script = "+files.ConnectScript)
	}
	if files.DisconnectScript != "" {
		lines = append(lines, "disconnect-script = "+files.DisconnectScript)
	}
	optionalInt := map[string]string{
		"cookie_timeout": "cookie-timeout", "idle_timeout": "idle-timeout",
		"mobile_idle_timeout": "mobile-idle-timeout", "session_timeout": "session-timeout",
		"auth_timeout": "auth-timeout", "min_reauth_time": "min-reauth-time",
		"max_ban_score": "max-ban-score", "ban_reset_time": "ban-reset-time",
		"rekey_time": "rekey-time", "switch_to_tcp_timeout": "switch-to-tcp-timeout",
		"stats_report_time": "stats-report-time", "rate_limit_ms": "rate-limit-ms",
	}
	for key, directive := range optionalInt {
		if value := openVPNIntSetting(inbound.Settings, key); value > 0 {
			lines = append(lines, directive+" = "+strconv.Itoa(value))
		}
	}
	if host := openVPNStringSetting(inbound.Settings, "listen_host", ""); host != "" {
		lines = append(lines, "listen-host = "+host)
	}
	if host := openVPNStringSetting(inbound.Settings, "udp_listen_host", ""); host != "" {
		lines = append(lines, "udp-listen-host = "+host)
	}
	if banner := openVPNStringSetting(inbound.Settings, "banner", ""); banner != "" {
		lines = append(lines, "banner = "+banner)
	}
	if domain := openVPNStringSetting(inbound.Settings, "default_domain", ""); domain != "" {
		lines = append(lines, "default-domain = "+domain)
	}
	if openVPNBoolSetting(inbound.Settings, "udp_enabled", true) {
		udpPort := openVPNIntSetting(inbound.Settings, "udp_port")
		if udpPort <= 0 {
			udpPort = port
		}
		lines = append(lines, "udp-port = "+strconv.Itoa(udpPort))
	} else {
		lines = append(lines, "udp-port = 0")
	}
	for _, dns := range openVPNStringListSetting(inbound.Settings, "dns_servers") {
		lines = append(lines, "dns = "+dns)
	}
	for _, route := range openVPNStringListSetting(inbound.Settings, "routes") {
		lines = append(lines, "route = "+route)
	}
	for _, route := range openVPNStringListSetting(inbound.Settings, "no_routes") {
		lines = append(lines, "no-route = "+route)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func anyConnectDeviceName(tag string) string {
	return "ac" + openVPNRuntimeDirName(tag)[:10]
}

func anyConnectAsOpenVPNInbound(inbound anyConnectRuntimeInbound) openVPNRuntimeInbound {
	settings := make(map[string]any, len(inbound.Settings)+1)
	for key, value := range inbound.Settings {
		settings[key] = value
	}
	if _, exists := settings["tproxy_enabled"]; !exists {
		settings["tproxy_enabled"] = false
	}
	return openVPNRuntimeInbound{Tag: "anyconnect:" + strings.TrimSpace(inbound.Tag), TunnelTag: inbound.TunnelTag, Port: inbound.Port, Transport: "tcp", TunnelPort: inbound.TunnelPort, Settings: settings}
}

func renderAnyConnectUserConfig(user anyConnectRuntimeUser, settings map[string]any) string {
	lines := make([]string, 0, 4)
	if address := strings.TrimSpace(user.IPv4Address); address != "" {
		lines = append(lines, "explicit-ipv4 = "+address)
	}
	limit := user.DeviceLimit
	if limit <= 0 {
		limit = int64(openVPNIntSetting(settings, "max_same_clients"))
	}
	if limit > 0 {
		lines = append(lines, "max-same-clients = "+strconv.FormatInt(limit, 10))
	}
	if user.DownloadSpeedLimit > 0 {
		lines = append(lines, "rx-data-per-sec = "+strconv.FormatInt(user.DownloadSpeedLimit, 10))
	}
	if user.UploadSpeedLimit > 0 {
		lines = append(lines, "tx-data-per-sec = "+strconv.FormatInt(user.UploadSpeedLimit, 10))
	}
	return strings.Join(lines, "\n") + "\n"
}

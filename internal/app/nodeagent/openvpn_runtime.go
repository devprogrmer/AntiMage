package nodeagent

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
)

type openVPNRuntimeFiles struct {
	CAFile                 string
	CertFile               string
	KeyFile                string
	DHFile                 string
	TLSCryptFile           string
	TLSAuthFile            string
	AuthScript             string
	ClientConnectScript    string
	ClientDisconnectScript string
	ClientConfigDir        string
	IPPoolPersist          string
	StatusFile             string
	ManagementSocket       string
}

func renderOpenVPNServerConfig(inbound openVPNRuntimeInbound, files openVPNRuntimeFiles) (string, error) {
	if inbound.Port <= 0 || inbound.Port > 65535 {
		return "", fmt.Errorf("openvpn %q: invalid port %d", inbound.Tag, inbound.Port)
	}
	if strings.TrimSpace(inbound.Tag) == "" {
		return "", fmt.Errorf("openvpn inbound tag is required")
	}

	network, mask, err := openVPNPoolNetwork(openVPNStringSetting(inbound.Settings, "ipv4_pool_cidr", "10.66.0.0/16"))
	if err != nil {
		return "", fmt.Errorf("openvpn %q: %w", inbound.Tag, err)
	}

	for name, value := range map[string]string{
		"ca":          files.CAFile,
		"certificate": files.CertFile,
		"key":         files.KeyFile,
		"auth script": files.AuthScript,
		"ccd":         files.ClientConfigDir,
	} {
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("openvpn %q: %s path is required", inbound.Tag, name)
		}
	}

	proto := "udp"
	if strings.EqualFold(strings.TrimSpace(inbound.Transport), "tcp") {
		proto = "tcp-server"
	}

	var b strings.Builder
	line := func(value string) {
		b.WriteString(value)
		b.WriteByte('\n')
	}

	line("port " + strconv.Itoa(inbound.Port))
	line("proto " + proto)
	line("dev " + openVPNTunName(inbound.Tag))
	line("dev-type tun")
	line("topology subnet")
	line("server " + network + " " + mask)
	line("persist-key")
	line("persist-tun")
	line("keepalive 10 60")
	line("tls-version-min 1.2")
	line("verb 3")
	line("script-security 2")

	line("ca " + quoteOpenVPNPath(files.CAFile))
	line("cert " + quoteOpenVPNPath(files.CertFile))
	line("key " + quoteOpenVPNPath(files.KeyFile))

	if files.DHFile != "" {
		line("dh " + quoteOpenVPNPath(files.DHFile))
	} else {
		line("dh none")
	}

	line("verify-client-cert none")
	line("username-as-common-name")
	line("auth-user-pass-verify " + quoteOpenVPNPath(files.AuthScript) + " via-file")

	if files.ClientConnectScript != "" {
		line("client-connect " + quoteOpenVPNPath(files.ClientConnectScript))
	}
	if files.ClientDisconnectScript != "" {
		line("client-disconnect " + quoteOpenVPNPath(files.ClientDisconnectScript))
	}
	line("client-config-dir " + quoteOpenVPNPath(files.ClientConfigDir))

	if files.IPPoolPersist != "" {
		line("ifconfig-pool-persist " + quoteOpenVPNPath(files.IPPoolPersist))
	}
	if files.StatusFile != "" &&
		(openVPNBoolSetting(inbound.Settings, "accounting_enabled", true) ||
			files.ClientConnectScript != "") {
		line("status " + quoteOpenVPNPath(files.StatusFile) + " 5")
		line("status-version 3")
	}

	if cipher := openVPNStringSetting(inbound.Settings, "cipher", ""); cipher != "" {
		line("cipher " + cipher)
	}
	if dataCiphers := openVPNStringSetting(inbound.Settings, "data_ciphers", ""); dataCiphers != "" {
		line("data-ciphers " + dataCiphers)
	}
	if auth := openVPNStringSetting(inbound.Settings, "auth", ""); auth != "" {
		line("auth " + auth)
	}

	if files.TLSCryptFile != "" {
		line("tls-crypt " + quoteOpenVPNPath(files.TLSCryptFile))
	}
	if files.TLSAuthFile != "" {
		line("tls-auth " + quoteOpenVPNPath(files.TLSAuthFile) + " 0")
	}

	if openVPNBoolSetting(inbound.Settings, "redirect_gateway", true) {
		line(`push "redirect-gateway def1"`)
	}

	for _, dns := range openVPNStringListSetting(inbound.Settings, "dns_servers") {
		line(`push "dhcp-option DNS ` + dns + `"`)
	}

	if managementPort := openVPNIntSetting(inbound.Settings, "management_port"); managementPort > 0 && managementPort <= 65535 {
		line("management 127.0.0.1 " + strconv.Itoa(managementPort))
	} else if files.ManagementSocket != "" {
		line("management " + quoteOpenVPNPath(files.ManagementSocket) + " unix")
	}

	return b.String(), nil
}

func openVPNTunName(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return fmt.Sprintf("amov%x", sum[:4])
}

func openVPNPoolNetwork(raw string) (string, string, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
	if err != nil || !prefix.Addr().Is4() {
		return "", "", fmt.Errorf("invalid IPv4 pool CIDR %q", raw)
	}
	prefix = prefix.Masked()
	mask := net.CIDRMask(prefix.Bits(), 32)
	return prefix.Addr().String(), net.IP(mask).String(), nil
}

func openVPNStringSetting(settings map[string]any, key, fallback string) string {
	if settings == nil {
		return fallback
	}
	value, ok := settings[key]
	if !ok || value == nil {
		return fallback
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" {
		return fallback
	}
	return text
}

func openVPNBoolSetting(settings map[string]any, key string, fallback bool) bool {
	if settings == nil {
		return fallback
	}
	value, ok := settings[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	}
	return fallback
}

func openVPNIntSetting(settings map[string]any, key string) int {
	if settings == nil {
		return 0
	}
	switch value := settings[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(value))
		return parsed
	default:
		return 0
	}
}

func openVPNStringListSetting(settings map[string]any, key string) []string {
	if settings == nil {
		return nil
	}
	switch value := settings[key].(type) {
	case []string:
		return value
	case []any:
		result := make([]string, 0, len(value))
		for _, item := range value {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				result = append(result, text)
			}
		}
		return result
	case string:
		return strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\r'
		})
	default:
		return nil
	}
}

func quoteOpenVPNPath(path string) string {
	return `"` + filepath.ToSlash(path) + `"`
}

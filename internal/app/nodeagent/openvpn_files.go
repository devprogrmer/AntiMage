package nodeagent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) prepareOpenVPNInbound(inbound openVPNRuntimeInbound, callbacks ...nativeRuntimeSessionCallback) (string, error) {
	if strings.TrimSpace(inbound.Tag) == "" {
		return "", fmt.Errorf("openvpn inbound tag is required")
	}

	var sessionCallback nativeRuntimeSessionCallback
	if len(callbacks) > 0 {
		sessionCallback = callbacks[0]
	}

	ca := openVPNStringSetting(inbound.Settings, "ca", "")
	cert := openVPNStringSetting(inbound.Settings, "server_certificate", "")
	key := openVPNStringSetting(inbound.Settings, "server_key", "")

	if ca == "" {
		return "", fmt.Errorf("openvpn %q: CA certificate is required", inbound.Tag)
	}
	if cert == "" {
		return "", fmt.Errorf("openvpn %q: server certificate is required", inbound.Tag)
	}
	if key == "" {
		return "", fmt.Errorf("openvpn %q: server private key is required", inbound.Tag)
	}

	root := filepath.Join(
		s.cfg.DataDir,
		"openvpn",
		openVPNRuntimeDirName(inbound.Tag),
	)

	ccdDir := filepath.Join(root, "ccd")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}

	if err := os.RemoveAll(ccdDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(ccdDir, 0700); err != nil {
		return "", err
	}

	files := openVPNRuntimeFiles{
		CAFile:           filepath.Join(root, "ca.crt"),
		CertFile:         filepath.Join(root, "server.crt"),
		KeyFile:          filepath.Join(root, "server.key"),
		AuthScript:       filepath.Join(root, "auth.sh"),
		ClientConfigDir:  ccdDir,
		IPPoolPersist:    filepath.Join(root, "ipp.txt"),
		StatusFile:       filepath.Join(root, "status.tsv"),
		ManagementSocket: filepath.Join(root, "management.sock"),
	}

	if err := os.WriteFile(files.CAFile, []byte(ca+"\n"), 0644); err != nil {
		return "", err
	}
	if err := os.WriteFile(files.CertFile, []byte(cert+"\n"), 0644); err != nil {
		return "", err
	}
	if err := os.WriteFile(files.KeyFile, []byte(key+"\n"), 0600); err != nil {
		return "", err
	}

	if dh := openVPNStringSetting(inbound.Settings, "dh", ""); dh != "" {
		files.DHFile = filepath.Join(root, "dh.pem")
		if err := os.WriteFile(files.DHFile, []byte(dh+"\n"), 0600); err != nil {
			return "", err
		}
	}

	if tlsCrypt := openVPNStringSetting(inbound.Settings, "tls_crypt", ""); tlsCrypt != "" {
		files.TLSCryptFile = filepath.Join(root, "tls-crypt.key")
		if err := os.WriteFile(files.TLSCryptFile, []byte(tlsCrypt+"\n"), 0600); err != nil {
			return "", err
		}
	}

	if tlsAuth := openVPNStringSetting(inbound.Settings, "tls_auth", ""); tlsAuth != "" {
		files.TLSAuthFile = filepath.Join(root, "tls-auth.key")
		if err := os.WriteFile(files.TLSAuthFile, []byte(tlsAuth+"\n"), 0600); err != nil {
			return "", err
		}
	}

	credentialsFile := filepath.Join(root, "credentials.sha256")
	var credentialRecords strings.Builder

	_, mask, err := openVPNPoolNetwork(
		openVPNStringSetting(inbound.Settings, "ipv4_pool_cidr", "10.66.0.0/16"),
	)
	if err != nil {
		return "", err
	}

	sessionUsers := map[string]int64{}
	sessionPolicies := buildNativeSessionUserPolicies(inbound.Users)

	for _, user := range inbound.Users {
		username := strings.TrimSpace(user.VPNUsername)
		if username == "" {
			username = strings.TrimSpace(user.Username)
		}
		if username == "" || user.Password == "" {
			return "", fmt.Errorf("openvpn %q: invalid credentials for user %d", inbound.Tag, user.UserID)
		}

		if err := validateOpenVPNUsername(username); err != nil {
			return "", fmt.Errorf("openvpn %q: user %d: %w", inbound.Tag, user.UserID, err)
		}
		if _, exists := sessionUsers[username]; exists {
			return "", fmt.Errorf(
				"openvpn %q: duplicate vpn username %q",
				inbound.Tag,
				username,
			)
		}

		if strings.TrimSpace(sessionCallback.URL) != "" && user.UserID <= 0 {
			return "", fmt.Errorf(
				"openvpn %q: invalid user id %d for session callback",
				inbound.Tag,
				user.UserID,
			)
		}

		sessionUsers[username] = user.UserID

		status := strings.ToLower(strings.TrimSpace(user.Status))

		var dataLimit int64
		if user.DataLimit != nil {
			dataLimit = *user.DataLimit
		}

		var expire int64
		if user.Expire != nil {
			expire = *user.Expire
		}

		hash := sha256.Sum256([]byte(username + "\x00" + user.Password))

		fmt.Fprintf(
			&credentialRecords,
			"%x\t%s\t%d\t%d\t%d\n",
			hash[:],
			status,
			user.UsedTraffic,
			dataLimit,
			expire,
		)

		if strings.TrimSpace(user.IPv4Address) != "" {
			ccd := "ifconfig-push " +
				strings.TrimSpace(user.IPv4Address) +
				" " + mask + "\n"

			if err := os.WriteFile(
				filepath.Join(ccdDir, username),
				[]byte(ccd),
				0600,
			); err != nil {
				return "", err
			}
		}
	}

	if err := os.WriteFile(
		credentialsFile,
		[]byte(credentialRecords.String()),
		0600,
	); err != nil {
		return "", err
	}

	authScript := `#!/bin/sh
set -eu

AUTH_FILE="${1:-}"
[ -r "$AUTH_FILE" ] || exit 1

USERNAME="$(sed -n '1p' "$AUTH_FILE" | tr -d '\r')"
PASSWORD="$(sed -n '2p' "$AUTH_FILE" | tr -d '\r')"

HASH="$(printf '%s\0%s' "$USERNAME" "$PASSWORD" | sha256sum | awk '{print $1}')"
NOW="$(date +%s)"

awk -F '\t' -v hash="$HASH" -v now="$NOW" '
$1 == hash {
    found = 1
    status = $2
    used = $3 + 0
    limit = $4 + 0
    expire = $5 + 0

    if (status == "on_hold") {
        allowed = 1
    } else if (status == "active") {
        allowed = 1

        if (limit > 0 && used >= limit) {
            allowed = 0
        }

        if (expire > 0 && expire <= now) {
            allowed = 0
        }
    }

    exit
}

END {
    if (found && allowed) {
        exit 0
    }

    exit 1
}
' ` + shellSingleQuote(credentialsFile) + `
`

	if err := os.WriteFile(files.AuthScript, []byte(authScript), 0700); err != nil {
		return "", err
	}

	usageConfigPath := filepath.Join(root, "usage-helper.json")

	if openVPNBoolSetting(
		inbound.Settings,
		"accounting_enabled",
		true,
	) {
		usageConfig := openVPNUsageRuntimeConfig{
			InboundTag: inbound.Tag,
			StatusFile: files.StatusFile,
			Users:      sessionUsers,
		}

		rawUsageConfig, err := json.Marshal(usageConfig)
		if err != nil {
			return "", fmt.Errorf(
				"openvpn %q: marshal usage config: %w",
				inbound.Tag,
				err,
			)
		}

		if err := os.WriteFile(
			usageConfigPath,
			rawUsageConfig,
			0600,
		); err != nil {
			return "", fmt.Errorf(
				"openvpn %q: write usage config: %w",
				inbound.Tag,
				err,
			)
		}
	} else {
		_ = os.Remove(usageConfigPath)
		_ = os.Remove(files.StatusFile)
	}

	if strings.TrimSpace(sessionCallback.URL) != "" {
		executable, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf(
				"openvpn %q: resolve node executable: %w",
				inbound.Tag,
				err,
			)
		}

		executable, err = filepath.Abs(executable)
		if err != nil {
			return "", fmt.Errorf(
				"openvpn %q: resolve absolute node executable: %w",
				inbound.Tag,
				err,
			)
		}

		sessionConfigPath := filepath.Join(root, "session-helper.json")
		sessionStateDir := filepath.Join(root, "sessions")

		managementNetwork := "unix"
		managementAddress := files.ManagementSocket

		if managementPort := openVPNIntSetting(
			inbound.Settings,
			"management_port",
		); managementPort > 0 && managementPort <= 65535 {
			managementNetwork = "tcp"
			managementAddress = fmt.Sprintf(
				"127.0.0.1:%d",
				managementPort,
			)
		}
		sessionConfig := nativeSessionHelperConfig{
			Callback:          sessionCallback,
			InboundTag:        inbound.Tag,
			Users:             sessionUsers,
			Policies:          sessionPolicies,
			StateDir:          sessionStateDir,
			ManagementNetwork: managementNetwork,
			ManagementAddress: managementAddress,
		}

		rawSessionConfig, err := json.Marshal(sessionConfig)
		if err != nil {
			return "", fmt.Errorf(
				"openvpn %q: marshal session helper config: %w",
				inbound.Tag,
				err,
			)
		}

		if err := os.WriteFile(
			sessionConfigPath,
			rawSessionConfig,
			0600,
		); err != nil {
			return "", fmt.Errorf(
				"openvpn %q: write session helper config: %w",
				inbound.Tag,
				err,
			)
		}

		if err := os.MkdirAll(sessionStateDir, 0700); err != nil {
			return "", fmt.Errorf(
				"openvpn %q: create session state directory: %w",
				inbound.Tag,
				err,
			)
		}

		files.ClientConnectScript = filepath.Join(
			root,
			"client-connect.sh",
		)
		files.ClientDisconnectScript = filepath.Join(
			root,
			"client-disconnect.sh",
		)

		connectScript := "#!/bin/sh\nset -eu\nexec " +
			shellSingleQuote(executable) +
			" session-event " +
			shellSingleQuote(sessionConfigPath) +
			" start\n"

		disconnectScript := "#!/bin/sh\nset -eu\nexec " +
			shellSingleQuote(executable) +
			" session-event " +
			shellSingleQuote(sessionConfigPath) +
			" stop\n"

		if err := os.WriteFile(
			files.ClientConnectScript,
			[]byte(connectScript),
			0700,
		); err != nil {
			return "", fmt.Errorf(
				"openvpn %q: write client-connect script: %w",
				inbound.Tag,
				err,
			)
		}

		if err := os.WriteFile(
			files.ClientDisconnectScript,
			[]byte(disconnectScript),
			0700,
		); err != nil {
			return "", fmt.Errorf(
				"openvpn %q: write client-disconnect script: %w",
				inbound.Tag,
				err,
			)
		}
	}

	config, err := renderOpenVPNServerConfig(inbound, files)
	if err != nil {
		return "", err
	}

	configPath := filepath.Join(root, "server.conf")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		return "", err
	}

	return configPath, nil
}

func openVPNRuntimeDirName(tag string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tag)))
	return fmt.Sprintf("%x", sum[:8])
}

func validateOpenVPNUsername(username string) error {
	if username == "." || username == ".." {
		return fmt.Errorf("invalid username")
	}
	if strings.ContainsAny(username, "/\\\r\n\x00") {
		return fmt.Errorf("username contains invalid characters")
	}
	return nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

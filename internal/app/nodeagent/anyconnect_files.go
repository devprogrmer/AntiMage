package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var anyConnectCommand = exec.Command

type anyConnectUsageRuntimeConfig struct {
	InboundTag string           `json:"inbound_tag"`
	SocketPath string           `json:"socket_path"`
	Users      map[string]int64 `json:"users"`
}

func (s *Server) prepareAnyConnectInbound(inbound anyConnectRuntimeInbound, callback nativeRuntimeSessionCallback) (preparedAnyConnectRuntime, error) {
	tag := strings.TrimSpace(inbound.Tag)
	if tag == "" {
		return preparedAnyConnectRuntime{}, fmt.Errorf("anyconnect inbound tag is required")
	}
	cert := openVPNStringSetting(inbound.Settings, "server_certificate", "")
	key := openVPNStringSetting(inbound.Settings, "server_key", "")
	if cert == "" || key == "" {
		return preparedAnyConnectRuntime{}, fmt.Errorf("anyconnect %q: server certificate and private key are required", tag)
	}
	root := filepath.Join(s.cfg.DataDir, "anyconnect", openVPNRuntimeDirName(tag))
	usersDir := filepath.Join(root, "users")
	if err := os.MkdirAll(root, 0700); err != nil {
		return preparedAnyConnectRuntime{}, err
	}
	if err := os.RemoveAll(usersDir); err != nil {
		return preparedAnyConnectRuntime{}, err
	}
	if err := os.MkdirAll(usersDir, 0700); err != nil {
		return preparedAnyConnectRuntime{}, err
	}
	files := anyConnectRuntimeFiles{
		ConfigPath: filepath.Join(root, "ocserv.conf"), PasswordFile: filepath.Join(root, "ocpasswd"),
		ServerCert: filepath.Join(root, "server.crt"), ServerKey: filepath.Join(root, "server.key"),
		CACert: filepath.Join(root, "ca.crt"), UserConfigDir: usersDir,
		ControlSocket: filepath.Join(root, "ocserv.sock"), PIDFile: filepath.Join(root, "ocserv.pid"),
		UsageConfig: filepath.Join(root, "usage-helper.json"),
	}
	if err := writeAtomicMode(files.ServerCert, []byte(cert+"\n"), 0644); err != nil {
		return preparedAnyConnectRuntime{}, err
	}
	if err := writeAtomicMode(files.ServerKey, []byte(key+"\n"), 0600); err != nil {
		return preparedAnyConnectRuntime{}, err
	}
	if ca := openVPNStringSetting(inbound.Settings, "ca_certificate", ""); ca != "" {
		if err := writeAtomicMode(files.CACert, []byte(ca+"\n"), 0644); err != nil {
			return preparedAnyConnectRuntime{}, err
		}
	}
	_ = os.Remove(files.PasswordFile)
	users := make(map[string]int64, len(inbound.Users))
	policies := make(map[string]nativeSessionUserPolicy, len(inbound.Users))
	first := true
	for _, user := range inbound.Users {
		username := strings.TrimSpace(user.Username)
		if err := validateOpenVPNUsername(username); err != nil || username == "" || user.Password == "" {
			return preparedAnyConnectRuntime{}, fmt.Errorf("anyconnect %q: invalid credentials for user %d", tag, user.UserID)
		}
		if _, exists := users[username]; exists {
			return preparedAnyConnectRuntime{}, fmt.Errorf("anyconnect %q: duplicate username %q", tag, username)
		}
		if !nativeSessionUserAllowed(user) {
			continue
		}
		args := []string{"-g", "antimage", files.PasswordFile, username}
		if first {
			args = append([]string{"-c"}, args...)
			first = false
		}
		cmd := anyConnectCommand("ocpasswd", args...)
		cmd.Stdin = strings.NewReader(user.Password + "\n" + user.Password + "\n")
		if output, err := cmd.CombinedOutput(); err != nil {
			return preparedAnyConnectRuntime{}, fmt.Errorf("anyconnect %q: create password record for user %d: %w: %s", tag, user.UserID, err, strings.TrimSpace(string(output)))
		}
		users[username] = user.UserID
		policies[username] = anyConnectSessionPolicy(user)
		if cfg := renderAnyConnectUserConfig(user, inbound.Settings); strings.TrimSpace(cfg) != "" {
			if err := writeAtomicMode(filepath.Join(usersDir, username), []byte(cfg), 0600); err != nil {
				return preparedAnyConnectRuntime{}, err
			}
		}
	}
	if err := os.Chmod(files.PasswordFile, 0600); err != nil && len(users) > 0 {
		return preparedAnyConnectRuntime{}, err
	}
	if len(users) == 0 {
		if err := writeAtomicMode(files.PasswordFile, nil, 0600); err != nil {
			return preparedAnyConnectRuntime{}, err
		}
	}
	usageRaw, _ := json.Marshal(anyConnectUsageRuntimeConfig{InboundTag: tag, SocketPath: files.ControlSocket, Users: users})
	if err := writeAtomicMode(files.UsageConfig, usageRaw, 0600); err != nil {
		return preparedAnyConnectRuntime{}, err
	}
	if strings.TrimSpace(callback.URL) != "" {
		executable, err := os.Executable()
		if err != nil {
			return preparedAnyConnectRuntime{}, err
		}
		sessionConfig := nativeSessionHelperConfig{Callback: callback, InboundTag: tag, Protocol: "anyconnect", Users: users, Policies: policies, StateDir: filepath.Join(root, "sessions")}
		raw, _ := json.Marshal(sessionConfig)
		path := filepath.Join(root, "session-helper.json")
		if err := writeAtomicMode(path, raw, 0600); err != nil {
			return preparedAnyConnectRuntime{}, err
		}
		files.ConnectScript = filepath.Join(root, "connect.sh")
		files.DisconnectScript = filepath.Join(root, "disconnect.sh")
		start := "#!/bin/sh\nset -eu\nexec " + shellSingleQuote(executable) + " session-event " + shellSingleQuote(path) + " start\n"
		stop := "#!/bin/sh\nset -eu\nexec " + shellSingleQuote(executable) + " session-event " + shellSingleQuote(path) + " stop\n"
		if err := writeAtomicMode(files.ConnectScript, []byte(start), 0700); err != nil {
			return preparedAnyConnectRuntime{}, err
		}
		if err := writeAtomicMode(files.DisconnectScript, []byte(stop), 0700); err != nil {
			return preparedAnyConnectRuntime{}, err
		}
	}
	config, err := renderAnyConnectConfig(inbound, files)
	if err != nil {
		return preparedAnyConnectRuntime{}, err
	}
	if err := writeAtomicMode(files.ConfigPath, []byte(config), 0600); err != nil {
		return preparedAnyConnectRuntime{}, err
	}
	tproxy, err := buildOpenVPNTProxySpec(anyConnectAsOpenVPNInbound(inbound))
	if err != nil {
		return preparedAnyConnectRuntime{}, err
	}
	if tproxy.Enabled {
		tproxy.Interface = anyConnectDeviceName(tag) + "+"
	}
	nat, err := buildOpenVPNNATSpec(anyConnectAsOpenVPNInbound(inbound))
	if err != nil {
		return preparedAnyConnectRuntime{}, err
	}
	return preparedAnyConnectRuntime{Tag: tag, Files: files, Inbound: inbound, TProxy: tproxy, NAT: nat}, nil
}

func nativeSessionUserAllowed(user anyConnectRuntimeUser) bool {
	policy := anyConnectSessionPolicy(user)
	ok, _ := nativeSessionUserPolicyAllowed(policy, timeNow())
	return ok
}

var timeNow = func() time.Time { return time.Now() }

func anyConnectSessionPolicy(user anyConnectRuntimeUser) nativeSessionUserPolicy {
	var limit, expire int64
	if user.DataLimit != nil {
		limit = *user.DataLimit
	}
	if user.Expire != nil {
		expire = *user.Expire
	}
	return nativeSessionUserPolicy{Status: user.Status, UsedTraffic: user.UsedTraffic, DataLimit: limit, Expire: expire, UploadSpeedLimit: user.UploadSpeedLimit, DownloadSpeedLimit: user.DownloadSpeedLimit}
}

func writeAtomicMode(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

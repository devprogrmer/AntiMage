package nodeagent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ikev2IPSecBlockStart = "# BEGIN ANTIMAGE IKEV2 IPSEC"
	ikev2IPSecBlockEnd   = "# END ANTIMAGE IKEV2 IPSEC"

	ikev2SecretBlockStart = "# BEGIN ANTIMAGE IKEV2 SECRETS"
	ikev2SecretBlockEnd   = "# END ANTIMAGE IKEV2 SECRETS"
)

type ikev2Process struct {
	tag     string
	inbound ikev2RuntimeInbound
	files   ikev2RuntimeFiles
}

var (
	ikev2CACertDir     = "/etc/ipsec.d/cacerts"
	ikev2ServerCertDir = "/etc/ipsec.d/certs"
	ikev2ServerKeyDir  = "/etc/ipsec.d/private"
)

func preflightIKEv2Runtimes(
	runtimes []preparedIKEv2Runtime,
) error {
	if len(runtimes) == 0 {
		return nil
	}

	for _, name := range []string{
		"ipsec",
		"swanctl",
		"nft",
		"iptables",
		"ip",
		"sysctl",
	} {
		if _, err := l2TPLookPath(name); err != nil {
			return fmt.Errorf(
				"ikev2 %q: executable %q is not installed",
				runtimes[0].Tag,
				name,
			)
		}
	}

	return nil
}

func (s *Server) applyIKEv2Runtimes(
	runtimes []preparedIKEv2Runtime,
) error {
	if len(runtimes) == 0 {
		clearIKEv2SystemConfig(s)
		clearIKEv2SpeedLimits()
		cleanupIKEv2Firewall()

		s.mu.Lock()
		s.ikev2Runtimes = make(map[string]*ikev2Process)
		s.mu.Unlock()

		return nil
	}

	configChanged, err := installIKEv2SystemConfig(runtimes)
	if err != nil {
		return err
	}

	if err := ensureIKEv2Firewall(); err != nil {
		return fmt.Errorf(
			"ikev2 firewall: %w",
			err,
		)
	}

	if err := runL2TPCommand(
		s,
		"sysctl",
		"-w",
		"net.ipv4.ip_forward=1",
	); err != nil {
		return fmt.Errorf("ikev2 enable ip forwarding: %w", err)
	}

	if configChanged || !l2TPIPSecActive() {
		if err := runL2TPCommand(s, "ipsec", "rereadsecrets"); err == nil {
			if err := runL2TPCommand(s, "ipsec", "reload"); err != nil {
				return fmt.Errorf("ikev2 reload strongSwan: %w", err)
			}
		} else {
			if err := runL2TPCommand(s, "ipsec", "start"); err != nil {
				return fmt.Errorf("ikev2 start strongSwan: %w", err)
			}
		}
	}

	next := make(map[string]*ikev2Process, len(runtimes))
	for _, runtime := range runtimes {
		next[runtime.Tag] = &ikev2Process{
			tag:     runtime.Tag,
			inbound: runtime.Inbound,
			files:   runtime.Files,
		}
	}

	s.mu.Lock()
	s.ikev2Runtimes = next
	s.mu.Unlock()

	s.appendLog(fmt.Sprintf(
		"ikev2 runtime applied: inbounds=%d",
		len(runtimes),
	))

	return nil
}

func installIKEv2SystemConfig(
	runtimes []preparedIKEv2Runtime,
) (bool, error) {
	if err := os.MkdirAll(ikev2CACertDir, 0755); err != nil {
		return false, err
	}
	if err := os.MkdirAll(ikev2ServerCertDir, 0755); err != nil {
		return false, err
	}
	if err := os.MkdirAll(ikev2ServerKeyDir, 0700); err != nil {
		return false, err
	}

	var configs strings.Builder
	var secrets strings.Builder

	desiredFiles := make(map[string]struct{})
	changed := false

	for _, runtime := range runtimes {
		rawConfig, err := os.ReadFile(runtime.Files.IPSecConfig)
		if err != nil {
			return false, fmt.Errorf(
				"ikev2 %q: read config: %w",
				runtime.Tag,
				err,
			)
		}
		rawSecret, err := os.ReadFile(runtime.Files.IPSecSecret)
		if err != nil {
			return false, fmt.Errorf(
				"ikev2 %q: read secrets: %w",
				runtime.Tag,
				err,
			)
		}

		if configs.Len() > 0 {
			configs.WriteByte('\n')
		}
		configs.Write(rawConfig)

		if secrets.Len() > 0 {
			secrets.WriteByte('\n')
		}
		secrets.Write(rawSecret)

		copyPairs := []struct {
			src  string
			dst  string
			mode os.FileMode
		}{
			{
				runtime.Files.CACert,
				filepath.Join(ikev2CACertDir, runtime.Files.CAName),
				0644,
			},
			{
				runtime.Files.ServerCert,
				filepath.Join(ikev2ServerCertDir, runtime.Files.CertName),
				0644,
			},
			{
				runtime.Files.ServerKey,
				filepath.Join(ikev2ServerKeyDir, runtime.Files.KeyName),
				0600,
			},
		}

		for _, pair := range copyPairs {
			raw, err := os.ReadFile(pair.src)
			if err != nil {
				return false, err
			}
			before, err := readFileIfExists(pair.dst)
			if err != nil {
				return false, err
			}
			if !bytes.Equal(before, raw) {
				if err := atomicWriteFile(pair.dst, raw, pair.mode); err != nil {
					return false, err
				}
				changed = true
			}
			desiredFiles[pair.dst] = struct{}{}
		}
	}

	configBefore, err := readFileIfExists(l2TPIPSecConfigPath)
	if err != nil {
		return false, err
	}
	if err := updateL2TPManagedBlock(
		l2TPIPSecConfigPath,
		ikev2IPSecBlockStart,
		ikev2IPSecBlockEnd,
		configs.String(),
	); err != nil {
		return false, fmt.Errorf("install ikev2 ipsec config: %w", err)
	}
	configAfter, err := readFileIfExists(l2TPIPSecConfigPath)
	if err != nil {
		return false, err
	}
	changed = changed || !bytes.Equal(configBefore, configAfter)

	secretsBefore, err := readFileIfExists(l2TPIPSecSecretsPath)
	if err != nil {
		return false, err
	}
	if err := updateL2TPManagedBlock(
		l2TPIPSecSecretsPath,
		ikev2SecretBlockStart,
		ikev2SecretBlockEnd,
		secrets.String(),
	); err != nil {
		return false, fmt.Errorf("install ikev2 secrets: %w", err)
	}
	secretsAfter, err := readFileIfExists(l2TPIPSecSecretsPath)
	if err != nil {
		return false, err
	}
	changed = changed || !bytes.Equal(secretsBefore, secretsAfter)

	changed = cleanupIKEv2CredentialDir(ikev2CACertDir, desiredFiles) || changed
	changed = cleanupIKEv2CredentialDir(ikev2ServerCertDir, desiredFiles) || changed
	changed = cleanupIKEv2CredentialDir(ikev2ServerKeyDir, desiredFiles) || changed

	return changed, nil
}

func readFileIfExists(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return raw, err
}

func cleanupIKEv2CredentialDir(
	dir string,
	desired map[string]struct{},
) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	changed := false

	for _, entry := range entries {
		if entry.IsDir() ||
			!strings.HasPrefix(entry.Name(), "antimage-ikev2-") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		if _, ok := desired[path]; ok {
			continue
		}

		if os.Remove(path) == nil {
			changed = true
		}
	}
	return changed
}

func clearIKEv2SystemConfig(s *Server) {
	if err := updateL2TPManagedBlock(
		l2TPIPSecConfigPath,
		ikev2IPSecBlockStart,
		ikev2IPSecBlockEnd,
		"",
	); err != nil {
		s.appendLog("clear IKEv2 ipsec config failed: " + err.Error())
	}

	if err := updateL2TPManagedBlock(
		l2TPIPSecSecretsPath,
		ikev2SecretBlockStart,
		ikev2SecretBlockEnd,
		"",
	); err != nil {
		s.appendLog("clear IKEv2 secrets failed: " + err.Error())
	}

	desired := map[string]struct{}{}
	cleanupIKEv2CredentialDir(ikev2CACertDir, desired)
	cleanupIKEv2CredentialDir(ikev2ServerCertDir, desired)
	cleanupIKEv2CredentialDir(ikev2ServerKeyDir, desired)

	if _, err := l2TPLookPath("ipsec"); err == nil {
		_ = runL2TPCommand(s, "ipsec", "rereadsecrets")
		_ = runL2TPCommand(s, "ipsec", "reload")
	}
}

func (s *Server) stopAllIKEv2Runtimes() {
	s.mu.Lock()
	had := len(s.ikev2Runtimes) != 0
	s.ikev2Runtimes = make(map[string]*ikev2Process)
	s.mu.Unlock()

	if !had {
		return
	}

	clearIKEv2SystemConfig(s)
	clearIKEv2SpeedLimits()
	cleanupIKEv2Firewall()
	s.appendLog("all ikev2 runtimes stopped")
}

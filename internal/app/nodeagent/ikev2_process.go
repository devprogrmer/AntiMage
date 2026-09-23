package nodeagent

import (
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
	tag string
}

func preflightIKEv2Runtimes(
	runtimes []preparedIKEv2Runtime,
) error {
	if len(runtimes) == 0 {
		return nil
	}

	for _, name := range []string{
		"ipsec",
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

		s.mu.Lock()
		s.ikev2Runtimes = make(map[string]*ikev2Process)
		s.mu.Unlock()

		return nil
	}

	if err := installIKEv2SystemConfig(runtimes); err != nil {
		return err
	}

	if err := runL2TPCommand(
		s,
		"sysctl",
		"-w",
		"net.ipv4.ip_forward=1",
	); err != nil {
		return fmt.Errorf("ikev2 enable ip forwarding: %w", err)
	}

	if err := runL2TPCommand(
		s,
		"ipsec",
		"rereadsecrets",
	); err == nil {
		if err := runL2TPCommand(
			s,
			"ipsec",
			"reload",
		); err != nil {
			return fmt.Errorf("ikev2 reload strongSwan: %w", err)
		}
	} else {
		if err := runL2TPCommand(
			s,
			"ipsec",
			"start",
		); err != nil {
			return fmt.Errorf("ikev2 start strongSwan: %w", err)
		}
	}

	next := make(map[string]*ikev2Process, len(runtimes))
	for _, runtime := range runtimes {
		next[runtime.Tag] = &ikev2Process{
			tag: runtime.Tag,
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
) error {
	if err := os.MkdirAll("/etc/ipsec.d/cacerts", 0755); err != nil {
		return err
	}
	if err := os.MkdirAll("/etc/ipsec.d/certs", 0755); err != nil {
		return err
	}
	if err := os.MkdirAll("/etc/ipsec.d/private", 0700); err != nil {
		return err
	}

	var configs strings.Builder
	var secrets strings.Builder

	desiredFiles := make(map[string]struct{})

	for _, runtime := range runtimes {
		rawConfig, err := os.ReadFile(runtime.Files.IPSecConfig)
		if err != nil {
			return fmt.Errorf(
				"ikev2 %q: read config: %w",
				runtime.Tag,
				err,
			)
		}
		rawSecret, err := os.ReadFile(runtime.Files.IPSecSecret)
		if err != nil {
			return fmt.Errorf(
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
				filepath.Join("/etc/ipsec.d/cacerts", runtime.Files.CAName),
				0644,
			},
			{
				runtime.Files.ServerCert,
				filepath.Join("/etc/ipsec.d/certs", runtime.Files.CertName),
				0644,
			},
			{
				runtime.Files.ServerKey,
				filepath.Join("/etc/ipsec.d/private", runtime.Files.KeyName),
				0600,
			},
		}

		for _, pair := range copyPairs {
			raw, err := os.ReadFile(pair.src)
			if err != nil {
				return err
			}
			if err := os.WriteFile(pair.dst, raw, pair.mode); err != nil {
				return err
			}
			desiredFiles[pair.dst] = struct{}{}
		}
	}

	if err := updateL2TPManagedBlock(
		l2TPIPSecConfigPath,
		ikev2IPSecBlockStart,
		ikev2IPSecBlockEnd,
		configs.String(),
	); err != nil {
		return fmt.Errorf("install ikev2 ipsec config: %w", err)
	}

	if err := updateL2TPManagedBlock(
		l2TPIPSecSecretsPath,
		ikev2SecretBlockStart,
		ikev2SecretBlockEnd,
		secrets.String(),
	); err != nil {
		return fmt.Errorf("install ikev2 secrets: %w", err)
	}

	cleanupIKEv2CredentialDir(
		"/etc/ipsec.d/cacerts",
		desiredFiles,
	)
	cleanupIKEv2CredentialDir(
		"/etc/ipsec.d/certs",
		desiredFiles,
	)
	cleanupIKEv2CredentialDir(
		"/etc/ipsec.d/private",
		desiredFiles,
	)

	return nil
}

func cleanupIKEv2CredentialDir(
	dir string,
	desired map[string]struct{},
) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() ||
			!strings.HasPrefix(entry.Name(), "antimage-ikev2-") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		if _, ok := desired[path]; ok {
			continue
		}

		_ = os.Remove(path)
	}
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
	cleanupIKEv2CredentialDir("/etc/ipsec.d/cacerts", desired)
	cleanupIKEv2CredentialDir("/etc/ipsec.d/certs", desired)
	cleanupIKEv2CredentialDir("/etc/ipsec.d/private", desired)

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
	s.appendLog("all ikev2 runtimes stopped")
}

package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	l2TPIPSecConfigPath  = "/etc/ipsec.conf"
	l2TPIPSecSecretsPath = "/etc/ipsec.secrets"
	l2TPXL2TPConfigPath  = "/etc/xl2tpd/xl2tpd.conf"

	l2TPIPSecBlockStart = "# BEGIN ANTIMAGE L2TP IPSEC"
	l2TPIPSecBlockEnd   = "# END ANTIMAGE L2TP IPSEC"

	l2TPSecretBlockStart = "# BEGIN ANTIMAGE L2TP IPSEC SECRET"
	l2TPSecretBlockEnd   = "# END ANTIMAGE L2TP IPSEC SECRET"
)

var (
	l2TPCommandContext = exec.CommandContext
	l2TPLookPath       = exec.LookPath
)

type l2TPProcess struct {
	tag string
}

func preflightL2TPRuntimes(runtimes []preparedL2TPRuntime) error {
	if len(runtimes) == 0 {
		return nil
	}

	for _, name := range []string{"ipsec", "xl2tpd", "pppd"} {
		if _, err := l2TPLookPath(name); err != nil {
			return fmt.Errorf(
				"l2tp %q: executable %q not installed",
				runtimes[0].Tag,
				name,
			)
		}
	}

	return nil
}

func runL2TPCommand(
	s *Server,
	name string,
	args ...string,
) error {
	path, err := l2TPLookPath(name)
	if err != nil {
		return fmt.Errorf("executable %q not installed", name)
	}

	cmd := l2TPCommandContext(
		context.Background(),
		path,
		args...,
	)
	cmd.Stdout = logWriter{server: s}
	cmd.Stderr = logWriter{server: s}
	cmd.Env = os.Environ()

	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"%s %s: %w",
			name,
			strings.Join(args, " "),
			err,
		)
	}

	return nil
}

func updateL2TPManagedBlock(
	path string,
	start string,
	end string,
	body string,
) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	text := string(existing)

	block := start + "\n"
	if strings.TrimSpace(body) != "" {
		block += strings.TrimRight(body, "\n") + "\n"
	}
	block += end + "\n"

	startIndex := strings.Index(text, start)
	endIndex := strings.Index(text, end)

	if startIndex >= 0 && endIndex > startIndex {
		endIndex += len(end)

		before := strings.TrimRight(text[:startIndex], "\n")
		after := strings.TrimLeft(text[endIndex:], "\n")

		switch {
		case before != "" && after != "":
			text = before + "\n" + block + after
		case before != "":
			text = before + "\n" + block
		case after != "":
			text = block + after
		default:
			text = block
		}
	} else {
		if strings.TrimSpace(text) != "" {
			text = strings.TrimRight(text, "\n") + "\n"
		}
		text += block
	}

	return os.WriteFile(path, []byte(text), 0600)
}

func installL2TPSystemConfig(
	ipsecConfig string,
	ipsecSecrets string,
	xl2tpConfig string,
) error {
	rawIPSec, err := os.ReadFile(ipsecConfig)
	if err != nil {
		return fmt.Errorf("read L2TP ipsec config: %w", err)
	}

	rawSecrets, err := os.ReadFile(ipsecSecrets)
	if err != nil {
		return fmt.Errorf("read L2TP ipsec secrets: %w", err)
	}

	rawXL2TP, err := os.ReadFile(xl2tpConfig)
	if err != nil {
		return fmt.Errorf("read L2TP xl2tpd config: %w", err)
	}

	if err := updateL2TPManagedBlock(
		l2TPIPSecConfigPath,
		l2TPIPSecBlockStart,
		l2TPIPSecBlockEnd,
		string(rawIPSec),
	); err != nil {
		return fmt.Errorf("update %s: %w", l2TPIPSecConfigPath, err)
	}

	if err := updateL2TPManagedBlock(
		l2TPIPSecSecretsPath,
		l2TPSecretBlockStart,
		l2TPSecretBlockEnd,
		string(rawSecrets),
	); err != nil {
		return fmt.Errorf("update %s: %w", l2TPIPSecSecretsPath, err)
	}

	if err := os.MkdirAll("/etc/xl2tpd", 0755); err != nil {
		return fmt.Errorf("create /etc/xl2tpd: %w", err)
	}

	if err := os.WriteFile(
		l2TPXL2TPConfigPath,
		rawXL2TP,
		0600,
	); err != nil {
		return fmt.Errorf("write %s: %w", l2TPXL2TPConfigPath, err)
	}

	return nil
}

func applyL2TPIPSec(
	s *Server,
	tag string,
) error {
	// When charon is already running, reload only our managed
	// connection and credentials without supervising charon as
	// an AntiMage child process.
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
			return fmt.Errorf(
				"l2tp %q: reload ipsec: %w",
				tag,
				err,
			)
		}
		return nil
	}

	// No usable daemon yet. Start the system-wide IPsec daemon.
	if err := runL2TPCommand(
		s,
		"ipsec",
		"start",
	); err != nil {
		return fmt.Errorf(
			"l2tp %q: start ipsec: %w",
			tag,
			err,
		)
	}

	return nil
}

func stopCommandProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil &&
		!errors.Is(err, os.ErrProcessDone) {
		if killErr := cmd.Process.Kill(); killErr != nil &&
			!errors.Is(killErr, os.ErrProcessDone) {
			return fmt.Errorf(
				"interrupt process: %v; kill process: %w",
				err,
				killErr,
			)
		}
	}

	return nil
}
func stopL2TPSystemService(s *Server) {
	if _, err := l2TPLookPath("systemctl"); err == nil {
		if err := runL2TPCommand(
			s,
			"systemctl",
			"stop",
			"xl2tpd",
		); err == nil {
			return
		}
	}

	if _, err := l2TPLookPath("service"); err == nil {
		_ = runL2TPCommand(
			s,
			"service",
			"xl2tpd",
			"stop",
		)
	}
}

func startL2TPSystemService(s *Server) error {
	stopL2TPSystemService(s)

	if _, err := l2TPLookPath("systemctl"); err == nil {
		if err := runL2TPCommand(
			s,
			"systemctl",
			"start",
			"xl2tpd",
		); err == nil {
			return nil
		}
	}

	if _, err := l2TPLookPath("service"); err == nil {
		if err := runL2TPCommand(
			s,
			"service",
			"xl2tpd",
			"start",
		); err == nil {
			return nil
		}
	}

	// Last-resort daemon start for systems without a service manager.
	if err := runL2TPCommand(s, "xl2tpd"); err != nil {
		return fmt.Errorf("start xl2tpd: %w", err)
	}

	return nil
}

func (s *Server) startL2TPInbound(
	tag string,
	ipsecConfig string,
	ipsecSecrets string,
	xl2tpConfig string,
) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("l2tp runtime tag is required")
	}

	if err := installL2TPSystemConfig(
		ipsecConfig,
		ipsecSecrets,
		xl2tpConfig,
	); err != nil {
		return fmt.Errorf(
			"l2tp %q: install system config: %w",
			tag,
			err,
		)
	}

	if err := applyL2TPIPSec(s, tag); err != nil {
		return err
	}

	if err := startL2TPSystemService(s); err != nil {
		return fmt.Errorf(
			"l2tp %q: %w",
			tag,
			err,
		)
	}

	runtime := &l2TPProcess{tag: tag}

	s.mu.Lock()
	s.l2TPRuntimes[tag] = runtime
	s.mu.Unlock()

	s.appendLog("l2tp runtime started: " + tag)
	return nil
}

func clearL2TPSystemConfig(s *Server) {
	if err := updateL2TPManagedBlock(
		l2TPIPSecConfigPath,
		l2TPIPSecBlockStart,
		l2TPIPSecBlockEnd,
		"",
	); err != nil {
		s.appendLog("clear L2TP ipsec config failed: " + err.Error())
	}

	if err := updateL2TPManagedBlock(
		l2TPIPSecSecretsPath,
		l2TPSecretBlockStart,
		l2TPSecretBlockEnd,
		"",
	); err != nil {
		s.appendLog("clear L2TP ipsec secrets failed: " + err.Error())
	}

	if _, err := l2TPLookPath("ipsec"); err == nil {
		_ = runL2TPCommand(
			s,
			"ipsec",
			"rereadsecrets",
		)
		_ = runL2TPCommand(
			s,
			"ipsec",
			"reload",
		)
	}
}

func (s *Server) stopAllL2TPRuntimes() {
	s.mu.Lock()
	hadRuntime := len(s.l2TPRuntimes) > 0
	s.l2TPRuntimes = make(map[string]*l2TPProcess)
	s.mu.Unlock()

	if !hadRuntime {
		return
	}

	stopL2TPSystemService(s)
	clearL2TPSystemConfig(s)

	s.appendLog("all l2tp runtimes stopped")
}

func (s *Server) stopRemovedL2TPRuntimes(
	desired map[string]struct{},
) {
	s.mu.Lock()

	removed := false
	for tag := range s.l2TPRuntimes {
		if _, ok := desired[tag]; ok {
			continue
		}

		delete(s.l2TPRuntimes, tag)
		removed = true
	}

	s.mu.Unlock()

	if !removed {
		return
	}

	stopL2TPSystemService(s)
	clearL2TPSystemConfig(s)

	s.appendLog("removed l2tp runtime stopped")
}

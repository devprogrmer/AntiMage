package nodeagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	pptpCHAPBlockStart = "# BEGIN ANTIMAGE PPTP CHAP"
	pptpCHAPBlockEnd   = "# END ANTIMAGE PPTP CHAP"
)

var (
	pptpCommandContext  = exec.CommandContext
	pptpLookPath        = exec.LookPath
	pptpStartupGrace    = 300 * time.Millisecond
	pptpShutdownGrace   = 3 * time.Second
	pptpKillGrace       = 2 * time.Second
	pptpCHAPSecretsPath = "/etc/ppp/chap-secrets"
)

type pptpProcess struct {
	cmd  *exec.Cmd
	done chan struct{}

	waitMu  sync.Mutex
	waitErr error
}

func preflightPPTPRuntimes(runtimes []preparedPPTPRuntime) error {
	if len(runtimes) == 0 {
		return nil
	}
	for _, name := range []string{"pptpd", "pppd"} {
		if _, err := pptpLookPath(name); err != nil {
			return fmt.Errorf("pptp %q: executable %q not installed", runtimes[0].Tag, name)
		}
	}
	return nil
}

func (r *pptpProcess) setWaitError(err error) {
	r.waitMu.Lock()
	r.waitErr = err
	r.waitMu.Unlock()
}

func (r *pptpProcess) waitError() error {
	r.waitMu.Lock()
	defer r.waitMu.Unlock()
	return r.waitErr
}

func renderPPTPSystemCHAPSecrets(runtimes []preparedPPTPRuntime) string {
	parts := make([]string, 0, len(runtimes))
	for _, runtime := range runtimes {
		parts = append(parts, runtime.CHAPSecrets)
	}
	return dedupePPTPCHAPSecrets(strings.Join(parts, "\n"))
}

func dedupePPTPCHAPSecrets(chapSecrets string) string {
	seen := make(map[string]struct{})
	var b strings.Builder
	for _, line := range strings.Split(chapSecrets, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func updatePPTPManagedBlock(path, body string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	text := string(existing)
	block := pptpCHAPBlockStart + "\n"
	if strings.TrimSpace(body) != "" {
		block += strings.TrimRight(body, "\n") + "\n"
	}
	block += pptpCHAPBlockEnd + "\n"

	startIndex := strings.Index(text, pptpCHAPBlockStart)
	endIndex := strings.Index(text, pptpCHAPBlockEnd)
	if startIndex >= 0 && endIndex > startIndex {
		endIndex += len(pptpCHAPBlockEnd)
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

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".chap-secrets-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func installPPTPSystemCHAPSecrets(chapSecrets string) error {
	return updatePPTPManagedBlock(pptpCHAPSecretsPath, dedupePPTPCHAPSecrets(chapSecrets))
}

func clearPPTPSystemCHAPSecrets() error {
	return updatePPTPManagedBlock(pptpCHAPSecretsPath, "")
}

func (s *Server) startPPTPInbound(tag, configPath, chapSecrets string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("pptp runtime tag is required")
	}
	if err := installPPTPSystemCHAPSecrets(chapSecrets); err != nil {
		return fmt.Errorf("pptp %q: install chap secrets: %w", tag, err)
	}
	pptpdPath, err := pptpLookPath("pptpd")
	if err != nil {
		return fmt.Errorf("pptp %q: executable %q not installed", tag, "pptpd")
	}

	s.mu.Lock()
	previous := s.pptpRuntimes[tag]
	s.mu.Unlock()
	if previous != nil {
		if err := stopPPTPProcess(previous); err != nil {
			return fmt.Errorf("pptp %q: stop previous runtime: %w", tag, err)
		}
	}

	cmd := pptpCommandContext(context.Background(), pptpdPath, "-f", "-c", configPath)
	cmd.Stdout = logWriter{server: s}
	cmd.Stderr = logWriter{server: s}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("pptp %q: start: %w", tag, err)
	}
	runtime := &pptpProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		runtime.setWaitError(err)
		close(runtime.done)
		s.mu.Lock()
		if s.pptpRuntimes[tag] == runtime {
			delete(s.pptpRuntimes, tag)
		}
		s.mu.Unlock()
		if err != nil {
			s.appendLog("pptp runtime stopped: " + tag + ": " + err.Error())
			return
		}
		s.appendLog("pptp runtime stopped: " + tag)
	}()

	timer := time.NewTimer(pptpStartupGrace)
	select {
	case <-runtime.done:
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		waitErr := runtime.waitError()
		if waitErr != nil {
			return fmt.Errorf("pptp %q: exited during startup: %w", tag, waitErr)
		}
		return fmt.Errorf("pptp %q: exited during startup", tag)
	case <-timer.C:
	}

	s.mu.Lock()
	select {
	case <-runtime.done:
		s.mu.Unlock()
		waitErr := runtime.waitError()
		if waitErr != nil {
			return fmt.Errorf("pptp %q: exited during startup: %w", tag, waitErr)
		}
		return fmt.Errorf("pptp %q: exited during startup", tag)
	default:
		s.pptpRuntimes[tag] = runtime
	}
	s.mu.Unlock()
	s.appendLog("pptp runtime started: " + tag)
	return nil
}

func stopPPTPProcess(runtime *pptpProcess) error {
	if runtime == nil {
		return nil
	}
	select {
	case <-runtime.done:
		return nil
	default:
	}
	_ = stopCommandProcess(runtime.cmd)
	select {
	case <-runtime.done:
		return nil
	case <-time.After(pptpShutdownGrace):
	}
	if runtime.cmd != nil && runtime.cmd.Process != nil {
		_ = runtime.cmd.Process.Kill()
	}
	select {
	case <-runtime.done:
		return nil
	case <-time.After(pptpKillGrace):
		return fmt.Errorf("process did not exit after forced kill")
	}
}

func (s *Server) stopAllPPTPRuntimes() {
	s.mu.Lock()
	runtimes := make(map[string]*pptpProcess, len(s.pptpRuntimes))
	for tag, runtime := range s.pptpRuntimes {
		runtimes[tag] = runtime
	}
	s.pptpRuntimes = make(map[string]*pptpProcess)
	s.mu.Unlock()
	for tag, runtime := range runtimes {
		s.appendLog("stopping pptp runtime: " + tag)
		if err := stopPPTPProcess(runtime); err != nil {
			s.appendLog("stop pptp runtime failed: " + tag + ": " + err.Error())
		}
	}
}

func (s *Server) stopRemovedPPTPRuntimes(desired map[string]struct{}) {
	s.mu.Lock()
	removed := make(map[string]*pptpProcess)
	for tag, runtime := range s.pptpRuntimes {
		if _, ok := desired[tag]; ok {
			continue
		}
		removed[tag] = runtime
		delete(s.pptpRuntimes, tag)
	}
	s.mu.Unlock()
	for tag, runtime := range removed {
		s.appendLog("stopping removed pptp runtime: " + tag)
		if err := stopPPTPProcess(runtime); err != nil {
			s.appendLog("stop removed pptp runtime failed: " + tag + ": " + err.Error())
		}
	}
}

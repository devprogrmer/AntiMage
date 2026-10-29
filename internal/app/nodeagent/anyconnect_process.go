package nodeagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	anyConnectLookPath       = exec.LookPath
	anyConnectCommandContext = exec.CommandContext
	anyConnectStartupGrace   = 400 * time.Millisecond
)

type anyConnectProcess struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitMu  sync.Mutex
	waitErr error
}

func (p *anyConnectProcess) setWaitError(err error) {
	p.waitMu.Lock()
	p.waitErr = err
	p.waitMu.Unlock()
}
func (p *anyConnectProcess) waitError() error {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitErr
}

func preflightAnyConnectRuntimes(runtimes []preparedAnyConnectRuntime) error {
	if len(runtimes) == 0 {
		return nil
	}
	for _, name := range []string{"ocserv", "ocpasswd", "occtl"} {
		if _, err := anyConnectLookPath(name); err != nil {
			return fmt.Errorf("anyconnect %q: %s executable not installed", runtimes[0].Tag, name)
		}
	}
	return nil
}

func (s *Server) startAnyConnectInbound(runtime preparedAnyConnectRuntime) error {
	tag := strings.TrimSpace(runtime.Tag)
	if tag == "" {
		return fmt.Errorf("anyconnect runtime tag is required")
	}
	path, err := anyConnectLookPath("ocserv")
	if err != nil {
		return fmt.Errorf("anyconnect %q: ocserv executable not installed", tag)
	}
	s.mu.Lock()
	previous := s.anyConnectRuntimes[tag]
	s.mu.Unlock()
	if previous != nil {
		if err := stopAnyConnectProcess(previous); err != nil {
			return fmt.Errorf("anyconnect %q: stop previous runtime: %w", tag, err)
		}
	}
	_ = os.Remove(runtime.Files.ControlSocket)
	_ = os.Remove(runtime.Files.PIDFile)
	cmd := anyConnectCommandContext(context.Background(), path, "-f", "-c", runtime.Files.ConfigPath)
	cmd.Stdout = logWriter{server: s}
	cmd.Stderr = logWriter{server: s}
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("anyconnect %q: start: %w", tag, err)
	}
	p := &anyConnectProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		p.setWaitError(err)
		close(p.done)
		s.mu.Lock()
		if s.anyConnectRuntimes[tag] == p {
			delete(s.anyConnectRuntimes, tag)
		}
		s.mu.Unlock()
		if err != nil {
			s.appendLog("anyconnect runtime stopped: " + tag + ": " + err.Error())
		} else {
			s.appendLog("anyconnect runtime stopped: " + tag)
		}
	}()
	timer := time.NewTimer(anyConnectStartupGrace)
	select {
	case <-p.done:
		if !timer.Stop() {
			<-timer.C
		}
		if err := p.waitError(); err != nil {
			return fmt.Errorf("anyconnect %q: exited during startup: %w", tag, err)
		}
		return fmt.Errorf("anyconnect %q: exited during startup", tag)
	case <-timer.C:
	}
	s.mu.Lock()
	s.anyConnectRuntimes[tag] = p
	s.mu.Unlock()
	s.appendLog("anyconnect runtime started: " + tag)
	return nil
}

func stopAnyConnectProcess(p *anyConnectProcess) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := openVPNTerminateProcess(p.cmd.Process); err != nil {
		_ = p.cmd.Process.Kill()
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(openVPNShutdownGrace):
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
		return nil
	case <-time.After(openVPNKillGrace):
		return fmt.Errorf("process did not exit after forced kill")
	}
}

func (s *Server) stopRemovedAnyConnectRuntimes(desired map[string]struct{}) {
	s.mu.Lock()
	removed := map[string]*anyConnectProcess{}
	for tag, p := range s.anyConnectRuntimes {
		if _, ok := desired[tag]; !ok {
			removed[tag] = p
			delete(s.anyConnectRuntimes, tag)
		}
	}
	s.mu.Unlock()
	for tag, p := range removed {
		s.appendLog("stopping removed anyconnect runtime: " + tag)
		if err := stopAnyConnectProcess(p); err != nil {
			s.appendLog("stop anyconnect runtime failed: " + tag + ": " + err.Error())
		}
	}
}

func (s *Server) stopAllAnyConnectRuntimes() {
	s.mu.Lock()
	runtimes := s.anyConnectRuntimes
	s.anyConnectRuntimes = make(map[string]*anyConnectProcess)
	s.mu.Unlock()
	for tag, p := range runtimes {
		s.appendLog("stopping anyconnect runtime: " + tag)
		_ = stopAnyConnectProcess(p)
	}
}

func (s *Server) activeAnyConnectRuntimeTags() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	tags := make([]string, 0, len(s.anyConnectRuntimes))
	for tag := range s.anyConnectRuntimes {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

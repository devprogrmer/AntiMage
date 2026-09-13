package nodeagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	pptpCommandContext = exec.CommandContext
	pptpLookPath       = exec.LookPath
	pptpStartupGrace   = 300 * time.Millisecond
	pptpShutdownGrace  = 3 * time.Second
	pptpKillGrace      = 2 * time.Second
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

func (s *Server) startPPTPInbound(tag, configPath string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("pptp runtime tag is required")
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

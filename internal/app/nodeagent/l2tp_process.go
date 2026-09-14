package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	l2TPCommandContext = exec.CommandContext
	l2TPLookPath       = exec.LookPath
	l2TPStartupGrace   = 300 * time.Millisecond
	l2TPShutdownGrace  = 3 * time.Second
	l2TPKillGrace      = 2 * time.Second
)

type l2TPProcess struct {
	ipsec *exec.Cmd
	xl2tp *exec.Cmd
	done  chan struct{}

	waitMu  sync.Mutex
	waitErr error
}

func preflightL2TPRuntimes(runtimes []preparedL2TPRuntime) error {
	if len(runtimes) == 0 {
		return nil
	}
	for _, name := range []string{"ipsec", "xl2tpd", "pppd"} {
		if _, err := l2TPLookPath(name); err != nil {
			return fmt.Errorf("l2tp %q: executable %q not installed", runtimes[0].Tag, name)
		}
	}
	return nil
}

func (r *l2TPProcess) setWaitError(err error) {
	r.waitMu.Lock()
	r.waitErr = err
	r.waitMu.Unlock()
}

func (r *l2TPProcess) waitError() error {
	r.waitMu.Lock()
	defer r.waitMu.Unlock()
	return r.waitErr
}

func (s *Server) startL2TPInbound(tag, ipsecConfig, xl2tpConfig string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("l2tp runtime tag is required")
	}
	ipsecPath, err := l2TPLookPath("ipsec")
	if err != nil {
		return fmt.Errorf("l2tp %q: executable %q not installed", tag, "ipsec")
	}
	xl2tpPath, err := l2TPLookPath("xl2tpd")
	if err != nil {
		return fmt.Errorf("l2tp %q: executable %q not installed", tag, "xl2tpd")
	}

	s.mu.Lock()
	previous := s.l2TPRuntimes[tag]
	s.mu.Unlock()
	if previous != nil {
		if err := stopL2TPProcess(previous); err != nil {
			return fmt.Errorf("l2tp %q: stop previous runtime: %w", tag, err)
		}
	}

	ipsec := l2TPCommandContext(context.Background(), ipsecPath, "start", "--nofork", "--conf", ipsecConfig)
	xl2tp := l2TPCommandContext(context.Background(), xl2tpPath, "-D", "-c", xl2tpConfig)
	for _, cmd := range []*exec.Cmd{ipsec, xl2tp} {
		cmd.Stdout = logWriter{server: s}
		cmd.Stderr = logWriter{server: s}
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
	}

	if err := ipsec.Start(); err != nil {
		return fmt.Errorf("l2tp %q: start ipsec: %w", tag, err)
	}
	runtime := &l2TPProcess{ipsec: ipsec, xl2tp: xl2tp, done: make(chan struct{})}
	waitCh := make(chan error, 2)
	go func() { waitCh <- fmt.Errorf("ipsec: %w", ipsec.Wait()) }()

	if err := xl2tp.Start(); err != nil {
		_ = stopCommandProcess(ipsec)
		return fmt.Errorf("l2tp %q: start xl2tpd: %w", tag, err)
	}
	go func() { waitCh <- fmt.Errorf("xl2tpd: %w", xl2tp.Wait()) }()

	go func() {
		err := <-waitCh
		runtime.setWaitError(err)
		_ = stopCommandProcess(ipsec)
		_ = stopCommandProcess(xl2tp)
		close(runtime.done)
		s.mu.Lock()
		if s.l2TPRuntimes[tag] == runtime {
			delete(s.l2TPRuntimes, tag)
		}
		s.mu.Unlock()
		if err != nil {
			s.appendLog("l2tp runtime stopped: " + tag + ": " + err.Error())
			return
		}
		s.appendLog("l2tp runtime stopped: " + tag)
	}()

	timer := time.NewTimer(l2TPStartupGrace)
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
			return fmt.Errorf("l2tp %q: exited during startup: %w", tag, waitErr)
		}
		return fmt.Errorf("l2tp %q: exited during startup", tag)
	case <-timer.C:
	}

	s.mu.Lock()
	select {
	case <-runtime.done:
		s.mu.Unlock()
		waitErr := runtime.waitError()
		if waitErr != nil {
			return fmt.Errorf("l2tp %q: exited during startup: %w", tag, waitErr)
		}
		return fmt.Errorf("l2tp %q: exited during startup", tag)
	default:
		s.l2TPRuntimes[tag] = runtime
	}
	s.mu.Unlock()
	s.appendLog("l2tp runtime started: " + tag)
	return nil
}

func stopCommandProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return fmt.Errorf("interrupt process: %v; kill process: %w", err, killErr)
		}
	}
	return nil
}

func stopL2TPProcess(runtime *l2TPProcess) error {
	if runtime == nil {
		return nil
	}
	select {
	case <-runtime.done:
		return nil
	default:
	}
	_ = stopCommandProcess(runtime.xl2tp)
	_ = stopCommandProcess(runtime.ipsec)
	select {
	case <-runtime.done:
		return nil
	case <-time.After(l2TPShutdownGrace):
	}
	if runtime.xl2tp != nil && runtime.xl2tp.Process != nil {
		_ = runtime.xl2tp.Process.Kill()
	}
	if runtime.ipsec != nil && runtime.ipsec.Process != nil {
		_ = runtime.ipsec.Process.Kill()
	}
	select {
	case <-runtime.done:
		return nil
	case <-time.After(l2TPKillGrace):
		return fmt.Errorf("processes did not exit after forced kill")
	}
}

func (s *Server) stopAllL2TPRuntimes() {
	s.mu.Lock()
	runtimes := make(map[string]*l2TPProcess, len(s.l2TPRuntimes))
	for tag, runtime := range s.l2TPRuntimes {
		runtimes[tag] = runtime
	}
	s.l2TPRuntimes = make(map[string]*l2TPProcess)
	s.mu.Unlock()
	for tag, runtime := range runtimes {
		s.appendLog("stopping l2tp runtime: " + tag)
		if err := stopL2TPProcess(runtime); err != nil {
			s.appendLog("stop l2tp runtime failed: " + tag + ": " + err.Error())
		}
	}
}

func (s *Server) stopRemovedL2TPRuntimes(desired map[string]struct{}) {
	s.mu.Lock()
	removed := make(map[string]*l2TPProcess)
	for tag, runtime := range s.l2TPRuntimes {
		if _, ok := desired[tag]; ok {
			continue
		}
		removed[tag] = runtime
		delete(s.l2TPRuntimes, tag)
	}
	s.mu.Unlock()
	for tag, runtime := range removed {
		s.appendLog("stopping removed l2tp runtime: " + tag)
		if err := stopL2TPProcess(runtime); err != nil {
			s.appendLog("stop removed l2tp runtime failed: " + tag + ": " + err.Error())
		}
	}
}

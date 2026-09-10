package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	openVPNCommandContext = exec.CommandContext
	openVPNLookPath       = exec.LookPath

	openVPNStartupGrace     = 300 * time.Millisecond
	openVPNShutdownGrace    = 3 * time.Second
	openVPNKillGrace        = 2 * time.Second
	openVPNTerminateProcess = terminateOpenVPNProcess
)

type openVPNProcess struct {
	cmd  *exec.Cmd
	done chan struct{}

	waitMu  sync.Mutex
	waitErr error
}

func (r *openVPNProcess) setWaitError(err error) {
	r.waitMu.Lock()
	r.waitErr = err
	r.waitMu.Unlock()
}

func (r *openVPNProcess) waitError() error {
	r.waitMu.Lock()
	defer r.waitMu.Unlock()

	return r.waitErr
}

func (s *Server) startOpenVPNInbound(
	tag,
	configPath string,
) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf(
			"openvpn runtime tag is required",
		)
	}

	openvpnPath, err := openVPNLookPath("openvpn")
	if err != nil {
		return fmt.Errorf(
			"openvpn %q: executable not installed",
			tag,
		)
	}

	s.mu.Lock()
	previous := s.openVPNRuntimes[tag]
	s.mu.Unlock()

	if previous != nil {
		if err := stopOpenVPNProcess(previous); err != nil {
			return fmt.Errorf(
				"openvpn %q: stop previous runtime: %w",
				tag,
				err,
			)
		}
	}

	_ = os.Remove(
		filepath.Join(
			filepath.Dir(configPath),
			"management.sock",
		),
	)

	cmd := openVPNCommandContext(
		context.Background(),
		openvpnPath,
		"--config",
		configPath,
	)

	cmd.Stdout = logWriter{server: s}
	cmd.Stderr = logWriter{server: s}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf(
			"openvpn %q: start: %w",
			tag,
			err,
		)
	}

	runtime := &openVPNProcess{
		cmd:  cmd,
		done: make(chan struct{}),
	}

	go func() {
		err := cmd.Wait()

		runtime.setWaitError(err)
		close(runtime.done)

		s.mu.Lock()
		if s.openVPNRuntimes[tag] == runtime {
			delete(s.openVPNRuntimes, tag)
		}
		s.mu.Unlock()

		if err != nil {
			s.appendLog(
				"openvpn runtime stopped: " +
					tag + ": " + err.Error(),
			)
			return
		}

		s.appendLog(
			"openvpn runtime stopped: " + tag,
		)
	}()

	timer := time.NewTimer(openVPNStartupGrace)

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
			return fmt.Errorf(
				"openvpn %q: exited during startup: %w",
				tag,
				waitErr,
			)
		}

		return fmt.Errorf(
			"openvpn %q: exited during startup",
			tag,
		)

	case <-timer.C:
	}

	s.mu.Lock()

	select {
	case <-runtime.done:
		s.mu.Unlock()

		waitErr := runtime.waitError()
		if waitErr != nil {
			return fmt.Errorf(
				"openvpn %q: exited during startup: %w",
				tag,
				waitErr,
			)
		}

		return fmt.Errorf(
			"openvpn %q: exited during startup",
			tag,
		)

	default:
		s.openVPNRuntimes[tag] = runtime
	}

	s.mu.Unlock()

	go s.runOpenVPNSessionSeenLoop(
		tag,
		filepath.Dir(configPath),
		runtime.done,
	)

	s.appendLog(
		"openvpn runtime started: " + tag,
	)

	return nil
}

func stopOpenVPNProcess(
	runtime *openVPNProcess,
) error {
	if runtime == nil ||
		runtime.cmd == nil ||
		runtime.cmd.Process == nil {
		return nil
	}

	select {
	case <-runtime.done:
		return nil
	default:
	}

	terminateErr := openVPNTerminateProcess(
		runtime.cmd.Process,
	)

	if terminateErr != nil &&
		!errors.Is(
			terminateErr,
			os.ErrProcessDone,
		) {
		killErr := runtime.cmd.Process.Kill()

		if killErr != nil &&
			!errors.Is(
				killErr,
				os.ErrProcessDone,
			) {
			return fmt.Errorf(
				"terminate process: %v; kill process: %w",
				terminateErr,
				killErr,
			)
		}

		select {
		case <-runtime.done:
			return nil

		case <-time.After(openVPNKillGrace):
			return fmt.Errorf(
				"process did not exit after forced kill",
			)
		}
	}

	select {
	case <-runtime.done:
		return nil

	case <-time.After(openVPNShutdownGrace):
	}

	killErr := runtime.cmd.Process.Kill()
	if killErr != nil &&
		!errors.Is(
			killErr,
			os.ErrProcessDone,
		) {
		return fmt.Errorf(
			"force kill process: %w",
			killErr,
		)
	}

	select {
	case <-runtime.done:
		return nil

	case <-time.After(openVPNKillGrace):
		return fmt.Errorf(
			"process did not exit after forced kill",
		)
	}
}

func (s *Server) stopOpenVPNInbound(
	tag string,
) error {
	s.mu.Lock()
	runtime := s.openVPNRuntimes[tag]
	delete(s.openVPNRuntimes, tag)
	s.mu.Unlock()

	if runtime == nil {
		return nil
	}

	s.appendLog(
		"stopping openvpn runtime: " + tag,
	)

	return stopOpenVPNProcess(runtime)
}

func (s *Server) stopAllOpenVPNRuntimes() {
	s.mu.Lock()

	runtimes := make(
		map[string]*openVPNProcess,
		len(s.openVPNRuntimes),
	)

	for tag, runtime := range s.openVPNRuntimes {
		runtimes[tag] = runtime
	}

	s.openVPNRuntimes =
		make(map[string]*openVPNProcess)

	s.mu.Unlock()

	for tag, runtime := range runtimes {
		if runtime == nil {
			continue
		}

		s.appendLog(
			"stopping openvpn runtime: " + tag,
		)

		if err := stopOpenVPNProcess(runtime); err != nil {
			s.appendLog(
				"stop openvpn runtime failed: " +
					tag + ": " + err.Error(),
			)
		}
	}
}

func (s *Server) stopRemovedOpenVPNRuntimes(
	desired map[string]struct{},
) {
	s.mu.Lock()

	removed :=
		make(map[string]*openVPNProcess)

	for tag, runtime := range s.openVPNRuntimes {
		if _, ok := desired[tag]; ok {
			continue
		}

		removed[tag] = runtime
		delete(s.openVPNRuntimes, tag)
	}

	s.mu.Unlock()

	for tag, runtime := range removed {
		if runtime == nil {
			continue
		}

		s.appendLog(
			"stopping removed openvpn runtime: " +
				tag,
		)

		if err := stopOpenVPNProcess(runtime); err != nil {
			s.appendLog(
				"stop removed openvpn runtime failed: " +
					tag + ": " + err.Error(),
			)
		}
	}
}

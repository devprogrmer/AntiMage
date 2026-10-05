package nodeagent

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"strconv"
	"strings"
	"time"
)

func pppOfflineSignalSession(identity string) error {
	return pppOfflineSignalSessionWithWait(context.Background(), identity, false)
}

func pppOfflineTerminateSession(ctx context.Context, identity string) error {
	return pppOfflineSignalSessionWithWait(ctx, identity, true)
}

func pppOfflineSignalSessionWithWait(ctx context.Context, identity string, wait bool) error {
	parts := strings.Split(identity, ":")
	if len(parts) != 3 {
		return fmt.Errorf("invalid PPP process identity")
	}
	pid, err := strconv.Atoi(parts[1])
	if err != nil {
		return err
	}
	// Bind the signal to the process before revalidating boot/start identity.
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return fmt.Errorf("open PPP process handle: %w", err)
	}
	defer unix.Close(fd)
	current, err := offlineProcessIdentity(parts[1])
	if err != nil {
		return err
	}
	if current != identity {
		return fmt.Errorf("PPP process generation changed before disconnect")
	}
	if err := unix.PidfdSendSignal(fd, unix.SIGTERM, nil, 0); err != nil {
		return fmt.Errorf("send SIGTERM to PPP process: %w", err)
	}
	if !wait {
		return nil
	}
	ready := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	deadline := time.Now().Add(5 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for PPP process exit: %w", err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("PPP process did not exit after SIGTERM")
		}
		waitMS := int(remaining / time.Millisecond)
		if waitMS < 1 {
			waitMS = 1
		}
		n, err := unix.Poll(ready, waitMS)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("wait for PPP process exit: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("PPP process did not exit after SIGTERM")
		}
		if ready[0].Revents&unix.POLLIN != 0 {
			return nil
		}
		return fmt.Errorf("unexpected PPP process pidfd poll events: %#x", ready[0].Revents)
	}
}

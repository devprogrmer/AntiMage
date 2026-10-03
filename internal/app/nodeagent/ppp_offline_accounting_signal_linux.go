package nodeagent

import (
	"fmt"
	"golang.org/x/sys/unix"
	"strconv"
	"strings"
)

func pppOfflineSignalSession(identity string) error {
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
	return unix.PidfdSendSignal(fd, unix.SIGTERM, nil, 0)
}

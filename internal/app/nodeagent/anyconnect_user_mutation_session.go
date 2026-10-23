package nodeagent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const anyConnectOcctlTimeout = 10 * time.Second

var (
	anyConnectOcctlCommandContext = exec.CommandContext
	anyConnectOcctlLookPath       = exec.LookPath
)

// disconnectAnyConnectUser revokes live ocserv sessions belonging to a single
// username via "occtl disconnect user". The daemon and every other user's
// session are deliberately left untouched.
func (s *Server) disconnectAnyConnectUser(tag, username string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil
	}
	path, err := anyConnectOcctlLookPath("occtl")
	if err != nil {
		return fmt.Errorf("occtl executable not installed: %w", err)
	}
	socket := filepath.Join(s.cfg.DataDir, "anyconnect", openVPNRuntimeDirName(tag), "ocserv.sock")
	ctx, cancel := context.WithTimeout(context.Background(), anyConnectOcctlTimeout)
	defer cancel()
	cmd := anyConnectOcctlCommandContext(ctx, path, "-s", socket, "disconnect", "user", username)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("occtl disconnect user %q: %w: %s", username, err, strings.TrimSpace(output.String()))
	}
	return nil
}

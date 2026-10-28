package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// executeWireGuardPeerMutationPlan applies only peers named in the validated
// plan. If a command reports failure after partially applying, that attempted
// peer is included in the reverse rollback.
func executeWireGuardPeerMutationPlan(ctx context.Context, wgPath, interfaceName string, plan wireGuardPeerMutationPlan) error {
	attempted := 0
	for _, step := range plan.Forward {
		attempted++
		if err := applyWireGuardPeerMutationStep(ctx, wgPath, interfaceName, step, false); err != nil {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var rollbackErr error
			for _, rollback := range plan.RollbackForAttempted(attempted) {
				if err := applyWireGuardPeerMutationStep(rollbackCtx, wgPath, interfaceName, rollback, true); err != nil {
					rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback WireGuard peer %q: %w", rollback.Peer.PublicKey, err))
				}
			}
			return errors.Join(fmt.Errorf("apply WireGuard peer %q: %w", step.Peer.PublicKey, err), rollbackErr)
		}
	}
	return nil
}

func applyWireGuardPeerMutationStep(ctx context.Context, wgPath, interfaceName string, step wireGuardPeerMutationStep, restoring bool) error {
	key := strings.TrimSpace(step.Peer.PublicKey)
	if key == "" || strings.TrimSpace(interfaceName) == "" {
		return fmt.Errorf("WireGuard mutation step has empty interface or peer key")
	}
	if step.Operation == "remove" {
		return runWireGuardRuntimeRequired(ctx, wgPath, "set", interfaceName, "peer", key, "remove")
	}
	if step.Operation != "set" {
		return fmt.Errorf("unsupported WireGuard mutation step %q", step.Operation)
	}
	args := []string{"set", interfaceName, "peer", key}
	pskPath := "/dev/null"
	cleanup := func() {}
	if step.Peer.PresharedKey != "" {
		path, remove, err := wireGuardWritePresharedKeyFile(step.Peer.PresharedKey)
		if err != nil {
			return err
		}
		pskPath, cleanup = path, remove
	}
	defer cleanup()
	args = append(args, "preshared-key", pskPath, "allowed-ips", strings.Join(step.Peer.AllowedIPs, ","))
	if restoring {
		if step.Peer.Endpoint != "" {
			args = append(args, "endpoint", step.Peer.Endpoint)
		}
		args = append(args, "persistent-keepalive", strconv.Itoa(int(step.Peer.PersistentKeepalive)))
	}
	return runWireGuardRuntimeRequired(ctx, wgPath, args...)
}

func wireGuardWritePresharedKeyFile(key string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "antimage-wg-mutation-")
	if err != nil {
		return "", func() {}, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, err
	}
	path := dir + string(os.PathSeparator) + "preshared-key"
	if err := os.WriteFile(path, []byte(key+"\n"), 0600); err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, err
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

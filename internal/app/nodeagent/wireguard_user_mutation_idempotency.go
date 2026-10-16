package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

var wireGuardMutationReceiptWrite = atomicWriteFile

// applyWireGuardUserMutationOperation binds a stable operation ID to the
// complete mutation envelope. The durable receipt is written only after the
// kernel, runtime config, usage policy, and committed native policy agree.
// A retry after a lost ACK therefore returns success without issuing another
// kernel mutation; reusing an ID for different content is rejected.
func (s *Server) applyWireGuardUserMutationOperation(ctx context.Context, operationID, inboundTag string, user *nodev1.InboundUser, operation string) error {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return fmt.Errorf("WireGuard mutation operation id is required")
	}
	unlock, err := s.lockOpenVPNMutation(ctx, operationID)
	if err != nil {
		return err
	}
	defer unlock()

	digest, err := wireGuardMutationDigest(inboundTag, operation, user)
	if err != nil {
		return err
	}
	path := s.wireGuardMutationResultPath(operationID)
	raw, err := os.ReadFile(path)
	if err == nil {
		var receipt openVPNMutationResult
		if json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || !receipt.Done || len(receipt.Digest) != sha256.Size*2 {
			return fmt.Errorf("WireGuard operation receipt is corrupt")
		}
		if receipt.Digest != digest {
			return fmt.Errorf("WireGuard operation id was reused with a different mutation")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("read WireGuard operation receipt: %w", err)
	}
	if err := s.applyWireGuardUserMutation(ctx, inboundTag, user, operation); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create WireGuard operation receipt directory: %w", err)
	}
	receipt, err := json.Marshal(openVPNMutationResult{Version: 1, Digest: digest, Done: true})
	if err != nil {
		return err
	}
	if err := wireGuardMutationReceiptWrite(path, receipt, 0600); err != nil {
		return fmt.Errorf("persist WireGuard operation receipt: %w", err)
	}
	return nil
}

func wireGuardMutationDigest(inboundTag, operation string, user *nodev1.InboundUser) (string, error) {
	raw, err := json.Marshal(struct {
		InboundTag string              `json:"inbound_tag"`
		Operation  string              `json:"operation"`
		User       *nodev1.InboundUser `json:"user"`
	}{strings.TrimSpace(inboundTag), strings.ToLower(strings.TrimSpace(operation)), user})
	if err != nil {
		return "", fmt.Errorf("encode WireGuard mutation identity: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) wireGuardMutationResultPath(operationID string) string {
	id := sha256.Sum256([]byte(operationID))
	return filepath.Join(s.cfg.DataDir, "wireguard", "mutation-results", hex.EncodeToString(id[:])+".json")
}

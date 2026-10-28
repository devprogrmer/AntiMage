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

var amneziaWGMutationReceiptWrite = atomicWriteFile

type amneziaWGMutationReceipt struct {
	Version int    `json:"version"`
	Digest  string `json:"digest"`
	Done    bool   `json:"done"`
}

func (s *Server) applyAmneziaWGUserMutationOperation(ctx context.Context, operationID, inboundTag string, user *nodev1.InboundUser, operation string) error {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return fmt.Errorf("AmneziaWG mutation operation id is required")
	}
	unlock, err := s.lockOpenVPNMutation(ctx, operationID)
	if err != nil {
		return err
	}
	defer unlock()
	digest, err := amneziaWGMutationDigest(inboundTag, operation, user)
	if err != nil {
		return err
	}
	path := s.amneziaWGMutationResultPath(operationID)
	raw, err := os.ReadFile(path)
	if err == nil {
		var receipt amneziaWGMutationReceipt
		if json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || !receipt.Done || len(receipt.Digest) != sha256.Size*2 {
			return fmt.Errorf("AmneziaWG operation receipt is corrupt")
		}
		if receipt.Digest != digest {
			return fmt.Errorf("AmneziaWG operation id was reused with different mutation content")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("read AmneziaWG operation receipt: %w", err)
	}
	if err := s.applyAmneziaWGUserMutation(ctx, inboundTag, user, operation); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create AmneziaWG operation receipt directory: %w", err)
	}
	receipt, err := json.Marshal(amneziaWGMutationReceipt{Version: 1, Digest: digest, Done: true})
	if err != nil {
		return err
	}
	if err := amneziaWGMutationReceiptWrite(path, receipt, 0600); err != nil {
		return fmt.Errorf("persist AmneziaWG operation receipt: %w", err)
	}
	return nil
}

func amneziaWGMutationDigest(inboundTag, operation string, user *nodev1.InboundUser) (string, error) {
	raw, err := json.Marshal(struct {
		InboundTag string              `json:"inbound_tag"`
		Operation  string              `json:"operation"`
		User       *nodev1.InboundUser `json:"user"`
	}{strings.TrimSpace(inboundTag), strings.ToLower(strings.TrimSpace(operation)), user})
	if err != nil {
		return "", fmt.Errorf("encode AmneziaWG mutation identity: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Server) amneziaWGMutationResultPath(operationID string) string {
	id := sha256.Sum256([]byte(operationID))
	return filepath.Join(s.cfg.DataDir, "amneziawg", "mutation-results", hex.EncodeToString(id[:])+".json")
}

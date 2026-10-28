package nodecontroller

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

const (
	grpcWireGuardUserIDField = "wireguard_user_id"
	grpcWireGuardPeersField  = "wireguard_peers_json"
	grpcWireGuardOpField     = "wireguard_operation"
)

func grpcWireGuardMutationUser(userID int64, operation string, peers []WGRuntimePeer) (*nodev1.InboundUser, error) {
	if userID <= 0 {
		return nil, fmt.Errorf("WireGuard user id must be positive")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "add" && operation != "update" && operation != "remove" {
		return nil, fmt.Errorf("unsupported WireGuard user operation %q", operation)
	}
	for _, peer := range peers {
		if peer.UserID != userID || strings.TrimSpace(peer.PublicKey) == "" || strings.TrimSpace(peer.Address) == "" {
			return nil, fmt.Errorf("WireGuard desired peer identity does not match user %d", userID)
		}
	}
	encoded, err := json.Marshal(peers)
	if err != nil {
		return nil, err
	}
	fields := map[string]string{
		grpcWireGuardUserIDField: strconv.FormatInt(userID, 10),
		grpcWireGuardPeersField:  string(encoded),
		grpcWireGuardOpField:     operation,
	}
	return &nodev1.InboundUser{Protocol: "wireguard", Fields: fields}, nil
}

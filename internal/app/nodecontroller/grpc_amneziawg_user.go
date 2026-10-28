package nodecontroller

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

const (
	grpcAmneziaWGUserIDField = "amneziawg_user_id"
	grpcAmneziaWGPeersField  = "amneziawg_peers_json"
	grpcAmneziaWGOpField     = "amneziawg_operation"
)

func grpcAmneziaWGMutationUser(userID int64, operation string, peers []AWGRuntimePeer) (*nodev1.InboundUser, error) {
	if userID <= 0 {
		return nil, fmt.Errorf("AmneziaWG user id must be positive")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "add" && operation != "update" && operation != "remove" {
		return nil, fmt.Errorf("unsupported AmneziaWG user operation %q", operation)
	}
	for _, peer := range peers {
		if peer.UserID != userID || strings.TrimSpace(peer.PublicKey) == "" || strings.TrimSpace(peer.Address) == "" {
			return nil, fmt.Errorf("AmneziaWG desired peer identity does not match user %d", userID)
		}
	}
	encoded, err := json.Marshal(peers)
	if err != nil {
		return nil, err
	}
	return &nodev1.InboundUser{Protocol: "amneziawg", Fields: map[string]string{
		grpcAmneziaWGUserIDField: strconv.FormatInt(userID, 10),
		grpcAmneziaWGPeersField:  string(encoded),
		grpcAmneziaWGOpField:     operation,
	}}, nil
}

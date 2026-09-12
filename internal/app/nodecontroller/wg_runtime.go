package nodecontroller

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	userapp "github.com/antimage/antimage/internal/app/user"
	"github.com/antimage/antimage/internal/app/xrayconfig"
)

type WGRuntime struct {
	GeneratedAt     string                 `json:"generated_at"`
	Target          string                 `json:"target,omitempty"`
	SessionCallback RuntimeSessionCallback `json:"session_callback,omitempty"`
	Inbounds        []WGRuntimeInbound     `json:"inbounds"`
}

type WGRuntimeInbound struct {
	Tag        string          `json:"tag"`
	TunnelTag  string          `json:"tunnel_tag"`
	ListenPort int             `json:"listen_port"`
	TunnelPort int             `json:"tunnel_port"`
	Settings   map[string]any  `json:"settings"`
	Peers      []WGRuntimePeer `json:"peers"`
}

type WGRuntimePeer struct {
	UserID                int64  `json:"user_id"`
	Username              string `json:"username"`
	PublicKey             string `json:"public_key"`
	PresharedKey          string `json:"preshared_key,omitempty"`
	Address               string `json:"address"`
	Status                string `json:"status"`
	UsedTraffic           int64  `json:"used_traffic"`
	ReflectedUsageBatchID string `json:"reflected_usage_batch_id,omitempty"`
	DataLimit             *int64 `json:"data_limit,omitempty"`
	Expire                *int64 `json:"expire,omitempty"`
	DeviceLimit           int64  `json:"device_limit,omitempty"`
}

func (r Repository) WGRuntime(ctx context.Context, nodeID int64) (WGRuntime, error) {
	configRepo := xrayconfig.NewRepository(r.db, r.dialect, xrayconfig.Options{})
	inbounds, err := configRepo.FullInbounds(ctx)
	if err != nil {
		return WGRuntime{}, err
	}
	return r.wgRuntime(ctx, nodeID, inbounds)
}

func (r Repository) wgRuntime(ctx context.Context, nodeID int64, inbounds []map[string]any) (WGRuntime, error) {
	target := xrayconfig.NodeTargetID(nodeID)
	usedPorts := map[int]struct{}{}
	for _, inbound := range inbounds {
		if port := OVIntValue(inbound["port"]); port > 0 {
			usedPorts[port] = struct{}{}
		}
	}
	runtimeConfig := WGRuntime{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Target:      target,
		Inbounds:    []WGRuntimeInbound{},
	}
	if callback, err := r.RuntimeSessionCallback(ctx, NodeRow{ID: nodeID}); err != nil {
		return WGRuntime{}, err
	} else {
		runtimeConfig.SessionCallback = callback
	}
	for _, inbound := range inbounds {
		if strings.ToLower(OVStringValue(inbound["protocol"])) != xrayconfig.WGProtocol {
			continue
		}
		if !OVInboundMatchesTarget(inbound, target) {
			continue
		}
		tag := OVStringValue(inbound["tag"])
		if tag == "" {
			continue
		}
		settings := WGRuntimeSettings(inbound)
		serviceIDs, err := r.OVServiceIDsForInbound(ctx, tag)
		if err != nil {
			return WGRuntime{}, err
		}
		if len(serviceIDs) == 0 {
			continue
		}
		peers, err := r.WGUsersForServices(ctx, tag, serviceIDs, OVStringValue(settings["address_pool"]), OVStringValue(settings["server_address"]))
		if err != nil {
			return WGRuntime{}, err
		}
		if len(peers) == 0 {
			continue
		}
		if err := r.attachWGUsageReflections(ctx, nodeID, peers); err != nil {
			return WGRuntime{}, fmt.Errorf(
				"WireGuard usage reflections: %w",
				err,
			)
		}

		tunnelPort := xrayconfig.RuntimeTunnelPortForInbound(inbound, usedPorts)
		if tunnelPort > 0 {
			usedPorts[tunnelPort] = struct{}{}
		}
		runtimeConfig.Inbounds = append(runtimeConfig.Inbounds, WGRuntimeInbound{
			Tag:        tag,
			TunnelTag:  xrayconfig.RuntimeTunnelTagForProtocol(xrayconfig.WGProtocol, tag),
			ListenPort: OVIntValue(inbound["port"]),
			TunnelPort: tunnelPort,
			Settings:   settings,
			Peers:      peers,
		})
	}
	return runtimeConfig, nil
}

func (r Repository) attachWGUsageReflections(
	ctx context.Context,
	nodeID int64,
	peers []WGRuntimePeer,
) error {
	if nodeID <= 0 || len(peers) == 0 {
		return nil
	}

	userIDs := make([]int64, 0, len(peers))
	seen := make(map[int64]struct{}, len(peers))

	for _, peer := range peers {
		if peer.UserID <= 0 {
			continue
		}
		if _, ok := seen[peer.UserID]; ok {
			continue
		}

		seen[peer.UserID] = struct{}{}
		userIDs = append(userIDs, peer.UserID)
	}

	if len(userIDs) == 0 {
		return nil
	}

	userPlaceholders := make([]string, len(userIDs))
	args := make([]any, 0, 1+len(userIDs))
	args = append(args, nodeID)

	for i, userID := range userIDs {
		userPlaceholders[i] = "?"
		args = append(args, userID)
	}

	query := `
SELECT user_id, batch_id
FROM node_wireguard_usage_reflection
WHERE node_id = ?
  AND user_id IN (` + strings.Join(userPlaceholders, ",") + `)`

	rows, err := r.db.QueryContext(
		ctx,
		query,
		args...,
	)
	if err != nil {
		return fmt.Errorf(
			"load wireguard usage reflections for node %d: %w",
			nodeID,
			err,
		)
	}
	defer rows.Close()

	reflected := make(map[int64]string, len(userIDs))

	for rows.Next() {
		var userID int64
		var batchID string

		if err := rows.Scan(&userID, &batchID); err != nil {
			return err
		}

		reflected[userID] = strings.TrimSpace(batchID)
	}

	if err := rows.Err(); err != nil {
		return err
	}

	for i := range peers {
		peers[i].ReflectedUsageBatchID = reflected[peers[i].UserID]
	}

	return nil
}

func (r Repository) WGUsersForServices(ctx context.Context, inboundTag string, serviceIDs []int64, pool string, serverAddress string) ([]WGRuntimePeer, error) {
	if len(serviceIDs) == 0 {
		return []WGRuntimePeer{}, nil
	}
	placeholders := make([]string, 0, len(serviceIDs))
	args := make([]any, 0, len(serviceIDs))
	for _, id := range serviceIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, username, COALESCE(credential_key, ''), status, COALESCE(used_traffic, 0), data_limit, expire, COALESCE(ip_limit, 0)
FROM users
WHERE status IN ('active', 'on_hold')
  AND service_id IN (`+strings.Join(placeholders, ",")+`)
ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	peers := []WGRuntimePeer{}
	for rows.Next() {
		var item WGRuntimePeer
		var credentialKey string
		var dataLimit, expire sql.NullInt64
		if err := rows.Scan(&item.UserID, &item.Username, &credentialKey, &item.Status, &item.UsedTraffic, &dataLimit, &expire, &item.DeviceLimit); err != nil {
			return nil, err
		}
		pair, err := userapp.WGKeyPairFromCredentialKey(credentialKey)
		if err != nil {
			return nil, fmt.Errorf("user %d WireGuard credential: %w", item.UserID, err)
		}
		item.PublicKey = pair.PublicKey
		item.DataLimit = nullableOVInt64(dataLimit)
		item.Expire = nullableOVInt64(expire)
		peers = append(peers, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	userIDs := make([]int64, len(peers))
	for i := range peers {
		userIDs[i] = peers[i].UserID
	}
	addresses, err := userapp.NewRepository(r.db, r.dialect).WGIPv4Addresses(ctx, inboundTag, userIDs, pool, serverAddress)
	if err != nil {
		return nil, err
	}
	for i := range peers {
		peers[i].Address = addresses[peers[i].UserID]
	}
	return peers, nil
}

func WGRuntimeSettings(inbound map[string]any) map[string]any {
	settings := OVMapValue(inbound["settings"])
	out := make(map[string]any, len(settings)+6)
	for key, value := range settings {
		out[key] = value
	}
	pool := strings.TrimSpace(firstNonEmptyOVString(out["address_pool"], out["ipv4_pool_cidr"], out["ipv4PoolCidr"]))
	if pool == "" {
		pool = "10.69.0.0/16"
	}
	out["address_pool"] = pool
	out["ipv4_pool_cidr"] = pool
	if _, ok := out["tproxy_enabled"]; !ok {
		out["tproxy_enabled"] = true
	}
	if _, ok := out["nat_enabled"]; !ok {
		out["nat_enabled"] = !OVBoolValue(out["tproxy_enabled"])
	} else if !OVBoolValue(out["tproxy_enabled"]) {
		out["nat_enabled"] = true
	}
	if _, ok := out["accounting_enabled"]; !ok {
		out["accounting_enabled"] = true
	}
	out["private_key"] = strings.TrimSpace(OVStringValue(out["private_key"]))
	out["server_address"] = strings.TrimSpace(OVStringValue(out["server_address"]))
	for _, key := range []string{"clients", "ipv4PoolCidr"} {
		delete(out, key)
	}
	return out
}

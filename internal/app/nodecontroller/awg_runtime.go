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

type AWGRuntime struct {
	GeneratedAt     string                 `json:"generated_at"`
	Target          string                 `json:"target,omitempty"`
	SessionCallback RuntimeSessionCallback `json:"session_callback,omitempty"`
	Inbounds        []AWGRuntimeInbound    `json:"inbounds"`
}

type AWGRuntimeInbound struct {
	Tag        string           `json:"tag"`
	TunnelTag  string           `json:"tunnel_tag"`
	ListenPort int              `json:"listen_port"`
	TunnelPort int              `json:"tunnel_port"`
	Settings   map[string]any   `json:"settings"`
	Peers      []AWGRuntimePeer `json:"peers"`
}

type AWGRuntimePeer struct {
	UserID             int64  `json:"user_id"`
	Username           string `json:"username"`
	DeviceIndex        int    `json:"device_index"`
	PublicKey          string `json:"public_key"`
	PresharedKey       string `json:"preshared_key,omitempty"`
	Address            string `json:"address"`
	Status             string `json:"status"`
	UsedTraffic        int64  `json:"used_traffic"`
	DataLimit          *int64 `json:"data_limit,omitempty"`
	Expire             *int64 `json:"expire,omitempty"`
	DeviceLimit        int64  `json:"device_limit,omitempty"`
	UploadSpeedLimit   int64  `json:"upload_speed_limit"`
	DownloadSpeedLimit int64  `json:"download_speed_limit"`
}

func (r Repository) AWGRuntime(ctx context.Context, nodeID int64) (AWGRuntime, error) {
	configRepo := xrayconfig.NewRepository(r.db, r.dialect, xrayconfig.Options{})
	inbounds, err := configRepo.FullInbounds(ctx)
	if err != nil {
		return AWGRuntime{}, err
	}
	return r.awgRuntime(ctx, nodeID, inbounds)
}

func (r Repository) awgRuntime(ctx context.Context, nodeID int64, inbounds []map[string]any) (AWGRuntime, error) {
	target := xrayconfig.NodeTargetID(nodeID)
	usedPorts := map[int]struct{}{}
	for _, inbound := range inbounds {
		if port := OVIntValue(inbound["port"]); port > 0 {
			usedPorts[port] = struct{}{}
		}
	}
	result := AWGRuntime{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Target: target, Inbounds: []AWGRuntimeInbound{}}
	if callback, err := r.RuntimeSessionCallback(ctx, NodeRow{ID: nodeID}); err != nil {
		return AWGRuntime{}, err
	} else {
		result.SessionCallback = callback
	}
	for _, inbound := range inbounds {
		if strings.ToLower(OVStringValue(inbound["protocol"])) != xrayconfig.AWGProtocol || !OVInboundMatchesTarget(inbound, target) {
			continue
		}
		tag := strings.TrimSpace(OVStringValue(inbound["tag"]))
		if tag == "" {
			continue
		}
		settings := AWGRuntimeSettings(inbound)
		serviceIDs, err := r.OVServiceIDsForInbound(ctx, tag)
		if err != nil {
			return AWGRuntime{}, err
		}
		peers, err := r.AWGUsersForServices(ctx, tag, serviceIDs, OVStringValue(settings["ipv4_pool_cidr"]), OVStringValue(settings["server_address"]), OVBoolValue(settings["psk_enabled"]))
		if err != nil {
			return AWGRuntime{}, err
		}
		tunnelPort := xrayconfig.RuntimeTunnelPortForInbound(inbound, usedPorts)
		if tunnelPort > 0 {
			usedPorts[tunnelPort] = struct{}{}
		}
		result.Inbounds = append(result.Inbounds, AWGRuntimeInbound{
			Tag: tag, TunnelTag: xrayconfig.RuntimeTunnelTagForProtocol(xrayconfig.AWGProtocol, tag),
			ListenPort: OVIntValue(inbound["port"]), TunnelPort: tunnelPort,
			Settings: settings, Peers: peers,
		})
	}
	return result, nil
}

func (r Repository) AWGUsersForServices(ctx context.Context, inboundTag string, serviceIDs []int64, pool, serverAddress string, pskEnabled bool) ([]AWGRuntimePeer, error) {
	if len(serviceIDs) == 0 {
		return []AWGRuntimePeer{}, nil
	}
	marks := make([]string, len(serviceIDs))
	args := make([]any, len(serviceIDs))
	for i, id := range serviceIDs {
		marks[i], args[i] = "?", id
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, username, status, COALESCE(used_traffic, 0), data_limit, expire, COALESCE(device_limit, 0), COALESCE(upload_speed_limit, 0), COALESCE(download_speed_limit, 0)
FROM users
WHERE status IN ('active', 'on_hold') AND service_id IN (`+strings.Join(marks, ",")+`)
ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	type runtimeUser struct {
		id, deviceLimit, uploadSpeedLimit, downloadSpeedLimit int64
		username, status                                      string
		used                                                  int64
		dataLimit, expire                                     sql.NullInt64
	}
	users := []runtimeUser{}
	for rows.Next() {
		var item runtimeUser
		if err := rows.Scan(&item.id, &item.username, &item.status, &item.used, &item.dataLimit, &item.expire, &item.deviceLimit, &item.uploadSpeedLimit, &item.downloadSpeedLimit); err != nil {
			rows.Close()
			return nil, err
		}
		users = append(users, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	peers := []AWGRuntimePeer{}
	deviceRepo := userapp.NewRepository(r.db, r.dialect)
	for _, item := range users {
		limit := int(item.deviceLimit)
		if limit <= 0 {
			limit = 1
		}
		devices, err := deviceRepo.ReconcileAmneziaWGDevices(ctx, inboundTag, item.id, limit, pool, serverAddress, pskEnabled)
		if err != nil {
			return nil, fmt.Errorf("user %d AmneziaWG devices: %w", item.id, err)
		}
		for _, device := range devices {
			peers = append(peers, AWGRuntimePeer{
				UserID: item.id, Username: item.username, DeviceIndex: device.DeviceIndex,
				PublicKey: device.PublicKey, PresharedKey: device.PresharedKey, Address: device.Address,
				Status: item.status, UsedTraffic: item.used, DataLimit: nullableOVInt64(item.dataLimit), Expire: nullableOVInt64(item.expire), DeviceLimit: item.deviceLimit, UploadSpeedLimit: item.uploadSpeedLimit, DownloadSpeedLimit: item.downloadSpeedLimit,
			})
		}
	}
	return peers, nil
}

func AWGRuntimeSettings(inbound map[string]any) map[string]any {
	settings := OVMapValue(inbound["settings"])
	out := make(map[string]any, len(settings))
	for key, value := range settings {
		out[key] = value
	}
	delete(out, "clients")
	return out
}

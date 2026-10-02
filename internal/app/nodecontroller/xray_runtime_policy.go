package nodecontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type XrayRuntimePolicy struct {
	UserID                int64   `json:"user_id"`
	Email                 string  `json:"email"`
	InboundTag            string  `json:"inbound_tag"`
	Protocol              string  `json:"protocol"`
	Status                string  `json:"status"`
	UsedTraffic           int64   `json:"used_traffic"`
	DataLimit             int64   `json:"data_limit"`
	Expire                int64   `json:"expire"`
	UsageCoefficient      float64 `json:"usage_coefficient"`
	InboundCoefficient    float64 `json:"inbound_coefficient"`
	ReflectedUsageBatchID string  `json:"reflected_usage_batch_id,omitempty"`
	UploadSpeedLimit      int64   `json:"upload_speed_limit"`
	DownloadSpeedLimit    int64   `json:"download_speed_limit"`
	DeviceLimit           int64   `json:"device_limit"`
	IPLimit               int64   `json:"ip_limit"`
}

func (r Repository) xrayRuntimePolicies(ctx context.Context, node NodeRow, configJSON string, inbounds []map[string]any) ([]XrayRuntimePolicy, error) {
	var cfg struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
			Settings struct {
				Clients []struct {
					Email string `json:"email"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, err
	}
	policies := []XrayRuntimePolicy{}
	ids := []int64{}
	seen := map[int64]bool{}
	coefficients := runtimeInboundUsageCoefficients(inbounds)
	for _, inbound := range cfg.Inbounds {
		for _, client := range inbound.Settings.Clients {
			prefix, _, found := strings.Cut(client.Email, ".")
			id, err := strconv.ParseInt(prefix, 10, 64)
			if !found || err != nil || id <= 0 {
				continue
			}
			policies = append(policies, XrayRuntimePolicy{UserID: id, Email: client.Email, InboundTag: inbound.Tag, Protocol: inbound.Protocol, UsageCoefficient: normalizeUsageFactor(node.UsageCoefficient), InboundCoefficient: normalizeUsageFactor(coefficients[inbound.Tag])})
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	byID := map[int64]XrayRuntimePolicy{}
	for start := 0; start < len(ids); start += 500 {
		end := start + 500
		if end > len(ids) {
			end = len(ids)
		}
		marks := make([]string, end-start)
		args := []any{node.ID}
		for i, id := range ids[start:end] {
			marks[i] = "?"
			args = append(args, id)
		}
		rows, err := r.db.QueryContext(ctx, `SELECT u.id,u.status,COALESCE(u.used_traffic,0),COALESCE(u.data_limit,0),COALESCE(u.expire,0),COALESCE(u.upload_speed_limit,0),COALESCE(u.download_speed_limit,0),COALESCE(u.device_limit,0),COALESCE(u.ip_limit,0),COALESCE(ref.batch_id,'')
FROM users u LEFT JOIN node_wireguard_usage_reflection ref ON ref.node_id=? AND ref.user_id=u.id WHERE u.id IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("load xray offline policy: %w", err)
		}
		for rows.Next() {
			var p XrayRuntimePolicy
			if err := rows.Scan(&p.UserID, &p.Status, &p.UsedTraffic, &p.DataLimit, &p.Expire, &p.UploadSpeedLimit, &p.DownloadSpeedLimit, &p.DeviceLimit, &p.IPLimit, &p.ReflectedUsageBatchID); err != nil {
				rows.Close()
				return nil, err
			}
			byID[p.UserID] = p
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	for i := range policies {
		loaded, ok := byID[policies[i].UserID]
		if !ok {
			return nil, fmt.Errorf("xray policy user %d disappeared", policies[i].UserID)
		}
		identity := policies[i]
		loaded.Email, loaded.InboundTag, loaded.Protocol = identity.Email, identity.InboundTag, identity.Protocol
		loaded.UsageCoefficient, loaded.InboundCoefficient = identity.UsageCoefficient, identity.InboundCoefficient
		policies[i] = loaded
	}
	return policies, nil
}

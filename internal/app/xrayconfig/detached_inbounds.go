package xrayconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/go-sql-driver/mysql"
)

type inboundConfigQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func detachedInboundTableAbsent(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1146
	}
	return strings.Contains(err.Error(), "no such table: detached_inbounds")
}

func detachedInboundConfig(ctx context.Context, db inboundConfigQuery, tag string) (json.RawMessage, error) {
	var raw []byte
	err := db.QueryRowContext(ctx, `SELECT config FROM detached_inbounds WHERE tag = ?`, tag).Scan(&raw)
	if err == nil {
		return raw, nil
	}
	if errors.Is(err, sql.ErrNoRows) || detachedInboundTableAbsent(err) {
		return nil, nil
	}
	return nil, err
}

// ManagementInbounds includes restored definitions that have no runtime target.
func (r Repository) ManagementInbounds(ctx context.Context) ([]map[string]any, error) {
	inbounds, err := r.FullInbounds(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT d.tag, d.config, COALESCE(i.uplink, 0), COALESCE(i.downlink, 0),
			COALESCE(i.usage_coefficient, 1), COALESCE(i.upload_speed_limit, 0),
			COALESCE(i.download_speed_limit, 0)
		FROM detached_inbounds d LEFT JOIN inbounds i ON i.tag = d.tag`)
	if err != nil {
		if detachedInboundTableAbsent(err) {
			return inbounds, nil
		}
		return nil, err
	}
	defer rows.Close()

	active := make(map[string]bool, len(inbounds))
	for _, inbound := range inbounds {
		active[stringValue(inbound["tag"])] = true
	}
	for rows.Next() {
		var tag string
		var raw []byte
		var metadata inboundMetadata
		if err := rows.Scan(&tag, &raw, &metadata.Uplink, &metadata.Downlink, &metadata.UsageCoefficient,
			&metadata.UploadSpeedLimit, &metadata.DownloadSpeedLimit); err != nil {
			return nil, err
		}
		if active[tag] {
			continue
		}
		var inbound map[string]any
		if err := json.Unmarshal(raw, &inbound); err != nil {
			return nil, fmt.Errorf("decode detached inbound %q: %w", tag, err)
		}
		if stringValue(inbound["tag"]) != tag || !IsManageableInbound(inbound) {
			return nil, fmt.Errorf("invalid detached inbound %q", tag)
		}
		inbound = sanitizeInbound(inbound, nil, nil)
		inbound["detached"] = true
		inbound["uplink"] = metadata.Uplink
		inbound["downlink"] = metadata.Downlink
		inbound["usage_coefficient"] = normalizedInboundUsageCoefficient(metadata.UsageCoefficient)
		inbound["upload_speed_limit"] = normalizeInboundSpeedLimit(metadata.UploadSpeedLimit)
		inbound["download_speed_limit"] = normalizeInboundSpeedLimit(metadata.DownloadSpeedLimit)
		inbounds = append(inbounds, inbound)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(inbounds, func(i, j int) bool {
		return stringValue(inbounds[i]["tag"]) < stringValue(inbounds[j]["tag"])
	})
	return inbounds, nil
}

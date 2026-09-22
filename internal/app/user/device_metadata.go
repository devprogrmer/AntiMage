package user

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

type subscriptionDeviceMetadata struct {
	DeviceType     string
	Manufacturer   string
	Model          string
	OSName         string
	OSVersion      string
	ClientName     string
	ClientVersion  string
	Platform       string
	MetadataSource string
}

var (
	androidVersionPattern = regexp.MustCompile(`(?i)\bAndroid[ /]([0-9][0-9._]*)`)
	androidModelPattern   = regexp.MustCompile(`(?i)Android [^;()]+;\s*([^;()]+)`)
	iosVersionPattern     = regexp.MustCompile(`(?i)(?:CPU (?:iPhone )?OS|iPhone OS) ([0-9_]+)`)
)

func cleanDeviceHint(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	if len(value) > 128 {
		value = value[:128]
	}
	return strings.TrimSpace(value)
}

func normalizeDeviceOSName(value string) string {
	value = cleanDeviceHint(value)
	switch strings.ToLower(value) {
	case "android":
		return "Android"
	case "ios", "iphone os":
		return "iOS"
	case "windows":
		return "Windows"
	case "macos", "mac os x":
		return "macOS"
	case "linux":
		return "Linux"
	default:
		return value
	}
}

func androidModelFromUserAgent(userAgent string) string {
	match := androidModelPattern.FindStringSubmatch(userAgent)
	if len(match) < 2 {
		return ""
	}
	model := strings.TrimSpace(match[1])
	if index := strings.Index(strings.ToLower(model), " build/"); index >= 0 {
		model = strings.TrimSpace(model[:index])
	}
	model = strings.TrimSuffix(model, " Build")
	if strings.EqualFold(model, "wv") {
		return ""
	}
	return cleanDeviceHint(model)
}

func subscriptionMetadataFromRequest(req SubscriptionRenderRequest, protocol string) subscriptionDeviceMetadata {
	ua := strings.TrimSpace(req.UserAgent)
	meta := subscriptionDeviceMetadata{
		DeviceType:     cleanDeviceHint(req.DeviceType),
		Manufacturer:   cleanDeviceHint(req.DeviceManufacturer),
		Model:          cleanDeviceHint(req.DeviceModel),
		OSName:         normalizeDeviceOSName(req.DevicePlatform),
		OSVersion:      cleanDeviceHint(req.DevicePlatformVersion),
		ClientName:     cleanDeviceHint(req.DeviceClientName),
		ClientVersion:  cleanDeviceHint(req.DeviceClientVersion),
		MetadataSource: "subscription",
	}

	lowerUA := strings.ToLower(ua)

	if meta.OSName == "" {
		switch {
		case strings.Contains(lowerUA, "android"):
			meta.OSName = "Android"
		case strings.Contains(lowerUA, "iphone"), strings.Contains(lowerUA, "ipad"):
			meta.OSName = "iOS"
		case strings.Contains(lowerUA, "windows"):
			meta.OSName = "Windows"
		case strings.Contains(lowerUA, "mac os x"), strings.Contains(lowerUA, "macintosh"):
			meta.OSName = "macOS"
		case strings.Contains(lowerUA, "linux"):
			meta.OSName = "Linux"
		}
	}

	if meta.OSVersion == "" {
		switch meta.OSName {
		case "Android":
			if match := androidVersionPattern.FindStringSubmatch(ua); len(match) > 1 {
				meta.OSVersion = strings.ReplaceAll(match[1], "_", ".")
			}
		case "iOS":
			if match := iosVersionPattern.FindStringSubmatch(ua); len(match) > 1 {
				meta.OSVersion = strings.ReplaceAll(match[1], "_", ".")
			}
		}
	}

	if meta.Model == "" && meta.OSName == "Android" {
		meta.Model = androidModelFromUserAgent(ua)
	}
	if meta.Model == "" && meta.OSName == "iOS" {
		// Stock browser/WireGuard traffic does not reveal the hardware SKU.
		meta.Model = "iPhone"
		if strings.Contains(lowerUA, "ipad") {
			meta.Model = "iPad"
		}
	}

	if meta.DeviceType == "" {
		switch meta.OSName {
		case "Android", "iOS":
			meta.DeviceType = "Mobile"
		case "Windows", "macOS", "Linux":
			meta.DeviceType = "Desktop"
		default:
			meta.DeviceType = "Unknown"
		}
	}

	if meta.ClientName == "" {
		switch strings.ToLower(strings.TrimSpace(protocol)) {
		case "wg", "wireguard":
			meta.ClientName = "WireGuard"
		case "awg", "amneziawg":
			meta.ClientName = "AmneziaWG"
		default:
			meta.ClientName = "Unknown"
		}
	}

	platformParts := []string{}
	if meta.OSName != "" {
		platformParts = append(platformParts, meta.OSName)
	}
	if meta.OSVersion != "" {
		platformParts = append(platformParts, meta.OSVersion)
	}
	meta.Platform = strings.Join(platformParts, " ")
	if meta.Platform == "" {
		meta.Platform = "Unknown"
	}

	return meta
}

func stableWGDeviceID(publicKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(publicKey)))
	return "wg-" + hex.EncodeToString(sum[:8])
}

func (s Service) recordSubscriptionDeviceMetadata(
	ctx context.Context,
	userID int64,
	protocol string,
	inboundTag string,
	publicKey string,
	req SubscriptionRenderRequest,
) error {
	if userID <= 0 || strings.TrimSpace(publicKey) == "" {
		return nil
	}

	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "wireguard":
		protocol = "wg"
	case "awg":
		protocol = "amneziawg"
	default:
		protocol = strings.ToLower(strings.TrimSpace(protocol))
	}

	if protocol != "wg" && protocol != "amneziawg" {
		return nil
	}

	meta := subscriptionMetadataFromRequest(req, protocol)
	deviceID := stableWGDeviceID(publicKey)
	inboundTag = strings.TrimSpace(inboundTag)
	now := time.Now().UTC()

	args := []any{
		userID,
		protocol,
		inboundTag,
		deviceID,
		meta.DeviceType,
		meta.Manufacturer,
		meta.Model,
		meta.OSName,
		meta.OSVersion,
		meta.ClientName,
		meta.ClientVersion,
		meta.Platform,
		meta.MetadataSource,
		now,
	}

	if strings.EqualFold(s.repo.dialect, "mysql") ||
		strings.EqualFold(s.repo.dialect, "mariadb") {
		_, err := s.repo.db.ExecContext(ctx, `
INSERT INTO vpn_device_metadata (
	user_id, protocol, inbound_tag, device_id, device_type,
	manufacturer, model, os_name, os_version,
	client_name, client_version, platform, metadata_source, last_seen_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	device_type = VALUES(device_type),
	manufacturer = VALUES(manufacturer),
	model = VALUES(model),
	os_name = VALUES(os_name),
	os_version = VALUES(os_version),
	client_name = VALUES(client_name),
	client_version = VALUES(client_version),
	platform = VALUES(platform),
	metadata_source = VALUES(metadata_source),
	last_seen_at = VALUES(last_seen_at)`,
			args...,
		)
		return err
	}

	_, err := s.repo.db.ExecContext(ctx, `
INSERT INTO vpn_device_metadata (
	user_id, protocol, inbound_tag, device_id, device_type,
	manufacturer, model, os_name, os_version,
	client_name, client_version, platform, metadata_source, last_seen_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(user_id, protocol, inbound_tag, device_id) DO UPDATE SET
	device_type = excluded.device_type,
	manufacturer = excluded.manufacturer,
	model = excluded.model,
	os_name = excluded.os_name,
	os_version = excluded.os_version,
	client_name = excluded.client_name,
	client_version = excluded.client_version,
	platform = excluded.platform,
	metadata_source = excluded.metadata_source,
	last_seen_at = excluded.last_seen_at`,
		args...,
	)
	return err
}

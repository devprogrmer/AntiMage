package user

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"golang.org/x/crypto/curve25519"
)

type AWGDevice struct {
	DeviceIndex  int    `json:"device_index"`
	PrivateKey   string `json:"private_key"`
	PublicKey    string `json:"public_key"`
	PresharedKey string `json:"preshared_key,omitempty"`
	Address      string `json:"address"`
	Generation   int    `json:"generation"`
}

func newAWGKeyPair() (string, string, error) {
	private := make([]byte, 32)
	if _, err := cryptorand.Read(private); err != nil {
		return "", "", err
	}
	private[0] &= 248
	private[31] &= 127
	private[31] |= 64
	public, err := curve25519.X25519(private, curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(private), base64.StdEncoding.EncodeToString(public), nil
}

func newAWGPSK() (string, error) {
	raw := make([]byte, 32)
	if _, err := cryptorand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func (r Repository) ReconcileAmneziaWGDevices(ctx context.Context, inboundTag string, userID int64, limit int, pool, serverAddress string, pskEnabled bool) ([]AWGDevice, error) {
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" || userID <= 0 {
		return nil, fmt.Errorf("AmneziaWG inbound tag and user ID are required")
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > 64 {
		return nil, fmt.Errorf("AmneziaWG device limit must not exceed 64")
	}
	addressPool, err := newWGAddressPool(pool, serverAddress)
	if err != nil {
		return nil, fmt.Errorf("%s", strings.Replace(err.Error(), "WireGuard", "AmneziaWG", 1))
	}

	wgAddressAllocationMu.Lock()
	defer wgAddressAllocationMu.Unlock()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	existing := map[int]AWGDevice{}
	rows, err := tx.QueryContext(ctx, `SELECT device_index, private_key, public_key, preshared_key, address, generation FROM amneziawg_devices WHERE inbound_tag = ? AND user_id = ? ORDER BY device_index`, inboundTag, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var item AWGDevice
		if err := rows.Scan(&item.DeviceIndex, &item.PrivateKey, &item.PublicKey, &item.PresharedKey, &item.Address, &item.Generation); err != nil {
			rows.Close()
			return nil, err
		}
		existing[item.DeviceIndex] = item
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM amneziawg_devices WHERE inbound_tag = ? AND user_id = ? AND device_index >= ?`, inboundTag, userID, limit); err != nil {
		return nil, err
	}

	used := map[string]struct{}{}
	allRows, err := tx.QueryContext(ctx, `SELECT address FROM amneziawg_devices WHERE inbound_tag = ?`, inboundTag)
	if err != nil {
		return nil, err
	}
	for allRows.Next() {
		var address string
		if err := allRows.Scan(&address); err != nil {
			allRows.Close()
			return nil, err
		}
		parsed, parseErr := netip.ParseAddr(strings.TrimSpace(address))
		if parseErr == nil && addressPool.prefix.Contains(parsed) && parsed.String() != addressPool.serverAddress() {
			used[parsed.String()] = struct{}{}
		}
	}
	if err := allRows.Close(); err != nil {
		return nil, err
	}

	nextAddress := func() (string, error) {
		for slot := uint64(0); slot < addressPool.capacity; slot++ {
			candidate := addressPool.address(slot)
			if _, exists := used[candidate]; !exists {
				used[candidate] = struct{}{}
				return candidate, nil
			}
		}
		return "", fmt.Errorf("AmneziaWG address pool %s has no free device address", addressPool.prefix)
	}

	devices := make([]AWGDevice, 0, limit)
	for index := 0; index < limit; index++ {
		if item, ok := existing[index]; ok {
			parsed, parseErr := netip.ParseAddr(strings.TrimSpace(item.Address))
			if parseErr != nil || !addressPool.prefix.Contains(parsed) || parsed.String() == addressPool.serverAddress() {
				item.Address, err = nextAddress()
				if err != nil {
					return nil, err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE amneziawg_devices SET address = ? WHERE inbound_tag = ? AND user_id = ? AND device_index = ?`, item.Address, inboundTag, userID, index); err != nil {
					return nil, err
				}
			}
			if pskEnabled && item.PresharedKey == "" {
				item.PresharedKey, err = newAWGPSK()
				if err != nil {
					return nil, err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE amneziawg_devices SET preshared_key = ? WHERE inbound_tag = ? AND user_id = ? AND device_index = ?`, item.PresharedKey, inboundTag, userID, index); err != nil {
					return nil, err
				}
			} else if !pskEnabled && item.PresharedKey != "" {
				item.PresharedKey = ""
				if _, err := tx.ExecContext(ctx, `UPDATE amneziawg_devices SET preshared_key = '' WHERE inbound_tag = ? AND user_id = ? AND device_index = ?`, inboundTag, userID, index); err != nil {
					return nil, err
				}
			}
			devices = append(devices, item)
			continue
		}
		privateKey, publicKey, err := newAWGKeyPair()
		if err != nil {
			return nil, err
		}
		psk := ""
		if pskEnabled {
			psk, err = newAWGPSK()
			if err != nil {
				return nil, err
			}
		}
		address, err := nextAddress()
		if err != nil {
			return nil, err
		}
		item := AWGDevice{DeviceIndex: index, PrivateKey: privateKey, PublicKey: publicKey, PresharedKey: psk, Address: address, Generation: 1}
		if _, err := tx.ExecContext(ctx, `INSERT INTO amneziawg_devices (inbound_tag, user_id, device_index, private_key, public_key, preshared_key, address, generation) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, inboundTag, userID, index, privateKey, publicKey, psk, address, 1); err != nil {
			return nil, err
		}
		devices = append(devices, item)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].DeviceIndex < devices[j].DeviceIndex })
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return devices, nil
}

func (r Repository) DeleteAmneziaWGDevices(ctx context.Context, inboundTag string, userID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM amneziawg_devices WHERE inbound_tag = ? AND user_id = ?`, strings.TrimSpace(inboundTag), userID)
	return err
}

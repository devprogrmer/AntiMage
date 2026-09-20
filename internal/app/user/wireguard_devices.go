package user

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

type WGDevice struct {
	DeviceIndex int    `json:"device_index"`
	PrivateKey  string `json:"private_key,omitempty"`
	PublicKey   string `json:"public_key"`
	Address     string `json:"address"`
	Generation  int    `json:"generation"`
}

func (r Repository) ReconcileWireGuardDevices(ctx context.Context, inboundTag string, userID int64, limit int, pool, serverAddress, credentialKey string) ([]WGDevice, error) {
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" || userID <= 0 {
		return nil, fmt.Errorf("WireGuard inbound tag and user ID are required")
	}
	if limit > 64 {
		return nil, fmt.Errorf("WireGuard device limit must not exceed 64")
	}
	addressPool, err := newWGAddressPool(pool, serverAddress)
	if err != nil {
		return nil, err
	}
	wgAddressAllocationMu.Lock()
	defer wgAddressAllocationMu.Unlock()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	existing := map[int]WGDevice{}
	rows, err := tx.QueryContext(ctx, `SELECT device_index, private_key, public_key, address, generation FROM wireguard_devices WHERE inbound_tag = ? AND user_id = ? ORDER BY device_index`, inboundTag, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var item WGDevice
		if err := rows.Scan(&item.DeviceIndex, &item.PrivateKey, &item.PublicKey, &item.Address, &item.Generation); err != nil {
			rows.Close()
			return nil, err
		}
		existing[item.DeviceIndex] = item
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	desired := limit
	if desired <= 0 {
		desired = len(existing)
		if desired == 0 {
			desired = 1
		}
	}
	if limit > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM wireguard_devices WHERE inbound_tag = ? AND user_id = ? AND device_index >= ?`, inboundTag, userID, limit); err != nil {
			return nil, err
		}
	}
	used := map[string]struct{}{}
	allRows, err := tx.QueryContext(ctx, `SELECT address FROM wireguard_devices WHERE inbound_tag = ?`, inboundTag)
	if err != nil {
		return nil, err
	}
	for allRows.Next() {
		var address string
		if err := allRows.Scan(&address); err != nil {
			allRows.Close()
			return nil, err
		}
		if parsed, e := netip.ParseAddr(strings.TrimSpace(address)); e == nil && addressPool.prefix.Contains(parsed) && parsed.String() != addressPool.serverAddress() {
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
		return "", fmt.Errorf("WireGuard address pool %s has no free device address", addressPool.prefix)
	}
	devices := make([]WGDevice, 0, desired)
	for index := 0; index < desired; index++ {
		if item, ok := existing[index]; ok {
			devices = append(devices, item)
			continue
		}
		privateKey, publicKey := "", ""
		if index == 0 && strings.TrimSpace(credentialKey) != "" {
			pair, e := WGKeyPairFromCredentialKey(credentialKey)
			if e != nil {
				return nil, e
			}
			privateKey, publicKey = pair.PrivateKey, pair.PublicKey
		} else {
			privateKey, publicKey, err = newAWGKeyPair()
			if err != nil {
				return nil, err
			}
		}
		address, err := nextAddress()
		if err != nil {
			return nil, err
		}
		item := WGDevice{DeviceIndex: index, PrivateKey: privateKey, PublicKey: publicKey, Address: address, Generation: 1}
		if _, err := tx.ExecContext(ctx, `INSERT INTO wireguard_devices (inbound_tag, user_id, device_index, private_key, public_key, address, generation) VALUES (?, ?, ?, ?, ?, ?, 1)`, inboundTag, userID, index, privateKey, publicKey, address); err != nil {
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

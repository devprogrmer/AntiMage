// Package usagereflection defines cumulative raw accounting receipts. A receipt
// is authoritative only when committed in the same transaction as UsedTraffic.
package usagereflection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type Watermark struct {
	LegacyRoot      string                       `json:"legacy_root,omitempty"`
	Root            string                       `json:"root,omitempty"`
	Baselines       map[string]uint64            `json:"baselines,omitempty"`
	LegacyRoots     map[string]string            `json:"legacy_roots,omitempty"`
	LegacyBatches   map[string]map[string]uint64 `json:"legacy_batches,omitempty"`
	PendingChildren map[string]PendingChild      `json:"pending_children,omitempty"`
	Version         int                          `json:"version"`
	Epoch           string                       `json:"epoch"`
	Totals          map[string]uint64            `json:"totals"`
}

type PendingChild struct {
	BatchID string `json:"batch_id"`
	Raw     uint64 `json:"raw"`
}

// Key is collision-free even when inbound tags contain separators.
func Key(protocol, tag string) string { return strconv.Quote(protocol) + ":" + strconv.Quote(tag) }

// RetirementProofID binds a durable panel retirement receipt to one node,
// owner, accounting epoch, cumulative total, and legacy anchor total. Replays
// of the same committed proof therefore have the same identity, while a newer
// watermark gets a distinct identity.
func RetirementProofID(nodeID, userID int64, epoch, ownerKey string, total, legacy uint64) string {
	payload := strconv.FormatInt(nodeID, 10) + "\x00" + strconv.FormatInt(userID, 10) + "\x00" + epoch + "\x00" + ownerKey + "\x00" + strconv.FormatUint(total, 10) + "\x00" + strconv.FormatUint(legacy, 10)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func StorageKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
func ParseKey(key string) (string, string, error) {
	var a, b string
	decoder := json.NewDecoder(strings.NewReader(key))
	if err := decoder.Decode(&a); err != nil {
		return "", "", err
	}
	offset := int(decoder.InputOffset())
	if offset >= len(key) || key[offset] != ':' {
		return "", "", fmt.Errorf("invalid usage owner key")
	}
	if err := json.Unmarshal([]byte(key[offset+1:]), &b); err != nil {
		return "", "", err
	}
	return a, b, nil
}

func Decode(raw string) (Watermark, error) {
	var w Watermark
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		return w, fmt.Errorf("decode usage reflection: %w", err)
	}
	if w.Version != 2 || w.Epoch == "" || w.Root == "" || w.Totals == nil {
		return w, fmt.Errorf("invalid usage reflection watermark")
	}
	for key, total := range w.Totals {
		protocol, _, err := ParseKey(key)
		if err != nil || protocol == "" {
			return w, fmt.Errorf("invalid usage receipt owner")
		}
		if baseline, ok := w.Baselines[key]; ok && baseline > total {
			return w, fmt.Errorf("usage receipt baseline exceeds total")
		}
		var legacy uint64
		for _, raw := range w.LegacyBatches[key] {
			if ^uint64(0)-legacy < raw {
				return w, fmt.Errorf("legacy usage receipt overflow")
			}
			legacy += raw
		}
		if legacy > total {
			return w, fmt.Errorf("legacy usage receipt exceeds cumulative total")
		}
	}
	for key, batches := range w.LegacyBatches {
		if _, ok := w.Totals[key]; !ok {
			return w, fmt.Errorf("legacy usage receipt has no cumulative owner")
		}
		for batch, raw := range batches {
			if strings.TrimSpace(batch) == "" || raw == 0 {
				return w, fmt.Errorf("invalid legacy usage batch receipt")
			}
		}
	}
	for key, child := range w.PendingChildren {
		if _, ok := w.Totals[key]; !ok || child.BatchID == "" || child.Raw == 0 || child.Raw > w.Totals[key] {
			return w, fmt.Errorf("invalid usage pending child receipt")
		}
	}
	return w, nil
}

// Merge never infers raw reflection from a transport ACK. Call only inside the
// transaction applying the matching staged usage. Unmentioned owners retain
// their previous watermark; a receipt must not reflect another pending source.
func Merge(previous, incoming, previousRoot string) (string, error) {
	if incoming == "" {
		return previous, nil
	}
	next, err := Decode(incoming)
	if err != nil {
		return "", err
	}
	next.LegacyRoot = previousRoot
	if next.Baselines == nil {
		next.Baselines = map[string]uint64{}
	}
	if next.LegacyRoots == nil {
		next.LegacyRoots = map[string]string{}
	}
	if next.LegacyBatches == nil {
		next.LegacyBatches = map[string]map[string]uint64{}
	}
	if next.PendingChildren == nil {
		next.PendingChildren = map[string]PendingChild{}
	}
	if previous != "" {
		old, err := Decode(previous)
		if err != nil {
			return "", err
		}
		if old.Epoch == next.Epoch {
			next.LegacyRoot = previousRoot
			for k, v := range old.Totals {
				if n, refreshed := next.Totals[k]; refreshed {
					if n < v {
						return "", fmt.Errorf("usage reflection watermark regressed")
					}
					continue
				}
				next.Totals[k] = v
				if _, ok := next.Baselines[k]; !ok {
					if baseline, exists := old.Baselines[k]; exists {
						next.Baselines[k] = baseline
					}
				}
				if _, ok := next.LegacyRoots[k]; !ok {
					if root, exists := old.LegacyRoots[k]; exists {
						next.LegacyRoots[k] = root
					}
				}
				if batches, exists := old.LegacyBatches[k]; exists {
					copyBatches := make(map[string]uint64, len(batches))
					for batch, raw := range batches {
						copyBatches[batch] = raw
					}
					next.LegacyBatches[k] = copyBatches
				}
				if child, exists := old.PendingChildren[k]; exists {
					next.PendingChildren[k] = child
				}
			}
		}
	}
	raw, err := json.Marshal(next)
	return string(raw), err
}

package nodeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestOfflineRestartBlocksExhaustedCredentialsAllProtocols(t *testing.T) {
	const mb = uint64(1024 * 1024)
	for protocol, field := range map[string]string{"xray": "xray_policies", "wireguard": "wg_inbounds", "amneziawg": "awg_inbounds", "openvpn": "inbounds", "l2tp": "l2tp_inbounds", "pptp": "pptp_inbounds", "ikev2": "ikev2_inbounds", "anyconnect": "anyconnect_inbounds"} {
		t.Run(protocol, func(t *testing.T) {
			dir := t.TempDir()
			s := New(Config{DataDir: dir})
			const uid = int64(10)
			const tag = "offline"
			var path string
			var state any
			switch protocol {
			case "xray":
				path = s.xrayUsageStatePath()
				state = xrayUsageDiskState{Counters: map[string]accountingCounter{"10.rb1_b2ZmbGluZQ.user:uplink": {Native: 20 * mb, Total: 20 * mb}}}
			case "wireguard":
				path = s.wireGuardUsageStatePath()
				state = wireGuardUsageDiskState{Carry: map[string]wireGuardUsageCarry{"offline|wg0|peer": {UserID: uid, InboundTag: tag, Value: 20 * mb, NextBaseline: 20 * mb}}}
			case "amneziawg":
				path = filepath.Join(dir, "amneziawg", "offline-accounting.json")
				state = amneziaWGOfflineState{Counters: map[string]amneziaWGOfflineCounter{"peer": {UserID: uid, InboundTag: tag, Total: 20 * mb}}}
			case "openvpn":
				path = s.openVPNUsageStatePath()
				state = openVPNUsageDiskState{Baseline: map[string]uint64{offlineAccountingTotalKey(uid, tag): 20 * mb}}
			case "l2tp":
				path = s.l2TPUsageStatePath()
				state = l2TPUsageDiskState{Baseline: map[string]uint64{offlineAccountingTotalKey(uid, tag): 20 * mb}}
			case "pptp":
				path = s.pptpUsageStatePath()
				state = pptpUsageDiskState{Baseline: map[string]uint64{offlineAccountingTotalKey(uid, tag): 20 * mb}}
			case "ikev2":
				path = s.ikev2UsageStatePath()
				state = ikev2UsageDiskState{Baseline: map[string]uint64{offlineOwnerKey(offlineUsageOwner{uid, tag}): 20 * mb}}
			case "anyconnect":
				path = s.anyConnectUsageStatePath()
				state = anyConnectUsageDiskState{Baseline: map[string]uint64{offlineOwnerKey(offlineUsageOwner{uid, tag}): 20 * mb}}
			}
			raw, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := writeAccountingState(path, raw); err != nil {
				t.Fatal(err)
			}
			// 20MB raw * 2 * 1.5 = 60MB, beyond the 50MB limit.
			user := map[string]any{"user_id": uid, "email": "10.rb1_b2ZmbGluZQ.user", "inbound_tag": tag, "status": "active", "used_traffic": 0, "data_limit": 50 * mb, "usage_coefficient": 2, "inbound_coefficient": 1.5}
			usersField := "users"
			if protocol == "wireguard" || protocol == "amneziawg" {
				usersField = "peers"
			}
			var payload any = []any{map[string]any{"tag": tag, usersField: []any{user}}}
			if protocol == "xray" {
				payload = []any{user}
			}
			native, _ := json.Marshal(map[string]any{field: payload})
			req := &nodev1.RuntimeConfigRequest{ConfigJson: `{"inbounds":[{"tag":"offline","protocol":"vless","settings":{"clients":[{"email":"10.rb1_b2ZmbGluZQ.user","id":"keep-secret"}]}}]}`, OvRuntimeJson: string(native)}
			if err := s.persistRuntimePolicy(req); err != nil {
				t.Fatal(err)
			}
			restarted := New(Config{DataDir: dir})
			policy, err := restarted.loadRuntimePolicy()
			if err != nil {
				t.Fatal(err)
			}
			guarded, err := restarted.guardOfflineRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: policy.ConfigJSON, OvRuntimeJson: policy.NativeJSON})
			if err != nil {
				t.Fatal(err)
			}
			if protocol == "xray" {
				var cfg struct {
					Inbounds []struct {
						Settings struct {
							Clients []json.RawMessage `json:"clients"`
						} `json:"settings"`
					} `json:"inbounds"`
				}
				if err := json.Unmarshal([]byte(guarded.ConfigJson), &cfg); err != nil {
					t.Fatal(err)
				}
				if len(cfg.Inbounds[0].Settings.Clients) != 0 {
					t.Fatal("exhausted Xray credential restored")
				}
			} else {
				var fields map[string][]map[string]json.RawMessage
				if err := json.Unmarshal([]byte(guarded.OvRuntimeJson), &fields); err != nil {
					t.Fatal(err)
				}
				var users []json.RawMessage
				if err := json.Unmarshal(fields[field][0][usersField], &users); err != nil {
					t.Fatal(err)
				}
				if len(users) != 0 {
					t.Fatal("exhausted native credential restored")
				}
			}
			got, err := restarted.durablePolicyRaw(protocol, uid, tag, "")
			if err != nil || got != 20*mb {
				t.Fatalf("restore lost raw traffic: %d %v", got, err)
			}
		})
	}
}

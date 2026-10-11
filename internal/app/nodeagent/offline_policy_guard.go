package nodeagent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// Activation consults durable totals before restoring credentials. No native
// counter query is needed here; watchdogs account subsequent live deltas.
func (s *Server) durablePolicyRaw(protocol string, uid int64, tag, reflected string) (uint64, error) {
	var baseline map[string]uint64
	var pendingID string
	var pendingRaw uint64
	switch protocol {
	case "openvpn":
		s.openVPNUsageMu.Lock()
		defer s.openVPNUsageMu.Unlock()
		if err := s.ensureOpenVPNUsageStateLoadedLocked(); err != nil {
			return 0, err
		}
		baseline = s.openVPNUsageBaseline
		if p := s.openVPNUsagePending; p != nil {
			pendingID = p.BatchID
			for _, sample := range p.Samples {
				if sample.UserID == uid && sample.InboundTag == tag {
					pendingRaw += sample.Value
				}
			}
		}
	case "l2tp":
		s.l2TPUsageMu.Lock()
		defer s.l2TPUsageMu.Unlock()
		if err := s.ensureL2TPUsageStateLoadedLocked(); err != nil {
			return 0, err
		}
		baseline = s.l2TPUsageBaseline
		if p := s.l2TPUsagePending; p != nil {
			pendingID = p.BatchID
			for _, sample := range p.Samples {
				if sample.UserID == uid && sample.InboundTag == tag {
					pendingRaw += sample.Value
				}
			}
		}
	case "pptp":
		s.pptpUsageMu.Lock()
		defer s.pptpUsageMu.Unlock()
		if err := s.ensurePPTPUsageStateLoadedLocked(); err != nil {
			return 0, err
		}
		baseline = s.pptpUsageBaseline
		if p := s.pptpUsagePending; p != nil {
			pendingID = p.BatchID
			for _, sample := range p.Samples {
				if sample.UserID == uid && sample.InboundTag == tag {
					pendingRaw += sample.Value
				}
			}
		}
	case "ikev2", "anyconnect":
		var captured map[string]uint64
		var samples []ikev2UsageSample
		if protocol == "ikev2" {
			s.ikev2UsageMu.Lock()
			defer s.ikev2UsageMu.Unlock()
			if err := s.ensureIKEv2UsageStateLoadedLocked(); err != nil {
				return 0, err
			}
			baseline = s.ikev2UsageBaseline
			if p := s.ikev2UsagePending; p != nil {
				pendingID, captured, samples = p.BatchID, p.NextBaseline, p.Samples
			}
		} else {
			s.anyConnectUsageMu.Lock()
			defer s.anyConnectUsageMu.Unlock()
			if err := s.ensureAnyConnectUsageStateLoadedLocked(); err != nil {
				return 0, err
			}
			baseline = s.anyConnectUsageBaseline
			if p := s.anyConnectUsagePending; p != nil {
				pendingID, captured = p.BatchID, p.NextBaseline
				for _, sample := range p.Samples {
					samples = append(samples, ikev2UsageSample{UserID: sample.UserID, InboundTag: sample.InboundTag, Value: sample.Value})
				}
			}
		}
		return s.offlineQuotaRaw(protocol, offlineUsageOwner{uid, tag}, baseline, captured, samples, pendingID, reflected)
	case "amneziawg":
		s.amneziaWGUsageMu.Lock()
		defer s.amneziaWGUsageMu.Unlock()
		if err := s.ensureAmneziaWGUsageStateLoadedLocked(); err != nil {
			return 0, err
		}
		state, err := s.readAmneziaWGOfflineState()
		if err != nil {
			return 0, err
		}
		raw, err := s.amneziaWGUnreflectedRawLocked(state, uid, tag)
		if err != nil {
			return 0, err
		}
		if p := s.amneziaWGUsagePending; p != nil {
			pendingID = p.BatchID
			matched, err := s.localPendingUsageReflected(protocol, uid, pendingID, reflected)
			if err != nil {
				return 0, err
			}
			if matched {
				for _, sample := range p.Samples {
					if sample.UserID == uid && sample.InboundTag == tag {
						if raw < sample.Value {
							return 0, fmt.Errorf("AWG reflected raw underflow")
						}
						raw -= sample.Value
					}
				}
			}
		}
		credit, err := s.localAwaitingReflectionUsage(protocol, uid, tag, pendingID, reflected)
		if err != nil {
			return 0, err
		}
		if ^uint64(0)-raw < credit {
			return 0, fmt.Errorf("AWG raw overflow")
		}
		return raw + credit, nil
	case "wireguard":
		s.wireGuardUsageMu.Lock()
		defer s.wireGuardUsageMu.Unlock()
		if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
			return 0, err
		}
		cfg := wireGuardUsageRuntimeConfig{InboundTag: tag, Peers: map[string]int64{"durable": uid}, Policies: map[string]nativeSessionUserPolicy{"durable": {ReflectedUsageBatchID: reflected}}}
		return s.wireGuardLiveUnackedUsageLocked(cfg, "durable-policy", nil, uid)
	case "xray":
		s.xrayUsageMu.Lock()
		defer s.xrayUsageMu.Unlock()
		if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
			return 0, err
		}
		var raw uint64
		for key, counter := range s.xrayAccountingCounters {
			email, _, ok := strings.Cut(key, ":")
			identity, err := parseXrayUserEmail(email)
			if !ok || err != nil || identity.UserID != uid || identity.InboundTag != tag {
				continue
			}
			if counter.Total < s.xrayUsageBaseline[key] {
				return 0, fmt.Errorf("xray durable quota underflow")
			}
			delta := counter.Total - s.xrayUsageBaseline[key]
			if ^uint64(0)-raw < delta {
				return 0, fmt.Errorf("xray durable quota overflow")
			}
			raw += delta
		}
		if p := s.xrayUsagePending; p != nil {
			pendingID = p.BatchID
			matched, err := s.localPendingUsageReflected(protocol, uid, pendingID, reflected)
			if err != nil {
				return 0, err
			}
			for _, sample := range p.Samples {
				if sample.UserID == uid && sample.InboundTag == tag {
					if len(s.xrayAccountingCounters) == 0 && !matched {
						raw += sample.Value
					} else if matched {
						if raw < sample.Value {
							return 0, fmt.Errorf("xray reflected quota underflow")
						}
						raw -= sample.Value
					}
				}
			}
		}
		credit, err := s.localAwaitingReflectionUsage(protocol, uid, tag, pendingID, reflected)
		if err != nil {
			return 0, err
		}
		if ^uint64(0)-raw < credit {
			return 0, fmt.Errorf("xray durable credit overflow")
		}
		return raw + credit, nil
	default:
		return 0, fmt.Errorf("unknown durable policy protocol %q", protocol)
	}
	return s.offlinePolicyRaw(baseline, protocol, uid, tag, pendingID, reflected, pendingRaw)
}

func (s *Server) guardOfflineRuntimePolicy(req *nodev1.RuntimeConfigRequest) (*nodev1.RuntimeConfigRequest, error) {
	var native map[string]json.RawMessage
	if strings.TrimSpace(req.GetOvRuntimeJson()) == "" {
		return req, nil
	}
	if err := json.Unmarshal([]byte(req.GetOvRuntimeJson()), &native); err != nil {
		return nil, err
	}
	effectiveTotals, err := s.offlineGuardEffectiveTotals(native)
	if err != nil {
		return nil, err
	}
	deniedEmails := map[string]bool{}
	for field, protocol := range map[string]string{"inbounds": "openvpn", "l2tp_inbounds": "l2tp", "pptp_inbounds": "pptp", "wg_inbounds": "wireguard", "awg_inbounds": "amneziawg", "ikev2_inbounds": "ikev2", "anyconnect_inbounds": "anyconnect", "xray_policies": "xray"} {
		if len(native[field]) == 0 || string(native[field]) == "null" {
			continue
		}
		var inbounds []map[string]json.RawMessage
		if protocol == "xray" {
			inbounds = []map[string]json.RawMessage{{"users": native[field]}}
		} else if err := json.Unmarshal(native[field], &inbounds); err != nil {
			return nil, err
		}
		for _, inbound := range inbounds {
			usersField := "users"
			if protocol == "wireguard" || protocol == "amneziawg" {
				usersField = "peers"
			}
			var users []json.RawMessage
			if len(inbound[usersField]) == 0 {
				continue
			}
			if err := json.Unmarshal(inbound[usersField], &users); err != nil {
				return nil, err
			}
			kept := make([]json.RawMessage, 0, len(users))
			for _, user := range users {
				var p struct {
					UserID int64  `json:"user_id"`
					Email  string `json:"email"`
					nativeSessionUserPolicy
				}
				if err := json.Unmarshal(user, &p); err != nil {
					return nil, err
				}
				allowed, _ := nativeSessionUserPolicyAllowed(p.nativeSessionUserPolicy, time.Now())
				if allowed && strings.EqualFold(p.Status, "active") && nativeSessionPolicyWouldExceedDataLimit(p.nativeSessionUserPolicy, effectiveTotals[p.UserID]) {
					allowed = false
				}
				if allowed {
					kept = append(kept, user)
				} else if protocol == "xray" {
					deniedEmails[p.Email] = true
				}
			}
			inbound[usersField], _ = json.Marshal(kept)
		}
		if protocol != "xray" {
			native[field], _ = json.Marshal(inbounds)
		}
	}
	configJSON := req.GetConfigJson()
	if len(deniedEmails) > 0 {
		var config map[string]json.RawMessage
		if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
			return nil, err
		}
		var inbounds []map[string]json.RawMessage
		if err := json.Unmarshal(config["inbounds"], &inbounds); err != nil {
			return nil, err
		}
		for _, inbound := range inbounds {
			var settings map[string]json.RawMessage
			if len(inbound["settings"]) == 0 {
				continue
			}
			if err := json.Unmarshal(inbound["settings"], &settings); err != nil {
				return nil, err
			}
			if len(settings["clients"]) == 0 {
				continue
			}
			var clients []json.RawMessage
			if err := json.Unmarshal(settings["clients"], &clients); err != nil {
				return nil, err
			}
			kept := []json.RawMessage{}
			for _, client := range clients {
				var identity struct {
					Email string `json:"email"`
				}
				if err := json.Unmarshal(client, &identity); err != nil {
					return nil, err
				}
				if !deniedEmails[identity.Email] {
					kept = append(kept, client)
				}
			}
			settings["clients"], _ = json.Marshal(kept)
			inbound["settings"], _ = json.Marshal(settings)
		}
		config["inbounds"], _ = json.Marshal(inbounds)
		raw, err := json.Marshal(config)
		if err != nil {
			return nil, err
		}
		configJSON = string(raw)
	}
	raw, err := json.Marshal(native)
	if err != nil {
		return nil, err
	}
	return &nodev1.RuntimeConfigRequest{ConfigJson: configJSON, OvRuntimeJson: string(raw), DesiredRevision: req.GetDesiredRevision(), OperationId: req.GetOperationId()}, nil
}

func (s *Server) offlineGuardEffectiveTotals(native map[string]json.RawMessage) (map[int64]uint64, error) {
	totals := map[int64]uint64{}
	seen := map[string]bool{}
	for field, protocol := range map[string]string{"inbounds": "openvpn", "l2tp_inbounds": "l2tp", "pptp_inbounds": "pptp", "wg_inbounds": "wireguard", "awg_inbounds": "amneziawg", "ikev2_inbounds": "ikev2", "anyconnect_inbounds": "anyconnect", "xray_policies": "xray"} {
		if len(native[field]) == 0 || string(native[field]) == "null" {
			continue
		}
		var inbounds []struct {
			Tag   string            `json:"tag"`
			Users []json.RawMessage `json:"users"`
			Peers []json.RawMessage `json:"peers"`
		}
		if protocol == "xray" {
			var policies []json.RawMessage
			if err := json.Unmarshal(native[field], &policies); err != nil {
				return nil, err
			}
			inbounds = append(inbounds, struct {
				Tag   string            `json:"tag"`
				Users []json.RawMessage `json:"users"`
				Peers []json.RawMessage `json:"peers"`
			}{Users: policies})
		} else if err := json.Unmarshal(native[field], &inbounds); err != nil {
			return nil, err
		}
		for _, inbound := range inbounds {
			users := inbound.Users
			if protocol == "wireguard" || protocol == "amneziawg" {
				users = inbound.Peers
			}
			for _, user := range users {
				var p struct {
					UserID     int64  `json:"user_id"`
					InboundTag string `json:"inbound_tag"`
					nativeSessionUserPolicy
				}
				if err := json.Unmarshal(user, &p); err != nil {
					return nil, err
				}
				tag := inbound.Tag
				if protocol == "xray" {
					tag = p.InboundTag
				}
				owner := fmt.Sprintf("%s/%d/%s", protocol, p.UserID, tag)
				if seen[owner] {
					continue
				}
				seen[owner] = true
				raw, err := s.durablePolicyRaw(protocol, p.UserID, tag, p.ReflectedUsageBatchID)
				if err != nil {
					return nil, err
				}
				effective := nativeSessionEffectiveLiveUsage(p.nativeSessionUserPolicy, raw)
				s.localQuotaAllowed(protocol, p.UserID, tag, p.nativeSessionUserPolicy, raw, time.Now())
				if ^uint64(0)-totals[p.UserID] < effective {
					return nil, fmt.Errorf("combined offline quota overflow")
				}
				totals[p.UserID] += effective
			}
		}
	}
	return totals, nil
}

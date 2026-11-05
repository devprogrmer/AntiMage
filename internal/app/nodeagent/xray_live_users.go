package nodeagent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

type xrayLiveUser struct {
	Email       string
	Level       uint32
	AccountType string
	Account     []byte
}
type xrayLiveAddition struct {
	tag  string
	user xrayLiveUser
}

func xrayDecodeConfig(raw string) (map[string]any, error) {
	var out map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing Xray config data")
	}
	return out, nil
}

// A conservative fast path: retain every existing credential unchanged and
// alter no listener, transport, route, policy or other core setting. Removals
// and credential edits still require the existing full-apply path.
func xrayLiveUserAdditions(previous, next string) ([]xrayLiveAddition, bool, error) {
	oldConfig, err := xrayDecodeConfig(previous)
	if err != nil {
		return nil, false, err
	}
	newConfig, err := xrayDecodeConfig(next)
	if err != nil {
		return nil, false, err
	}
	oldInbounds, oldOK := oldConfig["inbounds"].([]any)
	newInbounds, newOK := newConfig["inbounds"].([]any)
	if !oldOK || !newOK || len(oldInbounds) != len(newInbounds) {
		return nil, false, nil
	}
	var additions []xrayLiveAddition
	seenTags := map[string]bool{}
	for i, oldValue := range oldInbounds {
		oldInbound, ok := oldValue.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		newInbound, ok := newInbounds[i].(map[string]any)
		if !ok {
			return nil, false, nil
		}
		protocol, _ := oldInbound["protocol"].(string)
		if protocol != "vless" && protocol != "vmess" && protocol != "trojan" {
			continue
		}
		tag, _ := oldInbound["tag"].(string)
		if tag == "" || seenTags[tag] {
			return nil, false, nil
		}
		seenTags[tag] = true
		oldSettings, ok := oldInbound["settings"].(map[string]any)
		if !ok {
			return nil, false, nil
		}
		newSettings, ok := newInbound["settings"].(map[string]any)
		if !ok {
			return nil, false, nil
		}
		oldClients, ok := xrayLiveClientIndex(oldSettings["clients"])
		if !ok {
			return nil, false, nil
		}
		newClients, ok := xrayLiveClientIndex(newSettings["clients"])
		if !ok {
			return nil, false, nil
		}
		for email, client := range oldClients {
			if !reflect.DeepEqual(client, newClients[email]) {
				return nil, false, nil
			}
		}
		// Walk desired order for deterministic, retryable application.
		clients, _ := newSettings["clients"].([]any)
		for _, value := range clients {
			client := value.(map[string]any)
			email := strings.ToLower(client["email"].(string))
			if _, exists := oldClients[email]; exists {
				continue
			}
			user, ok := xrayLiveUserFromClient(protocol, client)
			if !ok {
				return nil, false, nil
			}
			additions = append(additions, xrayLiveAddition{tag, user})
		}
		delete(oldSettings, "clients")
		delete(newSettings, "clients")
	}
	if !reflect.DeepEqual(oldConfig, newConfig) {
		return nil, false, nil
	}
	return additions, true, nil
}

func xrayLiveClientIndex(value any) (map[string]map[string]any, bool) {
	out := map[string]map[string]any{}
	if value == nil {
		return out, true
	}
	clients, ok := value.([]any)
	if !ok {
		return nil, false
	}
	for _, raw := range clients {
		client, ok := raw.(map[string]any)
		if !ok {
			return nil, false
		}
		email, ok := client["email"].(string)
		if !ok || strings.TrimSpace(email) == "" {
			return nil, false
		}
		key := strings.ToLower(email)
		if _, exists := out[key]; exists {
			return nil, false
		}
		out[key] = client
	}
	return out, true
}

func xrayLiveUserFromClient(protocol string, client map[string]any) (xrayLiveUser, bool) {
	user := xrayLiveUser{AccountType: "xray.proxy." + protocol + ".Account"}
	user.Email, _ = client["email"].(string)
	for key, value := range client {
		switch key {
		case "email":
		case "level":
			number, ok := value.(json.Number)
			if !ok {
				return user, false
			}
			level, err := strconv.ParseUint(string(number), 10, 32)
			if err != nil {
				return user, false
			}
			user.Level = uint32(level)
		case "id":
			if protocol == "trojan" {
				return user, false
			}
		case "password":
			if protocol != "trojan" {
				return user, false
			}
		case "flow":
			if protocol != "vless" {
				return user, false
			}
		default:
			return user, false
		}
	}
	credentialKey := "id"
	if protocol == "trojan" {
		credentialKey = "password"
	}
	credential, ok := client[credentialKey].(string)
	if !ok || credential == "" {
		return user, false
	}
	if protocol != "trojan" {
		// Custom short Xray IDs and additional account features retain full apply.
		compact := strings.ReplaceAll(credential, "-", "")
		decoded, err := hex.DecodeString(compact)
		if err != nil || len(decoded) != 16 {
			return user, false
		}
		credential = fmt.Sprintf("%x-%x-%x-%x-%x", decoded[:4], decoded[4:6], decoded[6:8], decoded[8:10], decoded[10:])
	}
	user.Account = protowire.AppendTag(user.Account, 1, protowire.BytesType)
	user.Account = protowire.AppendString(user.Account, credential)
	if flow, exists := client["flow"]; exists {
		text, ok := flow.(string)
		if !ok || (text != "" && text != "xtls-rprx-vision") {
			return user, false
		}
		if text != "" {
			user.Account = protowire.AppendTag(user.Account, 2, protowire.BytesType)
			user.Account = protowire.AppendString(user.Account, text)
		}
	}
	return user, true
}

func xrayLiveAccountEqual(kind string, expected, actual []byte) bool {
	fields := func(raw []byte) (map[protowire.Number]string, bool) {
		out := map[protowire.Number]string{}
		for len(raw) > 0 {
			number, typ, n := protowire.ConsumeTag(raw)
			if n < 0 {
				return nil, false
			}
			raw = raw[n:]
			if typ != protowire.BytesType {
				return nil, false
			}
			value, n := protowire.ConsumeBytes(raw)
			if n < 0 {
				return nil, false
			}
			raw = raw[n:]
			// VMess returns client-side security defaults chosen by the core.
			if kind == "xray.proxy.vmess.Account" && number == 3 {
				continue
			}
			if number != 1 && !(kind == "xray.proxy.vless.Account" && number == 2) {
				return nil, false
			}
			if _, duplicate := out[number]; duplicate {
				return nil, false
			}
			out[number] = string(value)
		}
		return out, true
	}
	if bytes.Equal(expected, actual) {
		return true
	}
	a, aOK := fields(expected)
	b, bOK := fields(actual)
	return aOK && bOK && reflect.DeepEqual(a, b)
}

func (c *xrayStatsClient) ensureLiveUser(ctx context.Context, addition xrayLiveAddition) error {
	existing, err := c.getUserRPC(ctx, addition.tag, addition.user.Email)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.Email != addition.user.Email || existing.Level != addition.user.Level || existing.AccountType != addition.user.AccountType || !xrayLiveAccountEqual(existing.AccountType, addition.user.Account, existing.Account) {
			return fmt.Errorf("existing Xray account differs from desired addition")
		}
		return nil
	}
	if err := c.addUserRPC(ctx, addition.tag, addition.user); err != nil {
		return err
	}
	// An accepted RPC is not sufficient evidence that the account is usable.
	verified, err := c.getUserRPC(ctx, addition.tag, addition.user.Email)
	if err != nil {
		return err
	}
	if verified == nil || verified.Email != addition.user.Email || verified.Level != addition.user.Level || verified.AccountType != addition.user.AccountType || !xrayLiveAccountEqual(verified.AccountType, addition.user.Account, verified.Account) {
		return fmt.Errorf("Xray did not retain the desired live account")
	}
	return nil
}

// Caller holds xrayTransitionMu. The existing config file describes the last
// completely applied core configuration. Leave it unchanged on partial RPC
// failure so retries can verify already-added users and finish the same diff.
func (s *Server) tryLiveXrayUserAdditions(ctx context.Context, desired string) (bool, error) {
	s.mu.Lock()
	running, configPath, path, port := s.lastRuntime != nil, s.lastConfig, s.cfg.XrayPath, s.cfg.XrayAPIPort
	s.mu.Unlock()
	if !running || configPath == "" {
		return false, nil
	}
	previous, err := os.ReadFile(configPath)
	if err != nil {
		return false, fmt.Errorf("read applied Xray config: %w", err)
	}
	additions, eligible, err := xrayLiveUserAdditions(string(previous), desired)
	if err != nil || !eligible {
		return false, err
	}
	client := s.cachedXrayStatsClient(path, port)
	for _, addition := range additions {
		// Coordinate the core mutation and revocation marker with quota checks;
		// do not clear another user's pending active-stream cutoff on live apply.
		s.xrayUsageMu.Lock()
		err := client.ensureLiveUser(ctx, addition)
		if err == nil {
			delete(s.xrayQuotaRevoked, addition.user.Email)
		}
		s.xrayUsageMu.Unlock()
		if err != nil {
			return false, fmt.Errorf("apply live Xray user addition: %w", err)
		}
	}
	return true, nil
}

//go:build linux

package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

const (
	minManagedMSS = 536
	maxManagedMSS = 1340
)

var operationIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var managedInterfacePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,15}$`)

type networkOptimizationRule struct {
	Binary    string   `json:"binary"`
	Args      []string `json:"args"`
	Address   string   `json:"address"`
	Interface string   `json:"interface"`
}

type networkOptimizationOperation struct {
	OperationID string                    `json:"operation_id"`
	NodeID      int64                     `json:"node_id"`
	UserID      int64                     `json:"user_id"`
	MSS         uint32                    `json:"tcp_mss"`
	State       string                    `json:"state"`
	Rules       []networkOptimizationRule `json:"rules"`
	UpdatedAt   time.Time                 `json:"updated_at"`
}

type networkOptimizationState struct {
	Version    int                                     `json:"version"`
	Operations map[string]networkOptimizationOperation `json:"operations"`
}

func (s *Server) manageNetworkOptimizationPlatform(ctx context.Context, req *nodev1.NetworkOptimizationRequest, bindings []ni.Binding) (*nodev1.NetworkOptimizationResponse, error) {
	if req.GetAction() != nodev1.NetworkOptimizationRequest_LIST_USER && !operationIDPattern.MatchString(req.GetOperationId()) {
		return nil, errors.New("operation_id must be a 32-character lowercase hex token")
	}
	state, path, err := loadNetworkOptimizationState(s.cfg.DataDir)
	if err != nil {
		return nil, err
	}
	switch req.GetAction() {
	case nodev1.NetworkOptimizationRequest_LIST_USER:
		operations := make([]*nodev1.NetworkOptimizationOperation, 0)
		for _, record := range state.Operations {
			if record.NodeID == req.GetNodeId() && record.UserID == req.GetUserId() {
				operations = append(operations, &nodev1.NetworkOptimizationOperation{OperationId: record.OperationID, TcpMss: record.MSS, State: record.State})
			}
		}
		sort.Slice(operations, func(i, j int) bool { return operations[i].GetOperationId() < operations[j].GetOperationId() })
		return &nodev1.NetworkOptimizationResponse{State: "listed", Operations: operations}, nil
	case nodev1.NetworkOptimizationRequest_APPLY_TCP_MSS:
		if req.GetTcpMss() < minManagedMSS || req.GetTcpMss() > maxManagedMSS {
			return nil, fmt.Errorf("tcp_mss must be between %d and %d", minManagedMSS, maxManagedMSS)
		}
		if old, exists := state.Operations[req.GetOperationId()]; exists {
			if old.State == "active" && old.NodeID == req.GetNodeId() && old.UserID == req.GetUserId() && old.MSS == req.GetTcpMss() {
				return &nodev1.NetworkOptimizationResponse{OperationId: old.OperationID, State: old.State, Applied: true}, nil
			}
			return nil, errors.New("operation_id already exists")
		}
		for _, existing := range state.Operations {
			if existing.NodeID == req.GetNodeId() && existing.UserID == req.GetUserId() && existing.State == "active" {
				return nil, errors.New("user already has an active network optimization; roll it back first")
			}
		}
		record, err := buildNetworkOptimizationOperation(req, bindings, s.networkIntelligenceTracker)
		if err != nil {
			return nil, err
		}
		record.State = "applying"
		record.UpdatedAt = time.Now().UTC()
		state.Operations[record.OperationID] = record
		if err := saveNetworkOptimizationState(path, state); err != nil {
			return nil, err
		}
		if err := preflightNetworkOptimizationRules(ctx, record.Rules); err != nil {
			record.State = "failed"
			record.UpdatedAt = time.Now().UTC()
			state.Operations[record.OperationID] = record
			_ = saveNetworkOptimizationState(path, state)
			return nil, err
		}
		if err := applyNetworkOptimizationRules(ctx, record.Rules); err != nil {
			rollbackErr := removeNetworkOptimizationRules(context.WithoutCancel(ctx), record.Rules)
			record.State = "failed"
			if rollbackErr != nil {
				// The operation remains addressable for a later explicit rollback.
				record.State = "active"
			}
			record.UpdatedAt = time.Now().UTC()
			state.Operations[record.OperationID] = record
			_ = saveNetworkOptimizationState(path, state)
			if rollbackErr != nil {
				return nil, fmt.Errorf("apply failed: %w; partial-rule rollback failed: %v", err, rollbackErr)
			}
			return nil, err
		}
		record.State = "active"
		record.UpdatedAt = time.Now().UTC()
		state.Operations[record.OperationID] = record
		if err := saveNetworkOptimizationState(path, state); err != nil {
			rollbackErr := removeNetworkOptimizationRules(context.WithoutCancel(ctx), record.Rules)
			if rollbackErr == nil {
				record.State = "rolled_back"
				record.UpdatedAt = time.Now().UTC()
				state.Operations[record.OperationID] = record
				_ = saveNetworkOptimizationState(path, state)
				return nil, fmt.Errorf("persist applied state: %w", err)
			}
			return nil, fmt.Errorf("persist applied state: %w; safety rollback failed: %v", err, rollbackErr)
		}
		return &nodev1.NetworkOptimizationResponse{OperationId: record.OperationID, State: record.State, Applied: true}, nil
	case nodev1.NetworkOptimizationRequest_ROLLBACK:
		record, exists := state.Operations[req.GetOperationId()]
		if !exists || record.NodeID != req.GetNodeId() || record.UserID != req.GetUserId() {
			return nil, errors.New("active optimization operation not found for this node and user")
		}
		if record.State == "rolled_back" || record.State == "reset_runtime_change" {
			return &nodev1.NetworkOptimizationResponse{OperationId: record.OperationID, State: record.State, Applied: false}, nil
		}
		if record.State != "active" {
			return nil, fmt.Errorf("optimization operation is %s and cannot be rolled back", record.State)
		}
		if err := rollbackNetworkOptimizationRules(ctx, record.Rules); err != nil {
			return nil, err
		}
		record.State = "rolled_back"
		record.UpdatedAt = time.Now().UTC()
		state.Operations[record.OperationID] = record
		if err := saveNetworkOptimizationState(path, state); err != nil {
			if restoreErr := applyNetworkOptimizationRules(context.WithoutCancel(ctx), record.Rules); restoreErr != nil {
				return nil, fmt.Errorf("persist rollback state: %w; restoring prior rules failed: %v", err, restoreErr)
			}
			return nil, fmt.Errorf("persist rollback state: %w", err)
		}
		return &nodev1.NetworkOptimizationResponse{OperationId: record.OperationID, State: record.State, Applied: false}, nil
	default:
		return nil, errors.New("unsupported network optimization action")
	}
}

func resetNetworkOptimizationsForInterface(dataDir, interfaceName string) error {
	state, path, err := loadNetworkOptimizationState(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	changed := make([]networkOptimizationOperation, 0)
	restoreChanged := func() error {
		var restoreErr error
		for i := len(changed) - 1; i >= 0; i-- {
			restoreErr = errors.Join(restoreErr, applyNetworkOptimizationRules(context.Background(), changed[i].Rules))
		}
		return restoreErr
	}
	for _, record := range state.Operations {
		if record.State != "active" {
			continue
		}
		matches := false
		for _, rule := range record.Rules {
			matches = matches || rule.Interface == interfaceName
		}
		if !matches {
			continue
		}
		if err := rollbackNetworkOptimizationRules(context.Background(), record.Rules); err != nil {
			return errors.Join(fmt.Errorf("reset user optimization before WireGuard ownership change: %w", err), restoreChanged())
		}
		record.State = "reset_runtime_change"
		record.UpdatedAt = time.Now().UTC()
		state.Operations[record.OperationID] = record
		changed = append(changed, record)
	}
	if len(changed) == 0 {
		return nil
	}
	if err := saveNetworkOptimizationState(path, state); err != nil {
		return errors.Join(fmt.Errorf("persist ownership-change rollback: %w", err), restoreChanged())
	}
	return nil
}

func buildNetworkOptimizationOperation(req *nodev1.NetworkOptimizationRequest, bindings []ni.Binding, tracker *ni.Tracker) (networkOptimizationOperation, error) {
	if len(bindings) == 0 {
		return networkOptimizationOperation{}, errors.New("active WireGuard bindings required")
	}
	seen := make(map[netip.Addr]struct{}, len(bindings))
	record := networkOptimizationOperation{OperationID: req.GetOperationId(), NodeID: req.GetNodeId(), UserID: req.GetUserId(), MSS: req.GetTcpMss()}
	for _, binding := range bindings {
		address := binding.Address.Unmap()
		if binding.NodeID != req.GetNodeId() || binding.UserID != req.GetUserId() || binding.Session == "" || binding.Generation == 0 || binding.Since.IsZero() || !managedInterfacePattern.MatchString(binding.Interface) || !address.IsValid() || address.IsUnspecified() || address.IsMulticast() {
			return networkOptimizationOperation{}, errors.New("invalid authenticated WireGuard binding")
		}
		iface, err := net.InterfaceByName(binding.Interface)
		if err != nil {
			return networkOptimizationOperation{}, errors.New("authenticated WireGuard interface is unavailable")
		}
		headerReserve := 80
		if address.Is6() {
			headerReserve = 100
		}
		maxMSSForInterface := min(maxManagedMSS, iface.MTU-headerReserve)
		if maxMSSForInterface < minManagedMSS || int(req.GetTcpMss()) > maxMSSForInterface {
			return networkOptimizationOperation{}, errors.New("requested MSS exceeds the conservative limit for the WireGuard interface MTU")
		}
		if _, duplicate := seen[address]; duplicate {
			return networkOptimizationOperation{}, errors.New("same address is bound on multiple interfaces; refusing ambiguous rule scope")
		}
		owners := tracker.BindingsForAddress(binding.NodeID, address)
		if len(owners) != 1 || owners[0].UserID != req.GetUserId() || owners[0].Interface != binding.Interface || owners[0].Generation != binding.Generation {
			return networkOptimizationOperation{}, errors.New("address ownership is ambiguous or changed")
		}
		seen[address] = struct{}{}
		binary := "iptables"
		if address.Is6() {
			binary = "ip6tables"
		}
		comment := optimizationRuleComment(req.GetOperationId())
		args := []string{"-w", "-t", "mangle", "-A", "POSTROUTING", "-s", address.String(), "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-m", "comment", "--comment", comment, "-j", "TCPMSS", "--set-mss", fmt.Sprint(req.GetTcpMss())}
		record.Rules = append(record.Rules, networkOptimizationRule{Binary: binary, Args: args, Address: address.String(), Interface: binding.Interface})
	}
	return record, nil
}

func optimizationRuleComment(operationID string) string {
	digest := sha256.Sum256([]byte(operationID))
	return "amni-" + hex.EncodeToString(digest[:8])
}

func networkOptimizationStatePath(dataDir string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", errors.New("node data directory is required")
	}
	dir := filepath.Join(dataDir, "network-intelligence")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("network optimization state directory must be private")
	}
	return filepath.Join(dir, "optimizations.json"), nil
}

func loadNetworkOptimizationState(dataDir string) (networkOptimizationState, string, error) {
	path, err := networkOptimizationStatePath(dataDir)
	if err != nil {
		return networkOptimizationState{}, "", err
	}
	state := networkOptimizationState{Version: 1, Operations: make(map[string]networkOptimizationOperation)}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, path, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return networkOptimizationState{}, "", errors.New("network optimization state file is not a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return networkOptimizationState{}, "", err
	}
	if len(data) > 1<<20 {
		return networkOptimizationState{}, "", errors.New("network optimization state exceeds size limit")
	}
	if err := json.Unmarshal(data, &state); err != nil || state.Version != 1 || state.Operations == nil {
		return networkOptimizationState{}, "", errors.New("network optimization state is invalid")
	}
	return state, path, nil
}

func saveNetworkOptimizationState(path string, state networkOptimizationState) error {
	data, err := json.Marshal(state)
	if err != nil || len(data) > 1<<20 {
		return errors.New("network optimization state is invalid or too large")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".optimizations-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func runNetworkOptimizationCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed: %w", binary, err)
	}
	return output, nil
}

func applyNetworkOptimizationRules(ctx context.Context, rules []networkOptimizationRule) error {
	for _, rule := range rules {
		present, err := networkOptimizationRulePresent(ctx, rule)
		if err != nil {
			return err
		}
		if present {
			return errors.New("optimization rule already exists outside its recorded state")
		}
		if _, err := runNetworkOptimizationCommand(ctx, rule.Binary, rule.Args...); err != nil {
			return err
		}
	}
	return nil
}

func removeNetworkOptimizationRules(ctx context.Context, rules []networkOptimizationRule) error {
	removed := make([]networkOptimizationRule, 0, len(rules))
	for i := len(rules) - 1; i >= 0; i-- {
		rule := rules[i]
		present, err := networkOptimizationRulePresent(ctx, rule)
		if err != nil {
			return errors.Join(err, restoreRemovedNetworkOptimizationRules(context.WithoutCancel(ctx), removed))
		}
		if !present {
			continue
		}
		if _, err := runNetworkOptimizationCommand(ctx, rule.Binary, networkOptimizationArgs(rule, "-D")...); err != nil {
			return errors.Join(err, restoreRemovedNetworkOptimizationRules(context.WithoutCancel(ctx), removed))
		}
		removed = append(removed, rule)
	}
	return nil
}

func restoreRemovedNetworkOptimizationRules(ctx context.Context, removed []networkOptimizationRule) error {
	var restoreErr error
	for i := len(removed) - 1; i >= 0; i-- {
		rule := removed[i]
		if _, err := runNetworkOptimizationCommand(ctx, rule.Binary, rule.Args...); err != nil {
			restoreErr = errors.Join(restoreErr, err)
		}
	}
	return restoreErr
}

func rollbackNetworkOptimizationRules(ctx context.Context, rules []networkOptimizationRule) error {
	for _, rule := range rules {
		present, err := networkOptimizationRulePresent(ctx, rule)
		if err != nil || !present {
			return errors.New("rollback conflict: the recorded MSS rule is missing or changed")
		}
	}
	if err := removeNetworkOptimizationRules(ctx, rules); err != nil {
		return fmt.Errorf("remove MSS rules: %w", err)
	}
	return nil
}

func preflightNetworkOptimizationRules(ctx context.Context, rules []networkOptimizationRule) error {
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if _, exists := seen[rule.Binary]; exists {
			continue
		}
		seen[rule.Binary] = struct{}{}
		output, err := runNetworkOptimizationCommand(ctx, rule.Binary, "-w", "-t", "mangle", "-S")
		if err != nil {
			return errors.New("could not inspect existing mangle POSTROUTING rules")
		}
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "-A ") {
				continue
			}
			if strings.Contains(line, "-j ACCEPT") || strings.Contains(line, "-j RETURN") || strings.Contains(line, "-j DROP") || strings.Contains(line, "-g ACCEPT") || strings.Contains(line, "-g RETURN") || strings.Contains(line, "-g DROP") {
				return errors.New("existing terminal mangle rule could bypass the per-user MSS rule")
			}
		}
	}
	return nil
}

func networkOptimizationRulePresent(ctx context.Context, rule networkOptimizationRule) (bool, error) {
	_, err := runNetworkOptimizationCommand(ctx, rule.Binary, networkOptimizationArgs(rule, "-C")...)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func networkOptimizationArgs(rule networkOptimizationRule, operation string) []string {
	args := append([]string(nil), rule.Args...)
	for i := range args {
		if args[i] == "-A" {
			args[i] = operation
			break
		}
	}
	return args
}

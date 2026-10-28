package nodeagent

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestExecuteWireGuardMutationPlanRollsBackPartiallyAppliedTargetOnly(t *testing.T) {
	keyA, keyB, other := strings.Repeat("A", 43)+"=", strings.Repeat("B", 43)+"=", strings.Repeat("C", 43)+"="
	psk := strings.Repeat("D", 43) + "="
	initial := map[string]wireGuardKernelPeer{
		keyA:  {PublicKey: keyA, PresharedKey: psk, Endpoint: "198.51.100.8:50000", AllowedIPs: []string{"10.69.0.2/32"}, PersistentKeepalive: 25},
		other: {PublicKey: other, AllowedIPs: []string{"10.69.0.3/32"}, PersistentKeepalive: 0},
	}
	state := cloneKernelPeers(initial)
	var commands [][]string
	failAfterApply := 2
	previousRun := wireGuardRuntimeRun
	wireGuardRuntimeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "wg" {
			return nil, errors.New("unexpected non-WireGuard command")
		}
		commands = append(commands, append([]string(nil), args...))
		if err := applyKernelTestCommand(state, args); err != nil {
			return nil, err
		}
		if len(commands) == failAfterApply {
			return []byte("injected failure after kernel apply"), errors.New("injected command failure")
		}
		return nil, nil
	}
	t.Cleanup(func() { wireGuardRuntimeRun = previousRun })

	delta := wireGuardPeerDelta{
		Remove: []wireGuardRuntimePeer{{UserID: 7, PublicKey: keyA}},
		Set:    []wireGuardRuntimePeer{{UserID: 7, PublicKey: keyB, Address: "10.69.0.2"}},
	}
	plan, err := planWireGuardKernelMutation(delta, initial, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	err = executeWireGuardPeerMutationPlan(context.Background(), "wg", "wg-test", plan)
	if err == nil || !strings.Contains(err.Error(), "injected command failure") {
		t.Fatalf("mutation error = %v", err)
	}
	if !reflect.DeepEqual(state, initial) {
		t.Fatalf("rollback state = %+v, want exact configurable snapshot %+v", state, initial)
	}
	if len(commands) != 4 {
		t.Fatalf("kernel commands = %d, want 2 forward and 2 rollback", len(commands))
	}
	for _, command := range commands {
		if len(command) < 4 || command[0] != "set" || command[1] != "wg-test" || (command[3] != keyA && command[3] != keyB) {
			t.Fatalf("command escaped target peers: %v", command)
		}
	}
	if _, exists := state[other]; !exists {
		t.Fatal("unrelated peer was removed")
	}
}

func TestWireGuardMutationForwardSetPreservesLiveEndpointAndKeepalive(t *testing.T) {
	key := strings.Repeat("A", 43) + "="
	state := map[string]wireGuardKernelPeer{key: {PublicKey: key, Endpoint: "198.51.100.2:40000", AllowedIPs: []string{"10.69.0.2/32"}, PersistentKeepalive: 20}}
	previousRun := wireGuardRuntimeRun
	wireGuardRuntimeRun = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return nil, applyKernelTestCommand(state, args)
	}
	t.Cleanup(func() { wireGuardRuntimeRun = previousRun })
	peer := wireGuardKernelPeer{PublicKey: key, AllowedIPs: []string{"10.69.0.9/32"}}
	if err := applyWireGuardPeerMutationStep(context.Background(), "wg", "wg-test", wireGuardPeerMutationStep{Operation: "set", Peer: peer}, false); err != nil {
		t.Fatal(err)
	}
	got := state[key]
	if got.Endpoint != "198.51.100.2:40000" || got.PersistentKeepalive != 20 || len(got.AllowedIPs) != 1 || got.AllowedIPs[0] != "10.69.0.9/32" {
		t.Fatalf("forward mutation changed live endpoint or keepalive: %+v", got)
	}
}

func TestWireGuardMutationSetDoesNotExposePSKInArguments(t *testing.T) {
	key, psk := strings.Repeat("A", 43)+"=", strings.Repeat("B", 43)+"="
	var observed []string
	previousRun := wireGuardRuntimeRun
	wireGuardRuntimeRun = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		observed = append([]string(nil), args...)
		if len(args) < 8 || args[4] != "preshared-key" {
			return nil, errors.New("missing preshared-key file argument")
		}
		contents, err := os.ReadFile(args[5])
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(contents)) != psk {
			return nil, errors.New("preshared-key file contents differ")
		}
		return nil, nil
	}
	t.Cleanup(func() { wireGuardRuntimeRun = previousRun })
	step := wireGuardPeerMutationStep{Operation: "set", Peer: wireGuardKernelPeer{PublicKey: key, PresharedKey: psk, AllowedIPs: []string{"10.69.0.2/32"}}}
	if err := applyWireGuardPeerMutationStep(context.Background(), "wg", "wg-test", step, false); err != nil {
		t.Fatal(err)
	}
	for _, arg := range observed {
		if arg == psk {
			t.Fatal("preshared key appeared in process arguments")
		}
	}
}

func cloneKernelPeers(source map[string]wireGuardKernelPeer) map[string]wireGuardKernelPeer {
	out := make(map[string]wireGuardKernelPeer, len(source))
	for key, value := range source {
		out[key] = cloneWireGuardKernelPeer(value)
	}
	return out
}

func applyKernelTestCommand(state map[string]wireGuardKernelPeer, args []string) error {
	if len(args) < 5 || args[0] != "set" || args[2] != "peer" {
		return errors.New("invalid fake wg set command")
	}
	key := args[3]
	if args[4] == "remove" {
		delete(state, key)
		return nil
	}
	peer := state[key]
	if peer.PublicKey == "" {
		peer.PublicKey = key
	}
	for i := 4; i < len(args); {
		switch args[i] {
		case "preshared-key":
			if i+1 >= len(args) {
				return errors.New("missing psk path")
			}
			if args[i+1] == "/dev/null" {
				peer.PresharedKey = ""
			} else {
				contents, err := os.ReadFile(args[i+1])
				if err != nil {
					return err
				}
				peer.PresharedKey = strings.TrimSpace(string(contents))
			}
			i += 2
		case "allowed-ips":
			if i+1 >= len(args) {
				return errors.New("missing allowed IPs")
			}
			peer.AllowedIPs = strings.Split(args[i+1], ",")
			i += 2
		case "endpoint":
			if i+1 >= len(args) {
				return errors.New("missing endpoint")
			}
			peer.Endpoint = args[i+1]
			i += 2
		case "persistent-keepalive":
			if i+1 >= len(args) {
				return errors.New("missing keepalive")
			}
			value, err := strconv.ParseUint(args[i+1], 10, 16)
			if err != nil {
				return err
			}
			peer.PersistentKeepalive = uint16(value)
			i += 2
		default:
			return errors.New("unexpected wg set option " + args[i])
		}
	}
	state[key] = peer
	return nil
}

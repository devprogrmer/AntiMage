package nodeagent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteWireGuardUsageConfigReplacesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wg", "usage-helper.json")
	oldWriter := wireGuardUsageConfigWrite
	t.Cleanup(func() { wireGuardUsageConfigWrite = oldWriter })
	called := false
	wireGuardUsageConfigWrite = func(target string, raw []byte, mode os.FileMode) error {
		called = true
		if target != path || mode != 0600 {
			t.Fatalf("atomic write target/mode = %q/%#o", target, mode)
		}
		return atomicWriteFile(target, raw, mode)
	}
	if err := writeWireGuardUsageConfig(path, wireGuardUsageRuntimeConfig{
		InboundTag: "wg-main", InterfaceName: "wg0", ListenPort: 51820,
		Peers: map[string]int64{"peer": 7},
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("atomic writer was not used")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"inbound_tag":"wg-main","interface_name":"wg0","listen_port":51820,"peers":{"peer":7},"callback":{}}` {
		t.Fatalf("persisted helper = %s", got)
	}
}

func TestWriteWireGuardUsageConfigFailurePreservesPreviousFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wg", "usage-helper.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	previous := []byte(`{"inbound_tag":"old","peers":{"peer":7}}`)
	if err := os.WriteFile(path, previous, 0600); err != nil {
		t.Fatal(err)
	}
	oldWriter := wireGuardUsageConfigWrite
	t.Cleanup(func() { wireGuardUsageConfigWrite = oldWriter })
	wireGuardUsageConfigWrite = func(string, []byte, os.FileMode) error {
		return errors.New("injected disk failure")
	}
	if err := writeWireGuardUsageConfig(path, wireGuardUsageRuntimeConfig{
		InboundTag: "new", Peers: map[string]int64{"peer": 9},
	}); err == nil {
		t.Fatal("expected persistence failure")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(previous) {
		t.Fatalf("previous helper changed after failed atomic write: %s", got)
	}
}

package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestNativeHelperCacheChecksContentNotOnlyMetadata(t *testing.T) {
	s := &Server{}
	path := filepath.Join(t.TempDir(), "usage-helper.json")
	write := func(raw string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"inbound_tag":"vpn","peers":{"peer":7}}`)
	first, err := readNativeHelper[wireGuardUsageRuntimeConfig](s, path)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := readNativeHelper[wireGuardUsageRuntimeConfig](s, path)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%p", first.Peers) != fmt.Sprintf("%p", cached.Peers) {
		t.Fatal("unchanged helper decoded again")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same path, size, inode and timestamp: content checksum must invalidate.
	write(`{"inbound_tag":"vpn","peers":{"peer":8}}`)
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	changed, err := readNativeHelper[wireGuardUsageRuntimeConfig](s, path)
	if err != nil || changed.Peers["peer"] != 8 {
		t.Fatalf("same-metadata replacement missed: %v %v", changed, err)
	}
	if first.Peers["peer"] != 7 {
		t.Fatal("previous reader mutated")
	}
	write(`{"peers":`)
	if _, err := readNativeHelper[wireGuardUsageRuntimeConfig](s, path); err == nil {
		t.Fatal("malformed helper fell back to cached policy")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := readNativeHelper[wireGuardUsageRuntimeConfig](s, path); !os.IsNotExist(err) {
		t.Fatalf("deleted helper used cached policy: %v", err)
	}
}

func TestNativeHelperCacheConcurrentReaders(t *testing.T) {
	s := &Server{}
	path := filepath.Join(t.TempDir(), "helper.json")
	if err := os.WriteFile(path, []byte(`{"peers":{"p":7}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				cfg, err := readNativeHelper[wireGuardUsageRuntimeConfig](s, path)
				if err != nil || cfg.Peers["p"] != 7 {
					t.Error("invalid concurrent helper")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestNativeHelperCacheBoundsAndTypeSafety(t *testing.T) {
	s := &Server{}
	root := t.TempDir()
	for i := 0; i < maxNativeHelperCacheEntries+1; i++ {
		path := filepath.Join(root, fmt.Sprint(i)+".json")
		if err := os.WriteFile(path, []byte(`{"peers":{"p":7}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readNativeHelper[wireGuardUsageRuntimeConfig](s, path); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.nativeHelperEntries) != maxNativeHelperCacheEntries || s.nativeHelperBytes > maxNativeHelperCacheBytes {
		t.Fatal("helper cache exceeded capacity")
	}
	if _, ok := s.nativeHelperEntries[filepath.Join(root, "0.json")]; ok {
		t.Fatal("oldest helper not evicted")
	}
	path := filepath.Join(root, "1.json")
	// One path may be decoded into another helper schema without type panic.
	if _, err := readNativeHelper[amneziaWGUsageRuntimeConfig](s, path); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxOfflineAccountingBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	if _, err := readNativeHelper[wireGuardUsageRuntimeConfig](s, path); err == nil {
		t.Fatal("oversized helper fell back to cached content")
	}
}

func BenchmarkNativeHelper1000Peers(b *testing.B) {
	cfg := wireGuardUsageRuntimeConfig{InboundTag: "vpn", Peers: map[string]int64{}, Policies: map[string]nativeSessionUserPolicy{}}
	for i := 0; i < 1000; i++ {
		key := fmt.Sprint(i)
		cfg.Peers[key] = int64(i + 1)
		cfg.Policies[key] = nativeSessionUserPolicy{Status: "active", DataLimit: 100000000, UsedTraffic: 12345}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(b.TempDir(), "usage-helper.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		b.Fatal(err)
	}
	s := &Server{}
	if _, err := readNativeHelper[wireGuardUsageRuntimeConfig](s, path); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := readNativeHelper[wireGuardUsageRuntimeConfig](s, path)
		if err != nil || len(result.Peers) != 1000 {
			b.Fatalf("peers=%d err=%v", len(result.Peers), err)
		}
	}
}

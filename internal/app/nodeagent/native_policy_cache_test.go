package nodeagent

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func BenchmarkIKEv2RuntimePolicy1000Users(b *testing.B) {
	payload := nativeRuntimePayload{IKEv2Inbounds: []ikev2RuntimeInbound{{Tag: "vpn"}}}
	for i := 0; i < 1000; i++ {
		payload.IKEv2Inbounds[0].Users = append(payload.IKEv2Inbounds[0].Users, ikev2RuntimeUser{UserID: int64(i + 1), Username: fmt.Sprint(i), Password: "not-a-real-password"})
		payload.XrayPolicies = append(payload.XrayPolicies, xrayLocalUserPolicy{UserID: int64(i + 1), Email: fmt.Sprint(i)})
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		b.Fatal(err)
	}
	s := New(Config{DataDir: b.TempDir()})
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: string(raw), DesiredRevision: 1}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runtimes, err := s.offlineIKEv2RuntimesForCheckpoint(false)
		if err != nil || len(runtimes) != 1 {
			b.Fatalf("runtimes=%d err=%v", len(runtimes), err)
		}
	}
}

func TestNativeRuntimePolicyCachePublishRestartAndInvalidation(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	publish := func(revision uint64, name string) {
		t.Helper()
		raw := fmt.Sprintf(`{"ikev2_inbounds":[{"tag":%q}],"anyconnect_inbounds":[{"tag":%q}],"xray_policies":[{"email":%q}]}`, name, name, name)
		if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: raw, DesiredRevision: revision}); err != nil {
			t.Fatal(err)
		}
	}
	publish(1, "first")
	first, err := s.runtimePolicy.nativePayload()
	if err != nil {
		t.Fatal(err)
	}
	cached, err := s.runtimePolicy.nativePayload()
	if err != nil || &first.IKEv2Inbounds[0] != &cached.IKEv2Inbounds[0] {
		t.Fatal("published snapshot was decoded again")
	}
	// Same revision, changed content/checksum must not reuse the old payload.
	publish(1, "changed")
	next, err := s.runtimePolicy.nativePayload()
	if err != nil || next.IKEv2Inbounds[0].Tag != "changed" || &first.IKEv2Inbounds[0] == &next.IKEv2Inbounds[0] {
		t.Fatal("changed checksum reused old payload")
	}
	if first.IKEv2Inbounds[0].Tag != "first" {
		t.Fatal("publishing policy mutated previous snapshot")
	}
	restarted := New(s.cfg)
	snapshot, err := restarted.loadRuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := snapshot.nativePayload()
	if err != nil || loaded.AnyConnectInbounds[0].Tag != "changed" {
		t.Fatal("restart cache missing validated payload")
	}
	if err := restarted.persistStoppedRuntimePolicy(); err != nil {
		t.Fatal(err)
	}
	stopped, err := restarted.runtimePolicy.nativePayload()
	if err != nil || &stopped.IKEv2Inbounds[0] != &loaded.IKEv2Inbounds[0] {
		t.Fatal("stop reparsed or lost payload")
	}
}

func TestNativeRuntimePolicyCacheConcurrentReaders(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: `{"ikev2_inbounds":[{"tag":"vpn"}]}`, DesiredRevision: 1}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				snapshot, err := s.currentRuntimePolicy()
				if err != nil {
					t.Error(err)
					return
				}
				payload, err := snapshot.nativePayload()
				if err != nil || payload.IKEv2Inbounds[0].Tag != "vpn" {
					t.Error("invalid cached policy")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestNativeRuntimePolicyCacheInvalidInputKeepsPublishedSnapshot(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: `{"ikev2_inbounds":[{"tag":"vpn"}]}`, DesiredRevision: 1}); err != nil {
		t.Fatal(err)
	}
	before := s.runtimePolicy
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: `{"ikev2_inbounds":`, DesiredRevision: 2}); err == nil {
		t.Fatal("invalid policy accepted")
	}
	if s.runtimePolicy != before {
		t.Fatal("invalid policy replaced published snapshot")
	}
	loaded, err := s.loadRuntimePolicy()
	if err != nil || loaded.Revision != 1 {
		t.Fatal("invalid input changed durable policy")
	}
}

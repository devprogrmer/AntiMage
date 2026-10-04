package nodeagent

import (
	"testing"
)

func TestIKEv2IKERekeyPreservesChildCounterGeneration(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	oldBoot := offlineReadBootID
	offlineReadBootID = func() (string, error) { return "boot-a", nil }
	t.Cleanup(func() { offlineReadBootID = oldBoot })
	in := ikev2RuntimeInbound{Tag: "vpn", Users: []ikev2RuntimeUser{{UserID: 7, Username: "client"}}}
	runtimes := map[string]ikev2RuntimeInbound{ikev2ConnectionName("vpn"): in}
	sa := ikev2RawSA{ConnectionName: ikev2ConnectionName("vpn"), UniqueID: "1", InitiatorSPI: "aa", ResponderSPI: "bb", RemoteID: "client", State: "ESTABLISHED", Children: []ikev2RawChildSA{{UniqueID: "1", SPIIn: "11", SPIOut: "22", BytesIn: 252, BytesOut: 252}}}
	first, err := s.previewIKEv2OfflineLocked(runtimes, []ikev2RawSA{sa})
	if err != nil {
		t.Fatal(err)
	}
	s.ikev2UsageBaseline = first
	sa.UniqueID = "2"
	sa.InitiatorSPI = "cc"
	sa.ResponderSPI = "dd"
	sa.Children[0].BytesIn = 504
	sa.Children[0].BytesOut = 504
	next, err := s.previewIKEv2OfflineLocked(runtimes, []ikev2RawSA{sa})
	if err != nil {
		t.Fatal(err)
	}
	if got := next[offlineOwnerKey(offlineUsageOwner{7, "vpn"})]; got != 1008 {
		t.Fatalf("IKE rekey recharged existing child bytes: got %d want 1008", got)
	}
}

func TestIKEv2IKERekeyMigratesPersistedGeneration(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	oldBoot := offlineReadBootID
	offlineReadBootID = func() (string, error) { return "boot-a", nil }
	t.Cleanup(func() { offlineReadBootID = oldBoot })
	boot, err := offlineReadBootID()
	if err != nil {
		t.Skip(err)
	}
	owner := offlineUsageOwner{7, "vpn"}
	s.ikev2UsageBaseline = map[string]uint64{offlineGeneration(boot, "vpn", "1", "aa", "bb", "1", "11", "22"): 504, offlineOwnerKey(owner): 504}
	in := ikev2RuntimeInbound{Tag: "vpn", Users: []ikev2RuntimeUser{{UserID: 7, Username: "client"}}}
	sa := ikev2RawSA{ConnectionName: ikev2ConnectionName("vpn"), UniqueID: "2", InitiatorSPI: "cc", ResponderSPI: "dd", RemoteID: "client", State: "ESTABLISHED", Children: []ikev2RawChildSA{{UniqueID: "1", SPIIn: "11", SPIOut: "22", BytesIn: 504, BytesOut: 504}}}
	next, err := s.previewIKEv2OfflineLocked(map[string]ikev2RuntimeInbound{sa.ConnectionName: in}, []ikev2RawSA{sa})
	if err != nil {
		t.Fatal(err)
	}
	if got := next[offlineOwnerKey(owner)]; got != 1008 {
		t.Fatalf("migration recharged child bytes: got %d want 1008", got)
	}
}

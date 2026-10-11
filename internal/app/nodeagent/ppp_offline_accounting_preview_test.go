package nodeagent

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type offlineFileSnapshot struct {
	Hash    [32]byte
	ModTime time.Time
	Mode    fs.FileMode
}

func snapshotOfflineFiles(t *testing.T, root string) map[string]offlineFileSnapshot {
	t.Helper()
	result := map[string]offlineFileSnapshot{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result[path] = offlineFileSnapshot{sha256.Sum256(raw), info.ModTime(), info.Mode()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOpenVPNOfflineNormalQuotaPreviewDoesNotWrite(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	tag := "preview"
	root := filepath.Join(s.cfg.DataDir, "openvpn", openVPNRuntimeDirName(tag))
	if err := offlineDurableJSON(filepath.Join(root, "usage-helper.json"), openVPNUsageRuntimeConfig{InboundTag: tag, Users: map[string]int64{"alice": 42}}); err != nil {
		t.Fatal(err)
	}
	if err := offlineDurableJSON(filepath.Join(root, "session-helper.json"), nativeSessionHelperConfig{InboundTag: tag, Users: map[string]int64{"alice": 42}, Policies: map[string]nativeSessionUserPolicy{"alice": {Status: "active", DataLimit: 10000}}}); err != nil {
		t.Fatal(err)
	}
	status := "HEADER\tCLIENT_LIST\tUsername\tClient ID\tConnected Since (time_t)\tBytes Received\tBytes Sent\nCLIENT_LIST\talice\t7\t100\t25\t25\nCLIENT_LIST\talice\t8\t101\t25\t25\n"
	if err := os.WriteFile(filepath.Join(root, "status.tsv"), []byte(status), 0600); err != nil {
		t.Fatal(err)
	}
	before := snapshotOfflineFiles(t, s.cfg.DataDir)
	originalRead := openVPNOfflineReadStatus
	scans := 0
	openVPNOfflineReadStatus = func(path string) ([]byte, error) { scans++; return originalRead(path) }
	t.Cleanup(func() { openVPNOfflineReadStatus = originalRead })
	for i := 0; i < 3; i++ {
		if err := s.quotaCheckOpenVPNOffline(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if scans != 3 {
		t.Fatalf("expected one status scan per tick; got %d", scans)
	}
	if !reflect.DeepEqual(before, snapshotOfflineFiles(t, s.cfg.DataDir)) {
		t.Fatal("normal quota tick wrote files")
	}
	preview, err := s.previewOpenVPNOffline(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if preview.Baseline[offlineAccountingTotalKey(42, tag)] != 100 {
		t.Fatal("aggregate preview omitted simultaneous sessions")
	}
	if len(s.openVPNUsageBaseline) != 0 || s.openVPNUsagePending != nil {
		t.Fatal("preview mutated durable accounting state")
	}
}

func TestPPPOfflineNormalQuotaPreviewDoesNotWrite(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	for _, protocol := range []string{"l2tp", "pptp"} {
		peer := "10.0.0.2"
		if protocol == "pptp" {
			peer = "10.0.1.2"
		}
		root := filepath.Join(s.cfg.DataDir, protocol, "preview")
		if err := offlineDurableJSON(filepath.Join(root, "usage-helper.json"), map[string]any{"inbound_tag": protocol, "users": map[string]int64{peer: 42}}); err != nil {
			t.Fatal(err)
		}
		if err := offlineDurableJSON(filepath.Join(root, "session-helper.json"), nativeSessionHelperConfig{InboundTag: protocol, Users: map[string]int64{"alice": 42}, Policies: map[string]nativeSessionUserPolicy{"alice": {Status: "active", DataLimit: 10000}}}); err != nil {
			t.Fatal(err)
		}
	}
	originalQuery, originalCounter, originalIdentity := pppOfflineQuery, pppOfflineReadCounter, pppOfflineReadIdentity
	queries := 0
	pppOfflineQuery = func(context.Context) ([]byte, error) {
		queries++
		return []byte("20: ppp0 inet 10.0.0.1 peer 10.0.0.2/32 scope global ppp0\n21: ppp1 inet 10.0.1.1 peer 10.0.1.2/32 scope global ppp1"), nil
	}
	pppOfflineReadCounter = func(string, string) (uint64, error) { return 50, nil }
	pppOfflineReadIdentity = func(iface string) (string, error) { return "fixture-boot:" + iface, nil }
	t.Cleanup(func() {
		pppOfflineQuery = originalQuery
		pppOfflineReadCounter = originalCounter
		pppOfflineReadIdentity = originalIdentity
	})
	before := snapshotOfflineFiles(t, s.cfg.DataDir)
	for i := 0; i < 3; i++ {
		if err := s.quotaCheckPPPOffline(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if queries != 3 {
		t.Fatalf("expected one shared PPP query per tick; got %d", queries)
	}
	if !reflect.DeepEqual(before, snapshotOfflineFiles(t, s.cfg.DataDir)) {
		t.Fatal("normal PPP quota wrote files")
	}
	previews, err := s.previewPPPOffline(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{"l2tp", "pptp"} {
		if previews[protocol].Baseline[offlineAccountingTotalKey(42, protocol)] != 100 {
			t.Fatalf("%s preview omitted live traffic", protocol)
		}
	}
	if len(s.l2TPUsageBaseline) != 0 || len(s.pptpUsageBaseline) != 0 {
		t.Fatal("PPP preview mutated accounting state")
	}
}

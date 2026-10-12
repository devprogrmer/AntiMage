//go:build linux

package nodeagent

import (
	"context"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestGeoCommittedLostACKResumesOnlyReloadAndVerifiesFiles(t *testing.T) {
	oldGOOS, oldEUID, oldLookup, oldClient := maintenanceGOOS, maintenanceEUID, geoLookupIP, geoHTTPClient
	maintenanceGOOS = "linux"
	maintenanceEUID = func() int { return 0 }
	geoLookupIP = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("1.1.1.1")}, nil }
	t.Cleanup(func() {
		maintenanceGOOS, maintenanceEUID, geoLookupIP, geoHTTPClient = oldGOOS, oldEUID, oldLookup, oldClient
	})
	app := t.TempDir()
	t.Setenv("ANTIMAGE_NODE_APP_DIR", app)
	var downloads atomic.Int32
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		_, _ = w.Write([]byte("dataset-" + r.URL.Path))
	}))
	defer source.Close()
	geoHTTPClient = source.Client
	script := filepath.Join(app, "xray")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo 'Xray 1.2.3'; exit 0; fi\nexec sleep 120\n"), 0755); err != nil {
		t.Fatal(err)
	}
	s := New(Config{InstallMode: "binary", DataDir: app, XrayAssetsDir: app, XrayPath: script})
	t.Cleanup(func() { _ = s.stopRuntime() })
	fence := &nodev1.DestructiveFence{OperationId: "geo-op", CommandId: "geo-command", ResourceId: "1", ResourceGeneration: 51, LeaseGeneration: 1}
	req := &nodev1.GeoUpdateRequest{OperationId: fence.OperationId, Fence: fence, Files: []*nodev1.GeoFile{{Name: "geoip.dat", Url: source.URL + "/geoip.dat"}, {Name: "geosite.dat", Url: source.URL + "/geosite.dat"}}}
	// Commit production files while no runtime config is available. The operation
	// fails after commit, leaving exactly the reload step for recovery.
	if _, err := s.UpdateGeo(context.Background(), req); err == nil {
		t.Fatal("missing reload runtime reported as success")
	}
	if downloads.Load() != 2 {
		t.Fatal("expected two actual HTTP downloads")
	}
	healthReq := &nodev1.HealthRequest{OperationId: fence.OperationId, CommandId: fence.CommandId}
	before, err := s.Health(context.Background(), healthReq)
	if err != nil || before.GetRuntime().GetGeoTransactionPhase() != "files_committed" || before.GetRuntime().GetGeoDatasetSha256() == "" || before.GetRuntime().GetGeoReloadVerified() {
		t.Fatalf("committed evidence: %v %v", before, err)
	}
	transactionPath, err := geoTransactionPath(app, fence.OperationId)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := readGeoTransaction(transactionPath)
	if err != nil {
		t.Fatal(err)
	}
	transaction.Phase = "replacement_ready"
	if err := writeGeoTransaction(transactionPath, transaction); err != nil {
		t.Fatal(err)
	}
	before, err = s.Health(context.Background(), healthReq)
	if err != nil || before.GetRuntime().GetGeoTransactionPhase() != "files_committed" || before.GetRuntime().GetGeoDatasetSha256() == "" {
		t.Fatalf("lost file-commit marker was not reconciled from actual bytes: %v %v", before, err)
	}
	ipStat, err := os.Stat(filepath.Join(app, "geoip.dat"))
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(app, "runtime.json")
	if err := os.WriteFile(config, []byte(`{"inbounds":[],"outbounds":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.lastConfig = config
	s.mu.Unlock()
	reloadFence := &nodev1.DestructiveFence{OperationId: fence.OperationId, CommandId: fence.CommandId + "-reload", ResourceId: "1", ResourceGeneration: 52, LeaseGeneration: 2}
	// Discard reload ACK; inspect actual runtime and original command identity.
	if _, err := s.UpdateGeo(context.Background(), &nodev1.GeoUpdateRequest{OperationId: fence.OperationId, Fence: reloadFence, ResumeReloadOnly: true}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Health(context.Background(), healthReq)
	if err != nil || !after.GetRuntime().GetGeoReloadVerified() || after.GetRuntime().GetGeoTransactionPhase() != "reload_completed" || after.GetRuntime().GetEvidenceResourceGeneration() != 51 || after.GetRuntime().GetCurrentResourceGeneration() != 52 {
		t.Fatalf("reloaded evidence: %v %v", after, err)
	}
	current, err := os.Stat(filepath.Join(app, "geoip.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 2 || !os.SameFile(ipStat, current) || !ipStat.ModTime().Equal(current.ModTime()) {
		t.Fatal("reload downloaded or recommitted datasets")
	}
	processStart := after.Runtime.CoreProcessStartedAtUnixNano
	if _, err := s.UpdateGeo(context.Background(), req); err == nil {
		t.Fatal("full Geo replay allowed")
	}
	if _, err := s.UpdateGeo(context.Background(), &nodev1.GeoUpdateRequest{OperationId: fence.OperationId, Fence: reloadFence, ResumeReloadOnly: true}); err == nil {
		t.Fatal("reload replay allowed")
	}
	// Production startup evidence handles activation completed before the
	// reload_completed marker write, without another activation.
	txPath, err := geoTransactionPath(app, fence.OperationId)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := readGeoTransaction(txPath)
	if err != nil {
		t.Fatal(err)
	}
	tx.Phase = "reload_dispatched"
	if err := writeGeoTransaction(txPath, tx); err != nil {
		t.Fatal(err)
	}
	after, err = s.Health(context.Background(), healthReq)
	if err != nil || !after.GetRuntime().GetGeoReloadVerified() || after.Runtime.CoreProcessStartedAtUnixNano != processStart {
		t.Fatalf("lost transaction ACK: %v %v", after, err)
	}
	// Checksum disagreement is explicitly ambiguous; neither reload nor success.
	if err := os.WriteFile(filepath.Join(app, "geoip.dat"), []byte("changed-by-other-owner"), 0644); err != nil {
		t.Fatal(err)
	}
	after, err = s.Health(context.Background(), healthReq)
	if err != nil || after.GetRuntime().GetGeoTransactionPhase() != "identity_mismatch" || after.GetRuntime().GetGeoReloadVerified() {
		t.Fatalf("corrupt identity accepted: %v %v", after, err)
	}
	if downloads.Load() != 2 {
		t.Fatal("health reconciliation performed a download")
	}
}

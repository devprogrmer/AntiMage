package nodeagent

import (
	"context"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"path/filepath"
	"testing"
)

func TestOfflineUnconfiguredOpenVPNAndPPPDoNotCreateBatches(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	ctx := context.Background()
	collectors := []struct {
		name    string
		collect func(context.Context, *nodev1.CollectUsageRequest) (*nodev1.UserUsageBatch, error)
	}{
		{"openvpn", s.collectOpenVPNUserUsage}, {"l2tp", s.collectL2TPUserUsage}, {"pptp", s.collectPPTPUserUsage},
	}
	for _, collector := range collectors {
		for i := 0; i < 2; i++ {
			batch, err := collector.collect(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if batch.BatchId != "" || len(batch.Stats) != 0 {
				t.Fatalf("%s created empty-owner batch: %v", collector.name, batch)
			}
		}
	}
	if s.openVPNUsagePending != nil || s.l2TPUsagePending != nil || s.pptpUsagePending != nil {
		t.Fatal("spurious pending")
	}
}

func TestOfflineConfiguredUnknownOwnersDoNotCreateBatches(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	for _, protocol := range []string{"openvpn", "l2tp", "pptp"} {
		if err := offlineDurableJSON(filepath.Join(s.cfg.DataDir, protocol, "empty", "usage-helper.json"), map[string]any{"inbound_tag": "empty", "users": map[string]int64{}}); err != nil {
			t.Fatal(err)
		}
	}
	previousQuery := pppOfflineQuery
	pppOfflineQuery = func(context.Context) ([]byte, error) {
		return []byte("20: ppp0 inet 10.0.0.1 peer 10.0.0.2/32 scope global ppp0"), nil
	}
	t.Cleanup(func() { pppOfflineQuery = previousQuery })
	for _, collect := range []func(context.Context, *nodev1.CollectUsageRequest) (*nodev1.UserUsageBatch, error){s.collectOpenVPNUserUsage, s.collectL2TPUserUsage, s.collectPPTPUserUsage} {
		batch, err := collect(context.Background(), nil)
		if err != nil || batch.GetBatchId() != "" || len(batch.GetStats()) != 0 {
			t.Fatalf("no mapped owners: %v %v", batch, err)
		}
	}
}

func TestOfflineFilteredCredentialsRetainLegacyPendingUsage(t *testing.T) {
	for _, protocol := range []string{"openvpn", "l2tp", "pptp"} {
		t.Run(protocol, func(t *testing.T) {
			dir := t.TempDir()
			s := New(Config{DataDir: dir})
			id := protocol + "-filtered-pending"
			if err := offlineDurableJSON(filepath.Join(dir, protocol, "filtered", "usage-helper.json"), map[string]any{"inbound_tag": "tag", "users": map[string]int64{}}); err != nil {
				t.Fatal(err)
			}
			switch protocol {
			case "openvpn":
				s.openVPNUsageLoaded = true
				s.openVPNUsagePending = &openVPNUsagePendingBatch{BatchID: id, Samples: []openVPNUsageSample{{UserID: 42, InboundTag: "tag", Value: 500}}}
				if err := s.persistOpenVPNUsageStateLocked(); err != nil {
					t.Fatal(err)
				}
			case "l2tp":
				s.l2TPUsageLoaded = true
				s.l2TPUsagePending = &l2TPUsagePendingBatch{BatchID: id, Samples: []l2TPUsageSample{{UserID: 42, InboundTag: "tag", Value: 500}}}
				if err := s.persistL2TPUsageStateLocked(); err != nil {
					t.Fatal(err)
				}
			case "pptp":
				s.pptpUsageLoaded = true
				s.pptpUsagePending = &pptpUsagePendingBatch{BatchID: id, Samples: []pptpUsageSample{{UserID: 42, InboundTag: "tag", Value: 500}}}
				if err := s.persistPPTPUsageStateLocked(); err != nil {
					t.Fatal(err)
				}
			}
			s = New(Config{DataDir: dir})
			raw, err := s.durablePolicyRaw(protocol, 42, "tag", "")
			if err != nil || raw != 500 {
				t.Fatalf("activation guard lost legacy pending: %d %v", raw, err)
			}
			var batch *nodev1.UserUsageBatch
			switch protocol {
			case "openvpn":
				batch, err = s.collectOpenVPNUserUsage(context.Background(), nil)
			case "l2tp":
				batch, err = s.collectL2TPUserUsage(context.Background(), nil)
			case "pptp":
				batch, err = s.collectPPTPUserUsage(context.Background(), nil)
			}
			if err != nil || batch.GetBatchId() != id || len(batch.GetStats()) != 1 || batch.GetStats()[0].GetValue() != 500 {
				t.Fatalf("filtered pending uncollectable: %v %v", batch, err)
			}
		})
	}
}

package nodeagent

import (
	"context"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"testing"
)

func TestPPPOfflineACKRetainsNewerTotalsAfterRestart(t *testing.T) {
	for _, protocol := range []string{"l2tp", "pptp"} {
		t.Run(protocol, func(t *testing.T) {
			dir := t.TempDir()
			s := New(Config{DataDir: dir})
			key := offlineAccountingTotalKey(42, "ppp")
			snapshot := map[string]uint64{key: 100, "ppp|ppp0|peer\x00boot:1": 100}
			current := map[string]uint64{key: 350, "ppp|ppp0|peer\x00boot:1": 100, "ppp|ppp0|peer\x00boot:2": 250}
			id := protocol + "-fixture"
			if protocol == "l2tp" {
				s.l2TPUsageLoaded = true
				s.l2TPUsageBaseline = current
				s.l2TPUsagePending = &l2TPUsagePendingBatch{BatchID: id, NextBaseline: snapshot, Samples: []l2TPUsageSample{{UserID: 42, InboundTag: "ppp", Value: 100}}}
				if err := s.persistL2TPUsageStateLocked(); err != nil {
					t.Fatal(err)
				}
			} else {
				s.pptpUsageLoaded = true
				s.pptpUsageBaseline = current
				s.pptpUsagePending = &pptpUsagePendingBatch{BatchID: id, NextBaseline: snapshot, Samples: []pptpUsageSample{{UserID: 42, InboundTag: "ppp", Value: 100}}}
				if err := s.persistPPTPUsageStateLocked(); err != nil {
					t.Fatal(err)
				}
			}
			s = New(Config{DataDir: dir})
			var baseline map[string]uint64
			for i := 0; i < 2; i++ {
				var response *nodev1.AckUsageResponse
				var err error
				if protocol == "l2tp" {
					response, err = s.ackL2TPUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: id})
					baseline = s.l2TPUsageBaseline
				} else {
					response, err = s.ackPPTPUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: id})
					baseline = s.pptpUsageBaseline
				}
				if err != nil || !response.Acknowledged {
					t.Fatalf("ACK %v %v", response, err)
				}
				if baseline[key]-baseline[offlineAccountingSentKey(key)] != 250 {
					t.Fatalf("new usage lost: %v", baseline)
				}
			}
		})
	}
}

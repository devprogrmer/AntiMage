package api

import (
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/nodecontroller"
)

func TestAccountingAlertTrackerSuppressesRepeatsAndPreservesUnknownNodes(t *testing.T) {
	var tracker accountingAlertTracker
	now := time.Unix(1_700_000_000, 0)
	alert := nodecontroller.AccountingAlert{NodeID: 4, NodeName: "node", Protocol: "xray", Kind: "pending_ack_overdue"}
	if got := tracker.observe([]int64{4}, []nodecontroller.AccountingAlert{alert, alert}, now); len(got) != 1 {
		t.Fatalf("first observation = %#v", got)
	}
	if got := tracker.observe([]int64{4}, []nodecontroller.AccountingAlert{alert}, now.Add(time.Minute)); len(got) != 1 {
		t.Fatalf("failed delivery must retry: %#v", got)
	}
	tracker.markSent(alert, now.Add(time.Minute))
	if got := tracker.observe([]int64{4}, []nodecontroller.AccountingAlert{alert}, now.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("repeated observation = %#v", got)
	}
	if got := tracker.observe(nil, nil, now.Add(2*time.Minute)); len(got) != 0 {
		t.Fatalf("missing sample = %#v", got)
	}
	if got := tracker.observe([]int64{4}, []nodecontroller.AccountingAlert{alert}, now.Add(time.Hour+time.Minute)); len(got) != 1 {
		t.Fatalf("hourly reminder = %#v", got)
	}
	tracker.markSent(alert, now.Add(time.Hour+time.Minute))
	tracker.observe([]int64{4}, nil, now.Add(time.Hour+time.Minute))
	if got := tracker.observe([]int64{4}, []nodecontroller.AccountingAlert{alert}, now.Add(time.Hour+2*time.Minute)); len(got) != 1 {
		t.Fatalf("new fault after recovery = %#v", got)
	}
}

package api

import (
	"sync"
	"time"

	"github.com/antimage/antimage/internal/app/nodecontroller"
)

const accountingAlertRepeatInterval = time.Hour

type accountingAlertKey struct {
	nodeID   int64
	protocol string
	kind     string
}

type accountingAlertTracker struct {
	mu       sync.Mutex
	lastSent map[accountingAlertKey]time.Time
}

// A failed or absent node sample is not a recovery. Only a successful sample
// clears resolved alerts. Persistent faults are repeated at most hourly.
func (t *accountingAlertTracker) observe(checked []int64, alerts []nodecontroller.AccountingAlert, now time.Time) []nodecontroller.AccountingAlert {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastSent == nil {
		t.lastSent = make(map[accountingAlertKey]time.Time)
	}
	observed := make(map[int64]struct{}, len(checked))
	for _, nodeID := range checked {
		observed[nodeID] = struct{}{}
	}
	active := make(map[accountingAlertKey]struct{}, len(alerts))
	var send []nodecontroller.AccountingAlert
	for _, alert := range alerts {
		if _, ok := observed[alert.NodeID]; !ok {
			continue
		}
		key := accountingAlertKey{alert.NodeID, alert.Protocol, alert.Kind}
		if _, duplicate := active[key]; duplicate {
			continue
		}
		active[key] = struct{}{}
		if last, exists := t.lastSent[key]; !exists || now.Sub(last) >= accountingAlertRepeatInterval {
			send = append(send, alert)
		}
	}
	for key := range t.lastSent {
		if _, ok := observed[key.nodeID]; !ok {
			continue
		}
		if _, ok := active[key]; !ok {
			delete(t.lastSent, key)
		}
	}
	return send
}

func (t *accountingAlertTracker) markSent(alert nodecontroller.AccountingAlert, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastSent == nil {
		t.lastSent = make(map[accountingAlertKey]time.Time)
	}
	t.lastSent[accountingAlertKey{alert.NodeID, alert.Protocol, alert.Kind}] = now
}

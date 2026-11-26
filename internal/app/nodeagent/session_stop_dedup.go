package nodeagent

import (
	"crypto/sha256"
	"time"
)

const (
	maxNativeSessionReceipts = 16384
	nativeSessionStopRefresh = 5 * time.Minute
)

type nativeSessionReceiptKey struct {
	URL, Protocol, InboundTag, SessionID string
	NodeID, UserID                       int64
	Token                                [32]byte
}

type nativeSessionReceipt struct {
	gate        chan struct{}
	lastEvent   nativeSessionEvent
	lastSuccess time.Time
}

// Periodic WG/AWG observations include stopped peers. Remember only successful
// callbacks so an unchanged stop is not written to the panel on every poll.
// A seen/start, changed payload, destination or credential always sends again.
// Entries are bounded; after saturation new sessions fall back to normal sends.
func (s *Server) nativeSessionReceiptFor(callback nativeRuntimeSessionCallback, event nativeSessionEvent) *nativeSessionReceipt {
	key := nativeSessionReceiptKey{
		URL: callback.URL, Token: sha256.Sum256([]byte(callback.Token)),
		NodeID: callback.NodeID, UserID: event.UserID, Protocol: event.Protocol,
		InboundTag: event.InboundTag, SessionID: event.SessionID,
	}
	s.nativeSessionReceiptMu.Lock()
	defer s.nativeSessionReceiptMu.Unlock()
	if receipt := s.nativeSessionReceipts[key]; receipt != nil {
		return receipt
	}
	if len(s.nativeSessionReceipts) >= maxNativeSessionReceipts {
		return nil
	}
	if s.nativeSessionReceipts == nil {
		s.nativeSessionReceipts = make(map[nativeSessionReceiptKey]*nativeSessionReceipt)
	}
	receipt := &nativeSessionReceipt{gate: make(chan struct{}, 1)}
	s.nativeSessionReceipts[key] = receipt
	return receipt
}

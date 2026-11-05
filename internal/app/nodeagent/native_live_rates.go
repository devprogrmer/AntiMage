package nodeagent

import (
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

const nativeRateWindow = time.Second
const nativeRateTTL = 5 * time.Second

// Display-only raw byte counters. They never participate in billing or ACKs.
// Upload is traffic received from the client; download is traffic sent to it.
type nativeTrafficCounter struct {
	ID, Generation   string
	UserID           int64
	Upload, Download uint64
}

type nativeTrafficBaseline struct {
	nativeTrafficCounter
	seen time.Time
}

type nativeTrafficScopeKey struct{ protocol, tag string }
type nativeTrafficRate struct{ upload, download uint64 }
type nativeTrafficScope struct {
	at      time.Time
	sources map[string]nativeTrafficBaseline
	rates   map[int64]nativeTrafficRate
}

func (s *Server) nativeTrafficDue(protocol, tag string, at time.Time) bool {
	s.nativeLiveMu.Lock()
	defer s.nativeLiveMu.Unlock()
	previous := s.nativeLiveRates[nativeTrafficScopeKey{protocol, tag}]
	return previous == nil || at.Sub(previous.at) >= nativeRateWindow
}

// The caller supplies a complete, successfully read inbound snapshot. Failed
// scans must not replace it with an empty snapshot. Old snapshots expire anyway.
func (s *Server) observeNativeTraffic(protocol, tag string, at time.Time, counters []nativeTrafficCounter) {
	s.nativeLiveMu.Lock()
	defer s.nativeLiveMu.Unlock()
	key := nativeTrafficScopeKey{protocol, tag}
	scope := s.nativeLiveRates[key]
	if scope != nil && at.Sub(scope.at) < nativeRateWindow {
		return
	}
	if scope == nil {
		s.pruneNativeTrafficLocked(at)
		if len(s.nativeLiveRates) >= 1024 || len(counters) > maxAccountingCounterSeries {
			return
		}
		if s.nativeLiveRates == nil {
			s.nativeLiveRates = make(map[nativeTrafficScopeKey]*nativeTrafficScope)
		}
		scope = &nativeTrafficScope{sources: make(map[string]nativeTrafficBaseline, len(counters)), rates: make(map[int64]nativeTrafficRate)}
		s.nativeLiveRates[key] = scope
	}
	elapsed := at.Sub(scope.at)
	continuous := !scope.at.IsZero() && elapsed <= nativeRateTTL
	clear(scope.rates)
	for _, current := range counters {
		if current.UserID <= 0 || current.ID == "" {
			continue
		}
		previous, exists := scope.sources[current.ID]
		if exists && previous.seen.Equal(at) {
			continue
		} // Duplicate native row.
		if !exists && s.nativeLiveSeries >= maxAccountingCounterSeries {
			continue
		}
		rate := scope.rates[current.UserID]
		if continuous && exists && current.UserID == previous.UserID && current.Generation == previous.Generation && current.Upload >= previous.Upload && current.Download >= previous.Download {
			rate.upload = nativeRateAdd(rate.upload, nativeBytesPerSecond(current.Upload-previous.Upload, elapsed))
			rate.download = nativeRateAdd(rate.download, nativeBytesPerSecond(current.Download-previous.Download, elapsed))
		}
		scope.rates[current.UserID] = rate
		if !exists {
			s.nativeLiveSeries++
		}
		scope.sources[current.ID] = nativeTrafficBaseline{current, at}
	}
	for id, previous := range scope.sources {
		if !previous.seen.Equal(at) {
			delete(scope.sources, id)
			s.nativeLiveSeries--
		}
	}
	scope.at = at
}

func nativeBytesPerSecond(delta uint64, elapsed time.Duration) uint64 {
	// elapsed >= one second; the 128-bit division cannot overflow, even when
	// the native byte counter is near uint64's limit.
	hi, lo := bits.Mul64(delta, uint64(time.Second))
	value, _ := bits.Div64(hi, lo, uint64(elapsed))
	return value
}

func nativeRateAdd(a, b uint64) uint64 {
	if ^uint64(0)-a < b {
		return ^uint64(0)
	}
	return a + b
}

func (s *Server) observeNativePeerTraffic(protocol, tag, identity string, owners map[string]int64, generations map[string]string, peers []wireGuardPeerCounters, at time.Time) {
	if !s.nativeTrafficDue(protocol, tag, at) {
		return
	}
	counters := make([]nativeTrafficCounter, 0, len(peers))
	for _, peer := range peers {
		key := strings.TrimSpace(peer.PublicKey)
		if userID := owners[key]; userID > 0 {
			counters = append(counters, nativeTrafficCounter{key, identity + "\x00" + generations[key], userID, peer.ReceivedBytes, peer.SentBytes})
		}
	}
	s.observeNativeTraffic(protocol, tag, at, counters)
}

func (s *Server) pruneNativeTrafficLocked(at time.Time) {
	for key, scope := range s.nativeLiveRates {
		if at.Sub(scope.at) > nativeRateTTL {
			s.nativeLiveSeries -= len(scope.sources)
			delete(s.nativeLiveRates, key)
		}
	}
}

func (s *Server) nativeTrafficSpeeds(at time.Time) []*nodev1.UserTrafficSpeed {
	s.nativeLiveMu.Lock()
	defer s.nativeLiveMu.Unlock()
	s.pruneNativeTrafficLocked(at)
	totals := make(map[string]nativeTrafficRate)
	for key, scope := range s.nativeLiveRates {
		for userID, rate := range scope.rates {
			uid := key.protocol + ":" + strconv.FormatInt(userID, 10)
			previous := totals[uid]
			totals[uid] = nativeTrafficRate{nativeRateAdd(previous.upload, rate.upload), nativeRateAdd(previous.download, rate.download)}
		}
	}
	out := make([]*nodev1.UserTrafficSpeed, 0, len(totals))
	for uid, rate := range totals {
		out = append(out, &nodev1.UserTrafficSpeed{Uid: uid, Upload: rate.upload, Download: rate.download})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Uid < out[j].Uid })
	return out
}

func nativeLiveRateUID(uid string) bool {
	protocol, _, _ := strings.Cut(uid, ":")
	switch protocol {
	case "wireguard", "amneziawg", "openvpn", "l2tp", "pptp", "ikev2":
		return true
	}
	return false
}

func (s *Server) withNativeTrafficSpeeds(batch *nodev1.UserUsageBatch, at time.Time) *nodev1.UserUsageBatch {
	if batch == nil {
		return nil
	}
	// Build a new response without copying accounting data or modifying the
	// immutable pending batch. Live display samples deliberately bypass ACKs.
	out := &nodev1.UserUsageBatch{BatchId: batch.BatchId, Stats: batch.Stats, OnlineIps: batch.OnlineIps}
	out.ProtoReflect().SetUnknown(batch.ProtoReflect().GetUnknown())
	for _, rate := range batch.Speeds {
		if !nativeLiveRateUID(rate.GetUid()) {
			out.Speeds = append(out.Speeds, rate)
		}
	}
	out.Speeds = append(out.Speeds, s.nativeTrafficSpeeds(at)...)
	return out
}

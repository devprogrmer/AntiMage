package networkintelligence

import (
	"errors"
	"sync"
	"time"
)

type tcpSegment struct {
	start, end uint32
	at         time.Time
	ambiguous  bool
}

type tcpDirection struct {
	segments []tcpSegment
	lastACK  uint32
	hasACK   bool
}

// TCPQuality describes one observed connection. RTT samples measure the ACK
// round trip from the observation point. They do not measure in-game latency.
// Capture gaps and offload mean retransmissions and duplicate ACKs are observed
// indicators. Neither indicator establishes packet loss.
type TCPQuality struct {
	mu                                      sync.Mutex
	capacity                                int
	directions                              [2]tcpDirection
	last                                    time.Time
	retransmissions, duplicateACKs, dropped uint64
}

type TCPQualityResult struct {
	ACKRTT                  []time.Duration
	ObservedRetransmissions uint64
	ObservedDuplicateACKs   uint64
	DiscardedSegments       uint64
}

func NewTCPQuality(capacity int) (*TCPQuality, error) {
	if capacity < 1 || capacity > 65536 {
		return nil, errors.New("TCP outstanding capacity must be between 1 and 65536")
	}
	return &TCPQuality{capacity: capacity}, nil
}

func seqBefore(a, b uint32) bool { return int32(a-b) < 0 }

// Observe takes packets in capture order for one normalized connection.
// direction selects one of its two endpoints. Stale timestamps are rejected.
// Outstanding sequence spans must stay below half the TCP sequence space.
func (q *TCPQuality) Observe(p PacketMetadata, direction int, at time.Time) TCPQualityResult {
	q.mu.Lock()
	defer q.mu.Unlock()
	result := TCPQualityResult{}
	if direction < 0 || direction > 1 || p.Tuple.Protocol != "tcp" || at.IsZero() || at.Before(q.last) || p.TransportPayloadLength >= 1<<31 {
		return result
	}
	q.last = at
	own, opposite := &q.directions[direction], &q.directions[1-direction]
	if p.TCPFlags&0x10 != 0 {
		ack := p.TCPAcknowledgment
		if own.hasACK && ack == own.lastACK && p.TransportPayloadLength == 0 && p.TCPFlags&0x07 == 0 && len(opposite.segments) > 0 {
			q.duplicateACKs++
		}
		if !own.hasACK || seqBefore(own.lastACK, ack) {
			own.lastACK, own.hasACK = ack, true
			// Sample only an exact segment boundary. A cumulative ACK beyond
			// observed data can reflect a capture gap and yields no RTT.
			ambiguousACK := false
			for _, s := range opposite.segments {
				if !seqBefore(ack, s.end) && s.ambiguous {
					ambiguousACK = true
				}
			}
			for _, s := range opposite.segments {
				if s.end == ack && !ambiguousACK && at.After(s.at) {
					result.ACKRTT = append(result.ACKRTT, at.Sub(s.at))
				}
			}
			kept := opposite.segments[:0]
			for _, s := range opposite.segments {
				if seqBefore(ack, s.end) {
					kept = append(kept, s)
				}
			}
			opposite.segments = kept
		}
	}
	length := p.TransportPayloadLength
	if p.TCPFlags&0x02 != 0 {
		length++
	}
	if p.TCPFlags&0x01 != 0 {
		length++
	}
	if length > 0 {
		end := p.TCPSequence + length
		ambiguous := false
		for i := range own.segments {
			s := &own.segments[i]
			if seqBefore(p.TCPSequence, s.end) && seqBefore(s.start, end) {
				s.ambiguous, ambiguous = true, true
			}
		}
		if ambiguous {
			q.retransmissions++
		}
		if len(own.segments) < q.capacity {
			own.segments = append(own.segments, tcpSegment{p.TCPSequence, end, at, ambiguous})
		} else {
			q.dropped++
		}
	}
	result.ObservedRetransmissions = q.retransmissions
	result.ObservedDuplicateACKs = q.duplicateACKs
	result.DiscardedSegments = q.dropped
	return result
}

package networkintelligence

import "time"

// Called only while the session mutex is held. Unknown UDP allocates no state.
func (c *SessionCapture) observeUDP(tuple Tuple, packet CapturedPacket, deferred bool) *UDPFlowQuality {
	metadata := packet.Metadata.udp
	q := c.udp[tuple]
	if q != nil && packet.At.Sub(q.lastActivity) > c.tracker.timeout {
		*q = udpQuality{}
		delete(c.udp, tuple)
		q = nil
	}
	if metadata.protocol != 0 && metadata.kind == 1 && (q == nil || q.protocol != metadata.protocol) {
		if q == nil && len(c.udp) >= min(c.capacity, 128) {
			// Bound cleanup work even when many candidate tuples are received.
			if c.udpPrunedAt.IsZero() || packet.At.Sub(c.udpPrunedAt) >= time.Second {
				c.udpPrunedAt = packet.At
				for key, state := range c.udp {
					if packet.At.Sub(state.lastActivity) > c.tracker.timeout {
						*state = udpQuality{}
						delete(c.udp, key)
					}
				}
			}
			if len(c.udp) >= min(c.capacity, 128) {
				return nil
			}
		}
		if q != nil {
			*q = udpQuality{}
		}
		q = newUDPQuality(metadata.protocol)
		c.udp[tuple] = q
	}
	if q == nil {
		return nil
	}
	if packet.At.After(q.lastActivity) {
		q.lastActivity = packet.At
	}
	if metadata.protocol != 0 {
		q.observe(metadata, packet.Outgoing, packet.At)
	}
	if deferred {
		return nil
	}
	return q.snapshot(packet.At)
}

func (c *SessionCapture) clearUDP() {
	for _, state := range c.udp {
		*state = udpQuality{}
	}
	clear(c.udp)
}

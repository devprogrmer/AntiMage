package networkintelligence

import (
	"fmt"
	"time"
)

const udpEvidenceWindow = time.Minute
const udpExchangeTimeout = 10 * time.Second

type UDPFlowQuality struct {
	AdvertisedAppID         uint32              `json:"advertised_app_id,omitempty"`
	TimingUnavailableReason string              `json:"timing_unavailable_reason,omitempty"`
	Method                  string              `json:"method"`
	Protocol                string              `json:"protocol"`
	ObservedAt              time.Time           `json:"observed_at"`
	MeasurementAt           *time.Time          `json:"measurement_at"`
	UploadExchange          LatencyMilliseconds `json:"upload_exchange"`
	DownloadExchange        LatencyMilliseconds `json:"download_exchange"`
	PacketLoss              *float64            `json:"packet_loss"`
	Limited                 bool                `json:"limited"`
	Source                  string              `json:"source"`
}

type udpPending struct {
	token                     [16]byte
	at                        time.Time
	used, outgoing, ambiguous bool
}
type udpSeen struct {
	token          [16]byte
	at             time.Time
	used, outgoing bool
}
type udpQuality struct {
	appID                                       uint32
	protocol                                    uint8
	pending                                     [8]udpPending
	seen                                        [32]udpSeen
	windows                                     [2]*QualityWindow
	lastActivity, lastEvidence, lastMeasurement time.Time
	limited                                     bool
}

func newUDPQuality(protocol uint8) *udpQuality {
	up, _ := NewQualityWindow(32, udpEvidenceWindow)
	down, _ := NewQualityWindow(32, udpEvidenceWindow)
	return &udpQuality{protocol: protocol, windows: [2]*QualityWindow{up, down}}
}

func (q *udpQuality) observe(metadata udpProtocolMetadata, outgoing bool, at time.Time) {
	if metadata.protocol != q.protocol || at.IsZero() {
		return
	}
	if at.After(q.lastActivity) {
		q.lastActivity = at
	}
	if q.protocol == udpSourceQuery {
		if metadata.kind == 1 {
			q.pending[0] = udpPending{at: at, outgoing: outgoing, used: true}
			return
		}
		p := &q.pending[0]
		if metadata.kind == 2 && p.used && p.outgoing != outgoing && at.After(p.at) && at.Sub(p.at) <= udpExchangeTimeout {
			p.used = false
			q.lastEvidence = at
			q.appID = metadata.appID
		}
		return
	}

	if metadata.kind == 1 {
		for i := range q.pending {
			p := &q.pending[i]
			if p.used && p.token == metadata.token && p.outgoing == outgoing {
				p.ambiguous = true
				return
			}
		}
		seenSlot := -1
		for i, s := range q.seen {
			if s.used && s.token == metadata.token && s.outgoing == outgoing {
				return
			}
			// Discord's SSRC is stable rather than a fresh transaction nonce;
			// never time-expire its reuse guard during this flow generation.
			if !s.used || (q.protocol != udpDiscord && at.Sub(s.at) > udpEvidenceWindow) {
				seenSlot = i
			}
		}
		pendingSlot := -1
		for i, p := range q.pending {
			if !p.used || at.Sub(p.at) > udpExchangeTimeout {
				pendingSlot = i
			}
		}
		if seenSlot < 0 || pendingSlot < 0 {
			q.limited = true
			return
		}
		q.seen[seenSlot] = udpSeen{metadata.token, at, true, outgoing}
		q.pending[pendingSlot] = udpPending{token: metadata.token, at: at, used: true, outgoing: outgoing}
		return
	}
	if metadata.kind != 2 {
		return
	}
	for i := range q.pending {
		p := &q.pending[i]
		if !p.used || p.token != metadata.token || p.outgoing == outgoing {
			continue
		}
		elapsed := at.Sub(p.at)
		if elapsed <= 0 {
			return
		}
		p.used = false
		if elapsed > udpExchangeTimeout {
			return
		}
		if at.After(q.lastEvidence) {
			q.lastEvidence = at
		}
		// Retransmitted requests have no unambiguous request timestamp.
		if !p.ambiguous {
			direction := 0
			if p.outgoing {
				direction = 1
			}
			if q.windows[direction].Add(elapsed, at, at) {
				q.lastMeasurement = at
			}
		}
		return
	}
}

func udpProtocolDetails(protocol uint8) (string, string, string) {
	switch protocol {
	case udpSTUN:
		return "stun_binding", "STUN connectivity checks", "https://www.rfc-editor.org/rfc/rfc8489.html"
	case udpGNS:
		return "gns_direct_challenge", "GameNetworkingSockets (direct UDP)", "https://github.com/ValveSoftware/GameNetworkingSockets/blob/master/src/common/steamnetworkingsockets_messages_udp.proto"
	case udpSourceQuery:
		return "source_server_query", "Source server query", "https://github.com/rumblefrog/go-a2s/blob/master/info.go"
	case udpDiscord:
		return "discord_ip_discovery", "Discord Voice discovery", "https://docs.discord.com/developers/topics/voice-connections#ip-discovery"
	}
	return "", "Unknown", ""
}

func (q *udpQuality) snapshot(now time.Time) *UDPFlowQuality {
	if q == nil || q.lastEvidence.IsZero() || q.lastEvidence.After(now) || now.Sub(q.lastEvidence) >= udpEvidenceWindow {
		return nil
	}
	protocol, _, source := udpProtocolDetails(q.protocol)
	var measuredAt *time.Time
	if !q.lastMeasurement.IsZero() {
		value := q.lastMeasurement
		measuredAt = &value
	}
	unavailable := ""
	if q.protocol == udpSourceQuery {
		unavailable = "Server info replies do not echo a unique request token; query timing is unavailable"
	} else if q.windows[0].Snapshot(now).Samples == 0 && q.windows[1].Snapshot(now).Samples == 0 {
		unavailable = "No fresh unambiguous request/response timing sample is available"
	}
	return &UDPFlowQuality{AdvertisedAppID: q.appID, TimingUnavailableReason: unavailable, Method: "udp_request_response_observation", Protocol: protocol, ObservedAt: q.lastEvidence, MeasurementAt: measuredAt,
		UploadExchange: milliseconds(q.windows[0].Snapshot(now)), DownloadExchange: milliseconds(q.windows[1].Snapshot(now)), Limited: q.limited, Source: source}
}

// UDPApplicationDetection identifies an observed protocol exchange, never the
// executable or game title. Shared STUN/GNS transports cannot identify a game.
func UDPApplicationDetection(quality *UDPFlowQuality, now time.Time) ApplicationDetection {
	unknown := ApplicationDetection{Application: "Unknown", Confidence: "unknown", Evidence: []string{}}
	if quality == nil || quality.Method != "udp_request_response_observation" || quality.ObservedAt.IsZero() || quality.ObservedAt.After(now) || now.Sub(quality.ObservedAt) >= udpEvidenceWindow {
		return unknown
	}
	var name, source string
	for _, id := range []uint8{udpSTUN, udpGNS, udpDiscord, udpSourceQuery} {
		protocol, app, reference := udpProtocolDetails(id)
		if quality.Protocol == protocol {
			name, source = app, reference
		}
	}
	if name == "" {
		return unknown
	}
	if quality.Protocol == "source_server_query" {
		return ApplicationDetection{Application: name, Confidence: "probable", Scope: "protocol", Revision: "udp-protocols-v1", Evidence: []string{fmt.Sprintf("Matched Source query/reply with server-advertised full AppID %d; browsing a server is not proof of playing the game", quality.AdvertisedAppID), source}}
	}
	return ApplicationDetection{Application: name, Confidence: "probable", Scope: "protocol", Revision: "udp-protocols-v1", Evidence: []string{"Matched opposite-direction request/response headers and correlation identifier; application executable/game title not established", source}}
}

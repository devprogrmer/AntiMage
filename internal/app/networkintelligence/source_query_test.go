package networkintelligence

import (
	"encoding/binary"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func sourceQueryFixture(request bool, gameID uint64, fullID bool) []byte {
	b := []byte{255, 255, 255, 255}
	if request {
		return append(b, []byte("TSource Engine Query\x00")...)
	}
	b = append(b, 0x49, 17)
	for _, value := range []string{"private-name", "private-map", "private-dir", "private-title"} {
		b = append(b, []byte(value)...)
		b = append(b, 0)
	}
	b = append(b, byte(gameID), byte(gameID>>8), 1, 16, 0, 'd', 'l', 0, 1, '1', 0)
	if fullID {
		b = append(b, 1)
		var id [8]byte
		binary.LittleEndian.PutUint64(id[:], gameID)
		b = append(b, id[:]...)
	}
	return b
}

func TestSourceQueryFullAppIDEvidenceAndNoInventedTiming(t *testing.T) {
	var catalogue SignatureDatabase
	if err := json.Unmarshal(applicationCatalogue, &catalogue); err != nil {
		t.Fatal(err)
	}
	now := catalogue.PublishedAt.Add(time.Hour)
	detector, err := LoadDefaultApplicationDetector(now)
	if err != nil {
		t.Fatal(err)
	}
	tuple := Tuple{Destination: netip.MustParseAddrPort("192.0.2.1:49999"), Protocol: "udp"}
	for _, sample := range []struct {
		id   uint64
		full bool
		want string
	}{
		{730, true, "Counter-Strike 2 (server query)"}, {570, true, "Dota 2 (server query)"},
		{730, false, "Source server query"}, {730 + 65536, true, "Source server query"}, {1<<24 | 730, true, "Source server query"},
	} {
		request, reply := sourceQueryFixture(true, 0, false), sourceQueryFixture(false, sample.id, sample.full)
		a, b := parseUDPProtocol(request, len(request)), parseUDPProtocol(reply, len(reply))
		if a.protocol != udpSourceQuery || b.protocol != udpSourceQuery {
			t.Fatal("valid Source query header not recognized")
		}
		q := newUDPQuality(udpSourceQuery)
		q.observe(a, false, now)
		q.observe(b, true, now.Add(20*time.Millisecond))
		quality := q.snapshot(now.Add(20 * time.Millisecond))
		if quality == nil || quality.UploadExchange.Mean != nil || quality.PacketLoss != nil || quality.MeasurementAt != nil {
			t.Fatalf("invented query timing: %+v", quality)
		}
		result := detector.DetectObserved(tuple, quality, now.Add(20*time.Millisecond))
		if result.Application != sample.want || result.Confidence != "probable" {
			t.Fatalf("AppID %d full=%t detection=%+v", sample.id, sample.full, result)
		}
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), "private-") {
			t.Fatal("private server fields retained")
		}
		if sample.id == 730 && sample.full {
			quality.ObservedAt = catalogue.ExpiresAt.Add(time.Second)
			if expired := detector.DetectObserved(tuple, quality, quality.ObservedAt); expired.Application != "Source server query" {
				t.Fatalf("expired game catalogue used: %+v", expired)
			}
		}
	}
}

func TestSourceQueryRejectsTruncationAndSplitReplies(t *testing.T) {
	packet := sourceQueryFixture(false, 730, true)
	for n := 0; n < len(packet); n++ {
		if parseUDPProtocol(packet[:n], len(packet)).protocol != 0 {
			t.Fatalf("truncated Source reply accepted at %d", n)
		}
	}
	packet[0] = 254
	if parseUDPProtocol(packet, len(packet)).protocol != 0 {
		t.Fatal("split reply classified without reassembly")
	}
}

func TestApplicationCatalogueV2ValidationAndEvidenceBounds(t *testing.T) {
	now := time.Now().UTC()
	base := SignatureDatabase{Version: 2, Revision: "v2-fixture", PublishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), Rules: []ApplicationRule{{Application: "Example Game", SteamAppID: 123456, Protocol: "udp", Source: "isolated fixture"}}}
	for _, change := range []func(*SignatureDatabase){
		func(d *SignatureDatabase) { d.Version = 1 }, func(d *SignatureDatabase) { d.Rules[0].Protocol = "tcp" },
		func(d *SignatureDatabase) { d.Rules[0].SteamAppID = 1 << 24 }, func(d *SignatureDatabase) { d.Rules[0].Ports = []uint16{27015} },
		func(d *SignatureDatabase) { d.Rules[0].DestinationPrefix = "192.0.2.0/24" },
	} {
		db := base
		db.Rules = append([]ApplicationRule(nil), base.Rules...)
		change(&db)
		raw, _ := json.Marshal(db)
		if _, err := LoadApplicationDetector(strings.NewReader(string(raw)), now); err == nil {
			t.Fatal("invalid AppID rule accepted")
		}
	}
	raw, _ := json.Marshal(base)
	detector, err := LoadApplicationDetector(strings.NewReader(string(raw)), now)
	if err != nil {
		t.Fatal(err)
	}
	tuple := Tuple{Destination: netip.MustParseAddrPort("192.0.2.1:27015"), Protocol: "udp"}
	if detector.Detect(tuple, now).Application != "Unknown" {
		t.Fatal("AppID rule matched without protocol evidence")
	}
	quality := &UDPFlowQuality{Method: "udp_request_response_observation", Protocol: "source_server_query", ObservedAt: now, AdvertisedAppID: 123456}
	if got := detector.DetectObserved(tuple, quality, now); got.Application != "Example Game (server query)" {
		t.Fatal(got)
	}
	base.Rules = append(base.Rules, ApplicationRule{Application: "Conflicting Game", SteamAppID: 123456, Protocol: "udp", Source: "isolated fixture"})
	raw, _ = json.Marshal(base)
	detector, err = LoadApplicationDetector(strings.NewReader(string(raw)), now)
	if err != nil {
		t.Fatal(err)
	}
	if detector.DetectObserved(tuple, quality, now).Application != "Unknown" {
		t.Fatal("conflicting game ID rules guessed a title")
	}
	base.Rules = append(base.Rules[:1],
		ApplicationRule{Application: "Destination A", DestinationPrefix: "192.0.2.0/24", Protocol: "udp", Ports: []uint16{27015}, Source: "fixture"},
		ApplicationRule{Application: "Destination B", DestinationPrefix: "192.0.2.0/24", Protocol: "udp", Ports: []uint16{27015}, Source: "fixture"})
	raw, _ = json.Marshal(base)
	detector, err = LoadApplicationDetector(strings.NewReader(string(raw)), now)
	if err != nil {
		t.Fatal(err)
	}
	if detector.DetectObserved(tuple, quality, now).Application != "Unknown" {
		t.Fatal("query title overrode conflicting destination evidence")
	}
	legacy := signatureFixture(now)
	for i := 0; i < 100; i++ {
		rule := legacy.Rules[0]
		rule.Source = strings.Repeat("a", 1500) + string(rune('a'+i))
		legacy.Rules = append(legacy.Rules, rule)
	}
	raw, _ = json.Marshal(legacy)
	detector, err = LoadApplicationDetector(strings.NewReader(string(raw)), now)
	if err != nil {
		t.Fatal(err)
	}
	tuple.Destination = netip.MustParseAddrPort("192.0.2.1:27051")
	result := detector.Detect(tuple, now)
	if len(result.Evidence) > 4 {
		t.Fatal("unbounded detection evidence")
	}
	for _, evidence := range result.Evidence {
		if len(evidence) > 259 {
			t.Fatal("unbounded evidence string")
		}
	}
}

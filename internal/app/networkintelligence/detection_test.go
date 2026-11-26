package networkintelligence

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func signatureFixture(now time.Time) SignatureDatabase {
	return SignatureDatabase{Version: 1, Revision: "fixture-v1", PublishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), Rules: []ApplicationRule{{Application: "Fixture Game", DestinationPrefix: "192.0.2.0/24", Protocol: "udp", Ports: []uint16{27051}, Source: "isolated fixture"}}}
}

func TestApplicationDetectionRequiresDestinationEvidence(t *testing.T) {
	now := time.Now().UTC()
	raw, _ := json.Marshal(signatureFixture(now))
	detector, err := LoadApplicationDetector(strings.NewReader(string(raw)), now)
	if err != nil {
		t.Fatal(err)
	}
	tuple := Tuple{Source: netip.MustParseAddrPort("10.0.0.2:1234"), Destination: netip.MustParseAddrPort("192.0.2.7:27051"), Protocol: "udp"}
	got := detector.Detect(tuple, now)
	if got.Application != "Fixture Game" || got.Confidence != "probable" || len(got.Evidence) != 1 {
		t.Fatalf("detection=%+v", got)
	}
	tuple.Destination = netip.MustParseAddrPort("198.51.100.7:27051")
	if got := detector.Detect(tuple, now); got.Application != "Unknown" {
		t.Fatalf("port-only detection=%+v", got)
	}
	tuple.Destination = netip.MustParseAddrPort("192.0.2.7:443")
	if got := detector.Detect(tuple, now); got.Application != "Unknown" {
		t.Fatalf("prefix-only detection=%+v", got)
	}
	tuple.Destination = netip.MustParseAddrPort("192.0.2.7:27051")
	tuple.Protocol = "tcp"
	if got := detector.Detect(tuple, now); got.Application != "Unknown" {
		t.Fatalf("wrong protocol=%+v", got)
	}
	if got := detector.Detect(tuple, now.Add(time.Hour)); got.Application != "Unknown" {
		t.Fatalf("expired detection=%+v", got)
	}
}

func TestApplicationDetectionRejectsAmbiguousSharedRanges(t *testing.T) {
	now := time.Now().UTC()
	db := signatureFixture(now)
	rule := db.Rules[0]
	rule.Application = "Other Fixture"
	db.Rules = append(db.Rules, rule)
	raw, _ := json.Marshal(db)
	detector, err := LoadApplicationDetector(strings.NewReader(string(raw)), now)
	if err != nil {
		t.Fatal(err)
	}
	got := detector.Detect(Tuple{Destination: netip.MustParseAddrPort("192.0.2.7:27051"), Protocol: "udp"}, now)
	if got.Application != "Unknown" || len(got.Evidence) != 1 {
		t.Fatalf("ambiguous=%+v", got)
	}
}

func TestApplicationDatabaseValidation(t *testing.T) {
	now := time.Now().UTC()
	for name, mutate := range map[string]func(*SignatureDatabase){
		"version":       func(d *SignatureDatabase) { d.Version = 2 },
		"expired":       func(d *SignatureDatabase) { d.ExpiresAt = now },
		"future":        func(d *SignatureDatabase) { d.PublishedAt = now.Add(time.Minute) },
		"all addresses": func(d *SignatureDatabase) { d.Rules[0].DestinationPrefix = "0.0.0.0/0" },
		"no prefix":     func(d *SignatureDatabase) { d.Rules[0].DestinationPrefix = "" },
		"source":        func(d *SignatureDatabase) { d.Rules[0].Source = "" },
		"port":          func(d *SignatureDatabase) { d.Rules[0].Ports = []uint16{0} },
		"duplicate":     func(d *SignatureDatabase) { d.Rules[0].Ports = []uint16{443, 443} },
	} {
		t.Run(name, func(t *testing.T) {
			db := signatureFixture(now)
			mutate(&db)
			raw, _ := json.Marshal(db)
			if _, err := LoadApplicationDetector(strings.NewReader(string(raw)), now); err == nil {
				t.Fatal("accepted invalid database")
			}
		})
	}
	raw, _ := json.Marshal(signatureFixture(now))
	if _, err := LoadApplicationDetector(strings.NewReader(string(raw)+" {}"), now); err == nil {
		t.Fatal("accepted trailing content")
	}
	if _, err := LoadApplicationDetector(strings.NewReader(strings.Repeat(" ", maxSignatureBytes+1)), now); err == nil {
		t.Fatal("accepted oversized content")
	}
}

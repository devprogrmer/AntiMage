package networkintelligence

import (
	"testing"
	"time"
)

func TestUDPQualityRequiresPairedUnambiguousTransactions(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	for _, fixture := range []func(bool, byte) []byte{stunFixture, gnsFixture, discordFixture} {
		request, response := fixture(false, 7), fixture(true, 7)
		a, b := parseUDPProtocol(request, len(request)), parseUDPProtocol(response, len(response))
		q := newUDPQuality(a.protocol)
		q.observe(b, true, now)
		if q.snapshot(now) != nil {
			t.Fatal("unsolicited response acquired a protocol/RTT")
		}
		q.observe(a, false, now)
		if q.snapshot(now) != nil {
			t.Fatal("one-sided request acquired a protocol/RTT")
		}
		wrong := b
		wrong.token[0] ^= 1
		q.observe(wrong, true, now.Add(time.Millisecond))
		if q.snapshot(now) != nil {
			t.Fatal("wrong nonce acquired an RTT")
		}
		q.observe(b, true, now.Add(20*time.Millisecond))
		result := q.snapshot(now.Add(20 * time.Millisecond))
		if result == nil || result.UploadExchange.Samples != 1 || result.UploadExchange.Mean == nil || *result.UploadExchange.Mean != 20 || result.DownloadExchange.Mean != nil || result.PacketLoss != nil {
			t.Fatalf("quality=%+v", result)
		}
		q.observe(b, true, now.Add(30*time.Millisecond))
		if q.snapshot(now.Add(30*time.Millisecond)).UploadExchange.Samples != 1 {
			t.Fatal("duplicate response acquired a second RTT")
		}
		if q.snapshot(now.Add(2*time.Minute)) != nil {
			t.Fatal("stale protocol evidence retained")
		}
	}
}

func TestUDPQualityRetriesCapacityAndRTTVariation(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	q := newUDPQuality(udpSTUN)
	for i, delay := range []time.Duration{20 * time.Millisecond, 30 * time.Millisecond} {
		a := parseUDPProtocol(stunFixture(false, byte(i+1)), 20)
		b := parseUDPProtocol(stunFixture(true, byte(i+1)), 20)
		at := now.Add(time.Duration(i) * 100 * time.Millisecond)
		q.observe(a, false, at)
		q.observe(b, true, at.Add(delay))
	}
	result := q.snapshot(now.Add(200 * time.Millisecond))
	if result.UploadExchange.Jitter == nil || *result.UploadExchange.Jitter != 10 {
		t.Fatalf("RTT variation=%+v", result)
	}
	a, b := parseUDPProtocol(stunFixture(false, 9), 20), parseUDPProtocol(stunFixture(true, 9), 20)
	q.observe(a, false, now.Add(300*time.Millisecond))
	q.observe(a, false, now.Add(310*time.Millisecond))
	q.observe(b, true, now.Add(350*time.Millisecond))
	if q.snapshot(now.Add(350*time.Millisecond)).UploadExchange.Samples != 2 {
		t.Fatal("retried transaction produced ambiguous RTT")
	}
	for i := 10; i < 100; i++ {
		a := parseUDPProtocol(stunFixture(false, byte(i)), 20)
		q.observe(a, false, now.Add(400*time.Millisecond))
	}
	if !q.snapshot(now.Add(500 * time.Millisecond)).Limited {
		t.Fatal("bounded correlation overflow not reported")
	}
}

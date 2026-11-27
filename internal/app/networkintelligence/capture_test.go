package networkintelligence

import (
	"testing"
	"time"
)

func TestCaptureBudgetStopsAtWindowLimit(t *testing.T) {
	now := time.Now()
	b := captureBudget{limit: 2}
	if !b.allow(now) || !b.allow(now.Add(time.Millisecond)) || b.allow(now.Add(999*time.Millisecond)) {
		t.Fatal("packet budget limit not enforced")
	}
	if !b.allow(now.Add(time.Second)) || !b.allow(now.Add(time.Second)) || b.allow(now.Add(time.Second)) {
		t.Fatal("window renewal invalid")
	}
	if b.allow(now) {
		t.Fatal("clock regression granted budget")
	}
	uncapped := captureBudget{}
	for range 10 {
		if !uncapped.allow(now) {
			t.Fatal("uncapped capture limited")
		}
	}
	for _, rate := range []int{-1, 1000001} {
		if (CaptureConfig{Interface: "wg0", MaxPacketsPerSecond: rate}).validate() == nil {
			t.Fatalf("invalid budget %d accepted", rate)
		}
	}
}

func TestCaptureRequiresExplicitInterface(t *testing.T) {
	for _, name := range []string{"", " ", "wg0/other", "wg0\x00", "interface-too-long", " wg0"} {
		if err := (CaptureConfig{Interface: name}).validate(); err == nil {
			t.Errorf("accepted interface %q", name)
		}
	}
	for _, name := range []string{"wg0", "amwg12345678901", "vpn-test.1"} {
		if err := (CaptureConfig{Interface: name}).validate(); err != nil {
			t.Errorf("rejected interface %q: %v", name, err)
		}
	}
}

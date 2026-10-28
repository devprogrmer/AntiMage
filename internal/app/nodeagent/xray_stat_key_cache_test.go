package nodeagent

import (
	"encoding/base64"
	"strconv"
	"testing"
)

// TestXrayStatNativeKeyResolutionMatchesSpec pins the exact accepted/rejected
// names and the exact counter key for each accepted name. The memo added in
// front of this resolution must never change any of these outcomes.
func TestXrayStatNativeKeyResolutionMatchesSpec(t *testing.T) {
	tagged := "user>>>42.rb1_" + base64.RawURLEncoding.EncodeToString([]byte("vless-in")) + ".alice>>>traffic>>>uplink"
	cases := []struct {
		name   string
		wantOK bool
		want   string
	}{
		{"user>>>7.bob>>>traffic>>>uplink", true, "7.bob:uplink"},
		{"user>>>7.bob>>>traffic>>>downlink", true, "7.bob:downlink"},
		{" USER >>> 9.carol >>> TRAFFIC >>> UPLINK ", true, "9.carol:uplink"},
		{tagged, true, "42.rb1_" + base64.RawURLEncoding.EncodeToString([]byte("vless-in")) + ".alice:uplink"},
		// Rejected: not a user traffic series for this path.
		{"user>>>7.bob>>>online", false, ""},
		{"outbound>>>tag>>>traffic>>>uplink", false, ""},
		{"inbound>>>tag>>>traffic>>>downlink", false, ""},
		{"user>>>7.bob>>>traffic>>>sideways", false, ""},
		{"user>>>notanumber.bob>>>traffic>>>uplink", false, ""},
		{"user>>>0.bob>>>traffic>>>uplink", false, ""},
		{"user>>>", false, ""},
		{"", false, ""},
		{"user>>>7.bob>>>traffic>>>uplink>>>extra", false, ""},
	}
	for _, tc := range cases {
		got := resolveXrayStatNativeKey(tc.name)
		if got.OK != tc.wantOK || got.Key != tc.want {
			t.Errorf("resolveXrayStatNativeKey(%q) = %+v, want ok=%v key=%q", tc.name, got, tc.wantOK, tc.want)
		}
	}
}

// TestXrayStatKeyCacheWarmAndColdTicksAgree proves the memo cannot change which
// series are counted, how they are keyed, or what value each key receives.
func TestXrayStatKeyCacheWarmAndColdTicksAgree(t *testing.T) {
	stats := []xrayStat{
		{Name: "user>>>1.alice>>>traffic>>>uplink", Value: 10},
		{Name: "user>>>1.alice>>>traffic>>>downlink", Value: 20},
		{Name: "user>>>2.bob>>>traffic>>>uplink", Value: 30},
		// Duplicate key: the last series in slice order must win.
		{Name: "user>>>2.bob>>>traffic>>>uplink", Value: 31},
		// Rejected names must not create series.
		{Name: "user>>>2.bob>>>online", Value: 99},
		{Name: "outbound>>>direct>>>traffic>>>uplink", Value: 99},
		// Rejected by value, not by name: the same name with a valid value counts.
		{Name: "user>>>3.carol>>>traffic>>>uplink", Value: -1},
		{Name: "user>>>3.carol>>>traffic>>>uplink", Value: 5},
	}

	cold := New(Config{DataDir: t.TempDir()})
	cold.xrayUsageLoaded = true
	coldCounters, err := cold.previewXrayAccountingCountersLocked(stats, "gen")
	if err != nil {
		t.Fatal(err)
	}
	if len(cold.xrayStatKeyCache) == 0 {
		t.Fatal("expected the stat name memo to be populated")
	}

	// Warm: the same server resolves every name from the memo.
	warmCounters, err := cold.previewXrayAccountingCountersLocked(stats, "gen")
	if err != nil {
		t.Fatal(err)
	}

	fresh := New(Config{DataDir: t.TempDir()})
	fresh.xrayUsageLoaded = true
	freshCounters, err := fresh.previewXrayAccountingCountersLocked(stats, "gen")
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]uint64{"1.alice:uplink": 10, "1.alice:downlink": 20, "2.bob:uplink": 31, "3.carol:uplink": 5}
	for label, got := range map[string]map[string]accountingCounter{"cold": coldCounters, "warm": warmCounters, "fresh": freshCounters} {
		if len(got) != len(want) {
			t.Fatalf("%s: got %d series, want %d (%v)", label, len(got), len(want), got)
		}
		for key, native := range want {
			counter, ok := got[key]
			if !ok {
				t.Fatalf("%s: missing series %q", label, key)
			}
			if counter.Native != native {
				t.Fatalf("%s: series %q native=%d, want %d", label, key, counter.Native, native)
			}
		}
	}
}

// TestXrayStatKeyCacheOverflowKeepsCounting proves the bounded memo drops itself
// rather than silently disabling accounting when it is full.
func TestXrayStatKeyCacheOverflowKeepsCounting(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	s.xrayUsageLoaded = true
	full := make(map[string]xrayStatNativeKey, xrayStatKeyCacheMaxSeries)
	for i := 0; i < xrayStatKeyCacheMaxSeries; i++ {
		full[xrayStatNameForCacheProbe(i)] = xrayStatNativeKey{}
	}
	s.xrayStatKeyCache = full

	counters, err := s.previewXrayAccountingCountersLocked([]xrayStat{{Name: "user>>>5.dave>>>traffic>>>uplink", Value: 7}}, "gen")
	if err != nil {
		t.Fatal(err)
	}
	if got := counters["5.dave:uplink"].Native; got != 7 {
		t.Fatalf("series after memo overflow native=%d, want 7", got)
	}
	if len(s.xrayStatKeyCache) >= xrayStatKeyCacheMaxSeries {
		t.Fatalf("memo was not reset on overflow: %d entries", len(s.xrayStatKeyCache))
	}
	if _, ok := s.xrayStatKeyCache["user>>>5.dave>>>traffic>>>uplink"]; !ok {
		t.Fatal("probe name missing from the refilled memo")
	}
}

func xrayStatNameForCacheProbe(index int) string {
	return "user>>>" + strconv.Itoa(index) + ".probe>>>traffic>>>uplink"
}

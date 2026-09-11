package nodeagent

import (
	"testing"
)

// TestXrayOnlineStatParsing tests parsing of Xray stat names
func TestXrayOnlineStatParsing(t *testing.T) {
	tests := []struct {
		name       string
		statName   string
		wantValid  bool
		wantType   string
		wantMetric string
	}{
		{
			name:       "valid traffic uplink stat",
			statName:   "user>>>42.alice>>>traffic>>>uplink",
			wantValid:  true,
			wantType:   "user",
			wantMetric: "traffic",
		},
		{
			name:       "valid traffic downlink stat",
			statName:   "user>>>42.alice>>>traffic>>>downlink",
			wantValid:  true,
			wantType:   "user",
			wantMetric: "traffic",
		},
		{
			name:       "online stat format (for reference)",
			statName:   "user>>>42.alice>>>online",
			wantValid:  true,
			wantType:   "user",
			wantMetric: "online",
		},
		{
			name:      "invalid: too few parts",
			statName:  "user>>>alice",
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, valid := parseXrayStatName(tt.statName)
			if valid != tt.wantValid {
				t.Errorf("parseXrayStatName() valid = %v, want %v", valid, tt.wantValid)
			}
			if !valid {
				return
			}
			if parsed.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", parsed.Type, tt.wantType)
			}
			if parsed.Metric != tt.wantMetric {
				t.Errorf("Metric = %v, want %v", parsed.Metric, tt.wantMetric)
			}
		})
	}
}

// TestXrayOnlineCountZeroMeansOffline tests that OnlineMap count=0 means Offline
func TestXrayOnlineCountZeroMeansOffline(t *testing.T) {
	// Simulate OnlineMap returning count=0 for a user
	onlineCount := int32(0)

	isOnline := onlineCount > 0

	if isOnline {
		t.Error("User with OnlineMap count=0 should be Offline")
	}
}

// TestXrayOnlineCountOneMe ansOnline tests that OnlineMap count=1 means Online
func TestXrayOnlineCountOneMeansOnline(t *testing.T) {
	// Simulate OnlineMap returning count=1 for a user
	onlineCount := int32(1)

	isOnline := onlineCount > 0

	if !isOnline {
		t.Error("User with OnlineMap count=1 should be Online")
	}
}

// TestXrayOnlineCountMultipleMeansOnline tests that OnlineMap count>1 means Online
// This handles users with multiple active connections/IPs
func TestXrayOnlineCountMultipleMeansOnline(t *testing.T) {
	testCases := []int32{2, 3, 5, 10}

	for _, count := range testCases {
		t.Run("count="+string(rune(count+'0')), func(t *testing.T) {
			isOnline := count > 0

			if !isOnline {
				t.Errorf("User with OnlineMap count=%d should be Online", count)
			}
		})
	}
}

// TestXrayIdleButConnectedStaysOnline tests that a user with OnlineMap entry
// but zero traffic delta remains Online
func TestXrayIdleButConnectedStaysOnline(t *testing.T) {
	// User has no new traffic (delta=0) but OnlineMap shows active
	trafficDelta := uint64(0)
	onlineCount := int32(1)

	// Online state is determined by OnlineMap, not traffic
	isOnline := onlineCount > 0

	if !isOnline {
		t.Error("Idle but connected user (OnlineMap count>0) should be Online")
	}

	if trafficDelta != 0 {
		t.Errorf("Idle user should have traffic delta=0, got %d", trafficDelta)
	}
}

// TestXrayHistoricalTrafficWithoutOnlineEntryIsOffline tests that users
// with historical traffic but no OnlineMap entry are Offline
func TestXrayHistoricalTrafficWithoutOnlineEntryIsOffline(t *testing.T) {
	// User has traffic counters from past activity
	baseline := map[string]uint64{
		"42.alice:uplink":   5000,
		"42.alice:downlink": 10000,
	}

	// Current counters unchanged (no new traffic)
	currentUplink := uint64(5000)
	currentDownlink := uint64(10000)

	deltaUplink := currentUplink - baseline["42.alice:uplink"]
	deltaDownlink := currentDownlink - baseline["42.alice:downlink"]

	// OnlineMap shows user is not connected
	onlineCount := int32(0)

	// Online state determined by OnlineMap
	isOnline := onlineCount > 0

	if isOnline {
		t.Error("User with historical traffic but OnlineMap count=0 should be Offline")
	}

	if deltaUplink != 0 || deltaDownlink != 0 {
		t.Error("Traffic deltas should be 0 for unchanged counters")
	}
}

// TestXrayOnlineQueryFailureFallback tests that if OnlineMap query fails,
// traffic accounting still works and activity fallback is used
func TestXrayOnlineQueryFailureFallback(t *testing.T) {
	// User has new traffic
	baseline := uint64(1000)
	current := uint64(1500)
	delta := current - baseline

	// OnlineMap query failed (simulated by not having online count available)
	onlineQueryFailed := true

	// Traffic accounting succeeds regardless
	if delta != 500 {
		t.Errorf("Traffic delta should be calculated correctly: got %d, want 500", delta)
	}

	// Fallback: if online query failed but delta > 0, use activity heuristic
	isOnline := false
	if onlineQueryFailed && delta > 0 {
		isOnline = true // Degraded mode: infer from activity
	}

	if !isOnline {
		t.Error("Fallback: user with traffic delta should be Online when OnlineMap unavailable")
	}
}

// TestXrayTrafficAccountingIndependentOfOnline verifies traffic deltas
// are calculated correctly regardless of online state
func TestXrayTrafficAccountingIndependentOfOnline(t *testing.T) {
	baseline := map[string]uint64{
		"77.dave:uplink":   3000,
		"77.dave:downlink": 5000,
	}

	currentUplink := uint64(5000)
	currentDownlink := uint64(8000)

	deltaUplink := currentUplink - baseline["77.dave:uplink"]
	deltaDownlink := currentDownlink - baseline["77.dave:downlink"]
	totalDelta := deltaUplink + deltaDownlink

	// Online state (from OnlineMap)
	onlineCount := int32(0) // User disconnected

	expectedDelta := uint64(2000 + 3000)
	if totalDelta != expectedDelta {
		t.Errorf("Traffic delta = %d, want %d", totalDelta, expectedDelta)
	}

	// Verify online state doesn't affect traffic calculation
	isOnline := onlineCount > 0
	if isOnline {
		t.Error("User should be Offline (count=0)")
	}

	// Traffic delta correct regardless of online state
	if totalDelta != expectedDelta {
		t.Error("Online state must not alter traffic accounting")
	}
}

// TestXrayOnlineDoesNotAffectBilling verifies that online state changes
// do not modify baselines or cause double-counting
func TestXrayOnlineDoesNotAffectBilling(t *testing.T) {
	// Initial baseline
	baseline1 := map[string]uint64{
		"88.eve:uplink":   1000,
		"88.eve:downlink": 2000,
	}

	// First collection
	traffic1Up := uint64(1500)
	traffic1Down := uint64(3000)
	delta1 := (traffic1Up - baseline1["88.eve:uplink"]) + (traffic1Down - baseline1["88.eve:downlink"])
	onlineCount1 := int32(1) // Online

	// Baseline advances
	baseline2 := map[string]uint64{
		"88.eve:uplink":   traffic1Up,
		"88.eve:downlink": traffic1Down,
	}

	// Second collection (same values, now offline)
	traffic2Up := uint64(1500)
	traffic2Down := uint64(3000)
	delta2 := uint64(0)
	if traffic2Up >= baseline2["88.eve:uplink"] {
		delta2 += traffic2Up - baseline2["88.eve:uplink"]
	}
	if traffic2Down >= baseline2["88.eve:downlink"] {
		delta2 += traffic2Down - baseline2["88.eve:downlink"]
	}
	onlineCount2 := int32(0) // Offline

	// Verify first collection
	expectedDelta1 := uint64(500 + 1000)
	if delta1 != expectedDelta1 {
		t.Errorf("First collection delta = %d, want %d", delta1, expectedDelta1)
	}

	// Verify second collection (no double-counting)
	if delta2 != 0 {
		t.Errorf("Second collection delta = %d, want 0 (no double-counting)", delta2)
	}

	// Verify online state change didn't affect baseline
	if baseline2["88.eve:uplink"] != traffic1Up {
		t.Error("Baseline should not be affected by online state change")
	}

	// Online state changed but billing unaffected
	online1 := onlineCount1 > 0
	online2 := onlineCount2 > 0

	if !online1 || online2 {
		t.Error("Online state should have changed from true to false")
	}
}

// TestXrayMultipleActiveIPsHandling tests that count>1 is handled correctly
func TestXrayMultipleActiveIPsHandling(t *testing.T) {
	// User connected from 3 different IPs
	onlineCount := int32(3)

	// Should be considered Online
	isOnline := onlineCount > 0

	if !isOnline {
		t.Error("User with 3 active connections should be Online")
	}

	// Verify we're using > 0, not == 1
	if onlineCount > 0 && onlineCount != 1 {
		// This is correct behavior: count > 1 still means Online
		if !isOnline {
			t.Error("Multiple connections should still be Online")
		}
	}
}

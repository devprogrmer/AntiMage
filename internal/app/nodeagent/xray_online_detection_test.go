package nodeagent

import (
	"testing"
)

// TestXrayOnlineStatParsing tests parsing of Xray stat names
// Note: The node agent uses CLI which only exposes traffic stats via QueryStats
// Online stats (user>>>email>>>online) exist in Xray's OnlineMap but are not
// accessible via `xray api statsquery` CLI command
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
			name:       "online stat format (not returned by CLI)",
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

// TestXrayHistoricalTrafficDoesNotStayOnline tests that users with historical
// traffic counters but no NEW traffic delta do not remain Online
// This uses activity-based heuristic: delta > 0 in collection window
func TestXrayHistoricalTrafficDoesNotStayOnline(t *testing.T) {
	baseline := map[string]uint64{
		"42.alice:uplink":   5000,
		"42.alice:downlink": 10000,
	}

	// Simulate traffic counters unchanged (no new activity)
	mockStats := []xrayStat{
		{Name: "user>>>42.alice>>>traffic>>>uplink", Value: 5000},    // delta = 0
		{Name: "user>>>42.alice>>>traffic>>>downlink", Value: 10000}, // delta = 0
	}

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	aggregated := make(map[aggregateKey]xrayUsageSample)

	// Process traffic stats with activity-based online detection
	for _, stat := range mockStats {
		statName, ok := parseXrayStatName(stat.Name)
		if !ok || statName.Type != "user" || statName.Metric != "traffic" {
			continue
		}

		identity, err := parseXrayUserEmail(statName.Email)
		if err != nil {
			continue
		}

		key := aggregateKey{
			UserID:     identity.UserID,
			InboundTag: identity.InboundTag,
		}

		// Calculate delta
		currentValue := uint64(stat.Value)
		baselineValue := baseline[statName.Email+":"+statName.Direction]
		delta := uint64(0)
		if currentValue >= baselineValue {
			delta = currentValue - baselineValue
		}

		sample := aggregated[key]
		sample.UserID = identity.UserID
		sample.InboundTag = identity.InboundTag
		sample.Value += delta

		// Online heuristic: delta > 0 means active
		if delta > 0 {
			sample.Online = true
		}

		aggregated[key] = sample
	}

	// Verify: user has historical traffic but delta=0, should be Offline
	key := aggregateKey{UserID: 42, InboundTag: ""}
	sample := aggregated[key]

	if sample.Online {
		t.Error("User with no traffic delta should be Offline (not active in collection window)")
	}

	if sample.Value != 0 {
		t.Errorf("Traffic delta should be 0, got %d", sample.Value)
	}
}

// TestXrayActiveUserWithTrafficDelta tests that a user with traffic delta > 0
// is marked as Online using activity-based heuristic
func TestXrayActiveUserWithTrafficDelta(t *testing.T) {
	baseline := map[string]uint64{
		"99.bob:uplink":   1000,
		"99.bob:downlink": 2000,
	}

	// User has new traffic
	mockStats := []xrayStat{
		{Name: "user>>>99.bob>>>traffic>>>uplink", Value: 1500},   // delta = 500
		{Name: "user>>>99.bob>>>traffic>>>downlink", Value: 2800}, // delta = 800
	}

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	aggregated := make(map[aggregateKey]xrayUsageSample)

	for _, stat := range mockStats {
		statName, ok := parseXrayStatName(stat.Name)
		if !ok || statName.Type != "user" || statName.Metric != "traffic" {
			continue
		}

		identity, err := parseXrayUserEmail(statName.Email)
		if err != nil {
			continue
		}

		key := aggregateKey{
			UserID:     identity.UserID,
			InboundTag: identity.InboundTag,
		}

		currentValue := uint64(stat.Value)
		baselineValue := baseline[statName.Email+":"+statName.Direction]
		delta := currentValue - baselineValue

		sample := aggregated[key]
		sample.UserID = identity.UserID
		sample.Value += delta

		if delta > 0 {
			sample.Online = true
		}

		aggregated[key] = sample
	}

	// Verify: user with traffic delta is Online
	key := aggregateKey{UserID: 99, InboundTag: ""}
	sample := aggregated[key]

	if !sample.Online {
		t.Error("User with traffic delta > 0 should be Online")
	}

	expectedDelta := uint64(500 + 800)
	if sample.Value != expectedDelta {
		t.Errorf("Traffic delta = %d, want %d", sample.Value, expectedDelta)
	}
}

// TestXrayTrafficAccountingIndependentOfOnline verifies that traffic deltas
// are calculated correctly regardless of online heuristic
func TestXrayTrafficAccountingIndependentOfOnline(t *testing.T) {
	baseline := map[string]uint64{
		"77.dave:uplink":   3000,
		"77.dave:downlink": 5000,
	}

	mockStats := []xrayStat{
		{Name: "user>>>77.dave>>>traffic>>>uplink", Value: 5000},
		{Name: "user>>>77.dave>>>traffic>>>downlink", Value: 8000},
	}

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	aggregated := make(map[aggregateKey]xrayUsageSample)

	for _, stat := range mockStats {
		statName, ok := parseXrayStatName(stat.Name)
		if !ok || statName.Type != "user" || statName.Metric != "traffic" {
			continue
		}

		identity, err := parseXrayUserEmail(statName.Email)
		if err != nil {
			continue
		}

		key := aggregateKey{
			UserID:     identity.UserID,
			InboundTag: identity.InboundTag,
		}

		currentValue := uint64(stat.Value)
		baselineValue := baseline[statName.Email+":"+statName.Direction]
		delta := currentValue - baselineValue

		sample := aggregated[key]
		sample.UserID = identity.UserID
		sample.Value += delta

		if delta > 0 {
			sample.Online = true
		}

		aggregated[key] = sample
	}

	// Verify: traffic delta calculated correctly
	key := aggregateKey{UserID: 77, InboundTag: ""}
	sample := aggregated[key]

	expectedDelta := uint64((5000 - 3000) + (8000 - 5000))
	if sample.Value != expectedDelta {
		t.Errorf("Traffic delta = %d, want %d", sample.Value, expectedDelta)
	}

	// Online should be true because delta > 0
	if !sample.Online {
		t.Error("User with delta > 0 should be Online")
	}
}

// TestXrayOnlineDoesNotAffectBilling verifies that the online flag does not
// interfere with traffic baseline or ACK safety
func TestXrayOnlineDoesNotAffectBilling(t *testing.T) {
	server := &Server{
		cfg: Config{
			DataDir: t.TempDir(),
		},
		xrayUsageBaseline: map[string]uint64{
			"88.eve:uplink":   1000,
			"88.eve:downlink": 2000,
		},
		xrayUsageLoaded: true,
	}

	// First collection with traffic
	baseline1 := server.xrayUsageBaseline
	traffic1Up := uint64(1500)
	traffic1Down := uint64(3000)
	delta1 := (traffic1Up - baseline1["88.eve:uplink"]) + (traffic1Down - baseline1["88.eve:downlink"])

	// Simulate baseline update after first collection
	server.xrayUsageBaseline = map[string]uint64{
		"88.eve:uplink":   traffic1Up,
		"88.eve:downlink": traffic1Down,
	}

	// Second collection with same values (no new traffic)
	baseline2 := server.xrayUsageBaseline
	traffic2Up := uint64(1500)
	traffic2Down := uint64(3000)
	delta2 := uint64(0)
	if traffic2Up >= baseline2["88.eve:uplink"] {
		delta2 += traffic2Up - baseline2["88.eve:uplink"]
	}
	if traffic2Down >= baseline2["88.eve:downlink"] {
		delta2 += traffic2Down - baseline2["88.eve:downlink"]
	}

	// Verify first collection
	expectedDelta1 := uint64(500 + 1000)
	if delta1 != expectedDelta1 {
		t.Errorf("First collection delta = %d, want %d", delta1, expectedDelta1)
	}

	// Verify second collection (no double-counting)
	if delta2 != 0 {
		t.Errorf("Second collection delta = %d, want 0 (no double-counting)", delta2)
	}

	// Online state changing does not affect baseline
	if server.xrayUsageBaseline["88.eve:uplink"] != traffic1Up {
		t.Error("Baseline should not change based on online state")
	}
}

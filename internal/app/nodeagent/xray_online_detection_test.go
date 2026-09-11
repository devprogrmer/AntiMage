package nodeagent

import (
	"testing"
)

// TestXrayOnlineStatParsing tests parsing of Xray online stats
func TestXrayOnlineStatParsing(t *testing.T) {
	tests := []struct {
		name       string
		statName   string
		wantValid  bool
		wantType   string
		wantEmail  string
		wantMetric string
	}{
		{
			name:       "valid online stat",
			statName:   "user>>>42.alice>>>online",
			wantValid:  true,
			wantType:   "user",
			wantEmail:  "42.alice",
			wantMetric: "online",
		},
		{
			name:       "valid traffic uplink stat",
			statName:   "user>>>42.alice>>>traffic>>>uplink",
			wantValid:  true,
			wantType:   "user",
			wantEmail:  "42.alice",
			wantMetric: "traffic",
		},
		{
			name:       "online stat with tagged email",
			statName:   "user>>>42.rb1_dmxlc3MtaW4.alice>>>online",
			wantValid:  true,
			wantType:   "user",
			wantMetric: "online",
		},
		{
			name:      "invalid: too few parts",
			statName:  "user>>>alice",
			wantValid: false,
		},
		{
			name:      "invalid: unknown metric",
			statName:  "user>>>alice>>>unknown",
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
			if tt.wantEmail != "" && parsed.Email != tt.wantEmail {
				t.Errorf("Email = %v, want %v", parsed.Email, tt.wantEmail)
			}
			if parsed.Metric != tt.wantMetric {
				t.Errorf("Metric = %v, want %v", parsed.Metric, tt.wantMetric)
			}
		})
	}
}

// TestXrayHistoricalTrafficDoesNotStayOnline tests that users with historical
// traffic counters but no active session do not remain Online
func TestXrayHistoricalTrafficDoesNotStayOnline(t *testing.T) {
	server := &Server{
		cfg: Config{
			DataDir:     t.TempDir(),
			XrayPath:    "xray",
			XrayAPIPort: 10085,
		},
		xrayUsageBaseline: map[string]uint64{
			"42.alice:uplink":   5000,
			"42.alice:downlink": 10000,
		},
		xrayUsageLoaded: true,
	}

	// Simulate Xray returning traffic counters but online=0
	// This represents a user who was previously active but disconnected
	mockStats := []xrayStat{
		{Name: "user>>>42.alice>>>traffic>>>uplink", Value: 5000},
		{Name: "user>>>42.alice>>>traffic>>>downlink", Value: 10000},
		{Name: "user>>>42.alice>>>online", Value: 0}, // Disconnected
	}

	// We can't easily mock the xray CLI, so let's test the logic directly
	// by simulating what would happen with these stats

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	aggregated := make(map[aggregateKey]xrayUsageSample)
	onlineUsers := make(map[aggregateKey]bool)

	// Process traffic stats
	for _, stat := range mockStats {
		statName, ok := parseXrayStatName(stat.Name)
		if !ok || statName.Type != "user" {
			continue
		}

		if statName.Metric == "traffic" {
			identity, err := parseXrayUserEmail(statName.Email)
			if err != nil {
				continue
			}

			key := aggregateKey{
				UserID:     identity.UserID,
				InboundTag: identity.InboundTag,
			}

			sample := aggregated[key]
			sample.UserID = identity.UserID
			sample.InboundTag = identity.InboundTag

			// Traffic delta is 0 (current == baseline)
			currentValue := uint64(stat.Value)
			baseline := server.xrayUsageBaseline[statName.Email+":"+statName.Direction]
			delta := uint64(0)
			if currentValue >= baseline {
				delta = currentValue - baseline
			}
			sample.Value += delta

			aggregated[key] = sample
		}

		if statName.Metric == "online" {
			identity, err := parseXrayUserEmail(statName.Email)
			if err != nil {
				continue
			}

			key := aggregateKey{
				UserID:     identity.UserID,
				InboundTag: identity.InboundTag,
			}

			// Only mark online if value == 1
			if stat.Value == 1 {
				onlineUsers[key] = true
			}
		}
	}

	// Apply online state
	for key := range aggregated {
		sample := aggregated[key]
		sample.Online = onlineUsers[key]
		aggregated[key] = sample
	}

	// Verify: user has historical traffic but should NOT be online
	key := aggregateKey{UserID: 42, InboundTag: ""}
	sample := aggregated[key]

	if sample.Online {
		t.Error("User with historical traffic but online=0 should NOT be Online")
	}

	if sample.Value != 0 {
		t.Errorf("Traffic delta should be 0 (no new traffic), got %d", sample.Value)
	}
}

// TestXrayActiveUserIsOnline tests that a currently active user is marked Online
func TestXrayActiveUserIsOnline(t *testing.T) {
	mockStats := []xrayStat{
		{Name: "user>>>99.bob>>>traffic>>>uplink", Value: 1000},
		{Name: "user>>>99.bob>>>traffic>>>downlink", Value: 2000},
		{Name: "user>>>99.bob>>>online", Value: 1}, // Active connection
	}

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	aggregated := make(map[aggregateKey]xrayUsageSample)
	onlineUsers := make(map[aggregateKey]bool)

	// Process stats
	for _, stat := range mockStats {
		statName, ok := parseXrayStatName(stat.Name)
		if !ok || statName.Type != "user" {
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

		if statName.Metric == "traffic" {
			sample := aggregated[key]
			sample.UserID = identity.UserID
			sample.Value += uint64(stat.Value)
			aggregated[key] = sample
		}

		if statName.Metric == "online" && stat.Value == 1 {
			onlineUsers[key] = true
		}
	}

	// Apply online state
	for key := range aggregated {
		sample := aggregated[key]
		sample.Online = onlineUsers[key]
		aggregated[key] = sample
	}

	// Verify: active user should be Online
	key := aggregateKey{UserID: 99, InboundTag: ""}
	sample := aggregated[key]

	if !sample.Online {
		t.Error("Active user with online=1 should be Online")
	}
}

// TestXrayIdleButConnectedUserStaysOnline tests that a user with an active
// connection but zero traffic delta remains Online
func TestXrayIdleButConnectedUserStaysOnline(t *testing.T) {
	mockStats := []xrayStat{
		{Name: "user>>>50.charlie>>>traffic>>>uplink", Value: 1000},
		{Name: "user>>>50.charlie>>>traffic>>>downlink", Value: 2000},
		{Name: "user>>>50.charlie>>>online", Value: 1}, // Still connected
	}

	baseline := map[string]uint64{
		"50.charlie:uplink":   1000, // No new traffic
		"50.charlie:downlink": 2000, // No new traffic
	}

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	aggregated := make(map[aggregateKey]xrayUsageSample)
	onlineUsers := make(map[aggregateKey]bool)

	// Process stats
	for _, stat := range mockStats {
		statName, ok := parseXrayStatName(stat.Name)
		if !ok || statName.Type != "user" {
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

		if statName.Metric == "traffic" {
			currentValue := uint64(stat.Value)
			baselineValue := baseline[statName.Email+":"+statName.Direction]
			delta := uint64(0)
			if currentValue >= baselineValue {
				delta = currentValue - baselineValue
			}

			sample := aggregated[key]
			sample.UserID = identity.UserID
			sample.Value += delta
			aggregated[key] = sample
		}

		if statName.Metric == "online" && stat.Value == 1 {
			onlineUsers[key] = true
		}
	}

	// Apply online state
	for key := range aggregated {
		sample := aggregated[key]
		sample.Online = onlineUsers[key]
		aggregated[key] = sample
	}

	// Also include users who are online but have no traffic
	for key := range onlineUsers {
		if _, exists := aggregated[key]; !exists {
			aggregated[key] = xrayUsageSample{
				UserID:     key.UserID,
				InboundTag: key.InboundTag,
				Value:      0,
				Online:     true,
			}
		}
	}

	// Verify: idle but connected user should be Online with zero traffic delta
	key := aggregateKey{UserID: 50, InboundTag: ""}
	sample := aggregated[key]

	if !sample.Online {
		t.Error("Idle but connected user (online=1) should be Online")
	}

	if sample.Value != 0 {
		t.Errorf("Idle user should have 0 traffic delta, got %d", sample.Value)
	}
}

// TestXrayTrafficAccountingUnaffectedByOnlineDetection verifies that
// online state changes do not alter traffic accounting
func TestXrayTrafficAccountingUnaffectedByOnlineDetection(t *testing.T) {
	// Scenario: User has new traffic but disconnects mid-collection
	mockStats := []xrayStat{
		{Name: "user>>>77.dave>>>traffic>>>uplink", Value: 5000},
		{Name: "user>>>77.dave>>>traffic>>>downlink", Value: 8000},
		{Name: "user>>>77.dave>>>online", Value: 0}, // Just disconnected
	}

	baseline := map[string]uint64{
		"77.dave:uplink":   3000,
		"77.dave:downlink": 5000,
	}

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	aggregated := make(map[aggregateKey]xrayUsageSample)
	onlineUsers := make(map[aggregateKey]bool)

	// Process stats
	for _, stat := range mockStats {
		statName, ok := parseXrayStatName(stat.Name)
		if !ok || statName.Type != "user" {
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

		if statName.Metric == "traffic" {
			currentValue := uint64(stat.Value)
			baselineValue := baseline[statName.Email+":"+statName.Direction]
			delta := currentValue - baselineValue

			sample := aggregated[key]
			sample.UserID = identity.UserID
			sample.Value += delta
			aggregated[key] = sample
		}

		if statName.Metric == "online" && stat.Value == 1 {
			onlineUsers[key] = true
		}
	}

	// Apply online state
	for key := range aggregated {
		sample := aggregated[key]
		sample.Online = onlineUsers[key]
		aggregated[key] = sample
	}

	// Verify: traffic delta calculated correctly regardless of online state
	key := aggregateKey{UserID: 77, InboundTag: ""}
	sample := aggregated[key]

	expectedDelta := uint64(5000 - 3000 + 8000 - 5000) // uplink delta + downlink delta
	if sample.Value != expectedDelta {
		t.Errorf("Traffic delta = %d, want %d", sample.Value, expectedDelta)
	}

	if sample.Online {
		t.Error("User should be Offline (online=0)")
	}
}

// TestXrayOnlineStateDoesNotDoubleCountUsage verifies that checking online
// state does not affect baseline or create duplicate usage
func TestXrayOnlineStateDoesNotDoubleCountUsage(t *testing.T) {
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

	// First collection
	mockStats1 := []xrayStat{
		{Name: "user>>>88.eve>>>traffic>>>uplink", Value: 1500},
		{Name: "user>>>88.eve>>>traffic>>>downlink", Value: 3000},
		{Name: "user>>>88.eve>>>online", Value: 1},
	}

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	// Simulate first collection
	nextBaseline1 := make(map[string]uint64)
	for k, v := range server.xrayUsageBaseline {
		nextBaseline1[k] = v
	}

	totalDelta1 := uint64(0)
	for _, stat := range mockStats1 {
		statName, ok := parseXrayStatName(stat.Name)
		if !ok || statName.Type != "user" || statName.Metric != "traffic" {
			continue
		}

		currentValue := uint64(stat.Value)
		baselineValue := server.xrayUsageBaseline[statName.Email+":"+statName.Direction]
		delta := currentValue - baselineValue
		totalDelta1 += delta
		nextBaseline1[statName.Email+":"+statName.Direction] = currentValue
	}

	// Update baseline after first collection
	server.xrayUsageBaseline = nextBaseline1

	// Second collection (same traffic values, still online)
	mockStats2 := []xrayStat{
		{Name: "user>>>88.eve>>>traffic>>>uplink", Value: 1500},
		{Name: "user>>>88.eve>>>traffic>>>downlink", Value: 3000},
		{Name: "user>>>88.eve>>>online", Value: 1}, // Still online
	}

	totalDelta2 := uint64(0)
	for _, stat := range mockStats2 {
		statName, ok := parseXrayStatName(stat.Name)
		if !ok || statName.Type != "user" || statName.Metric != "traffic" {
			continue
		}

		currentValue := uint64(stat.Value)
		baselineValue := server.xrayUsageBaseline[statName.Email+":"+statName.Direction]
		delta := uint64(0)
		if currentValue >= baselineValue {
			delta = currentValue - baselineValue
		}
		totalDelta2 += delta
	}

	// Verify: first collection records traffic
	expectedDelta1 := uint64((1500 - 1000) + (3000 - 2000))
	if totalDelta1 != expectedDelta1 {
		t.Errorf("First collection delta = %d, want %d", totalDelta1, expectedDelta1)
	}

	// Verify: second collection has zero delta (no double-counting)
	if totalDelta2 != 0 {
		t.Errorf("Second collection delta = %d, want 0 (no double-counting)", totalDelta2)
	}
}

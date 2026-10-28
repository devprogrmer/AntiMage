package xrayconfig

import (
	"encoding/json"
	"math"
	"testing"
)

func TestExtractInboundSpeedLimitsAcceptsJSONNumbersAndRemovesMetadata(t *testing.T) {
	for name, raw := range map[string]any{
		"float":       float64(8_000_000),
		"json number": json.Number("8000000"),
		"integer":     int64(8_000_000),
		"string":      "8000000",
	} {
		t.Run(name, func(t *testing.T) {
			up, down, clean, err := extractInboundSpeedLimits(map[string]any{
				"protocol": "wireguard", "upload_speed_limit": raw,
			}, 0, 4_000_000)
			if err != nil {
				t.Fatal(err)
			}
			if up != 8_000_000 || down != 4_000_000 {
				t.Fatalf("limits = %d/%d, want 8000000/4000000", up, down)
			}
			if _, ok := clean["upload_speed_limit"]; ok {
				t.Fatal("upload speed metadata leaked into core config payload")
			}
			if _, ok := clean["download_speed_limit"]; ok {
				t.Fatal("download speed metadata leaked into core config payload")
			}
			if clean["protocol"] != "wireguard" {
				t.Fatalf("other config fields changed: %#v", clean)
			}
		})
	}
}

func TestInboundSpeedLimitRejectsInvalidNumericValues(t *testing.T) {
	for name, raw := range map[string]any{
		"fractional":  float64(8_000_000.5),
		"negative":    int64(-1),
		"too large":   maxInboundSpeedLimitBits + 1,
		"infinite":    math.Inf(1),
		"not numeric": "8mbps",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := inboundSpeedLimitValue(map[string]any{"upload_speed_limit": raw}, "upload_speed_limit", 0); err == nil {
				t.Fatalf("accepted invalid value %#v", raw)
			}
		})
	}
}

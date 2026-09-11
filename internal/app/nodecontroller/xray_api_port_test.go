package nodecontroller

import "testing"

func TestApplyRuntimeAPIUsesRequestedPort(t *testing.T) {
	raw := map[string]any{
		"inbounds": []any{
			map[string]any{
				"tag":      "API_INBOUND",
				"listen":   "127.0.0.1",
				"port":     10085,
				"protocol": "tunnel",
			},
		},
	}

	applyRuntimeAPI(raw, 10090)

	inbounds := listOfMaps(raw["inbounds"])
	if len(inbounds) != 1 {
		t.Fatalf("inbounds=%d, want 1", len(inbounds))
	}

	apiInbound := inbounds[0]
	if got := stringValue(apiInbound["tag"]); got != "API_INBOUND" {
		t.Fatalf("tag=%q, want API_INBOUND", got)
	}

	port, ok := apiInbound["port"].(int)
	if !ok {
		t.Fatalf("API_INBOUND port type=%T, want int", apiInbound["port"])
	}
	if port != 10090 {
		t.Fatalf("API_INBOUND port=%d, want 10090", port)
	}
}

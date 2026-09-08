package nodeagent

import (
	"testing"
)

func TestParseRouteOutboundStats(t *testing.T) {
	raw := []byte(`{
"stat": [
{
"name":"outbound>>>direct>>>traffic>>>uplink",
"value":"120"
},
{
"name":"outbound>>>direct>>>traffic>>>downlink",
"value":"880"
},
{
"name":"outbound>>>fallback>>>traffic>>>uplink",
"value":25
},
{
"name":"outbound>>>fallback>>>traffic>>>downlink",
"value":75
},
{
"name":"inbound>>>test>>>traffic>>>uplink",
"value":"9999"
}
]
}`)

	tag, groups, traffic, err := parseRouteOutboundStats(raw)
	if err != nil {
		t.Fatalf("parseRouteOutboundStats: %v", err)
	}

	if tag != "direct" {
		t.Fatalf("outbound tag=%q want direct", tag)
	}

	if len(groups) != 1 || groups[0] != "fallback" {
		t.Fatalf("unexpected group tags: %#v", groups)
	}

	if len(traffic) != 2 {
		t.Fatalf("unexpected traffic: %#v", traffic)
	}

	if traffic[0].GetTag() != "direct" {
		t.Fatalf("first traffic tag=%q", traffic[0].GetTag())
	}

	if traffic[0].GetUp() != 120 {
		t.Fatalf("direct up=%d", traffic[0].GetUp())
	}

	if traffic[0].GetDown() != 880 {
		t.Fatalf("direct down=%d", traffic[0].GetDown())
	}
}

func TestParseRouteOutboundStatsIgnoresZeroCounters(t *testing.T) {
	raw := []byte(`{
"stat": [
{
"name":"outbound>>>direct>>>traffic>>>uplink",
"value":"0"
},
{
"name":"outbound>>>direct>>>traffic>>>downlink",
"value":"0"
}
]
}`)

	tag, groups, traffic, err := parseRouteOutboundStats(raw)
	if err != nil {
		t.Fatalf("parseRouteOutboundStats: %v", err)
	}

	if tag != "" {
		t.Fatalf("unexpected outbound tag=%q", tag)
	}

	if len(groups) != 0 {
		t.Fatalf("unexpected groups=%#v", groups)
	}

	if len(traffic) != 0 {
		t.Fatalf("unexpected traffic=%#v", traffic)
	}
}

func TestParseOutboundStatName(t *testing.T) {
	tag, direction, ok := parseOutboundStatName(
		"outbound>>>warp>>>traffic>>>downlink",
	)

	if !ok {
		t.Fatal("expected outbound stat name to parse")
	}

	if tag != "warp" {
		t.Fatalf("tag=%q", tag)
	}

	if direction != "downlink" {
		t.Fatalf("direction=%q", direction)
	}
}

func TestParseRouteOutboundStatsRejectsInvalidJSON(t *testing.T) {
	_, _, _, err := parseRouteOutboundStats(
		[]byte(`{broken`),
	)
	if err == nil {
		t.Fatal("expected invalid JSON to fail")
	}
}

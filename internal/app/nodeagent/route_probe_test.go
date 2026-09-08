package nodeagent

import (
	"encoding/json"
	"testing"
)

func TestBuildRouteProbeConfigPreservesRoutingAndUsesLoopback(t *testing.T) {
	source := `{
"inbounds":[
{
"tag":"production-in",
"listen":"0.0.0.0",
"port":443,
"protocol":"vless"
}
],
"outbounds":[
{
"tag":"direct",
"protocol":"freedom"
},
{
"tag":"blocked",
"protocol":"blackhole"
}
],
"routing":{
"domainStrategy":"AsIs",
"rules":[
{
"type":"field",
"inboundTag":["customer-in"],
"domain":["domain:example.com"],
"outboundTag":"blocked"
}
]
}
}`

	raw, apiTag, err := buildRouteProbeConfig(
		source,
		"customer-in",
		32100,
		32101,
	)
	if err != nil {
		t.Fatalf("buildRouteProbeConfig: %v", err)
	}

	if apiTag != "antimage-route-api-32101" {
		t.Fatalf("unexpected API tag: %q", apiTag)
	}

	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("decode probe config: %v", err)
	}

	inbounds, ok := config["inbounds"].([]any)
	if !ok || len(inbounds) != 2 {
		t.Fatalf("unexpected probe inbounds: %#v", config["inbounds"])
	}

	probeInbound := objectValue(inbounds[0])
	if probeInbound["tag"] != "customer-in" {
		t.Fatalf("probe inbound tag=%#v", probeInbound["tag"])
	}
	if probeInbound["listen"] != "127.0.0.1" {
		t.Fatalf("probe inbound is not loopback-only: %#v", probeInbound["listen"])
	}
	if intValue(probeInbound["port"]) != 32100 {
		t.Fatalf("probe inbound port=%#v", probeInbound["port"])
	}

	apiInbound := objectValue(inbounds[1])
	if apiInbound["listen"] != "127.0.0.1" {
		t.Fatalf("API inbound is not loopback-only: %#v", apiInbound["listen"])
	}
	if intValue(apiInbound["port"]) != 32101 {
		t.Fatalf("API inbound port=%#v", apiInbound["port"])
	}

	routing := objectValue(config["routing"])
	if routing["domainStrategy"] != "AsIs" {
		t.Fatalf("domainStrategy was not preserved: %#v", routing["domainStrategy"])
	}

	rules, ok := routing["rules"].([]any)
	if !ok || len(rules) != 2 {
		t.Fatalf("unexpected routing rules: %#v", routing["rules"])
	}

	apiRule := objectValue(rules[0])
	if apiRule["outboundTag"] != apiTag {
		t.Fatalf("API rule does not target API outbound: %#v", apiRule)
	}

	originalRule := objectValue(rules[1])
	if originalRule["outboundTag"] != "blocked" {
		t.Fatalf("original route rule changed: %#v", originalRule)
	}

	policy := objectValue(config["policy"])
	system := objectValue(policy["system"])

	if system["statsOutboundUplink"] != true {
		t.Fatal("outbound uplink stats were not enabled")
	}
	if system["statsOutboundDownlink"] != true {
		t.Fatal("outbound downlink stats were not enabled")
	}

	outbounds, ok := config["outbounds"].([]any)
	if !ok || len(outbounds) != 2 {
		t.Fatalf("outbounds were not preserved: %#v", config["outbounds"])
	}
}

func TestBuildRouteProbeConfigRejectsMissingOutbounds(t *testing.T) {
	_, _, err := buildRouteProbeConfig(
		`{"routing":{"rules":[]}}`,
		"test-in",
		32000,
		32001,
	)
	if err == nil {
		t.Fatal("expected config without outbounds to fail")
	}
}

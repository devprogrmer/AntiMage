package api

import (
	"encoding/json"
	"testing"

	"github.com/antimage/antimage/internal/app/nodecontroller"
)

func TestNodeAccountingDiagnosticsReachLiveClients(t *testing.T) {
	health := &nodecontroller.AccountingHealth{
		Protocols: []nodecontroller.AccountingProtocolStatus{{Protocol: "xray", PendingBytes: 42}},
	}
	node := nodecontroller.NodeListItem{ID: 7, Accounting: health}
	if got := flattenNodeItem(node)["accounting"]; got != health {
		t.Fatalf("node details accounting = %#v", got)
	}
	if got := flattenNodeLiveItem(node)["accounting"]; got != health {
		t.Fatalf("live accounting = %#v", got)
	}
	if _, ok := flattenNodeStaticItem(node)["accounting"]; ok {
		t.Fatal("static update includes live accounting diagnostics")
	}
	node.Accounting = nil
	if _, ok := flattenNodeItem(node)["accounting"]; ok {
		t.Fatal("static node response must omit unavailable diagnostics")
	}
	raw, err := json.Marshal(flattenNodeLiveItem(node)["accounting"])
	if err != nil || string(raw) != "null" {
		t.Fatalf("unavailable live accounting must serialize as null: %s, %v", raw, err)
	}
	if _, ok := flattenNodeLiveItem(node)["accounting"]; !ok {
		t.Fatal("live update must explicitly clear unavailable diagnostics")
	}
	for _, status := range []string{"disabled", "limited"} {
		node.Status = status
		if value, ok := flattenNodeStaticItem(node)["accounting"]; !ok || value != nil {
			t.Fatalf("%s node is skipped by the live poll and must clear accounting in its static update", status)
		}
	}
}

func TestFlattenNodeStaticItemOmitsRuntimeFields(t *testing.T) {
	xrayVersion := "26.5.9"
	nodeVersion := "dev-abc123"
	item := flattenNodeStaticItem(nodecontroller.NodeListItem{
		ID:                 7,
		Name:               "de-1",
		Address:            "192.0.2.7",
		Port:               62050,
		APIPort:            62051,
		Status:             "connected",
		XrayVersion:        &xrayVersion,
		NodeServiceVersion: &nodeVersion,
		CPU: nodecontroller.CPUInfo{
			UsagePercent: 42,
		},
		Transfer: nodecontroller.NetInfo{
			UploadSpeed:   100,
			DownloadSpeed: 200,
		},
	})

	for _, key := range []string{
		"status",
		"message",
		"node_service_version",
		"cpu_usage_percent",
		"upload_speed",
		"download_speed",
	} {
		if _, exists := item[key]; exists {
			t.Fatalf("runtime field %q must not be included in a static update", key)
		}
	}
	if item["xray_version"] != &xrayVersion {
		t.Fatalf("persisted xray version was not preserved: %#v", item["xray_version"])
	}
	if item["name"] != "de-1" {
		t.Fatalf("static node fields were not preserved: %#v", item)
	}
}

func TestFlattenNodeLiveItemOmitsUnavailableRuntime(t *testing.T) {
	item := flattenNodeLiveItem(nodecontroller.NodeListItem{
		ID:      7,
		Name:    "de-1",
		Address: "192.0.2.7",
		Status:  "error",
	})
	for _, key := range []string{
		"node_service_version",
		"cpu_usage_percent",
		"upload_speed",
		"download_speed",
	} {
		if _, exists := item[key]; exists {
			t.Fatalf("unavailable runtime field %q must not overwrite cached data", key)
		}
	}
}

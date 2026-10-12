package nodecontroller

import "testing"

func TestApplyRuntimePreservesUnknownInstalledAndRunningVersions(t *testing.T) {
	installed := "disk-tag-v1"
	item := NodeListItem{NodeBinaryTag: &installed}
	applyRuntimeToNodeItem(&item, RuntimeResult{})
	if item.RunningNodeVersion != nil {
		t.Fatalf("running version inferred from installed tag: %q", *item.RunningNodeVersion)
	}
	if item.InstalledNodeVersion != nil {
		t.Fatalf("missing installed report was fabricated: %q", *item.InstalledNodeVersion)
	}
}

func TestApplyRuntimeMapsReportedRunningVersionWithoutChangingLegacyFields(t *testing.T) {
	legacy := "v2.3.4"
	item := NodeListItem{NodeServiceVersion: &legacy}
	applyRuntimeToNodeItem(&item, RuntimeResult{NodeServiceVersion: "v2.3.5", RunningNodeVersion: "v2.3.5", InstalledNodeVersion: "v2.3.5", DesiredNodeVersion: "v2.3.5", UpdateChannel: "stable"})
	if item.NodeServiceVersion == nil || *item.NodeServiceVersion != "v2.3.5" || item.RunningNodeVersion == nil || *item.RunningNodeVersion != "v2.3.5" {
		t.Fatalf("reported running version mapping broke compatibility: %+v", item)
	}
	if item.InstalledNodeVersion == nil || *item.InstalledNodeVersion != "v2.3.5" || item.DesiredNodeVersion == nil || *item.DesiredNodeVersion != "v2.3.5" {
		t.Fatalf("version state fields were not mapped: %+v", item)
	}
}

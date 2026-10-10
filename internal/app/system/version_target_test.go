package system

import (
	"strings"
	"testing"
)

func TestResolveBuildFreezesExactPlatformArtifact(t *testing.T) {
	old := BuildCatalogEntry{Version: "v1.2.0", Channel: "stable", Commit: strings.Repeat("a", 40), PublishedAt: "2026-10-01T00:00:00Z", ArtifactName: "panel.tar.gz", DownloadURL: "https://example.test/v1.2.0/panel.tar.gz", SHA256: strings.Repeat("a", 64), Size: 42, OS: "linux", Architecture: "amd64"}
	next := old
	next.Version = "v1.3.0"
	next.PublishedAt = "2026-10-02T00:00:00Z"
	next.SHA256 = strings.Repeat("b", 64)
	catalog := VersionCatalog{Stable: []BuildCatalogEntry{old, next}}
	got, err := ResolveBuild(catalog, "stable", "latest", "", "linux", "amd64")
	if err != nil || got.Version != next.Version || got.SHA256 != next.SHA256 {
		t.Fatalf("latest=%+v, %v", got, err)
	}
	pinned, err := ResolveBuild(catalog, "stable", "pinned", old.Version, "linux", "amd64")
	if err != nil || pinned.Version != old.Version {
		t.Fatalf("pinned=%+v, %v", pinned, err)
	}
	catalog.Stable[1].SHA256 = strings.Repeat("c", 64)
	if got.SHA256 != next.SHA256 {
		t.Fatal("resolved identity changed with catalog")
	}
	for _, test := range []struct{ policy, version, os, arch string }{
		{"latest", old.Version, "linux", "amd64"}, {"pinned", "v9.0.0", "linux", "amd64"},
		{"pinned", old.Version, "linux", "arm64"}, {"pinned", "", "linux", "amd64"},
	} {
		if _, err := ResolveBuild(catalog, "stable", test.policy, test.version, test.os, test.arch); err == nil {
			t.Fatalf("accepted invalid selection: %+v", test)
		}
	}
}

func TestResolveBuildRejectsUnverifiableOrAmbiguousArtifacts(t *testing.T) {
	entry := BuildCatalogEntry{Version: "dev-abcdef1", Channel: "dev", Commit: "abcdef1" + strings.Repeat("0", 33), WorkflowRunID: "1234", PublishedAt: "2026-10-01T00:00:00Z", ArtifactName: "node", DownloadURL: "https://example.test/node", SHA256: strings.Repeat("a", 64), Size: 42, OS: "linux", Architecture: "amd64"}
	if _, err := ResolveBuild(VersionCatalog{Dev: []BuildCatalogEntry{entry}}, "dev", "pinned", entry.Version, "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BuildCatalogEntry){
		func(e *BuildCatalogEntry) { e.Size = 0 }, func(e *BuildCatalogEntry) { e.SHA256 = "bad" },
		func(e *BuildCatalogEntry) { e.Commit = "abcdef1" }, func(e *BuildCatalogEntry) { e.Version = "dev-fffffff" },
		func(e *BuildCatalogEntry) { e.DownloadURL = "http://example.test/node" }, func(e *BuildCatalogEntry) { e.WorkflowRunID = "" },
	} {
		bad := entry
		mutate(&bad)
		if _, err := ResolveBuild(VersionCatalog{Dev: []BuildCatalogEntry{bad}}, "dev", "pinned", bad.Version, "linux", "amd64"); err == nil {
			t.Fatalf("accepted unverifiable entry: %+v", bad)
		}
	}
	if _, err := ResolveBuild(VersionCatalog{Dev: []BuildCatalogEntry{entry, entry}}, "dev", "pinned", entry.Version, "linux", "amd64"); err == nil {
		t.Fatal("ambiguous version accepted")
	}
}

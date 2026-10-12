package system

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGitHubVersionCatalogFiltersToDownloadableVerifiedArtifactsAndRefreshes(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/test/AntiMage/commits/v1.2.0":
			_, _ = w.Write([]byte(`{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
		case "/repos/test/AntiMage/releases":
			_, _ = w.Write([]byte(`[{
"tag_name":"v1.2.0","name":"v1.2.0","draft":false,"prerelease":false,"published_at":"2026-10-01T00:00:00Z",
"assets":[{"name":"antimage-linux-amd64.tar.gz","browser_download_url":"https://download/stable.tar.gz","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":42}]},
{"tag_name":"v1.3.0-rc1","draft":false,"prerelease":true,"assets":[]}]`))
		case "/repos/test/AntiMage/releases/tags/dev-builds":
			_, _ = w.Write([]byte(`{"assets":[{"name":"antimage-linux-amd64-dev-abcdef1.tar.gz","browser_download_url":"https://download/dev.tar.gz","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":84},{"name":"antimage-node-dev-abcdef1-linux-amd64","browser_download_url":"https://download/node","digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":20}]}`))
		case "/test/AntiMage/dev-build-manifest/dev-builds.json":
			_, _ = w.Write([]byte(`{"builds":[{"tag":"dev-abcdef1","sha":"abcdef1234567890","branch":"dev","run_id":"1234","created_at":"2026-10-02T00:00:00Z","assets":{"linux-amd64":{"name":"antimage-linux-amd64-dev-abcdef1.tar.gz","url":"https://download/dev.tar.gz"},"node-linux-amd64":{"name":"antimage-node-dev-abcdef1-linux-amd64","url":"https://download/node"}}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	checker := &GitHubUpdateChecker{APIBase: server.URL, RawBase: server.URL, ManifestBranch: "dev-build-manifest", ManifestPath: "dev-builds.json", OS: "linux", Arch: "amd64"}
	catalog, err := checker.Versions(context.Background(), "test/AntiMage", "panel", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Stable) != 1 || catalog.Stable[0].Version != "v1.2.0" || catalog.Stable[0].SHA256[0] != 'a' {
		t.Fatalf("stable catalog = %#v", catalog.Stable)
	}
	if len(catalog.Dev) != 1 || catalog.Dev[0].Version != "dev-abcdef1" || catalog.Dev[0].WorkflowRunID != "1234" {
		t.Fatalf("dev catalog = %#v", catalog.Dev)
	}
	firstRequestCount := requests.Load()
	if _, err := checker.Versions(context.Background(), "test/AntiMage", "panel", false); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != firstRequestCount {
		t.Fatal("catalog cache did not prevent duplicate GitHub requests")
	}
	if _, err := checker.Versions(context.Background(), "test/AntiMage", "panel", true); err != nil {
		t.Fatal(err)
	}
	if requests.Load() <= firstRequestCount {
		t.Fatal("explicit refresh did not bypass the cache")
	}
	nodeCatalog, err := checker.Versions(context.Background(), "test/AntiMage", "node", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodeCatalog.Dev) != 1 || nodeCatalog.Dev[0].ArtifactName != "antimage-node-dev-abcdef1-linux-amd64" {
		t.Fatalf("node dev catalog = %#v", nodeCatalog.Dev)
	}
}

func TestChecksumFromTextRejectsMalformedSHA(t *testing.T) {
	if got := checksumFromText(strings.Repeat("a", 64)+"  artifact.bin", "artifact.bin"); got == "" {
		t.Fatal("valid SHA256 entry was rejected")
	}
	if got := checksumFromText("not-a-hash  artifact.bin", "artifact.bin"); got != "" {
		t.Fatalf("malformed SHA256 accepted: %q", got)
	}
}

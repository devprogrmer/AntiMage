package system

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type staticRuntimeDetector struct{ info RuntimeInfo }

func (d staticRuntimeDetector) Info() RuntimeInfo { return d.info }

type recordingUpdateChecker struct{ repos []string }

func (c *recordingUpdateChecker) Status(_ context.Context, repo string, _ *string, _ string) UpdateStatus {
	c.repos = append(c.repos, repo)
	return UpdateStatus{Repo: repo}
}

func TestMaintenanceInfoUsesCurrentRepositoryForUpdates(t *testing.T) {
	checker := &recordingUpdateChecker{}
	service := NewMaintenanceServiceWithDeps(staticRuntimeDetector{}, checker, nil)
	info, err := service.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := "devprogrmer/AntiMage"
	if info.Panel.Update == nil || info.Panel.Update.Repo != want {
		t.Fatalf("panel update repo = %#v, want %q", info.Panel.Update, want)
	}
	if info.NodeUpdate.Repo != want {
		t.Fatalf("node update repo = %q, want %q", info.NodeUpdate.Repo, want)
	}
	if len(checker.repos) != 2 || checker.repos[0] != want || checker.repos[1] != want {
		t.Fatalf("update repositories = %#v, want two entries for %q", checker.repos, want)
	}
}

func TestGitHubUpdateCheckerCachesSuccessfulStatus(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		switch r.URL.Path {
		case "/repos/antimagepanel/AntiMage/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name":     "v0.2.0",
				"name":         "v0.2.0",
				"published_at": "2026-06-24T00:00:00Z",
			})
		case "/antimagepanel/AntiMage/dev-build-manifest/dev-builds.json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"latest": map[string]any{
					"build_tag":    "dev-abcdef0",
					"sha":          "abcdef0123456789",
					"run_id":       "123",
					"generated_at": "2026-06-24T00:00:00Z",
					"assets": []string{
						"antimage-linux-amd64-dev-abcdef0.tar.gz",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	now := time.Unix(1_780_000_000, 0)
	checker := &GitHubUpdateChecker{
		APIBase:        server.URL,
		RawBase:        server.URL,
		HTTPClient:     server.Client(),
		ManifestBranch: "dev-build-manifest",
		ManifestPath:   "dev-builds.json",
		Now:            func() time.Time { return now },
		CacheTTL:       time.Hour,
		ErrorTTL:       time.Hour,
	}
	current := "dev-0000000"

	first := checker.Status(context.Background(), "antimagepanel/AntiMage", &current, "dev")
	second := checker.Status(context.Background(), "antimagepanel/AntiMage", &current, "dev")

	if first.Error != "" || second.Error != "" {
		t.Fatalf("unexpected errors: first=%q second=%q", first.Error, second.Error)
	}
	if first.Target == nil || *first.Target != "dev-abcdef0" {
		t.Fatalf("unexpected first target: %#v", first.Target)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected one release and one manifest request, got %d", got)
	}
}

func TestGitHubUpdateCheckerFindsDevBuildThroughWorkflowEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/antimagepanel/AntiMage/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.2.0"})
		case "/antimagepanel/AntiMage/dev-build-manifest/dev-builds.json":
			http.NotFound(w, r)
		case "/repos/antimagepanel/AntiMage/actions/workflows/binary-build.yml/runs":
			query := r.URL.Query()
			if query.Get("branch") != "dev" || query.Get("event") != "push" || query.Get("status") != "success" {
				t.Fatalf("unexpected workflow query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workflow_runs": []map[string]any{
					{
						"head_branch": "dev",
						"event":       "push",
						"conclusion":  "success",
						"status":      "completed",
						"head_sha":    "abcdef0123456789",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	checker := &GitHubUpdateChecker{
		APIBase:        server.URL,
		RawBase:        server.URL,
		HTTPClient:     server.Client(),
		ManifestBranch: "dev-build-manifest",
		ManifestPath:   "dev-builds.json",
	}
	current := "dev-0000000"
	status := checker.Status(context.Background(), "antimagepanel/AntiMage", &current, "dev")

	if status.Error != "" {
		t.Fatalf("unexpected update error: %q", status.Error)
	}
	if status.Target == nil || *status.Target != "dev-abcdef0" {
		t.Fatalf("unexpected dev target: %#v", status.Target)
	}
}

func TestSelectManifestBuildUsesLatestTag(t *testing.T) {
	data := map[string]any{
		"latest": "dev-newest",
		"builds": []any{
			map[string]any{"tag": "dev-older"},
			map[string]any{"tag": "dev-newest"},
		},
	}

	build := selectManifestBuild(data)
	if build == nil || stringFromAny((*build)["tag"]) != "dev-newest" {
		t.Fatalf("unexpected selected manifest build: %#v", build)
	}
}

func TestGitHubUpdateCheckerCachesErrors(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer server.Close()

	now := time.Unix(1_780_000_000, 0)
	checker := &GitHubUpdateChecker{
		APIBase:    server.URL,
		RawBase:    server.URL,
		HTTPClient: server.Client(),
		Now:        func() time.Time { return now },
		CacheTTL:   time.Hour,
		ErrorTTL:   time.Hour,
	}

	first := checker.Status(context.Background(), "antimagepanel/AntiMage", nil, "latest")
	second := checker.Status(context.Background(), "antimagepanel/AntiMage", nil, "latest")

	if first.Error == "" || second.Error == "" {
		t.Fatalf("expected cached error, got first=%q second=%q", first.Error, second.Error)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("expected one failed request to be cached, got %d", got)
	}
}

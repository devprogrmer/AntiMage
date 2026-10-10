package system

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type VersionCatalog struct {
	Stable []BuildCatalogEntry `json:"stable"`
	Dev    []BuildCatalogEntry `json:"dev"`
}

type BuildCatalogEntry struct {
	Version       string `json:"version"`
	Channel       string `json:"channel"`
	Commit        string `json:"commit,omitempty"`
	PublishedAt   string `json:"published_at,omitempty"`
	WorkflowRunID string `json:"workflow_run_id,omitempty"`
	ArtifactName  string `json:"artifact_name"`
	DownloadURL   string `json:"download_url"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	OS            string `json:"os"`
	Architecture  string `json:"arch"`
}

type VersionCatalogProvider interface {
	Versions(ctx context.Context, repo, target string, refresh bool) (VersionCatalog, error)
}

type versionCatalogCacheEntry struct {
	catalog   VersionCatalog
	expiresAt time.Time
}

func (c *GitHubUpdateChecker) Versions(ctx context.Context, repo, target string, refresh bool) (VersionCatalog, error) {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	target = strings.ToLower(strings.TrimSpace(target))
	if repo == "" || (target != "panel" && target != "node") {
		return VersionCatalog{}, fmt.Errorf("repo and target=panel|node are required")
	}
	key := repo + "|" + target
	now := c.now()
	c.mu.Lock()
	if c.catalogCache == nil {
		c.catalogCache = make(map[string]versionCatalogCacheEntry)
	}
	if !refresh {
		if cached, ok := c.catalogCache[key]; ok && now.Before(cached.expiresAt) {
			catalog := cloneVersionCatalog(cached.catalog)
			c.mu.Unlock()
			return catalog, nil
		}
	}
	c.mu.Unlock()

	catalog, err := c.fetchVersionCatalog(ctx, repo, target)
	if err != nil {
		return VersionCatalog{}, err
	}
	ttl := c.CacheTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	c.mu.Lock()
	c.catalogCache[key] = versionCatalogCacheEntry{catalog: cloneVersionCatalog(catalog), expiresAt: now.Add(ttl)}
	c.mu.Unlock()
	return catalog, nil
}

func (c *GitHubUpdateChecker) fetchVersionCatalog(ctx context.Context, repo, target string) (VersionCatalog, error) {
	goos := strings.TrimSpace(c.OS)
	if goos == "" {
		goos = runtime.GOOS
	}
	arch := strings.TrimSpace(c.Arch)
	if arch == "" {
		arch = runtime.GOARCH
	}
	if arch == "arm64" {
		arch = "arm64"
	}
	assetName := "antimage-linux-" + arch + ".tar.gz"
	manifestKey := "linux-" + arch
	if target == "node" {
		if goos != "linux" {
			return VersionCatalog{}, fmt.Errorf("node binary catalog is available only on Linux")
		}
		manifestKey = "node-linux-" + arch
	}
	api := strings.TrimRight(c.APIBase, "/") + "/repos/" + repo
	var releases []map[string]any
	if err := c.getJSON(ctx, api+"/releases?per_page=100", &releases); err != nil {
		return VersionCatalog{}, fmt.Errorf("load %s release catalog: %w", target, err)
	}
	var stable []BuildCatalogEntry
	for _, release := range releases {
		if release["draft"] == true || release["prerelease"] == true {
			continue
		}
		tag := firstNonEmptyString(stringFromAny(release["tag_name"]), stringFromAny(release["name"]))
		assets, _ := release["assets"].([]any)
		want := assetName
		if target == "node" {
			want = "antimage-node-" + tag + "-linux-" + arch
		}
		asset, ok := findReleaseAsset(assets, want)
		if !ok {
			continue
		}
		sha := assetSHA(asset)
		if sha == "" {
			checksumsName := "checksums.txt"
			checksums, _ := findReleaseAsset(assets, checksumsName)
			if checksums["browser_download_url"] != nil {
				text, err := c.getText(ctx, stringFromAny(checksums["browser_download_url"]))
				if err == nil {
					sha = checksumFromText(text, want)
				}
			}
		}
		if sha == "" {
			continue
		}
		var commitInfo map[string]any
		if err := c.getJSON(ctx, api+"/commits/"+url.PathEscape(tag), &commitInfo); err != nil {
			return VersionCatalog{}, fmt.Errorf("resolve release commit %s: %w", tag, err)
		}
		commit := strings.TrimSpace(stringFromAny(commitInfo["sha"]))
		if decoded, err := hex.DecodeString(commit); err != nil || len(decoded) != 20 {
			return VersionCatalog{}, fmt.Errorf("release %s has no exact commit", tag)
		}
		stable = append(stable, BuildCatalogEntry{
			Version: tag, Channel: "stable", Commit: commit, PublishedAt: stringFromAny(release["published_at"]),
			ArtifactName: want, DownloadURL: stringFromAny(asset["browser_download_url"]), SHA256: sha,
			Size: int64FromAny(asset["size"]), OS: "linux", Architecture: arch,
		})
	}

	manifestBranch := strings.TrimSpace(c.ManifestBranch)
	if manifestBranch == "" {
		manifestBranch = "dev-build-manifest"
	}
	manifestPath := strings.Trim(strings.TrimSpace(c.ManifestPath), "/")
	if manifestPath == "" {
		manifestPath = "dev-builds.json"
	}
	manifestURL := strings.TrimRight(c.RawBase, "/") + "/" + repo + "/" + manifestBranch + "/" + manifestPath
	var manifest map[string]any
	if err := c.getJSON(ctx, manifestURL, &manifest); err != nil {
		return VersionCatalog{}, fmt.Errorf("load successful dev build manifest: %w", err)
	}
	var rolling map[string]any
	if err := c.getJSON(ctx, api+"/releases/tags/dev-builds", &rolling); err != nil {
		return VersionCatalog{}, fmt.Errorf("load immutable dev artifacts: %w", err)
	}
	releaseAssets, _ := rolling["assets"].([]any)
	builds, _ := manifest["builds"].([]any)
	dev := make([]BuildCatalogEntry, 0, len(builds))
	for _, raw := range builds {
		build, ok := raw.(map[string]any)
		if !ok || stringFromAny(build["branch"]) != "dev" {
			continue
		}
		tag := strings.TrimSpace(stringFromAny(build["tag"]))
		sha := strings.TrimSpace(stringFromAny(build["sha"]))
		assetMap, _ := build["assets"].(map[string]any)
		assetInfo, _ := assetMap[manifestKey].(map[string]any)
		name := strings.TrimSpace(stringFromAny(assetInfo["name"]))
		if tag == "" || sha == "" || name == "" {
			continue
		}
		asset, ok := findReleaseAsset(releaseAssets, name)
		if !ok {
			continue
		}
		artifactSHA := assetSHA(asset)
		if artifactSHA == "" {
			checksumsName := "checksums-" + tag + ".txt"
			checksums, ok := findReleaseAsset(releaseAssets, checksumsName)
			if ok {
				text, err := c.getText(ctx, stringFromAny(checksums["browser_download_url"]))
				if err == nil {
					artifactSHA = checksumFromText(text, name)
				}
			}
		}
		if artifactSHA == "" {
			continue
		}
		dev = append(dev, BuildCatalogEntry{
			Version: tag, Channel: "dev", Commit: sha, PublishedAt: firstNonEmptyString(stringFromAny(build["created_at"]), stringFromAny(build["generated_at"])),
			WorkflowRunID: stringFromAny(build["run_id"]), ArtifactName: name,
			DownloadURL: stringFromAny(asset["browser_download_url"]), SHA256: artifactSHA,
			Size: int64FromAny(asset["size"]), OS: "linux", Architecture: arch,
		})
	}
	return VersionCatalog{Stable: stable, Dev: dev}, nil
}

func findReleaseAsset(assets []any, name string) (map[string]any, bool) {
	for _, raw := range assets {
		asset, ok := raw.(map[string]any)
		if ok && stringFromAny(asset["name"]) == name {
			return asset, true
		}
	}
	return nil, false
}

func assetSHA(asset map[string]any) string {
	digest := strings.TrimSpace(stringFromAny(asset["digest"]))
	digest = strings.TrimPrefix(strings.ToLower(digest), "sha256:")
	if len(digest) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return ""
	}
	return digest
}

func checksumFromText(text, name string) string {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == name && len(fields[0]) == 64 {
			if _, err := hex.DecodeString(fields[0]); err == nil {
				return strings.ToLower(fields[0])
			}
		}
	}
	return ""
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	default:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(stringFromAny(value)), 10, 64)
		return parsed
	}
}

func (c *GitHubUpdateChecker) getText(ctx context.Context, url string) (string, error) {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "AntiMage-update-check")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(body), err
}

func cloneVersionCatalog(catalog VersionCatalog) VersionCatalog {
	return VersionCatalog{Stable: append([]BuildCatalogEntry(nil), catalog.Stable...), Dev: append([]BuildCatalogEntry(nil), catalog.Dev...)}
}

var _ VersionCatalogProvider = (*GitHubUpdateChecker)(nil)

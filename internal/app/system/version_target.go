package system

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

type ResolvedInstall struct {
	BuildCatalogEntry
	RequestedChannel string `json:"requested_channel"`
	RequestedPolicy  string `json:"requested_policy"`
	RequestedVersion string `json:"requested_version"`
}

func ResolveInstall(catalog VersionCatalog, channel, policy, version, os, arch string) (ResolvedInstall, error) {
	if channel == "" || channel == "latest" {
		channel = "stable"
	}
	if policy == "" {
		policy = "latest"
		if version != "" {
			policy = "pinned"
		}
	}
	entry, err := ResolveBuild(catalog, channel, policy, version, os, arch)
	return ResolvedInstall{BuildCatalogEntry: entry, RequestedChannel: channel, RequestedPolicy: policy, RequestedVersion: version}, err
}

// ResolveBuild freezes a catalog selection into artifact identity. Callers must
// persist this value before scheduling installation and never resolve it again
// during retries of the same operation.
func ResolveBuild(catalog VersionCatalog, channel, policy, version, os, arch string) (BuildCatalogEntry, error) {
	if channel != "stable" && channel != "dev" {
		return BuildCatalogEntry{}, fmt.Errorf("channel must be stable or dev")
	}
	if policy != "pinned" && policy != "latest" {
		return BuildCatalogEntry{}, fmt.Errorf("policy must be pinned or latest")
	}
	if policy == "pinned" && strings.TrimSpace(version) == "" {
		return BuildCatalogEntry{}, fmt.Errorf("pinned policy requires an exact version")
	}
	if policy == "latest" && version != "" {
		return BuildCatalogEntry{}, fmt.Errorf("latest policy cannot include a pinned version")
	}
	entries := catalog.Stable
	if channel == "dev" {
		entries = catalog.Dev
	}
	candidates := make([]BuildCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Channel != channel || entry.OS != os || entry.Architecture != arch {
			continue
		}
		if policy == "pinned" && entry.Version != version {
			continue
		}
		if err := validateBuildIdentity(entry); err != nil {
			return BuildCatalogEntry{}, err
		}
		candidates = append(candidates, entry)
	}
	if len(candidates) == 0 {
		return BuildCatalogEntry{}, fmt.Errorf("no verified %s artifact for %s/%s and requested policy", channel, os, arch)
	}
	if policy == "pinned" && len(candidates) != 1 {
		return BuildCatalogEntry{}, fmt.Errorf("ambiguous artifact identity for %s", version)
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339, candidates[i].PublishedAt)
		b, _ := time.Parse(time.RFC3339, candidates[j].PublishedAt)
		if a.Equal(b) {
			return candidates[i].Version > candidates[j].Version
		}
		return a.After(b)
	})
	if policy == "latest" {
		if _, err := time.Parse(time.RFC3339, candidates[0].PublishedAt); err != nil {
			return BuildCatalogEntry{}, fmt.Errorf("latest artifact has no verified publication time")
		}
	}
	return candidates[0], nil
}

func validateBuildIdentity(entry BuildCatalogEntry) error {
	commit, err := hex.DecodeString(entry.Commit)
	if err != nil || len(commit) != 20 {
		return fmt.Errorf("artifact requires an exact full commit")
	}
	sum, err := hex.DecodeString(entry.SHA256)
	if err != nil || len(sum) != 32 || entry.Size <= 0 || entry.Version == "" || entry.ArtifactName == "" {
		return fmt.Errorf("artifact %s has incomplete integrity metadata", entry.Version)
	}
	u, err := url.Parse(entry.DownloadURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("artifact %s requires an HTTPS download URL", entry.Version)
	}
	if entry.Channel == "dev" {
		commit, err := hex.DecodeString(entry.Commit)
		if err != nil || len(commit) != 20 || entry.WorkflowRunID == "" || !strings.HasPrefix(entry.Version, "dev-") {
			return fmt.Errorf("dev artifact requires full commit and successful-build identity")
		}
		suffix := strings.TrimPrefix(entry.Version, "dev-")
		if len(suffix) < 7 || !strings.HasPrefix(strings.ToLower(entry.Commit), strings.ToLower(suffix)) {
			return fmt.Errorf("dev tag and commit do not match")
		}
	}
	return nil
}

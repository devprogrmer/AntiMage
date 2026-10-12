package nodecontroller

import (
	"context"
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestLegacyCapabilityProfileDoesNotDispatchDestructiveCommands(t *testing.T) {
	for _, raw := range []any{nil, `[]`, `["runtime_evidence_v1","verified_updates_v1"]`, `["shared_fencing_v1"]`} {
		c := rolloutTestController(t)
		if _, err := c.repo.db.Exec(`UPDATE nodes SET node_capabilities=? WHERE id=1`, raw); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"core_update", "core_restart", "sync_config", "geo_update", "stop_runtime", "node_restart", "host_reboot"} {
			err := c.executeLegacyNodeCommand(context.Background(), 1, "legacy-policy-"+kind, kind, func(context.Context, *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
				t.Fatal("unsafe legacy command dispatched")
				return nil, nil
			}, nil)
			if err == nil || !strings.Contains(err.Error(), "upgrade required") {
				t.Fatalf("legacy policy %s: %v", kind, err)
			}
		}
		var count int
		if err := c.repo.db.QueryRow(`SELECT COUNT(*) FROM operations`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("unsupported action created executable operation: %d %v", count, err)
		}
	}
}

func TestCapabilityClassificationDoesNotInferFencingFromBinaryUpdates(t *testing.T) {
	profile := ClassifyDestructiveCapabilities([]string{"verified_updates_v1", "runtime_evidence_v1"})
	if profile.SupportsFencing || profile.SupportsCommandIdempotency || profile.SupportsGeoIdentity {
		t.Fatal("legacy metadata fabricated destructive proof")
	}
	profile = ClassifyDestructiveCapabilities([]string{"shared_fencing_v1", "command_idempotency_v1", "config_identity_v1"})
	if !profile.SupportsFencing || !profile.SupportsCommandIdempotency || !profile.SupportsConfigGeneration || profile.SupportsGeoIdentity {
		t.Fatal("explicit capability classification incorrect")
	}
}

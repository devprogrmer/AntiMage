package nodecontroller

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type DestructiveCapabilities struct {
	SupportsFencing            bool `json:"supports_fencing"`
	SupportsCommandIdempotency bool `json:"supports_command_idempotency"`
	SupportsVerifiedRestart    bool `json:"supports_verified_restart"`
	SupportsRunningVersion     bool `json:"supports_running_version"`
	SupportsConfigGeneration   bool `json:"supports_config_generation"`
	SupportsGeoIdentity        bool `json:"supports_geo_identity"`
}

func ClassifyDestructiveCapabilities(capabilities []string) DestructiveCapabilities {
	var profile DestructiveCapabilities
	for _, capability := range capabilities {
		switch capability {
		case "shared_fencing_v1":
			profile.SupportsFencing = true
		case "command_idempotency_v1":
			profile.SupportsCommandIdempotency = true
		case "runtime_evidence_v1":
			profile.SupportsVerifiedRestart = true
			profile.SupportsRunningVersion = true
		case "config_identity_v1":
			profile.SupportsConfigGeneration = true
		case "geo_identity_v1":
			profile.SupportsGeoIdentity = true
		}
	}
	return profile
}

// Absence is a compatibility limitation, never evidence of remote enforcement.
// Read-only connection and visibility remain available to legacy agents.
func (c Controller) requireDestructiveCapability(ctx context.Context, nodeID int64) error {
	var raw sql.NullString
	if err := c.repo.db.QueryRowContext(ctx, `SELECT node_capabilities FROM nodes WHERE id=?`, nodeID).Scan(&raw); err != nil {
		return err
	}
	var capabilities []string
	if raw.Valid && raw.String != "" {
		if err := json.Unmarshal([]byte(raw.String), &capabilities); err != nil {
			return fmt.Errorf("node capability metadata is invalid")
		}
	}
	profile := ClassifyDestructiveCapabilities(capabilities)
	if !profile.SupportsFencing || !profile.SupportsCommandIdempotency {
		return fmt.Errorf("node agent upgrade required for shared destructive fencing and command idempotency; read-only connection remains available")
	}
	return nil
}

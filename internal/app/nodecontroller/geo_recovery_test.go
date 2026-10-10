package nodecontroller

import (
	operationapp "github.com/antimage/antimage/internal/app/operations"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"testing"
	"time"
)

func TestGeoRecoveryUsesCommittedDatasetAndReloadEvidence(t *testing.T) {
	op := operationapp.Operation{ID: "geo-op", Type: "geo_update", Metadata: map[string]any{"command_id": "geo-command", "dispatched_resource_generation": int64(51), "restart_boundary_nanos": time.Now().Add(-time.Minute).UnixNano()}}
	state := &nodev1.RuntimeState{Connected: true, Started: true, SampledAtUnixNano: time.Now().UnixNano(), EvidenceOperationId: op.ID, EvidenceCommandId: "geo-command", EvidenceResourceGeneration: 51, EvidenceCommandState: "started", GeoTransactionPhase: "files_committed", GeoDatasetSha256: "verified-dataset"}
	if !geoCommittedNeedsReload(op, state) || legacyRuntimeEvidenceMatches(op, state) {
		t.Fatal("committed pending reload misclassified")
	}
	state.GeoTransactionPhase = "reload_completed"
	state.GeoReloadVerified = true
	if !legacyRuntimeEvidenceMatches(op, state) || geoCommittedNeedsReload(op, state) {
		t.Fatal("verified loaded dataset rejected")
	}
	state.GeoTransactionPhase = "identity_mismatch"
	state.GeoReloadVerified = false
	if geoCommittedNeedsReload(op, state) || legacyRuntimeEvidenceMatches(op, state) {
		t.Fatal("ambiguous Geo state blindly replayed")
	}
	state.GeoTransactionPhase = "files_committed"
	op.Metadata["geo_reload_dispatched"] = true
	if geoCommittedNeedsReload(op, state) {
		t.Fatal("lost reload ACK blindly retried")
	}
	op.Metadata["geo_reload_dispatched"] = false
	state.EvidenceCommandState = "superseded"
	if geoCommittedNeedsReload(op, state) {
		t.Fatal("stale owner replayed reload")
	}
}

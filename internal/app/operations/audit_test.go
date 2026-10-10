package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

func TestEvidenceAuditDeduplicatesAcrossDatabaseSessionsWithoutStateTransition(t *testing.T) {
	a, b := leaseTestDatabases(t)
	op := seedLeaseOperation(t, a, "installing")
	ctx := WithAuditOrigin(context.Background(), "recovery")
	for _, db := range []*sql.DB{a, b, a} {
		if err := AppendAudit(ctx, db, op.ID, "fencing_rejected", map[string]any{"command_id": "original-command", "error_code": "executor_lease_lost", "token": "audit-private-secret"}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := a.QueryRow(`SELECT COUNT(*) FROM operation_events WHERE operation_id=? AND event_type='fencing.rejected'`, op.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit spam count=%d err=%v", count, err)
	}
	stored, err := Get(ctx, b, op.ID)
	if err != nil || stored.Phase != op.Phase || stored.State != op.State {
		t.Fatalf("audit changed executor state: %+v %v", stored, err)
	}
	events, err := ListAudit(ctx, b, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(events)
	if strings.Contains(string(data), "audit-private-secret") {
		t.Fatal("secret entered audit history")
	}
}

func TestOrphanRepairPersistsDetectionAndRepairWithoutRepeatSpam(t *testing.T) {
	db, _ := leaseTestDatabases(t)
	if _, err := db.Exec(`INSERT INTO operation_locks(target_type,target_id,operation_id) VALUES ('node','7','missing-owner')`); err != nil {
		t.Fatal(err)
	}
	repaired, err := RepairOrphanLocks(context.Background(), db)
	if err != nil || len(repaired) != 1 {
		t.Fatalf("repair=%v err=%v", repaired, err)
	}
	events, err := ListAudit(context.Background(), db, "missing-owner")
	if err != nil || len(events) != 2 || events[0].Type != "lock.orphan_repaired" || events[1].Type != "lock.orphan_detected" {
		t.Fatalf("audit=%v err=%v", events, err)
	}
	if repaired, err = RepairOrphanLocks(context.Background(), db); err != nil || len(repaired) != 0 {
		t.Fatalf("repeated repair=%v err=%v", repaired, err)
	}
	events, err = ListAudit(context.Background(), db, "missing-owner")
	if err != nil || len(events) != 2 {
		t.Fatalf("duplicate orphan audit=%v err=%v", events, err)
	}
}

func TestStructuredAuditPreservesIdentityAndExcludesSecrets(t *testing.T) {
	db, _ := leaseTestDatabases(t)
	op := seedLeaseOperation(t, db, "installing")
	lease, err := AcquireExecutorLease(context.Background(), db, "sqlite", op.ID, "executor", 60000000000)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAuditOrigin(WithExecutorLease(context.Background(), lease), "recovery")
	op.Phase, op.State = "outcome_unknown", "waiting"
	op.Metadata["command_id"] = "command-original"
	op.Metadata["node_token"] = "secret-marker"
	op.Metadata["config"] = "secret-marker"
	if err := Save(ctx, db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	var eventType, raw string
	if err := db.QueryRow(`SELECT event_type,payload_json FROM operation_events WHERE operation_id=? ORDER BY sequence DESC LIMIT 1`, op.ID).Scan(&eventType, &raw); err != nil {
		t.Fatal(err)
	}
	if eventType != "command.outcome_unknown" || strings.Contains(raw, "secret-marker") {
		t.Fatalf("unsafe audit: %s %s", eventType, raw)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]string{"origin": "recovery", "actor": op.RequestedBy, "request_id": op.RequestID, "operation_id": op.ID, "command_id": "command-original", "executor_id": "executor", "target_id": "7"} {
		if payload[key] != expected {
			t.Fatalf("audit %s=%v expected %s", key, payload[key], expected)
		}
	}
	if payload["resource_generation"] != float64(lease.ResourceGeneration) || payload["lease_generation"] != float64(lease.Generation) {
		t.Fatal("audit lost fencing identity")
	}
	events, err := ListAudit(context.Background(), db, op.ID)
	if err != nil || len(events) == 0 {
		t.Fatalf("persisted audit unavailable: %v", err)
	}
	if events[0].Type != eventType || events[0].Evidence["command_id"] != "command-original" {
		t.Fatalf("audit history lost original identity: %+v", events[0])
	}
	if _, err := db.Exec(`UPDATE operation_events SET payload_json=? WHERE operation_id=?`, `{"command_id":"safe-command","download_url":"secret-marker","resource_generation":7,"actor":{"token":"secret-marker"}}`, op.ID); err != nil {
		t.Fatal(err)
	}
	events, err = ListAudit(context.Background(), db, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(events)
	if err != nil || strings.Contains(string(encoded), "secret-marker") || events[0].Evidence["resource_generation"] != float64(7) {
		t.Fatalf("legacy audit payload was not sanitized: %s %v", encoded, err)
	}
}

func TestStructuredAuditExtractsNestedVersionEvidenceWithoutRecoveryPayload(t *testing.T) {
	op := Operation{ID: "nested-operation", Type: "node_update", TargetType: "node", TargetID: "7", Phase: "reconciling", State: "running", Metadata: map[string]any{
		"update":     map[string]any{"requested_version": "v2.0.0", "running_version": "v1.0.0", "resolved_target": map[string]any{"commit": "abcdef1234567", "sha256": strings.Repeat("a", 64), "size": 42, "download_url": "https://private.test?token=private-audit-secret"}, "error": "private-audit-secret", "config": "private-audit-secret"},
		"command_id": map[string]any{"token": "private-audit-secret"},
		"origin":     "unapproved-origin",
	}}
	payload := operationAuditPayload(WithAuditOrigin(context.Background(), "recovery"), op)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-audit-secret") || strings.Contains(string(data), "download_url") || payload["requested_version"] != "v2.0.0" || payload["artifact_sha256"] != strings.Repeat("a", 64) || payload["resolved_commit"] != "abcdef1234567" || payload["origin"] != "recovery" {
		t.Fatalf("invalid audit projection: %s", data)
	}
	if _, present := payload["command_id"]; present {
		t.Fatal("non-scalar command identity leaked")
	}
}

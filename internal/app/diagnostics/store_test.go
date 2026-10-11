package diagnostics

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE diagnostics (id TEXT PRIMARY KEY, source TEXT, resource_type TEXT, resource_id TEXT, severity TEXT, code TEXT, summary TEXT, detail TEXT, first_seen_at BIGINT, last_seen_at BIGINT, occurrence_count BIGINT, status TEXT, recommended_action TEXT)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return Store{DB: db}
}

func TestObserveDeduplicatesAndCountsSeparatedOccurrences(t *testing.T) {
	store, ctx, now := testStore(t), context.Background(), time.Unix(1000, 0)
	observation := Observation{Source: "database", ResourceType: "panel", Severity: "critical", Code: "db.down", Summary: "Database unavailable"}
	first, err := store.Observe(ctx, observation, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Observe(ctx, observation, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.OccurrenceCount != 1 {
		t.Fatalf("within-window duplicate was not deduplicated: %#v", second)
	}
	third, err := store.Observe(ctx, observation, now.Add(6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if third.OccurrenceCount != 2 {
		t.Fatalf("occurrences = %d, want 2", third.OccurrenceCount)
	}
}

func TestAcknowledgeAndResolveMissing(t *testing.T) {
	store, ctx := testStore(t), context.Background()
	record, err := store.Observe(ctx, Observation{Source: "runtime", ResourceType: "panel", Severity: "warning", Code: "disk.pressure", Summary: "Disk pressure"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(ctx, record.ID, "acknowledged"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Observe(ctx, Observation{Source: "runtime", ResourceType: "panel", Severity: "warning", Code: "disk.pressure", Summary: "Disk pressure persists"}, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	items, err := store.List(ctx, "acknowledged", "warning", "runtime", 25)
	if err != nil || len(items) != 1 {
		t.Fatalf("acknowledged list = %#v, %v", items, err)
	}
	if err := store.ResolveMissing(ctx, map[string]struct{}{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(ctx, record.ID, "active"); err == nil {
		t.Fatal("resolved diagnostic was reopened by status mutation")
	}
}

func TestResolveMissingOnlyClosesSuccessfullyCollectedSources(t *testing.T) {
	store, ctx := testStore(t), context.Background()
	now := time.Now()
	item, err := store.Observe(ctx, Observation{Source: "runtime", ResourceType: "panel", Severity: "warning", Code: "disk.pressure", Summary: "Disk pressure"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveMissingSources(ctx, map[string]struct{}{}, now.Add(time.Minute), []string{"xray"}); err != nil {
		t.Fatal(err)
	}
	active, err := store.List(ctx, "active", "", "", 10)
	if err != nil || len(active) != 1 || active[0].ID != item.ID {
		t.Fatalf("partial source collection closed runtime finding: %#v %v", active, err)
	}
	if err := store.ResolveMissingSources(ctx, map[string]struct{}{}, now.Add(2*time.Minute), []string{"runtime"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.List(ctx, "resolved", "", "", 10)
	if err != nil || len(resolved) != 1 {
		t.Fatalf("complete source collection did not resolve: %#v %v", resolved, err)
	}
}

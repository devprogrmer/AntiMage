package nodeagent

import "testing"

func TestPruneOpenVPNUsageBaselines(t *testing.T) {
	activeA := "ov-a\x00client-id\x001\x00100"
	staleA := "ov-a\x00client-id\x002\x00100"
	staleB := "ov-b\x00client-id\x003\x00100"
	similar := "ov-a2\x00client-id\x004\x00100"

	baseline := map[string]uint64{
		activeA: 10,
		staleA:  20,
		staleB:  30,
		similar: 40,
	}

	changed := pruneOpenVPNUsageBaselines(
		baseline,
		map[string]struct{}{
			"ov-a": {},
		},
		map[string]struct{}{
			activeA: {},
		},
	)

	if !changed {
		t.Fatal("expected baseline pruning")
	}

	if _, ok := baseline[activeA]; !ok {
		t.Fatal("active session baseline was removed")
	}

	if _, ok := baseline[staleA]; ok {
		t.Fatal("stale baseline for scanned inbound was not removed")
	}

	if _, ok := baseline[staleB]; !ok {
		t.Fatal("baseline for unscanned inbound was removed")
	}

	if _, ok := baseline[similar]; !ok {
		t.Fatal("similar inbound prefix was incorrectly removed")
	}
}

func TestPruneOpenVPNUsageBaselinesNoSuccessfulScan(
	t *testing.T,
) {
	key := "ov-a\x00client-id\x001\x00100"

	baseline := map[string]uint64{
		key: 100,
	}

	changed := pruneOpenVPNUsageBaselines(
		baseline,
		map[string]struct{}{},
		map[string]struct{}{},
	)

	if changed {
		t.Fatal("baseline changed without a successfully scanned inbound")
	}

	if baseline[key] != 100 {
		t.Fatal("baseline was removed without successful scan")
	}
}

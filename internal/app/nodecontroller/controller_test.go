package nodecontroller

import (
	"errors"
	"reflect"
	"testing"
)

func TestNodeGRPCPortCandidatesPreferControlPortWithLegacyFallback(t *testing.T) {
	got := NodeGRPCPortCandidates(62033, 62034)
	want := []int{62033, 62034}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected candidates: got %v want %v", got, want)
	}
}

func TestNodeGRPCPortCandidatesDoesNotInventAdjacentAPIPort(t *testing.T) {
	got := NodeGRPCPortCandidates(62050, 62051)
	want := []int{62050, 62051}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected candidates: got %v want %v", got, want)
	}
	for _, port := range got {
		if port == 62052 {
			t.Fatalf("unexpected invented API-adjacent port %d in %v", port, got)
		}
	}
}

func TestNodeGRPCPortCandidatesFallbackToServicePortWithoutAPIPort(t *testing.T) {
	got := NodeGRPCPortCandidates(62033, 0)
	want := []int{62033}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected candidates: got %v want %v", got, want)
	}
}

func TestOperationRevisionUsesStableQueueID(t *testing.T) {
	if got := operationRevision("sync_config-1842"); got != 1842 {
		t.Fatalf("unexpected revision: %d", got)
	}
}

func TestMissingNodeCLIIsPermanentOperationError(t *testing.T) {
	err := errors.New("Node error during update service: unable to locate antimage-node CLI on this host")
	if !isPermanentOperationError(err) {
		t.Fatal("a missing node CLI cannot be fixed by retrying the same update")
	}
}

package nodeagent

import "testing"

func TestAdvanceAccountingCountersIntoMatchesAllocatingPathAndReusesMap(t *testing.T) {
	previous := map[string]accountingCounter{"alice:up": {Native: 5, Total: 15}}
	native := map[string]uint64{"alice:up": 12, "bob:down": 20}
	seeds := map[string]uint64{"bob:down": 8}
	want, err := advanceAccountingCounters(previous, native, seeds)
	if err != nil {
		t.Fatal(err)
	}
	scratch := make(map[string]accountingCounter)
	got, err := advanceAccountingCountersInto(scratch, previous, native, func(key string) uint64 { return seeds[key] })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || got["alice:up"] != want["alice:up"] || got["bob:down"] != want["bob:down"] {
		t.Fatalf("scratch result = %#v, want %#v", got, want)
	}
	previousNext := map[string]accountingCounter{"alice:up": {Native: 12, Total: 22}, "bob:down": {Native: 20, Total: 20}}
	next, err := advanceAccountingCountersInto(got, previousNext, map[string]uint64{"alice:up": 14}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["alice:up"].Total != 24 || next["bob:down"].Total != 20 {
		t.Fatalf("reused result = %#v", next)
	}
}

package nodeagent

import "testing"

func TestXrayPendingReflectionTotalsPreserveIdentityOverflowAndReuse(t *testing.T) {
	samples := []xrayUsageSample{
		{UserID: 7, InboundTag: "a", Value: 11},
		{UserID: 7, InboundTag: "a", Value: 13},
		{UserID: 7, InboundTag: "b", Value: 17},
		{UserID: 8, InboundTag: "a", Value: ^uint64(0)},
		{UserID: 8, InboundTag: "a", Value: 1},
	}
	dest := map[xrayPendingReflectionKey]xrayPendingReflectionTotal{{UserID: 99, InboundTag: "stale"}: {Value: 77}}
	got := xrayPendingReflectionTotalsInto(samples, dest)
	if len(got) != 3 || got[xrayPendingReflectionKey{UserID: 7, InboundTag: "a"}].Value != 24 || got[xrayPendingReflectionKey{UserID: 7, InboundTag: "b"}].Value != 17 || !got[xrayPendingReflectionKey{UserID: 8, InboundTag: "a"}].Overflow {
		t.Fatalf("incorrect pending totals: %#v", got)
	}
	if _, stale := got[xrayPendingReflectionKey{UserID: 99, InboundTag: "stale"}]; stale {
		t.Fatal("reused pending index kept a stale sample")
	}
	got = xrayPendingReflectionTotalsInto(nil, got)
	if len(got) != 0 {
		t.Fatalf("empty pending batch retained earlier usage: %#v", got)
	}
}

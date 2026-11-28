package nodeagent

import (
	"testing"
	"time"
)

func TestNetworkInfrastructureDeltaResetAndOrdering(t *testing.T) {
	var sampler networkInfrastructureSampler
	now := time.Now()
	first := sampler.observe(now, map[string]uint64{"drops": 100}, nil)
	if first.Rates != nil || first.IntervalSeconds != 0 {
		t.Fatal(first)
	}
	next := sampler.observe(now.Add(2*time.Second), map[string]uint64{"drops": 106}, nil)
	if next.Rates["drops"] != 3 {
		t.Fatal(next)
	}
	reset := sampler.observe(now.Add(3*time.Second), map[string]uint64{"drops": 1}, nil)
	if _, ok := reset.Rates["drops"]; ok {
		t.Fatal("counter reset produced rate")
	}
	stale := sampler.observe(now, map[string]uint64{"drops": 999}, nil)
	if stale.Rates != nil {
		t.Fatal("stale sample produced rates")
	}
	last := sampler.observe(now.Add(4*time.Second), map[string]uint64{"drops": 3}, nil)
	if last.Rates["drops"] != 2 {
		t.Fatal("stale sample replaced baseline", last)
	}
}

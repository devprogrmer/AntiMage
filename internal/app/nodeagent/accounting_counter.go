package nodeagent

import "fmt"

// A logical counter survives native counter resets. Its total is never pruned
// merely because a sample was sent; only the separate ACK baseline advances.
type accountingCounter struct {
	Native uint64 `json:"native"`
	Total  uint64 `json:"total"`
}

const maxAccountingCounterSeries = 65536

func advanceAccountingCounters(previous map[string]accountingCounter, native, seeds map[string]uint64) (map[string]accountingCounter, error) {
	next := make(map[string]accountingCounter, len(previous)+len(native))
	for key, counter := range previous {
		if counter.Native > counter.Total {
			return nil, fmt.Errorf("invalid accounting counter %q", key)
		}
		next[key] = counter
	}
	for key, current := range native {
		counter, exists := next[key]
		if !exists {
			counter = accountingCounter{Native: seeds[key], Total: seeds[key]}
		}
		delta := current
		if current >= counter.Native {
			delta = current - counter.Native
		}
		if ^uint64(0)-counter.Total < delta {
			return nil, fmt.Errorf("accounting counter overflow %q", key)
		}
		counter.Total += delta
		counter.Native = current
		next[key] = counter
	}
	if len(next) > maxAccountingCounterSeries {
		return nil, fmt.Errorf("accounting series capacity exceeded; refusing to discard usage")
	}
	return next, nil
}

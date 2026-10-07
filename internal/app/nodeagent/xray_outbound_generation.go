package nodeagent

// xrayOutboundUsageDeltas converts native outbound counters into deltas. The
// native values are only comparable inside one Xray process generation.
func xrayOutboundUsageDeltas(
	stats []xrayStat,
	baseline map[string]uint64,
	nextBaseline map[string]uint64,
	storedGeneration string,
	currentGeneration string,
) (map[string]struct{ Up, Down uint64 }, map[string]uint64) {
	if nextBaseline == nil {
		nextBaseline = make(map[string]uint64, len(baseline))
	}
	freshGeneration := currentGeneration != "" && currentGeneration != storedGeneration
	result := make(map[string]struct{ Up, Down uint64 })
	for _, stat := range stats {
		parsed, ok := parseXrayStatName(stat.Name)
		if !ok || parsed.Type != "outbound" || parsed.Tag == "" || stat.Value < 0 {
			continue
		}
		key := parsed.Tag + ":" + parsed.Direction
		current := uint64(stat.Value)
		delta := current
		if !freshGeneration {
			if previous, exists := baseline[key]; exists && current >= previous {
				delta = current - previous
			}
		}
		nextBaseline[key] = current
		traffic := result[parsed.Tag]
		if parsed.Direction == "uplink" {
			traffic.Up = delta
		} else {
			traffic.Down = delta
		}
		result[parsed.Tag] = traffic
	}
	return result, nextBaseline
}

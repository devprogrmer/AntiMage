package nodeagent

import (
	"fmt"
	"strings"
)

func (s *Server) wireGuardAwaitingReflectionUsageLocked(
	inboundTag string,
	userID int64,
	reflectedBatchID string,
) (uint64, error) {
	inboundTag = strings.TrimSpace(inboundTag)
	reflectedBatchID = strings.TrimSpace(reflectedBatchID)

	var total uint64
	reflectedThrough := -1

	for index, batch := range s.wireGuardUsageAwaitingReflection {
		if reflectedBatchID != "" &&
			strings.TrimSpace(batch.BatchID) == reflectedBatchID {
			reflectedThrough = index
		}
	}

	if reflectedThrough < 0 &&
		reflectedBatchID != "" &&
		s.wireGuardUsagePending != nil &&
		strings.TrimSpace(s.wireGuardUsagePending.BatchID) ==
			reflectedBatchID {
		reflectedThrough =
			len(s.wireGuardUsageAwaitingReflection) - 1
	}

	changed := false

	for index, batch := range s.wireGuardUsageAwaitingReflection {
		for _, sample := range batch.Samples {
			if sample.UserID != userID {
				continue
			}
			if index <= reflectedThrough && sample.UserID == userID {
				changed = true
				continue
			}
			if strings.TrimSpace(sample.InboundTag) != inboundTag {
				continue
			}

			if ^uint64(0)-total < sample.Value {
				return 0, fmt.Errorf(
					"wireguard awaiting reflection usage overflow for user %d",
					userID,
				)
			}
			total += sample.Value
		}
	}

	if !changed {
		return total, nil
	}

	// Quota reads run for every peer on each tick. Allocate only when a
	// committed receipt actually removes this owner's rows. Preserve the
	// original slices so a failed durable write can roll back unchanged.
	next := make([]wireGuardUsageAwaitingReflectionBatch, 0, len(s.wireGuardUsageAwaitingReflection))
	for index, batch := range s.wireGuardUsageAwaitingReflection {
		if index <= reflectedThrough {
			remove := 0
			for _, sample := range batch.Samples {
				if sample.UserID == userID {
					remove++
				}
			}
			if remove > 0 {
				kept := make([]wireGuardUsageSample, 0, len(batch.Samples)-remove)
				for _, sample := range batch.Samples {
					if sample.UserID != userID {
						kept = append(kept, sample)
					}
				}
				batch.Samples = kept
			}
		}
		if len(batch.Samples) != 0 {
			next = append(next, batch)
		}
	}

	previous := s.wireGuardUsageAwaitingReflection
	s.wireGuardUsageAwaitingReflection = next

	if err := s.persistWireGuardUsageStateLocked(); err != nil {
		s.wireGuardUsageAwaitingReflection = previous
		return 0, fmt.Errorf(
			"persist wireguard reflection reconciliation: %w",
			err,
		)
	}

	return total, nil
}

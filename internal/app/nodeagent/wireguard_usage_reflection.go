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

	next := make(
		[]wireGuardUsageAwaitingReflectionBatch,
		0,
		len(s.wireGuardUsageAwaitingReflection),
	)
	changed := false

	for index, batch := range s.wireGuardUsageAwaitingReflection {
		kept := make(
			[]wireGuardUsageSample,
			0,
			len(batch.Samples),
		)

		for _, sample := range batch.Samples {
			if index <= reflectedThrough && sample.UserID == userID {
				changed = true
				continue
			}

			kept = append(kept, sample)

			if sample.UserID != userID ||
				strings.TrimSpace(sample.InboundTag) != inboundTag {
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

		if len(kept) != 0 {
			batch.Samples = kept
			next = append(next, batch)
		}
	}

	if !changed {
		return total, nil
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

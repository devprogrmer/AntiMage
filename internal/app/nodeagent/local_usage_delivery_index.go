package nodeagent

import "fmt"

type localUsageIndexedRow struct {
	Position int
	Row      localUsageDeliveryRow
}

type localUsageDeliveryIndex struct {
	Batches map[string]int
	Users   map[int64][]localUsageIndexedRow
}

// localUsageMu protects both the ledger and its derived index. No query may
// mutate indexed rows; successful durable writes invalidate the entire index.
func (s *Server) localUsageReflectionIndexedLocked(queries []localUsageReflectionQuery, results []localUsageReflectionResult) (bool, error) {
	index := s.localUsageIndexLocked()
	for queryPosition, query := range queries {
		reflectedThrough, found := index.Batches[query.ReflectedRootBatchID]
		for _, entry := range index.Users[query.UserID] {
			row := entry.Row
			if found {
				if entry.Position < reflectedThrough || entry.Position == reflectedThrough && (row.Raw != 0 || row.Protocol == query.Protocol && row.ChildID != query.PendingChildID) {
					return false, nil
				}
				if entry.Position == reflectedThrough && query.PendingChildID != "" && query.ReflectedRootBatchID != "" && row.Protocol == query.Protocol && row.ChildID == query.PendingChildID {
					results[queryPosition].PendingReflected = true
				}
			}
			if row.Protocol != query.Protocol || row.InboundTag != query.InboundTag || row.ChildID == query.PendingChildID {
				continue
			}
			if ^uint64(0)-results[queryPosition].Awaiting < row.Raw {
				return false, fmt.Errorf("unreflected accounting usage overflow")
			}
			results[queryPosition].Awaiting += row.Raw
		}
	}
	return true, nil
}

func (s *Server) localUsageIndexLocked() *localUsageDeliveryIndex {
	index := s.localUsageDeliveryIndex
	if index == nil {
		index = &localUsageDeliveryIndex{Batches: make(map[string]int, len(s.localUsageDeliveries)), Users: make(map[int64][]localUsageIndexedRow)}
		for position, delivery := range s.localUsageDeliveries {
			index.Batches[delivery.BatchID] = position
			for _, row := range delivery.Rows {
				index.Users[row.UserID] = append(index.Users[row.UserID], localUsageIndexedRow{Position: position, Row: row})
			}
		}
		s.localUsageDeliveryIndex = index
	}
	return index
}

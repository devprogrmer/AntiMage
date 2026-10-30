package nodeagent

import "fmt"

type localUsageIndexedRow struct {
	Position    int
	RowPosition int
}

type localUsageDeliveryIndex struct {
	Batches map[string]int
	Users   map[int64][]localUsageIndexedRow
}

// localUsageMu protects the durable ledger and this derived read index.
func (s *Server) localUsageIndexLocked() *localUsageDeliveryIndex {
	if s.localUsageDeliveryIndex == nil {
		index := &localUsageDeliveryIndex{
			Batches: make(map[string]int, len(s.localUsageDeliveries)),
			Users:   make(map[int64][]localUsageIndexedRow),
		}
		for position, delivery := range s.localUsageDeliveries {
			index.Batches[delivery.BatchID] = position
			for rowPosition, row := range delivery.Rows {
				index.Users[row.UserID] = append(index.Users[row.UserID], localUsageIndexedRow{Position: position, RowPosition: rowPosition})
			}
		}
		s.localUsageDeliveryIndex = index
	}
	return s.localUsageDeliveryIndex
}

func (s *Server) localUsageReflectionIndexedLocked(protocol string, userID int64, inboundTag, pendingChildID, reflectedRootBatchID string) (uint64, bool, error) {
	index := s.localUsageIndexLocked()
	reflectedThrough, found := index.Batches[reflectedRootBatchID]
	var total uint64
	for _, entry := range index.Users[userID] {
		row := s.localUsageDeliveries[entry.Position].Rows[entry.RowPosition]
		if found && (entry.Position < reflectedThrough || entry.Position == reflectedThrough && (row.Raw != 0 || row.Protocol == protocol && row.ChildID != pendingChildID)) {
			return 0, false, nil
		}
		if row.Protocol != protocol || row.InboundTag != inboundTag || row.ChildID == pendingChildID {
			continue
		}
		if ^uint64(0)-total < row.Raw {
			return 0, true, fmt.Errorf("unreflected accounting usage overflow")
		}
		total += row.Raw
	}
	return total, true, nil
}

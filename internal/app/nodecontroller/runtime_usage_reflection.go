package nodecontroller

import (
	"context"
	"fmt"
	"strings"
)

type runtimeUsageReflection struct {
	UsedTraffic int64
	BatchID     string
}

// Usage and its marker must come from the same SQL statement. Reading the
// marker after an earlier user query can incorrectly acknowledge newer bytes.
func (r Repository) runtimeUsageReflections(ctx context.Context, nodeID int64, userIDs []int64) (map[int64]runtimeUsageReflection, error) {
	result := make(map[int64]runtimeUsageReflection, len(userIDs))
	for start := 0; start < len(userIDs); start += 500 {
		end := start + 500
		if end > len(userIDs) {
			end = len(userIDs)
		}
		marks := make([]string, end-start)
		args := make([]any, 0, end-start+1)
		args = append(args, nodeID)
		for i, id := range userIDs[start:end] {
			marks[i] = "?"
			args = append(args, id)
		}
		rows, err := r.db.QueryContext(ctx, `SELECT u.id, COALESCE(u.used_traffic, 0), COALESCE(ref.batch_id, '')
FROM users u LEFT JOIN node_wireguard_usage_reflection ref ON ref.node_id = ? AND ref.user_id = u.id
WHERE u.id IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("load coherent runtime usage reflection: %w", err)
		}
		for rows.Next() {
			var id int64
			var reflected runtimeUsageReflection
			if err := rows.Scan(&id, &reflected.UsedTraffic, &reflected.BatchID); err != nil {
				rows.Close()
				return nil, err
			}
			result[id] = reflected
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	for _, id := range userIDs {
		if _, ok := result[id]; !ok {
			return nil, fmt.Errorf("runtime policy user %d disappeared during snapshot", id)
		}
	}
	return result, nil
}

func (r Repository) attachNativeRuntimeUsageReflections(ctx context.Context, nodeID int64, ov *OVRuntime, l2 *L2TPRuntime, pptp *PPTPRuntime, wg *WGRuntime, awg *AWGRuntime, ike, ac *RemoteAccessRuntime) error {
	seen := map[int64]bool{}
	ids := []int64{}
	add := func(id int64) {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, inbound := range ov.Inbounds {
		for _, user := range inbound.Users {
			add(user.UserID)
		}
	}
	for _, inbound := range l2.Inbounds {
		for _, user := range inbound.Users {
			add(user.UserID)
		}
	}
	for _, inbound := range pptp.Inbounds {
		for _, user := range inbound.Users {
			add(user.UserID)
		}
	}
	for _, inbound := range wg.Inbounds {
		for _, user := range inbound.Peers {
			add(user.UserID)
		}
	}
	for _, inbound := range awg.Inbounds {
		for _, user := range inbound.Peers {
			add(user.UserID)
		}
	}
	for _, inbound := range ike.Inbounds {
		for _, user := range inbound.Users {
			add(user.UserID)
		}
	}
	for _, inbound := range ac.Inbounds {
		for _, user := range inbound.Users {
			add(user.UserID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	reflections, err := r.runtimeUsageReflections(ctx, nodeID, ids)
	if err != nil {
		return err
	}
	for i := range ov.Inbounds {
		for j := range ov.Inbounds[i].Users {
			u := &ov.Inbounds[i].Users[j]
			ref := reflections[u.UserID]
			u.UsedTraffic = ref.UsedTraffic
			u.ReflectedUsageBatchID = ref.BatchID
		}
	}
	for i := range l2.Inbounds {
		for j := range l2.Inbounds[i].Users {
			u := &l2.Inbounds[i].Users[j]
			ref := reflections[u.UserID]
			u.UsedTraffic = ref.UsedTraffic
			u.ReflectedUsageBatchID = ref.BatchID
		}
	}
	for i := range pptp.Inbounds {
		for j := range pptp.Inbounds[i].Users {
			u := &pptp.Inbounds[i].Users[j]
			ref := reflections[u.UserID]
			u.UsedTraffic = ref.UsedTraffic
			u.ReflectedUsageBatchID = ref.BatchID
		}
	}
	for i := range wg.Inbounds {
		for j := range wg.Inbounds[i].Peers {
			u := &wg.Inbounds[i].Peers[j]
			ref := reflections[u.UserID]
			u.UsedTraffic = ref.UsedTraffic
			u.ReflectedUsageBatchID = ref.BatchID
		}
	}
	for i := range awg.Inbounds {
		for j := range awg.Inbounds[i].Peers {
			u := &awg.Inbounds[i].Peers[j]
			ref := reflections[u.UserID]
			u.UsedTraffic = ref.UsedTraffic
			u.ReflectedUsageBatchID = ref.BatchID
		}
	}
	for i := range ike.Inbounds {
		for j := range ike.Inbounds[i].Users {
			u := &ike.Inbounds[i].Users[j]
			ref := reflections[u.UserID]
			u.UsedTraffic = ref.UsedTraffic
			u.ReflectedUsageBatchID = ref.BatchID
		}
	}
	for i := range ac.Inbounds {
		for j := range ac.Inbounds[i].Users {
			u := &ac.Inbounds[i].Users[j]
			ref := reflections[u.UserID]
			u.UsedTraffic = ref.UsedTraffic
			u.ReflectedUsageBatchID = ref.BatchID
		}
	}
	return nil
}

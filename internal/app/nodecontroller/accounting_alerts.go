package nodecontroller

import "strings"

// accountingAlerts only interprets the node's sampled health. It never changes
// usage, ACKs, quota decisions or the durable reflection ledger.
func accountingAlerts(runtime RuntimeResult) []AccountingAlert {
	if runtime.Accounting == nil {
		return nil
	}
	alerts := make([]AccountingAlert, 0, len(runtime.Accounting.Protocols)*3+1)
	add := func(protocol, kind string) {
		alerts = append(alerts, AccountingAlert{
			NodeID: runtime.NodeID, NodeName: runtime.Name,
			Protocol: protocol, Kind: kind,
		})
	}
	if runtime.Accounting.ReflectionOverdue {
		add("all", "reflection_overdue")
	}
	for _, protocol := range runtime.Accounting.Protocols {
		name := strings.TrimSpace(protocol.Protocol)
		if name == "" || !protocol.StateLoaded {
			continue
		}
		if protocol.CheckpointOverdue {
			add(name, "checkpoint_overdue")
		}
		if protocol.PendingACKOverdue {
			add(name, "pending_ack_overdue")
		}
		if strings.TrimSpace(protocol.Failure) != "" {
			add(name, "collector_failure")
		}
	}
	return alerts
}

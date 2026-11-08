package nodecontroller

import (
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

const (
	// The node accepts a checkpoint interval up to one minute.
	accountingCheckpointWarning = int64(3 * time.Minute / time.Second)
	accountingPendingACKWarning = int64(10 * time.Minute / time.Second)
)

func accountingTimingWarnings(sampledAt int64, item *nodev1.AccountingProtocolStatus) (checkpointOverdue, pendingACKOverdue bool) {
	if sampledAt <= 0 || !item.GetStateLoaded() {
		return false, false
	}
	protocol := item.GetProtocol()
	switch protocol {
	case "xray", "wireguard", "amneziawg", "openvpn", "l2tp", "pptp", "ikev2", "anyconnect":
	default:
		return false, false
	}
	// Compare timestamps from the same node sample so differences between
	// the panel and node clocks do not turn fresh work into a warning.
	lastCheckpoint := item.GetLastCheckpointUnix()
	checkpointOverdue = lastCheckpoint > 0 && sampledAt-lastCheckpoint > accountingCheckpointWarning

	// Native collectors currently generate "<protocol>-<UnixNano>" IDs.
	// Leave unfamiliar IDs opaque. This is diagnostic only and never
	// changes the batch, its ACK or the accounting ledger.
	stamp, ok := strings.CutPrefix(item.GetPendingBatchId(), protocol+"-")
	if !ok || len(stamp) != 19 || stamp[0] < '1' || stamp[0] > '9' {
		return checkpointOverdue, false
	}
	createdAt, err := strconv.ParseInt(stamp, 10, 64)
	return checkpointOverdue, err == nil && sampledAt-createdAt/int64(time.Second) > accountingPendingACKWarning
}

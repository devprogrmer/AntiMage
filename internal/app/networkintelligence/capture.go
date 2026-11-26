package networkintelligence

import (
	"errors"
	"strings"
	"time"
)

// CapturedPacket contains metadata only. At is userspace receipt time, so it
// cannot establish which address ownership epoch applied when a queued packet
// entered the kernel. Collectors need a lifecycle barrier before attribution.
type CapturedPacket struct {
	Metadata PacketMetadata
	At       time.Time
	Outgoing bool
}

// CaptureConfig selects one observation point. Capture is started only by an
// explicit RunCapture call, independently of the VPN forwarding path.
type CaptureConfig struct {
	Interface string
	// ReceiveBufferBytes defaults to 256 KiB. Linux may clamp this value.
	ReceiveBufferBytes int
	// Statistics receives cumulative collector counters once per second and
	// when capture stops. It runs synchronously and must return promptly.
	Statistics func(CaptureStatistics)
}

// CaptureStatistics describes observation loss, not VPN packet loss.
type CaptureStatistics struct {
	ReceivedPackets           uint64
	DecodedPackets            uint64
	RejectedPackets           uint64
	KernelPackets             uint64
	KernelDrops               uint64
	KernelStatisticsAvailable bool
	ReceiveBufferBytes        int
}

func (c CaptureConfig) validate() error {
	if c.ReceiveBufferBytes < 0 || c.ReceiveBufferBytes > 4*1024*1024 {
		return errors.New("capture receive buffer must be between zero and 4 MiB")
	}
	if strings.TrimSpace(c.Interface) != c.Interface || c.Interface == "" || len(c.Interface) > 15 || strings.ContainsAny(c.Interface, "/\x00") {
		return errors.New("an explicit valid interface name is required")
	}
	return nil
}

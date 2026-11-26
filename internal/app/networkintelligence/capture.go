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
}

func (c CaptureConfig) validate() error {
	if strings.TrimSpace(c.Interface) != c.Interface || c.Interface == "" || len(c.Interface) > 15 || strings.ContainsAny(c.Interface, "/\x00") {
		return errors.New("an explicit valid interface name is required")
	}
	return nil
}

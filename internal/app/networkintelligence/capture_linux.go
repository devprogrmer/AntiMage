//go:build linux

package networkintelligence

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// RunCapture observes IP metadata on one interface without modifying it. The
// callback runs synchronously and must return promptly. No packet bytes escape
// this function. Receive truncation, non-IP traffic and undecodable packets are
// discarded. Socket drops make this unsuitable as an accounting source.
func RunCapture(ctx context.Context, cfg CaptureConfig, consume func(CapturedPacket)) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if consume == nil {
		return errors.New("metadata consumer is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	iface, err := net.InterfaceByName(cfg.Interface)
	if err != nil {
		return fmt.Errorf("resolve capture interface: %w", err)
	}
	// AF_PACKET protocol is in network byte order, independent of host endian.
	protocol := int(binary.NativeEndian.Uint16([]byte{0, unix.ETH_P_ALL}))
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, protocol)
	if err != nil {
		return fmt.Errorf("open metadata socket: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: uint16(protocol), Ifindex: iface.Index}); err != nil {
		return fmt.Errorf("bind metadata socket: %w", err)
	}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Usec: 200000}); err != nil {
		return err
	}
	buffer := make([]byte, 65535)
	defer clear(buffer)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, _, flags, source, err := unix.Recvmsg(fd, buffer, nil, 0)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			continue
		}
		if err != nil {
			return fmt.Errorf("receive metadata: %w", err)
		}
		if flags&unix.MSG_TRUNC != 0 || n == 0 {
			continue
		}
		link, ok := source.(*unix.SockaddrLinklayer)
		if !ok || link.Ifindex != iface.Index {
			continue
		}
		metadata, err := DecodePacket(buffer[:n])
		clear(buffer[:n])
		if err != nil {
			continue
		}
		consume(CapturedPacket{Metadata: metadata, At: time.Now().UTC(), Outgoing: link.Pkttype == unix.PACKET_OUTGOING})
	}
}

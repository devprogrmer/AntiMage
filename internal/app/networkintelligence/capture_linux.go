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
	return runCapture(ctx, cfg, consume, nil)
}

func runCapture(ctx context.Context, cfg CaptureConfig, consume func(CapturedPacket), ready func()) error {
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
	receiveBuffer := cfg.ReceiveBufferBytes
	if receiveBuffer == 0 {
		receiveBuffer = 256 * 1024
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, receiveBuffer); err != nil {
		return fmt.Errorf("set metadata receive buffer: %w", err)
	}
	actualBuffer, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF)
	if err != nil {
		return fmt.Errorf("read metadata receive buffer: %w", err)
	}
	statistics := CaptureStatistics{ReceiveBufferBytes: actualBuffer}
	report := func() {
		kernel, err := unix.GetsockoptTpacketStats(fd, unix.SOL_PACKET, unix.PACKET_STATISTICS)
		statistics.KernelStatisticsAvailable = err == nil
		if err == nil {
			// PACKET_STATISTICS resets kernel counters on every read.
			statistics.KernelPackets += uint64(kernel.Packets)
			statistics.KernelDrops += uint64(kernel.Drops)
		}
		if cfg.Statistics != nil {
			cfg.Statistics(statistics)
		}
	}
	defer report()
	nextReport := time.Now().Add(time.Second)
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: uint16(protocol), Ifindex: iface.Index}); err != nil {
		return fmt.Errorf("bind metadata socket: %w", err)
	}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Usec: 200000}); err != nil {
		return err
	}
	batch := new(captureBatch)
	budget := captureBudget{limit: cfg.MaxPacketsPerSecond}
	defer batch.clear()
	if err := ctx.Err(); err != nil {
		return err
	}
	if ready != nil {
		ready()
	}
	for {
		if time.Now().After(nextReport) {
			report()
			nextReport = time.Now().Add(time.Second)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := batch.receive(fd)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			continue
		}
		if err != nil {
			return fmt.Errorf("receive metadata: %w", err)
		}
		for i := 0; i < count; i++ {
			message := batch.messages[i]
			n := int(message.Length)
			buffer := batch.buffers[i][:]
			statistics.ReceivedPackets++
			if !budget.allow(time.Now()) {
				statistics.BudgetExceeded = true
				return ErrCaptureBudget
			}
			if message.Header.Flags&unix.MSG_TRUNC != 0 || n == 0 || n > len(buffer) {
				statistics.RejectedPackets++
				clear(buffer)
				continue
			}
			link := batch.sources[i]
			if message.Header.Namelen < uint32(20) || link.Family != unix.AF_PACKET || int(link.Ifindex) != iface.Index {
				statistics.RejectedPackets++
				clear(buffer[:n])
				continue
			}
			metadata, err := DecodePacket(buffer[:n])
			clear(buffer[:n])
			if err != nil {
				statistics.RejectedPackets++
				continue
			}
			statistics.DecodedPackets++
			consume(CapturedPacket{Metadata: metadata, At: time.Now().UTC(), Outgoing: link.Pkttype == unix.PACKET_OUTGOING})
		}
	}
}

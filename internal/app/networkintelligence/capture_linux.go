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
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
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
		kernel, err := unix.GetsockoptTpacketStatsV3(fd, unix.SOL_PACKET, unix.PACKET_STATISTICS)
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
	ring, err := newCaptureRing(fd, cfg.ReceiveBufferBytes)
	if err != nil {
		return fmt.Errorf("create metadata ring: %w", err)
	}
	defer ring.close()
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: uint16(protocol), Ifindex: iface.Index}); err != nil {
		return fmt.Errorf("bind metadata socket: %w", err)
	}
	statistics.ReceiveBufferBytes = len(ring.data)
	budget := captureBudget{limit: cfg.MaxPacketsPerSecond}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ready != nil {
		ready()
	}
	for {
		now := time.Now().UTC()
		if now.After(nextReport) {
			report()
			nextReport = now.Add(time.Second)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !ring.ready() {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			statistics.PollCalls++
			_, err := unix.Poll(poll, 200)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				return fmt.Errorf("poll metadata ring: %w", err)
			}
			if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
				return errors.New("metadata ring socket failed")
			}
			continue
		}
		statistics.ReceivedPackets++
		if !budget.allow(now) {
			ring.release()
			statistics.BudgetExceeded = true
			return ErrCaptureBudget
		}
		packet, err := ring.packet(iface.Index)
		if err != nil {
			statistics.RejectedPackets++
			continue
		}
		statistics.DecodedPackets++
		consume(packet)
	}
}

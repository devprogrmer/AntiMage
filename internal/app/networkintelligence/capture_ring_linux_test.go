//go:build linux

package networkintelligence

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCaptureRingIdleWaitDoesNotSpin(t *testing.T) {
	if os.Getenv("ANTIMAGE_CAPTURE_E2E") != "1" {
		t.Skip("requires isolated Linux capture guest")
	}
	if _, err := os.Stat("/run/antimage-disposable-network-lab"); err != nil {
		t.Fatal("requires disposable guest marker")
	}
	const name = "ni-ring-idle"
	if output, err := exec.Command("ip", "link", "add", name, "type", "dummy").CombinedOutput(); err != nil {
		t.Fatalf("create fixture: %v %s", err, output)
	}
	defer exec.Command("ip", "link", "del", name).Run()
	// A down interface reports POLLERR immediately. Bring up only the private
	// dummy, disabling its IPv6 autoconfiguration to keep the fixture idle.
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/"+name+"/disable_ipv6", []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("ip", "link", "set", name, "up").CombinedOutput(); err != nil {
		t.Fatalf("activate fixture: %v %s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var final CaptureStatistics
	err := RunCapture(ctx, CaptureConfig{Interface: name, Statistics: func(s CaptureStatistics) { final = s }}, func(CapturedPacket) { t.Error("unexpected traffic on idle fixture") })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capture did not stop: %v", err)
	}
	if final.PollCalls == 0 || final.PollCalls > 4 {
		t.Fatalf("idle collector spun or failed to wait: poll calls=%d", final.PollCalls)
	}
}

func ringFixture() []byte {
	frame := make([]byte, 256)
	packet := packetFixture(false, false)
	binary.NativeEndian.PutUint32(frame[4:], 1795850000)
	binary.NativeEndian.PutUint32(frame[8:], 123456789)
	binary.NativeEndian.PutUint32(frame[12:], uint32(len(packet)))
	binary.NativeEndian.PutUint32(frame[16:], uint32(len(packet)))
	binary.NativeEndian.PutUint16(frame[26:], 80)
	binary.NativeEndian.PutUint16(frame[48:], unix.AF_PACKET)
	binary.NativeEndian.PutUint32(frame[52:], 7)
	frame[58] = unix.PACKET_OUTGOING
	copy(frame[80:], packet)
	return frame
}

func TestCaptureRingMetadataBoundsAndRelease(t *testing.T) {
	record := ringFixture()
	want, _ := DecodePacket(packetFixture(false, false))
	packet, err := decodeRingFrame(record, 7)
	if err != nil || !packet.Outgoing || packet.Metadata != want || !packet.At.Equal(time.Unix(1795850000, 123456789)) {
		t.Fatalf("packet=%+v err=%v", packet, err)
	}
	block := make([]byte, captureRingFrameSize)
	binary.NativeEndian.PutUint32(block, unix.TPACKET_V3)
	binary.NativeEndian.PutUint32(block[8:], unix.TP_STATUS_USER)
	binary.NativeEndian.PutUint32(block[12:], 2)
	binary.NativeEndian.PutUint32(block[16:], 48)
	binary.NativeEndian.PutUint32(block[20:], 560)
	copy(block[48:], record)
	copy(block[304:], record)
	binary.NativeEndian.PutUint32(block[48:], 256)
	binary.NativeEndian.PutUint32(block[304+8:], 143456789)
	r := captureRing{data: block}
	if !r.ready() {
		t.Fatal("block not owned by user")
	}
	first, err := r.packet(7)
	if err != nil || first.Metadata != want || !r.ready() {
		t.Fatalf("first packet=%+v err=%v", first, err)
	}
	second, err := r.packet(7)
	if err != nil || second.Metadata != want || second.At.Sub(first.At) != 20*time.Millisecond {
		t.Fatalf("kernel packet timestamps not preserved: %v %v %v", first.At, second.At, err)
	}
	if r.ready() || r.index != 0 || r.remaining != 0 {
		t.Fatal("block not returned or wrapped")
	}
	for _, v := range block[12:] {
		if v != 0 {
			t.Fatal("retained packet data")
		}
	}
	if first.Metadata != want {
		t.Fatal("metadata retained mapped memory")
	}
}

func TestCaptureRingRejectsTruncationAndInvalidOffsets(t *testing.T) {
	for _, change := range []func([]byte){
		func(b []byte) { binary.NativeEndian.PutUint32(b[12:], 1) },
		func(b []byte) {
			binary.NativeEndian.PutUint32(b[12:], ^uint32(0))
			binary.NativeEndian.PutUint32(b[16:], ^uint32(0))
		},
		func(b []byte) { binary.NativeEndian.PutUint16(b[26:], 0) },
		func(b []byte) { binary.NativeEndian.PutUint32(b[52:], 8) },
		func(b []byte) { binary.NativeEndian.PutUint16(b[48:], unix.AF_INET) },
		func(b []byte) { binary.NativeEndian.PutUint32(b[8:], 1000000000) },
	} {
		frame := ringFixture()
		change(frame)
		if _, err := decodeRingFrame(frame, 7); err == nil {
			t.Fatal("invalid record accepted")
		}
	}
	if _, err := decodeRingFrame(make([]byte, 67), 7); err == nil {
		t.Fatal("short record accepted")
	}
}

func TestCaptureRingRejectsInvalidBlockAndStride(t *testing.T) {
	for _, change := range []func([]byte){
		func(b []byte) { binary.NativeEndian.PutUint32(b[12:], 0) },
		func(b []byte) { binary.NativeEndian.PutUint32(b[16:], ^uint32(0)) },
		func(b []byte) { binary.NativeEndian.PutUint32(b[20:], ^uint32(0)) },
		func(b []byte) { binary.NativeEndian.PutUint32(b[48:], 1) },
	} {
		block := make([]byte, captureRingFrameSize)
		binary.NativeEndian.PutUint32(block, unix.TPACKET_V3)
		binary.NativeEndian.PutUint32(block[8:], unix.TP_STATUS_USER)
		binary.NativeEndian.PutUint32(block[12:], 2)
		binary.NativeEndian.PutUint32(block[16:], 48)
		binary.NativeEndian.PutUint32(block[20:], 560)
		change(block)
		r := captureRing{data: block}
		if _, err := r.packet(7); err == nil {
			t.Fatal("invalid block/stride accepted")
		}
		if r.ready() {
			t.Fatal("invalid block not released")
		}
	}
}

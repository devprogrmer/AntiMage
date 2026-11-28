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
	frame := make([]byte, captureRingFrameSize)
	packet := packetFixture(false, false)
	binary.NativeEndian.PutUint32(frame, unix.TP_STATUS_USER)
	binary.NativeEndian.PutUint32(frame[4:], uint32(len(packet)))
	binary.NativeEndian.PutUint32(frame[8:], uint32(len(packet)))
	binary.NativeEndian.PutUint16(frame[14:], 64)
	binary.NativeEndian.PutUint16(frame[32:], unix.AF_PACKET)
	binary.NativeEndian.PutUint32(frame[36:], 7)
	frame[42] = unix.PACKET_OUTGOING
	copy(frame[64:], packet)
	return frame
}

func TestCaptureRingMetadataBoundsAndRelease(t *testing.T) {
	frame := ringFixture()
	want, _ := DecodePacket(packetFixture(false, false))
	got, outgoing, err := decodeRingFrame(frame, 7)
	if err != nil || !outgoing || got != want {
		t.Fatalf("metadata=%+v outgoing=%t err=%v", got, outgoing, err)
	}
	r := captureRing{data: frame}
	if !r.ready() {
		t.Fatal("user frame not ready")
	}
	r.release()
	if r.ready() || r.index != 0 {
		t.Fatal("frame ownership not returned or cursor failed to wrap")
	}
	for _, value := range frame {
		if value != 0 {
			t.Fatal("frame retained packet data")
		}
	}
	if got != want {
		t.Fatal("metadata retained shared frame memory")
	}
}

func TestCaptureRingRejectsTruncationAndInvalidOffsets(t *testing.T) {
	for _, change := range []func([]byte){
		func(b []byte) { binary.NativeEndian.PutUint32(b[8:], 1) },
		func(b []byte) {
			binary.NativeEndian.PutUint32(b[8:], ^uint32(0))
			binary.NativeEndian.PutUint32(b[4:], ^uint32(0))
		},
		func(b []byte) { binary.NativeEndian.PutUint16(b[14:], 0) },
		func(b []byte) { binary.NativeEndian.PutUint32(b[36:], 8) },
		func(b []byte) { binary.NativeEndian.PutUint16(b[32:], unix.AF_INET) },
	} {
		frame := ringFixture()
		change(frame)
		if _, _, err := decodeRingFrame(frame, 7); err == nil {
			t.Fatal("invalid ring frame accepted")
		}
	}
	if _, _, err := decodeRingFrame(make([]byte, 51), 7); err == nil {
		t.Fatal("short frame accepted")
	}
}

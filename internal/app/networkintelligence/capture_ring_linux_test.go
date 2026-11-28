//go:build linux

package networkintelligence

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"
)

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

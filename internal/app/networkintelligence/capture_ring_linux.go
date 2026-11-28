//go:build linux

package networkintelligence

import (
	"encoding/binary"
	"errors"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// V2 publishes individual frames immediately: unlike block retirement in V3,
// reading never waits to accumulate traffic. Poll sleeps when the ring is empty.
// Only this observation socket is configured; no TC hook or forwarding changes.
const captureRingFrameSize = 128 * 1024

type captureRing struct {
	data  []byte
	index int
}

func newCaptureRing(fd, requested int) (*captureRing, error) {
	if err := unix.SetsockoptInt(fd, unix.SOL_PACKET, unix.PACKET_VERSION, unix.TPACKET_V2); err != nil {
		return nil, err
	}
	frames := max(2, requested/captureRingFrameSize)
	if requested == 0 {
		frames = 8
	}
	req := unix.TpacketReq{Block_size: captureRingFrameSize, Block_nr: uint32(frames), Frame_size: captureRingFrameSize, Frame_nr: uint32(frames)}
	if err := unix.SetsockoptTpacketReq(fd, unix.SOL_PACKET, unix.PACKET_RX_RING, &req); err != nil {
		return nil, err
	}
	data, err := unix.Mmap(fd, 0, frames*captureRingFrameSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	return &captureRing{data: data}, nil
}

func (r *captureRing) frame() []byte { return r.data[r.index : r.index+captureRingFrameSize] }
func (r *captureRing) ready() bool {
	return atomic.LoadUint32((*uint32)(unsafe.Pointer(&r.frame()[0])))&unix.TP_STATUS_USER != 0
}

// Packet bytes are borrowed only until release. Validate every offset before
// creating a slice; truncated/oversized frames must never become valid metadata.
func decodeRingFrame(frame []byte, interfaceIndex int) (PacketMetadata, bool, error) {
	if len(frame) < 52 {
		return PacketMetadata{}, false, errors.New("short ring header")
	}
	snap := uint64(binary.NativeEndian.Uint32(frame[8:12]))
	wire := uint64(binary.NativeEndian.Uint32(frame[4:8]))
	net := uint64(binary.NativeEndian.Uint16(frame[14:16]))
	if snap == 0 || snap != wire || net < 52 || net > uint64(len(frame)) || snap > uint64(len(frame))-net {
		return PacketMetadata{}, false, errors.New("truncated or invalid ring frame")
	}
	// TPACKET_ALIGN(sizeof(tpacket2_hdr)) == 32 on every supported Linux ABI.
	if binary.NativeEndian.Uint16(frame[32:34]) != unix.AF_PACKET || int(int32(binary.NativeEndian.Uint32(frame[36:40]))) != interfaceIndex {
		return PacketMetadata{}, false, errors.New("ring interface mismatch")
	}
	metadata, err := DecodePacket(frame[net : net+snap])
	return metadata, frame[42] == unix.PACKET_OUTGOING, err
}

func (r *captureRing) release() {
	frame := r.frame()
	// The kernel cannot reuse the frame until ownership is returned below.
	end := uint64(binary.NativeEndian.Uint16(frame[14:16])) + uint64(binary.NativeEndian.Uint32(frame[8:12]))
	if end < 52 || end > uint64(len(frame)) {
		end = uint64(len(frame))
	}
	clear(frame[4:end])
	atomic.StoreUint32((*uint32)(unsafe.Pointer(&frame[0])), unix.TP_STATUS_KERNEL)
	r.index += captureRingFrameSize
	if r.index == len(r.data) {
		r.index = 0
	}
}

func (r *captureRing) close() error { return unix.Munmap(r.data) }

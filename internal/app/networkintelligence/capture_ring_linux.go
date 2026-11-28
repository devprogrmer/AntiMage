//go:build linux

package networkintelligence

import (
	"encoding/binary"
	"errors"
	"golang.org/x/sys/unix"
	"sync/atomic"
	"time"
	"unsafe"
)

// V3 packs variable-sized records into bounded blocks. The 32 ms retirement
// timeout affects telemetry delivery only; RTT uses each kernel packet timestamp.
const captureRingFrameSize = 256 * 1024

// IPv4 with maximum IP and TCP options needs at most 120 header bytes.
const captureHeaderBytes = 128

type captureRing struct {
	data      []byte
	index     int
	offset    int
	remaining uint32
	used      int
}

func newCaptureRing(fd, requested int) (*captureRing, error) {
	if err := unix.SetsockoptInt(fd, unix.SOL_PACKET, unix.PACKET_VERSION, unix.TPACKET_V3); err != nil {
		return nil, err
	}
	// Socket-local classic BPF truncates copies, never packets in the VPN path.
	// Every packet is still observed; original lengths remain in tpacket headers.
	filter := []unix.SockFilter{{Code: unix.BPF_RET | unix.BPF_K, K: captureHeaderBytes}}
	if err := unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}); err != nil {
		return nil, err
	}
	blocks := max(2, requested/captureRingFrameSize)
	if requested == 0 {
		blocks = 4
	}
	req := unix.TpacketReq3{Block_size: captureRingFrameSize, Block_nr: uint32(blocks), Frame_size: 2048, Frame_nr: uint32(blocks * captureRingFrameSize / 2048), Retire_blk_tov: 32}
	if err := unix.SetsockoptTpacketReq3(fd, unix.SOL_PACKET, unix.PACKET_RX_RING, &req); err != nil {
		return nil, err
	}
	data, err := unix.Mmap(fd, 0, blocks*captureRingFrameSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	return &captureRing{data: data}, nil
}
func (r *captureRing) frame() []byte { return r.data[r.index : r.index+captureRingFrameSize] }
func (r *captureRing) ready() bool {
	return atomic.LoadUint32((*uint32)(unsafe.Pointer(&r.frame()[8])))&unix.TP_STATUS_USER != 0
}

func decodeRingFrame(frame []byte, interfaceIndex int) (CapturedPacket, error) {
	if len(frame) < 68 {
		return CapturedPacket{}, errors.New("short ring packet header")
	}
	snap := uint64(binary.NativeEndian.Uint32(frame[12:16]))
	wire := uint64(binary.NativeEndian.Uint32(frame[16:20]))
	net := uint64(binary.NativeEndian.Uint16(frame[26:28]))
	sec := binary.NativeEndian.Uint32(frame[4:8])
	nsec := binary.NativeEndian.Uint32(frame[8:12])
	if snap == 0 || snap != min(wire, captureHeaderBytes) || net < 68 || net > uint64(len(frame)) || snap > uint64(len(frame))-net || sec == 0 || nsec >= 1000000000 {
		return CapturedPacket{}, errors.New("invalid ring packet bounds or timestamp")
	}
	if binary.NativeEndian.Uint16(frame[48:50]) != unix.AF_PACKET || int(int32(binary.NativeEndian.Uint32(frame[52:56]))) != interfaceIndex {
		return CapturedPacket{}, errors.New("ring interface mismatch")
	}
	metadata, err := decodePacketHeaders(frame[net:net+snap], uint32(wire))
	return CapturedPacket{Metadata: metadata, Outgoing: frame[58] == unix.PACKET_OUTGOING, At: time.Unix(int64(sec), int64(nsec)).UTC()}, err
}

func (r *captureRing) packet(interfaceIndex int) (CapturedPacket, error) {
	block := r.frame()
	if r.remaining == 0 {
		r.remaining = binary.NativeEndian.Uint32(block[12:16])
		r.offset = int(binary.NativeEndian.Uint32(block[16:20]))
		r.used = int(binary.NativeEndian.Uint32(block[20:24]))
		if binary.NativeEndian.Uint32(block[:4]) != unix.TPACKET_V3 || r.used < 48 || r.used > len(block) || r.offset < 48 || r.offset > r.used-68 || r.remaining == 0 || uint64(r.remaining) > uint64(r.used)/68 {
			r.release()
			return CapturedPacket{}, errors.New("invalid ring block")
		}
	}
	record := block[r.offset:r.used]
	next := int(binary.NativeEndian.Uint32(record[:4]))
	end := len(record)
	if r.remaining > 1 {
		if next < 68 || next%8 != 0 || next > len(record)-68 {
			r.release()
			return CapturedPacket{}, errors.New("invalid ring record stride")
		}
		end = next
	}
	packet, err := decodeRingFrame(record[:end], interfaceIndex)
	r.remaining--
	if r.remaining == 0 {
		r.release()
	} else {
		r.offset += next
	}
	return packet, err
}
func (r *captureRing) release() {
	block := r.frame()
	// Clear borrowed packet data before atomically returning the entire block.
	end := r.used
	if end < 48 || end > len(block) {
		end = len(block)
	}
	clear(block[12:end])
	atomic.StoreUint32((*uint32)(unsafe.Pointer(&block[8])), unix.TP_STATUS_KERNEL)
	r.index += captureRingFrameSize
	if r.index == len(r.data) {
		r.index = 0
	}
	r.remaining = 0
	r.offset = 0
	r.used = 0
}
func (r *captureRing) close() error { return unix.Munmap(r.data) }

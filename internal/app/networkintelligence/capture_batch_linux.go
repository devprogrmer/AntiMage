//go:build linux

package networkintelligence

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Eight bounded buffers replace a syscall per packet. MSG_WAITFORONE blocks
// only for the first datagram, then drains already queued packets without
// waiting for a full batch. No ring, interface or traffic-control rule is used.
const captureBatchSize = 8

type captureMessage struct {
	Header unix.Msghdr
	Length uint32
}

type captureBatch struct {
	messages [captureBatchSize]captureMessage
	vectors  [captureBatchSize]unix.Iovec
	sources  [captureBatchSize]unix.RawSockaddrLinklayer
	buffers  [captureBatchSize][65535]byte
}

func (b *captureBatch) receive(fd int) (int, error) {
	for i := range b.messages {
		b.vectors[i].Base = &b.buffers[i][0]
		b.vectors[i].SetLen(len(b.buffers[i]))
		b.messages[i] = captureMessage{Header: unix.Msghdr{
			Name: (*byte)(unsafe.Pointer(&b.sources[i])), Namelen: uint32(unsafe.Sizeof(b.sources[i])),
			Iov: &b.vectors[i], Iovlen: 1,
		}}
	}
	n, _, errno := unix.Syscall6(unix.SYS_RECVMMSG, uintptr(fd), uintptr(unsafe.Pointer(&b.messages[0])), captureBatchSize, unix.MSG_WAITFORONE, 0, 0)
	runtime.KeepAlive(b)
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

func (b *captureBatch) clear() {
	for i := range b.buffers {
		clear(b.buffers[i][:])
	}
}

//go:build linux

package networkintelligence

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCaptureBatchDrainsQueuedDatagramsWithoutWaitingForFullBatch(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	if err := unix.SetsockoptTimeval(fds[1], unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 1}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []byte{11, 22, 33} {
		if _, err := unix.Write(fds[0], []byte{value}); err != nil {
			t.Fatal(err)
		}
	}
	batch := new(captureBatch)
	start := time.Now()
	n, err := batch.receive(fds[1])
	if err != nil || n != 3 {
		t.Fatalf("received=%d err=%v", n, err)
	}
	if time.Since(start) >= time.Second {
		t.Fatal("waited for an unfilled batch")
	}
	for i, want := range []byte{11, 22, 33} {
		if batch.messages[i].Length != 1 || batch.buffers[i][0] != want {
			t.Fatalf("datagram %d corrupted", i)
		}
	}
	batch.clear()
	for i := range batch.buffers {
		for _, value := range batch.buffers[i] {
			if value != 0 {
				t.Fatal("retained packet bytes")
			}
		}
	}
	if _, err := unix.Write(fds[0], []byte{44, 55}); err != nil {
		t.Fatal(err)
	}
	n, err = batch.receive(fds[1])
	if err != nil || n != 1 || batch.messages[0].Length != 2 || batch.buffers[0][0] != 44 {
		t.Fatalf("reused batch n=%d err=%v", n, err)
	}
	batch.clear()
}

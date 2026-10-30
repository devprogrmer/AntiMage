package nodeagent

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const (
	accountingWALHeaderSize = 12
	accountingWALMaxRecord  = 64 << 20
	accountingWALMaxBytes   = 128 << 20
)

var accountingWALMagic = [4]byte{'A', 'M', 'W', 'L'}

// appendAccountingWAL commits one framed record before returning. expectedOffset
// is the last known-good end of file, allowing a retry to discard a torn tail
// without scanning or rewriting the journal.
func appendAccountingWAL(path string, expectedOffset int64, payload []byte) (int64, error) {
	if len(payload) == 0 || len(payload) > accountingWALMaxRecord {
		return expectedOffset, fmt.Errorf("accounting journal record size %d is invalid", len(payload))
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return expectedOffset, fmt.Errorf("create accounting journal directory: %w", err)
	}
	if err := ensureAccountingDirectoryDurable(dir); err != nil {
		return expectedOffset, fmt.Errorf("sync accounting journal directory chain: %w", err)
	}
	_, statErr := os.Stat(path)
	created := os.IsNotExist(statErr)
	if statErr != nil && !created {
		return expectedOffset, statErr
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return expectedOffset, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return expectedOffset, err
	}
	if info.Size() < expectedOffset {
		return expectedOffset, fmt.Errorf("accounting journal shrank from %d to %d bytes", expectedOffset, info.Size())
	}
	if info.Size() != expectedOffset {
		if err := file.Truncate(expectedOffset); err != nil {
			return expectedOffset, fmt.Errorf("repair accounting journal tail: %w", err)
		}
		if err := accountingWALFileSync(file); err != nil {
			return expectedOffset, fmt.Errorf("sync repaired accounting journal: %w", err)
		}
	}
	if expectedOffset+int64(accountingWALHeaderSize+len(payload)) > accountingWALMaxBytes {
		return expectedOffset, fmt.Errorf("accounting journal exceeds safe byte capacity")
	}
	if _, err := file.Seek(expectedOffset, io.SeekStart); err != nil {
		return expectedOffset, err
	}
	var header [accountingWALHeaderSize]byte
	copy(header[:4], accountingWALMagic[:])
	binary.BigEndian.PutUint32(header[4:8], uint32(len(payload)))
	binary.BigEndian.PutUint32(header[8:12], crc32.ChecksumIEEE(payload))
	frame := make([]byte, accountingWALHeaderSize+len(payload))
	copy(frame, header[:])
	copy(frame[accountingWALHeaderSize:], payload)
	if err := writeAccountingWALFrame(file, frame); err != nil {
		return expectedOffset, rollbackAccountingWALAppend(file, expectedOffset, err)
	}
	if err := accountingWALFileSync(file); err != nil {
		return expectedOffset, rollbackAccountingWALAppend(file, expectedOffset, fmt.Errorf("sync accounting journal: %w", err))
	}
	if created {
		if err := accountingDirectorySync(dir); err != nil {
			return expectedOffset, rollbackAccountingWALAppend(file, expectedOffset, fmt.Errorf("sync accounting journal directory: %w", err))
		}
	}
	return expectedOffset + int64(len(frame)), nil
}

func writeAccountingWALFrame(file *os.File, frame []byte) error {
	for len(frame) > 0 {
		n, err := file.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

func rollbackAccountingWALAppend(file *os.File, offset int64, cause error) error {
	if err := file.Truncate(offset); err != nil {
		return errors.Join(cause, fmt.Errorf("truncate failed accounting journal append: %w", err))
	}
	if err := accountingWALFileSync(file); err != nil {
		return errors.Join(cause, fmt.Errorf("sync truncated accounting journal: %w", err))
	}
	return cause
}

// replayAccountingWAL applies complete, checksum-verified records in order.
// A short final frame is a crash-torn append and is truncated before new writes.
func replayAccountingWAL(path string, apply func([]byte) error) (offset int64, records uint64, err error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0600)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	for {
		var header [accountingWALHeaderSize]byte
		_, readErr := io.ReadFull(file, header[:])
		if readErr == io.EOF {
			return offset, records, nil
		}
		if readErr == io.ErrUnexpectedEOF {
			return truncateAccountingWALTail(file, offset, records)
		}
		if readErr != nil {
			return offset, records, readErr
		}
		if string(header[:4]) != string(accountingWALMagic[:]) {
			return offset, records, fmt.Errorf("invalid accounting journal frame magic at offset %d", offset)
		}
		length := binary.BigEndian.Uint32(header[4:8])
		if length == 0 || length > accountingWALMaxRecord {
			return offset, records, fmt.Errorf("invalid accounting journal frame length %d at offset %d", length, offset)
		}
		payload := make([]byte, length)
		if _, readErr := io.ReadFull(file, payload); readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			return truncateAccountingWALTail(file, offset, records)
		} else if readErr != nil {
			return offset, records, readErr
		}
		if crc32.ChecksumIEEE(payload) != binary.BigEndian.Uint32(header[8:12]) {
			return offset, records, fmt.Errorf("accounting journal checksum mismatch at offset %d", offset)
		}
		if apply != nil {
			if err := apply(payload); err != nil {
				return offset, records, err
			}
		}
		offset += int64(accountingWALHeaderSize) + int64(length)
		records++
	}
}

func truncateAccountingWALTail(file *os.File, offset int64, records uint64) (int64, uint64, error) {
	if err := file.Truncate(offset); err != nil {
		return offset, records, fmt.Errorf("truncate crash-torn accounting journal tail: %w", err)
	}
	if err := accountingWALFileSync(file); err != nil {
		return offset, records, fmt.Errorf("sync repaired accounting journal: %w", err)
	}
	return offset, records, nil
}

func compactAccountingWAL(path string) error {
	dir := filepath.Dir(path)
	if err := ensureAccountingDirectoryDurable(dir); err != nil {
		return err
	}
	if err := atomicWriteFile(path, nil, 0600); err != nil {
		return err
	}
	return accountingDirectorySync(dir)
}

func syncAccountingDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}

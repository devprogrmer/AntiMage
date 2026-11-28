package networkintelligence

import (
	"bytes"
	"encoding/binary"
)

// Source server info queries identify a server advertisement, not gameplay.
// Split/compressed replies and snapshots missing the full query reply remain
// unclassified. Strings, player counts and Steam IDs are skipped, never saved.
func parseSourceQuery(b []byte, wireLength int) udpProtocolMetadata {
	var unknown udpProtocolMetadata
	if len(b) != wireLength || len(b) < 6 || binary.LittleEndian.Uint32(b[:4]) != 0xffffffff {
		return unknown
	}
	if b[4] == 0x54 {
		if (len(b) != 25 && len(b) != 29) || !bytes.Equal(b[5:25], []byte("Source Engine Query\x00")) {
			return unknown
		}
		return udpProtocolMetadata{protocol: udpSourceQuery, kind: 1}
	}
	if b[4] != 0x49 || b[5] == 0 {
		return unknown
	}
	i := 6
	skipString := func() bool {
		if i >= len(b) {
			return false
		}
		n := bytes.IndexByte(b[i:], 0)
		if n < 0 {
			return false
		}
		i += n + 1
		return true
	}
	skip := func(n int) bool {
		if n > len(b)-i {
			return false
		}
		i += n
		return true
	}
	for range 4 {
		if !skipString() {
			return unknown
		}
	}
	if len(b)-i < 9 {
		return unknown
	}
	shortID := binary.LittleEndian.Uint16(b[i : i+2])
	if shortID == 2400 {
		return unknown
	} // The Ship has an extended base layout.
	if b[i+2] > b[i+3] || b[i+4] > b[i+2] || !bytes.ContainsRune([]byte("dlp"), rune(b[i+5])) || !bytes.ContainsRune([]byte("lwmo"), rune(b[i+6])) || b[i+7] > 1 || b[i+8] > 1 {
		return unknown
	}
	i += 9
	if !skipString() {
		return unknown
	}
	result := udpProtocolMetadata{protocol: udpSourceQuery, kind: 2}
	if i == len(b) {
		return result
	} // 16-bit IDs alone are not enough for a title.
	flags := b[i]
	i++
	if flags & ^byte(0xf1) != 0 {
		return unknown
	}
	if flags&0x80 != 0 && !skip(2) {
		return unknown
	}
	if flags&0x10 != 0 && !skip(8) {
		return unknown
	}
	if flags&0x40 != 0 {
		if !skip(2) || !skipString() {
			return unknown
		}
	}
	if flags&0x20 != 0 && !skipString() {
		return unknown
	}
	if flags&1 != 0 {
		if len(b)-i < 8 {
			return unknown
		}
		gameID := binary.LittleEndian.Uint64(b[i : i+8])
		i += 8
		// Only plain application GameIDs; never infer a title from a truncated
		// 16-bit ID, an inconsistent ID, a mod or a non-application GameID.
		if gameID>>24 == 0 && uint16(gameID) == shortID {
			result.appID = uint32(gameID)
		}
	}
	if i != len(b) {
		return unknown
	}
	return result
}

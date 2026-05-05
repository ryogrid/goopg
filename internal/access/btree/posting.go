package btree

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// BTPostingFlag is the high bit of keyLen in a posting-list item.
// When set, the raw item encodes multiple heap TIDs for a single key
// rather than the standard single-TID layout (M0047-0003).
const BTPostingFlag = uint16(0x8000)

// postingHeaderSize is the fixed-size prefix before TID arrays and key bytes.
//   [0:2] keyLen | BTPostingFlag  (uint16 LE)
//   [2:4] TID count              (uint16 LE)
// = 4 bytes
const postingHeaderSize = 4

// marshalPosting serialises a posting-list item:
//   postingHeaderSize | count*6 bytes (TIDs) | keyLen bytes (key)
// The keyLen high bit is set to BTPostingFlag so readers distinguish
// this from the standard 8-byte prefix item layout.
func marshalPosting(key []byte, tids []storage.ItemPointer) []byte {
	n := len(tids)
	raw := make([]byte, postingHeaderSize+n*6+len(key))
	binary.LittleEndian.PutUint16(raw[0:2], uint16(len(key))|BTPostingFlag)
	binary.LittleEndian.PutUint16(raw[2:4], uint16(n))
	off := postingHeaderSize
	for _, tid := range tids {
		binary.LittleEndian.PutUint32(raw[off:off+4], uint32(tid.Block))
		binary.LittleEndian.PutUint16(raw[off+4:off+6], tid.Offset)
		off += 6
	}
	copy(raw[off:], key)
	return raw
}

// isPostingRaw reports whether raw encodes a posting-list item.
func isPostingRaw(raw []byte) bool {
	if len(raw) < 2 {
		return false
	}
	return binary.LittleEndian.Uint16(raw[0:2])&BTPostingFlag != 0
}

// parsePostingRaw decodes a posting-list item into its key and TID list.
// Returns an error when the bytes are structurally invalid.
func parsePostingRaw(raw []byte) (key []byte, tids []storage.ItemPointer, err error) {
	if len(raw) < postingHeaderSize {
		return nil, nil, fmt.Errorf("btree posting: raw too short (%d bytes)", len(raw))
	}
	keyLen := int(binary.LittleEndian.Uint16(raw[0:2]) &^ BTPostingFlag)
	n := int(binary.LittleEndian.Uint16(raw[2:4]))
	wantLen := postingHeaderSize + n*6 + keyLen
	if len(raw) != wantLen {
		return nil, nil, fmt.Errorf("btree posting: size mismatch (got %d want %d, n=%d keyLen=%d)",
			len(raw), wantLen, n, keyLen)
	}
	tids = make([]storage.ItemPointer, n)
	off := postingHeaderSize
	for i := range tids {
		tids[i] = storage.ItemPointer{
			Block:  storage.BlockNumber(binary.LittleEndian.Uint32(raw[off : off+4])),
			Offset: binary.LittleEndian.Uint16(raw[off+4 : off+6]),
		}
		off += 6
	}
	key = append([]byte(nil), raw[off:off+keyLen]...)
	return key, tids, nil
}

// postingKeyOf extracts only the key bytes from a raw posting item
// without allocating a TID slice. Used for key comparisons.
func postingKeyOf(raw []byte) []byte {
	if len(raw) < postingHeaderSize {
		return nil
	}
	keyLen := int(binary.LittleEndian.Uint16(raw[0:2]) &^ BTPostingFlag)
	n := int(binary.LittleEndian.Uint16(raw[2:4]))
	off := postingHeaderSize + n*6
	if off+keyLen > len(raw) {
		return nil
	}
	return raw[off : off+keyLen]
}

// appendTIDToPosting (M0055-0003) returns a new posting payload
// with `tid` appended to an existing posting's TID list. Used by
// the steady-state insert path to grow a posting in place when an
// inserted key matches an existing posting's key, instead of
// allocating a fresh single-TID line pointer.
func appendTIDToPosting(raw []byte, tid storage.ItemPointer) ([]byte, error) {
	key, tids, err := parsePostingRaw(raw)
	if err != nil {
		return nil, err
	}
	tids = append(tids, tid)
	return marshalPosting(key, tids), nil
}

// promoteSingleToPosting (M0055-0003) builds a 2-TID posting
// payload from an existing single-TID line pointer's `(key, ptr)`
// plus a new `tid`. Used by the steady-state insert path when a
// duplicate-key insert hits a non-posting line pointer.
func promoteSingleToPosting(existingKey []byte, existingPtr storage.ItemPointer, newTID storage.ItemPointer) []byte {
	return marshalPosting(existingKey, []storage.ItemPointer{existingPtr, newTID})
}

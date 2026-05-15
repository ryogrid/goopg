package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sync"
)

const (
	recordHeaderSizeLegacy = 8
	// recordHeaderSize keeps the legacy name for existing call-sites
	// and tests that exercise the pre-M0014 frame.
	recordHeaderSize = recordHeaderSizeLegacy

	xlogRecordHeaderSize = SizeOfXLogRecord

	// Upstream XLogRecord payload chunk tags
	// (postgres/src/include/access/xlogrecord.h).
	xlrBlockIDDataLong  byte = 254
	xlrBlockIDDataShort byte = 255
)

const (
	xlogInfoDefault uint8 = 0xF0
	xlogInfoDelete  uint8 = 0x10
	xlogInfoSplit   uint8 = 0x20
	xlogInfoAbort   uint8 = 0x20
	xlogInfoVacuum  uint8 = 0x10
	xlogInfoLock    uint8 = 0x20
)

var (
	ErrCorruptRecord = errors.New("wal: corrupt record")
	// ErrEOS signals that the decoder reached the end-of-stream
	// sentinel (a zero record header) inside a preallocated
	// segment's zero-fill tail. Callers stop reading on this. See
	// docs/design/0007-0001-wal-segment-preallocation.md.
	ErrEOS = errors.New("wal: end of stream")
)

// crcCache caches the CRC-32 of the most recently encoded payload.
// When the same payload bytes are written consecutively (common for
// commit/delete markers), the cache avoids recomputing the CRC.
// M0027-0001.
var crcCache struct {
	mu       sync.Mutex
	last     []byte
	checksum uint32
}

func cachedCRC(payload []byte) uint32 {
	crcCache.mu.Lock()
	defer crcCache.mu.Unlock()
	if len(payload) == len(crcCache.last) {
		match := true
		for i := range payload {
			if payload[i] != crcCache.last[i] {
				match = false
				break
			}
		}
		if match {
			return crcCache.checksum
		}
	}
	crc := crc32.ChecksumIEEE(payload)
	crcCache.last = append(crcCache.last[:0], payload...)
	crcCache.checksum = crc
	return crc
}

// isZeroHeader reports whether the first recordHeaderSize bytes
// of stream are all zero — the EOS sentinel. Stream shorter than
// the header counts as the same condition (no further records can
// fit anyway).
func isZeroHeader(stream []byte) bool {
	if len(stream) < recordHeaderSize {
		return true
	}
	for i := 0; i < recordHeaderSize; i++ {
		if stream[i] != 0 {
			return false
		}
	}
	return true
}

// isZeroXLogRecordHeader reports whether the first 24 bytes are all
// zero. In page-header mode this is the in-page EOS sentinel inside
// a preallocated segment tail.
func isZeroXLogRecordHeader(stream []byte) bool {
	if len(stream) < xlogRecordHeaderSize {
		return true
	}
	for i := 0; i < xlogRecordHeaderSize; i++ {
		if stream[i] != 0 {
			return false
		}
	}
	return true
}

func encodeRecord(payload []byte) []byte {
	buf := make([]byte, recordHeaderSize+len(payload))
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(buf[4:8], cachedCRC(payload))
	copy(buf[recordHeaderSize:], payload)
	return buf
}

func decodeRecord(stream []byte) ([]byte, int, error) {
	if len(stream) < recordHeaderSize {
		return nil, 0, fmt.Errorf("%w: truncated header", ErrCorruptRecord)
	}
	payloadLen := int(binary.LittleEndian.Uint32(stream[0:4]))
	crc := binary.LittleEndian.Uint32(stream[4:8])
	total := recordHeaderSize + payloadLen
	if payloadLen < 0 || total > len(stream) {
		return nil, 0, fmt.Errorf("%w: bad length %d", ErrCorruptRecord, payloadLen)
	}
	payload := stream[recordHeaderSize:total]
	if crc32.ChecksumIEEE(payload) != crc {
		return nil, 0, fmt.Errorf("%w: checksum mismatch", ErrCorruptRecord)
	}
	out := make([]byte, len(payload))
	copy(out, payload)
	return out, total, nil
}

func xlogMainDataHeaderSize(payloadLen int) int {
	if payloadLen <= 0xFF {
		return 2
	}
	return 5
}

func xlogRecordWireSize(payloadLen int) int {
	return xlogRecordHeaderSize + xlogMainDataHeaderSize(payloadLen) + payloadLen
}

// xlogRecordAlign is the upstream MAXALIGN value for WAL record
// boundaries. Every XLogRecord on disk is followed by zero pad bytes
// out to this multiple so the next record header begins MAXALIGN-aligned.
// pg_waldump enforces this by advancing nextRecord = MAXALIGN(xl_tot_len).
const xlogRecordAlign = 8

func maxAlignXLog(n int) int {
	return (n + xlogRecordAlign - 1) &^ (xlogRecordAlign - 1)
}

func wrapXLogMainData(payload []byte) []byte {
	if len(payload) <= 0xFF {
		out := make([]byte, 2+len(payload))
		out[0] = xlrBlockIDDataShort
		out[1] = byte(len(payload))
		copy(out[2:], payload)
		return out
	}
	out := make([]byte, 5+len(payload))
	out[0] = xlrBlockIDDataLong
	binary.LittleEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

func unwrapXLogMainData(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: missing xlog data header", ErrCorruptRecord)
	}
	switch data[0] {
	case xlrBlockIDDataShort:
		if len(data) < 2 {
			return nil, fmt.Errorf("%w: truncated short xlog data header", ErrCorruptRecord)
		}
		n := int(data[1])
		if n < 0 || 2+n > len(data) {
			return nil, fmt.Errorf("%w: bad short xlog main-data length %d", ErrCorruptRecord, n)
		}
		if 2+n != len(data) {
			return nil, fmt.Errorf("%w: unsupported trailing xlog chunks", ErrCorruptRecord)
		}
		out := make([]byte, n)
		copy(out, data[2:2+n])
		return out, nil
	case xlrBlockIDDataLong:
		if len(data) < 5 {
			return nil, fmt.Errorf("%w: truncated long xlog data header", ErrCorruptRecord)
		}
		n := int(binary.LittleEndian.Uint32(data[1:5]))
		if n < 0 || 5+n > len(data) {
			return nil, fmt.Errorf("%w: bad long xlog main-data length %d", ErrCorruptRecord, n)
		}
		if 5+n != len(data) {
			return nil, fmt.Errorf("%w: unsupported trailing xlog chunks", ErrCorruptRecord)
		}
		out := make([]byte, n)
		copy(out, data[5:5+n])
		return out, nil
	default:
		return nil, fmt.Errorf("%w: unsupported xlog main-data tag 0x%02x", ErrCorruptRecord, data[0])
	}
}

func classifyXLogRecord(payload []byte) (Rmgr, uint8, uint32) {
	if len(payload) == 0 {
		return RmgrXLog, xlogInfoDefault, 0
	}
	kind := payload[0]
	switch kind {
	case RecordKindPageImage, RecordKindCheckpoint:
		return RmgrXLog, xlogInfoDefault, 0
	case RecordKindHeapInsert:
		return RmgrHeap, xlogInfoDefault, 0
	case RecordKindHeapDelete:
		_, _, _, xmax, _, err := DecodeHeapDelete(payload)
		if err == nil {
			return RmgrHeap, xlogInfoDelete, uint32(xmax)
		}
		return RmgrHeap, xlogInfoDelete, 0
	case RecordKindHeapLock:
		_, _, _, xmax, _, err := DecodeHeapLock(payload)
		if err == nil {
			return RmgrHeap, xlogInfoLock, uint32(xmax)
		}
		return RmgrHeap, xlogInfoLock, 0
	case RecordKindHeapVacuum:
		return RmgrHeap2, xlogInfoVacuum, 0
	case RecordKindBtreeInsert:
		return RmgrBtree, xlogInfoDefault, 0
	case RecordKindBtreeSplit:
		return RmgrBtree, xlogInfoSplit, 0
	case RecordKindXactCommit:
		xid, err := DecodeXactMarker(payload)
		if err == nil {
			return RmgrXact, xlogInfoDefault, uint32(xid)
		}
		return RmgrXact, xlogInfoDefault, 0
	case RecordKindXactAbort:
		xid, err := DecodeXactMarker(payload)
		if err == nil {
			return RmgrXact, xlogInfoAbort, uint32(xid)
		}
		return RmgrXact, xlogInfoAbort, 0
	default:
		return RmgrXLog, xlogInfoDefault, 0
	}
}

// encodeRecordXLog returns the on-disk XLogRecord stream
// (header + wrapped main-data chunk + trailing zero pad bytes out
// to the next MAXALIGN(8) boundary). The second return value is
// the unpadded record length — equal to xl_tot_len — and is used
// by emitWithPageHeaders to compute xlp_rem_len for cross-page
// records (pad bytes are not counted in xlp_rem_len upstream).
func encodeRecordXLog(payload []byte, prev uint64) ([]byte, int, error) {
	wrapped := wrapXLogMainData(payload)
	rmgr, info, xid := classifyXLogRecord(payload)
	realLen := xlogRecordHeaderSize + len(wrapped)
	header := XLogRecord{
		TotLen: uint32(realLen),
		XID:    xid,
		Prev:   prev,
		Info:   info,
		Rmid:   rmgr,
	}
	// out is zero-filled to MAXALIGN — the trailing pad bytes are
	// the upstream record-alignment requirement so pg_waldump's
	// nextRecord = MAXALIGN(xl_tot_len) advance lands on the next
	// header.
	out := make([]byte, maxAlignXLog(realLen))
	if err := EncodeXLogRecordHeader(out[:xlogRecordHeaderSize], header, wrapped); err != nil {
		return nil, 0, err
	}
	copy(out[xlogRecordHeaderSize:realLen], wrapped)
	return out, realLen, nil
}

func decodeRecordXLog(stream []byte) ([]byte, int, error) {
	decoded, err := decodeRecordXLogDetailed(stream)
	if err != nil {
		return nil, 0, err
	}
	if decoded.Payload == nil {
		return nil, decoded.Consumed, fmt.Errorf("%w: record carries structured xlog fragments", ErrCorruptRecord)
	}
	return decoded.Payload, decoded.Consumed, nil
}

func formatSegmentName(segNo uint64) string {
	return fmt.Sprintf("%024X", segNo)
}

func parseSegmentName(name string) (uint64, bool) {
	if len(name) != 24 {
		return 0, false
	}
	var seg uint64
	_, err := fmt.Sscanf(name, "%024X", &seg)
	if err != nil {
		return 0, false
	}
	return seg, true
}

func segmentForLSN(lsn uint64, segSize int64) uint64 {
	// lsn is 1-based byte position; segment math is 0-based.
	return uint64((int64(lsn) - 1) / segSize)
}

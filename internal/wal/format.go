package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// (recordHeaderSizeLegacy / recordHeaderSize — the retired 8-byte IEEE-CRC
	// frame header — were removed in A9.)

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
	// M0102-0007: PG-compatible xl_info values for checkpoint
	// records so a PG standby can recognise them during recovery.
	xlogCheckpointOnline   uint8 = 0x10 // XLOG_CHECKPOINT_ONLINE
	xlogCheckpointShutdown uint8 = 0x00 // XLOG_CHECKPOINT_SHUTDOWN
	// XLOG_CHECKPOINT_REDO (PG17+, pg_control.h): inserted AT the redo
	// point of an online checkpoint — recovery validates the record found
	// at CheckPoint.redo is exactly this when redo < the checkpoint record.
	xlogCheckpointRedo uint8 = 0xE0
)

var (
	ErrCorruptRecord = errors.New("wal: corrupt record")
	// ErrEOS signals that the decoder reached the end-of-stream
	// sentinel (a zero record header) inside a preallocated
	// segment's zero-fill tail. Callers stop reading on this. See
	// docs/design/0007-0001-wal-segment-preallocation.md.
	ErrEOS = errors.New("wal: end of stream")
)

// (isZeroHeader — the legacy 8-byte-frame EOS sentinel — was removed in A9 with
// the IEEE-CRC frame; the page-aware walk uses isZeroXLogRecordHeader.)

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
	// A9-checkpoint-opcode: the classify-by-len==88 checkpoint hack
	// (M0102-0007/M0105-0009, which could only ever stamp SHUTDOWN) is
	// retired — PG-compat checkpoints now carry their EXPLICIT opcode
	// (online/shutdown) via the pre-assembled envelope (EncodeCheckpointPG),
	// which short-circuits in encodeRecordXLog before classification.
	// doc 04 §3/§5.4: dispatch on the real PG-compatible (xl_rmid, xl_info)
	// pair for this RecordKind — a real PG analog (RmgrHeap/RmgrBtree/…)
	// when one exists, else goopg's custom RmgrGoopgCatalog rmgr (§3.2).
	// Previously every native record classified as RmgrXLog/0xF0
	// (M0105-0007's blanket catch-all); recovery.go's §4 dispatch rework
	// (isGoopgOwnedRmgr) landed in the same change to keep goopg's own
	// crash recovery routing to the native payload[0] switch for every
	// rmgr returned here. xid stays 0 — the real xid (when one exists)
	// lives in the payload body, not the XLogRecord header, for native
	// records (see stream_replayer.go's replayedXactInfo).
	rmgr, info := recordKindToRmgrInfo(payload[0])
	return rmgr, info, 0
}

// encodeRecordXLog returns the on-disk XLogRecord stream
// (header + wrapped main-data chunk + trailing zero pad bytes out
// to the next MAXALIGN(8) boundary). The second return value is
// the unpadded record length — equal to xl_tot_len — and is used
// by emitWithPageHeaders to compute xlp_rem_len for cross-page
// records (pad bytes are not counted in xlp_rem_len upstream).
func encodeRecordXLog(payload []byte, prev uint64) ([]byte, int, error) {
	// Pre-assembled PG record (block refs + FPI + main-data chunk already built
	// by assembleXLogRecord): emit its body verbatim under the carried
	// (rmid, info, xid) header — never re-wrap or re-classify. See
	// pg_assembled_emit.go.
	if rmid, info, xid, body, ok := unframePGAssembled(payload); ok {
		return encodeAssembledXLog(body, rmid, info, xid, prev)
	}
	wrapped := wrapXLogMainData(payload)
	rmgr, info, xid := classifyXLogRecord(payload)
	realLen := xlogRecordHeaderSize + len(wrapped)
	// prev is the caller's 0-based PG LSN (writer.go stores prevRecPtr as
	// start-1 which is already the 0-based RecPtr). InvalidXLogRecPtr (0)
	// means "no previous record" and is used verbatim.
	prevPG := prev
	header := XLogRecord{
		TotLen: uint32(realLen),
		XID:    xid,
		Prev:   prevPG,
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

// formatSegmentName generates a PG-compatible WAL segment filename for
// the given absolute segment number (segNo * DefaultSegmentSize = first byte
// of the segment). Uses TLI=1 (the bootstrap timeline). Mirrors
// XLogFilePath() in postgres/src/include/access/xlog_internal.h.
func formatSegmentName(segNo uint64) string {
	const tli = 1
	totalBytes := segNo * uint64(DefaultSegmentSize)
	logNo := uint32(totalBytes >> 32)
	segInLog := uint32((totalBytes & 0xFFFFFFFF) / uint64(DefaultSegmentSize))
	return fmt.Sprintf("%08X%08X%08X", tli, logNo, segInLog)
}

// parseSegmentName parses a PG-compatible WAL segment filename
// (24 hex chars: TLI + LOG + SEG, each 8 hex digits) and returns
// the absolute segment number such that segNo*DefaultSegmentSize equals
// the segment's first byte position. Returns false for non-WAL filenames.
func parseSegmentName(name string) (uint64, bool) {
	_, segno, ok := ParseXLogFileName(name, 0)
	return segno, ok
}

func segmentForLSN(lsn uint64, segSize int64) uint64 {
	// lsn is 1-based byte position; segment math is 0-based.
	return uint64((int64(lsn) - 1) / segSize)
}

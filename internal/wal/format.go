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
	xlogInfoDefault  uint8 = 0xF0
	xlogInfoDelete   uint8 = 0x10
	xlogInfoSplit    uint8 = 0x20
	xlogInfoAbort    uint8 = 0x20
	xlogInfoVacuum   uint8 = 0x10
	xlogInfoLock     uint8 = 0x20
	// M0102-0007: PG-compatible xl_info values for checkpoint
	// records so a PG standby can recognise them during recovery.
	xlogCheckpointOnline   uint8 = 0x10 // XLOG_CHECKPOINT_ONLINE
	xlogCheckpointShutdown uint8 = 0x00 // XLOG_CHECKPOINT_SHUTDOWN
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
	// M0102-0007: PG-compatible checkpoint record (88-byte CheckPoint struct).
	// Goopg's record-kind byte (0x02=RecordKindCheckpoint) would map to an
	// implausible redo LSN (<256 bytes), so this path takes priority over the
	// legacy kind-byte dispatch.
	if len(payload) == 88 {
		// M0105-0009: use XLOG_CHECKPOINT_SHUTDOWN (0x00) instead of
		// ONLINE (0x10). PG's xlog_redo for shutdown checkpoints calls
		// ProcArrayApplyRecoveryInfo() which constructs synthetic
		// RunningTransactionsData and transitions standbyState to
		// STANDBY_SNAPSHOT_READY. This enables CheckRecoveryConsistency
		// to send PMSIGNAL_BEGIN_HOT_STANDBY, allowing the postmaster
		// to enter PM_HOT_STANDBY. Without this, pg_ctl -w never sees
		// the server as ready.
		return RmgrXLog, xlogCheckpointShutdown, 0
	}
	// Classify by the goopg RecordKind (payload[0]) to a PG-compatible
	// (xl_rmid, xl_info): records with a PG analog carry the real rmgr +
	// opcode; goopg-private records carry the custom RmgrGoopgCatalog.
	// This replaces the historical RM_XLOG/0xF0 "skip-me" tag so goopg
	// emits ordinary-PG-shaped headers. goopg's own recovery re-keys on
	// payload[0] inside each rmgr arm (see ApplyRecord).
	// xl_xid stays 0 here — native bodies carry their own xid, which
	// goopg recovery reads directly.
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

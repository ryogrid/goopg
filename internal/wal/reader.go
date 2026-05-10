package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Record is one decoded WAL record from the stream.
type Record struct {
	StartLSN uint64
	EndLSN   uint64
	Payload  []byte
}

// ReadAll decodes every record found under walDir across ordered
// segments. It is intended for recovery and unit tests.
//
// Auto-detects the on-disk format via DetectWALFormat: page-emitted
// segments (WALFormatPGCompat) are walked with page-header skipping
// plus XLogRecord decode; legacy bare-record streams use the
// pre-M0014 zero-record-header EOS sentinel. The caller does not
// need to know which format is in use — the same call site handles
// both during the M0014 rollout window.
func ReadAll(walDir string, segmentSize int64) ([]Record, error) {
	if segmentSize <= 0 {
		segmentSize = DefaultSegmentSize
	}

	// M0045-0001/0003: WAL retention may remove segment 0.  Determine
	// the first retained segment so (a) we read the correct files and
	// (b) we offset all LSN values by firstSegNo*segmentSize so that
	// returned StartLSN/EndLSN are ABSOLUTE byte positions — matching
	// the values the WAL writer assigned and the values stored in page
	// pd_lsn headers for idempotency checks.
	firstSegNoI, err := firstAvailableSegment(walDir)
	if err != nil {
		return nil, err
	}
	if firstSegNoI < 0 {
		return nil, nil
	}
	firstSegNo := uint64(firstSegNoI)

	stream, err := readStreamFrom(walDir, segmentSize, firstSegNo)
	if err != nil {
		return nil, err
	}
	if len(stream) == 0 {
		return nil, nil
	}

	// baseOffset is the absolute byte position of the first byte of
	// stream[].  LSN = baseOffset + stream_offset + 1 (1-based).
	baseOffset := firstSegNo * uint64(segmentSize)

	// Format auto-detection: a successful classification picks
	// the matching reader path. Classification errors (e.g.
	// segment shorter than a page header — short tests use
	// 32-byte segments) silently fall back to the legacy
	// flat-record-stream walk. The legacy path's per-record
	// CRC validates correctness; misclassification of a real
	// page-emitted segment as legacy would surface as a CRC
	// mismatch on the very first record.
	if format, derr := DetectWALFormat(walDir); derr == nil && format == WALFormatPGCompat {
		return readAllPageAware(stream, segmentSize, baseOffset)
	}

	var records []Record
	off := 0
	for off < len(stream) {
		// The EOS sentinel (zero header) inside a preallocated
		// segment's zero-fill tail terminates the record stream.
		// See docs/design/0007-0001-wal-segment-preallocation.md.
		if isZeroHeader(stream[off:]) {
			break
		}
		payload, n, err := decodeRecord(stream[off:])
		if err != nil {
			// M0088-0001: torn-tail heuristic. If the bytes AFTER
			// the corrupt record's claimed end are all zero, this
			// is a non-clean shutdown signature — the writer was
			// killed mid-record and the preallocated zero-fill
			// tail extends to EOF. Treat as EOS. Real mid-stream
			// corruption (CRC mismatch with a valid following
			// record) leaves non-zero bytes after `record_end`,
			// so it still surfaces as an error. Indistinguishable
			// edge case: the very last record in a normal stream
			// gets corrupted in-place — but PG semantics already
			// accept this trade-off (the tail is unrecoverable
			// either way).
			if afterCorruptIsZeroTail(stream, off) {
				break
			}
			return nil, fmt.Errorf("wal: decode at offset %d: %w", off, err)
		}
		start := baseOffset + uint64(off) + 1
		end := start + uint64(n) - 1
		records = append(records, Record{StartLSN: start, EndLSN: end, Payload: payload})
		off += n
	}

	return records, nil
}

// readAllPageAware walks a page-emitted WAL stream (M0014-0003):
// at every page boundary skip the page header (or stop on an
// all-zero header — preallocated tail), then decode records that
// may straddle page boundaries. Record-byte LSNs include the page
// header bytes between record fragments, mirroring upstream's
// LSN-as-byte-offset semantics.
func readAllPageAware(stream []byte, segSize int64, baseOffset uint64) ([]Record, error) {
	var records []Record
	off := 0
	for off < len(stream) {
		pos := int64(off)
		if pos%XLOGBlockSize == 0 {
			hsize := pageHeaderSizeAt(pos, segSize)
			if off+hsize > len(stream) {
				break
			}
			if isZeroBytes(stream[off : off+hsize]) {
				break
			}
			off += hsize
			continue
		}
		// Mid-page record. The first 24 bytes are the XLogRecord
		// header; if all zero, EOS.
		if off+xlogRecordHeaderSize > len(stream) {
			break
		}
		header, _ := extractRecordBytes(stream[off:], pos, segSize, xlogRecordHeaderSize)
		if len(header) < xlogRecordHeaderSize {
			break
		}
		if isZeroXLogRecordHeader(header) {
			break
		}
		h, err := DecodeXLogRecordHeader(header)
		if err != nil {
			// M0088-0001: torn-tail tolerance — see comment in
			// ReadAll. For the page-aware path the simplest safe
			// check is "everything from the corrupt header on is
			// zero" because page headers in the preallocated tail
			// are also zero (xlog_page.go writes them only when
			// emitting real data into a page).
			if isPreallocatedTail(stream[off:]) {
				break
			}
			return nil, fmt.Errorf("wal: decode at offset %d: %w", off, err)
		}
		total := int(h.TotLen)
		if total < xlogRecordHeaderSize {
			if isPreallocatedTail(stream[off:]) {
				break
			}
			return nil, fmt.Errorf("wal: decode at offset %d: bad xlog total length %d", off, total)
		}
		paddedTotal := maxAlignXLog(total)
		fullBytes, consumed := extractRecordBytes(stream[off:], pos, segSize, paddedTotal)
		if len(fullBytes) < total {
			break
		}
		payload, n, err := decodeRecordXLog(fullBytes)
		if err != nil {
			// Compute the byte offset AFTER the corrupt record
			// (accounting for page-header bytes inside the span)
			// and check that tail. If the writer was killed
			// mid-record, those post-record bytes are all zero;
			// if a real mid-stream record is corrupt, the next
			// record's bytes follow.
			tailStart := off + consumed
			if tailStart > len(stream) {
				tailStart = len(stream)
			}
			if isPreallocatedTail(stream[tailStart:]) {
				break
			}
			return nil, fmt.Errorf("wal: decode at offset %d: %w", off, err)
		}
		if n != len(fullBytes) {
			tailStart := off + consumed
			if tailStart > len(stream) {
				tailStart = len(stream)
			}
			if isPreallocatedTail(stream[tailStart:]) {
				break
			}
			return nil, fmt.Errorf("wal: decode size mismatch at offset %d: %d vs %d", off, n, len(fullBytes))
		}
		start := baseOffset + uint64(off) + 1
		end := baseOffset + uint64(off) + uint64(consumed)
		records = append(records, Record{StartLSN: start, EndLSN: end, Payload: payload})
		off += consumed
	}
	return records, nil
}

// afterCorruptIsZeroTail reports whether the bytes after the
// corrupt-record at `off` are entirely zero. The "after" point is the
// record's CLAIMED end (off + header + payloadLen as read from the
// corrupt record's own header). When the corrupt record's header is
// itself partially garbage, this still works: a small claimed length
// makes the check stricter (more zeros required), and an absurdly
// large claimed length makes the check unreachable — both safe.
// M0088-0001.
func afterCorruptIsZeroTail(stream []byte, off int) bool {
	if off+recordHeaderSize > len(stream) {
		// Corrupt header is itself torn; the rest is the tail.
		return isPreallocatedTail(stream[off:])
	}
	payloadLen := int(binary.LittleEndian.Uint32(stream[off : off+4]))
	if payloadLen < 0 {
		return false
	}
	claimedEnd := off + recordHeaderSize + payloadLen
	if claimedEnd > len(stream) {
		// Record extends past EOF — definitely torn. The tail
		// bytes that did get written may be partial-payload
		// non-zeros; the bytes past EOF aren't ours to inspect.
		// Conservative: only declare torn if everything from the
		// corrupt header start onward is zero. (Rare: most torn
		// records leave a non-zero len field.)
		return isPreallocatedTail(stream[off:])
	}
	return isPreallocatedTail(stream[claimedEnd:])
}

// isPreallocatedTail reports whether b is entirely zero — the
// preallocated zero-fill tail of a WAL segment. Scans in 64 KiB
// chunks for memory-bandwidth speed — even a 1 GiB zero tail
// completes in ~100 ms. M0088-0001.
func isPreallocatedTail(b []byte) bool {
	const chunk = 64 * 1024
	var zeros [chunk]byte
	for off := 0; off < len(b); off += chunk {
		end := off + chunk
		if end > len(b) {
			end = len(b)
		}
		seg := b[off:end]
		ref := zeros[:len(seg)]
		// bytes.Equal short-circuits on first mismatch; using []byte
		// indexed compare is just as fast and avoids an import.
		for i := range seg {
			if seg[i] != ref[i] {
				return false
			}
		}
	}
	return true
}

func readStream(walDir string, segSize int64) ([]byte, error) {
	// M0045-0001 / M0045-0003: WAL retention deletes pre-checkpoint
	// segments so segment 0 may no longer be present. Enumerate the
	// directory to find the first available segment and start from
	// there instead of always starting at 0.
	firstSegNo, err := firstAvailableSegment(walDir)
	if err != nil {
		return nil, err
	}
	if firstSegNo < 0 {
		return nil, nil // fresh cluster, no WAL
	}
	return readStreamFrom(walDir, segSize, uint64(firstSegNo))
}

// firstAvailableSegment scans walDir and returns the smallest segment
// number found, or -1 when no WAL segments exist.
func firstAvailableSegment(walDir string) (int64, error) {
	entries, err := os.ReadDir(walDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return -1, nil
		}
		return -1, fmt.Errorf("wal: list %s: %w", walDir, err)
	}
	first := int64(-1)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		segNo, ok := parseSegmentName(e.Name())
		if !ok {
			continue
		}
		if first < 0 || int64(segNo) < first {
			first = int64(segNo)
		}
	}
	return first, nil
}

// readStreamFrom concatenates all WAL segments starting at firstSegNo
// into a single byte slice. Stops on the first gap (missing segment)
// or when the last segment is shorter than segSize (lazy-grow sentinel).
func readStreamFrom(walDir string, segSize int64, firstSegNo uint64) ([]byte, error) {
	stream := make([]byte, 0)
	for segNo := firstSegNo; ; segNo++ {
		path := filepath.Join(walDir, formatSegmentName(segNo))
		b, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			return nil, fmt.Errorf("wal: read %s: %w", path, err)
		}
		if int64(len(b)) > segSize {
			return nil, fmt.Errorf("wal: segment %s too large: %d > %d", path, len(b), segSize)
		}
		stream = append(stream, b...)
		// Legacy lazy-grown last segment: shorter than segSize means
		// there is no next segment. Preallocated mode: full-size
		// segments continue until ENOENT. The EOS sentinel inside
		// the byte stream terminates record iteration in ReadAll.
		if int64(len(b)) < segSize {
			break
		}
	}
	return stream, nil
}

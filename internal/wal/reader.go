package wal

import (
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

	stream, err := readStream(walDir, segmentSize)
	if err != nil {
		return nil, err
	}
	if len(stream) == 0 {
		return nil, nil
	}

	// Format auto-detection: a successful classification picks
	// the matching reader path. Classification errors (e.g.
	// segment shorter than a page header — short tests use
	// 32-byte segments) silently fall back to the legacy
	// flat-record-stream walk. The legacy path's per-record
	// CRC validates correctness; misclassification of a real
	// page-emitted segment as legacy would surface as a CRC
	// mismatch on the very first record.
	if format, derr := DetectWALFormat(walDir); derr == nil && format == WALFormatPGCompat {
		return readAllPageAware(stream, segmentSize)
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
			// Corrupt record near the end of the stream (within
			// one segment of EOF) is likely an unclean shutdown
			// (OOM kill). Treat as EOS rather than failing
			// startup. Early-segment corruption is extremely
			// unlikely and treated as a hard error.
			if int64(len(stream)-off) <= segmentSize {
				break
			}
			return nil, fmt.Errorf("wal: decode at offset %d: %w", off, err)
		}
		start := uint64(off) + 1
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
func readAllPageAware(stream []byte, segSize int64) ([]Record, error) {
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
			if int64(len(stream)-off) <= segSize {
				break
			}
			return nil, fmt.Errorf("wal: decode at offset %d: %w", off, err)
		}
		total := int(h.TotLen)
		if total < xlogRecordHeaderSize {
			if int64(len(stream)-off) <= segSize {
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
			if int64(len(stream)-off) <= segSize {
				break
			}
			return nil, fmt.Errorf("wal: decode at offset %d: %w", off, err)
		}
		if n != len(fullBytes) {
			if int64(len(stream)-off) <= segSize {
				break
			}
			return nil, fmt.Errorf("wal: decode size mismatch at offset %d: %d vs %d", off, n, len(fullBytes))
		}
		start := uint64(off) + 1
		end := uint64(off) + uint64(consumed)
		records = append(records, Record{StartLSN: start, EndLSN: end, Payload: payload})
		off += consumed
	}
	return records, nil
}

func readStream(walDir string, segSize int64) ([]byte, error) {
	stream := make([]byte, 0)
	for segNo := uint64(0); ; segNo++ {
		path := filepath.Join(walDir, formatSegmentName(segNo))
		b, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if segNo == 0 {
					return nil, nil
				}
				break
			}
			return nil, fmt.Errorf("wal: read %s: %w", path, err)
		}
		if int64(len(b)) > segSize {
			return nil, fmt.Errorf("wal: segment %s too large: %d > %d", path, len(b), segSize)
		}
		stream = append(stream, b...)
		// Legacy lazy-grown last segment: shorter than segSize, no
		// next segment exists. Preallocated mode: every segment is
		// full-size, and we keep reading until ENOENT. The EOS
		// sentinel inside the byte stream terminates record
		// iteration in ReadAll.
		if int64(len(b)) < segSize {
			break
		}
	}
	return stream, nil
}

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
func ReadAll(walDir string, segmentSize int64) ([]Record, error) {
	if segmentSize <= 0 {
		segmentSize = DefaultSegmentSize
	}

	stream, err := readStream(walDir, segmentSize)
	if err != nil {
		return nil, err
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
			return nil, fmt.Errorf("wal: decode at offset %d: %w", off, err)
		}
		start := uint64(off) + 1
		end := start + uint64(n) - 1
		records = append(records, Record{StartLSN: start, EndLSN: end, Payload: payload})
		off += n
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

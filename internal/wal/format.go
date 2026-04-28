package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

const recordHeaderSize = 8

var ErrCorruptRecord = errors.New("wal: corrupt record")

func encodeRecord(payload []byte) []byte {
	buf := make([]byte, recordHeaderSize+len(payload))
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(buf[4:8], crc32.ChecksumIEEE(payload))
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

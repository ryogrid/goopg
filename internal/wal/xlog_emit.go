package wal

// XLOG page-aware emission and reading (M0014-0001 step 2).
//
// Step 1 (xlog_page.go) added the page-header types and helpers
// without touching the writer's append path. This step wires those
// helpers in: when Config.PageHeaders is true, the writer interleaves
// XLogPageHeader / XLogLongPageHeader at every 8 KiB page boundary,
// records crossing a page boundary set XLP_FIRST_IS_CONTRECORD on the
// next page and stamp xlp_rem_len with the bytes-remaining of the
// partial record, and the very first page of every segment carries the
// 40-byte long form. The reader side (RecordIterator + ReadAll)
// transparently skips page-header bytes when reconstructing record
// streams.
//
// LSN semantics: WAL stream byte position is preserved. A goopg LSN
// is a 1-based byte offset into the on-disk stream, including page
// header bytes. Records may be discontiguous in LSN space: a record
// that crosses a page boundary occupies LSN [start, start+x] then
// [start+x+headerSize, start+x+headerSize+y], where headerSize is 24
// (short) or 40 (long, if the boundary is also a segment boundary).
//
// Mirrors postgres/src/backend/access/transam/xlog.c::CopyXLogRecordToWAL
// for the writer side and postgres/src/backend/access/transam/xlogreader.c
// for the reader side.
//
// See docs/design/0014-0001-xlog-page-and-segment-layout-compat.md.

// pageHeaderSizeAt returns the on-disk page-header size for a stream
// byte position that sits exactly on a page boundary. Long-form (40
// bytes) at a segment boundary, short-form (24 bytes) elsewhere. The
// caller has already verified pos%XLOGBlockSize == 0.
func pageHeaderSizeAt(pos int64, segSize int64) int {
	if segSize > 0 && pos%segSize == 0 {
		return SizeOfXLogLongPHD
	}
	return SizeOfXLogShortPHD
}

// emitWithPageHeaders interleaves PG-compatible WAL page headers into
// `record` bytes starting at WAL stream byte offset `startPos`. The
// returned `out` is the on-disk byte sequence to write at startPos.
//
// `leading` is the byte count of any page header inserted at the
// front of `out` (24 or 40 bytes when startPos sits on a page
// boundary; 0 otherwise). The caller uses this to compute startLSN
// for the record (the LSN of its first record byte, after any
// leading page header).
//
// Per-page rules:
//   - The first page of every segment (pos%segSize == 0) gets a
//     40-byte long header.
//   - All other page boundaries get a 24-byte short header.
//   - When the record's bytes span a page boundary mid-emit, the
//     next page's header sets XLP_FIRST_IS_CONTRECORD and stores
//     the bytes-still-to-go of the record in xlp_rem_len.
//
// `record` is the encoded record (header + payload — encodeRecord's
// output for the v0 record framing, or future XLogRecord bytes once
// M0014-0002 wires the upstream record header in). The helper is
// agnostic to the record's internal shape.
func emitWithPageHeaders(record []byte, realRecLen int, startPos int64, segSize int64, sysID uint64, tli uint32) (out []byte, leading int) {
	out = make([]byte, 0, len(record)+SizeOfXLogShortPHD)
	pos := startPos

	// Optional leading page header at startPos (writer landed on a
	// page boundary).
	if pos%XLOGBlockSize == 0 {
		hdr := buildPageHeader(pos, segSize, sysID, tli, false, 0)
		out = append(out, hdr...)
		leading = len(hdr)
		pos += int64(len(hdr))
	}

	consumed := 0
	for consumed < len(record) {
		// Compute remaining space in the current page.
		space := XLOGBlockSize - int(pos%XLOGBlockSize)
		// pos can never sit at a page boundary inside this loop
		// — we always emit at least one record byte before
		// crossing, and the cross-boundary path inserts the
		// next page's header inline below.
		chunk := len(record) - consumed
		if chunk > space {
			chunk = space
		}
		out = append(out, record[consumed:consumed+chunk]...)
		pos += int64(chunk)
		consumed += chunk

		// If we still have record bytes and we just landed on a
		// page boundary, emit the contrecord page header for the
		// next page.
		if consumed < len(record) && pos%XLOGBlockSize == 0 {
			// xlp_rem_len upstream-semantic: bytes-still-to-go
			// of the *actual* record (xl_tot_len), not the
			// MAXALIGN trailing pad.
			remaining := uint32(0)
			if consumed < realRecLen {
				remaining = uint32(realRecLen - consumed)
			}
			hdr := buildPageHeader(pos, segSize, sysID, tli, true, remaining)
			out = append(out, hdr...)
			pos += int64(len(hdr))
		}
	}
	return out, leading
}

// buildPageHeader returns the encoded page header for the page that
// starts at `pos`. Long-form (40 bytes) at segment boundaries,
// short-form (24 bytes) elsewhere. `contRecord`/`remLen` set the
// XLP_FIRST_IS_CONTRECORD flag and xlp_rem_len fields when the page
// begins with a record continuation.
func buildPageHeader(pos int64, segSize int64, sysID uint64, tli uint32, contRecord bool, remLen uint32) []byte {
	std := XLogPageHeader{
		Magic:    XLOGPageMagic,
		TLI:      tli,
		PageAddr: uint64(pos),
		RemLen:   remLen,
	}
	if contRecord {
		std.Info |= XLPFirstIsContRecord
	}
	if segSize > 0 && pos%segSize == 0 {
		out := make([]byte, SizeOfXLogLongPHD)
		long := XLogLongPageHeader{
			Std:        std,
			SysID:      sysID,
			SegSize:    uint32(segSize),
			XLogBlcksz: XLOGBlockSize,
		}
		// EncodeXLogLongPageHeader forces XLPLongHeader into Info.
		_ = EncodeXLogLongPageHeader(out, long)
		return out
	}
	out := make([]byte, SizeOfXLogShortPHD)
	_ = EncodeXLogPageHeader(out, std)
	return out
}

// extractRecordBytes inverts emitWithPageHeaders for in-memory
// streams: walks `stream` starting at the byte offset `streamStart`
// (relative to the WAL stream's logical 0-byte origin) and copies
// out only the record bytes — page-header bytes are skipped.
//
// Used by ReadAll's page-aware path which reads whole segments into
// memory. The returned `recordBytes` is a contiguous record-stream
// slice the legacy decodeRecord can walk; record-byte LSNs are
// reconstructed via the streamStart/page-header bookkeeping. Returns
// the number of input bytes consumed.
//
// `wantBytes` caps how many record bytes to extract. When the
// caller wants the whole stream they pass len(stream) — the helper
// stops at end-of-stream or the first all-zero page header (EOS).
func extractRecordBytes(stream []byte, streamStart int64, segSize int64, wantBytes int) (recordBytes []byte, consumed int) {
	recordBytes = make([]byte, 0, len(stream))
	for consumed < len(stream) && len(recordBytes) < wantBytes {
		pos := streamStart + int64(consumed)
		if pos%XLOGBlockSize == 0 {
			hsize := pageHeaderSizeAt(pos, segSize)
			if consumed+hsize > len(stream) {
				return recordBytes, consumed
			}
			// All-zero header → EOS sentinel inside a
			// preallocated tail. Caller stops here.
			if isZeroBytes(stream[consumed : consumed+hsize]) {
				return recordBytes, consumed
			}
			consumed += hsize
			continue
		}
		// Inside a page: copy record bytes up to the next page
		// boundary or end-of-stream.
		space := XLOGBlockSize - int(pos%XLOGBlockSize)
		chunk := len(stream) - consumed
		if chunk > space {
			chunk = space
		}
		want := wantBytes - len(recordBytes)
		if chunk > want {
			chunk = want
		}
		recordBytes = append(recordBytes, stream[consumed:consumed+chunk]...)
		consumed += chunk
	}
	return recordBytes, consumed
}

// isZeroBytes reports whether every byte of b is zero. Used by the
// page-aware reader to detect the preallocated-tail EOS sentinel:
// a page header of all zeros means no record has crossed into this
// page yet, so iteration stops.
func isZeroBytes(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

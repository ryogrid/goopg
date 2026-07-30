package wal

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
)

// Record is one decoded WAL record from the stream.
type Record struct {
	StartLSN uint64
	EndLSN   uint64
	Payload  []byte
	XLog     *XLogDecodedRecord
}

// readAllUncached decodes every record found under walDir across ordered
// segments. It is the implementation behind ReadAll; the exported ReadAll
// (recovery_cache.go) adds a startup-recovery memoization layer.
//
// Auto-detects the on-disk format via DetectWALFormat: page-emitted
// segments (WALFormatPGCompat) are walked with page-header skipping
// plus XLogRecord decode; legacy bare-record streams use the
// pre-M0014 zero-record-header EOS sentinel. The caller does not
// need to know which format is in use — the same call site handles
// both during the M0014 rollout window.
func readAllUncached(walDir string, segmentSize int64) ([]Record, error) {
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

	// root-0035: a caller that does not know the cluster's
	// wal_segment_size passes 0, which used to mean DefaultSegmentSize
	// (16 MiB) — wrong for any cluster built with a different size, and
	// wrong in a way that is silent and destructive: baseOffset below is
	// firstSegNo*segmentSize, so every StartLSN/EndLSN comes out 16× too
	// large, the pd_lsn idempotency check in replay never fires, and redo
	// re-applies records the running server had already applied. Derive
	// it from the stream itself instead, exactly as upstream's stand-alone
	// WAL readers do (pg_waldump.c search_directory() takes WalSegSz from
	// the first file's longhdr->xlp_seg_size and validates it with
	// IsValidWalSegSize).
	if segmentSize <= 0 {
		segmentSize = detectSegmentSizeAt(walDir, firstSegNo)
	}
	if segmentSize <= 0 {
		segmentSize = DefaultSegmentSize
	}

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
	// A9: the legacy IEEE-CRC frame is retired — goopg WAL is always the
	// PG-compat page-headered stream, so ReadAll always walks it page-aware.
	// A legacy data dir (written by a pre-A9 PageHeaders=false writer) is
	// rejected explicitly; re-init is required (there is no in-place upgrade).
	if format, derr := DetectWALFormat(walDir); derr == nil && format == WALFormatLegacy {
		return nil, fmt.Errorf("wal: legacy WAL format in %s is unsupported (retired in A9) — re-init required", walDir)
	}
	return readAllPageAware(stream, segmentSize, baseOffset)
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

	// root-0032: the stream does not necessarily begin on a record boundary.
	// It starts at the first segment of the live run, and the record that was
	// being written when that segment began may have started in a segment that
	// retention has since removed — the page then carries XLP_FIRST_IS_CONTRECORD
	// and xlp_rem_len, and its first MAXALIGN(xlp_rem_len) bytes are the tail of
	// a record whose header is gone. Decoding those bytes as a record header
	// yields garbage ("padding bytes nonzero", "unknown rmid=N"). Upstream skips
	// them the same way — XLogReadRecord/XLogFindNextRecord advance past
	// xlp_rem_len when they pick a page up mid-record
	// (postgres/src/backend/access/transam/xlogreader.c). The truncated record
	// is not replayable and predates the retention keep point, so dropping it is
	// correct, not lossy.
	if hsize := pageHeaderSizeAt(0, segSize); len(stream) >= hsize {
		if hdr, err := DecodeXLogPageHeader(stream[:hsize]); err == nil && hdr.Info&XLPFirstIsContRecord != 0 {
			off = hsize + maxAlignXLog(int(hdr.RemLen))
			if off > len(stream) {
				return nil, nil
			}
		}
	}

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
			// M0088-0001: see ReadAll. Page-aware path uses
			// "everything from the corrupt header on is zero" OR
			// "within one segment-size of EOF" as the EOS signal.
			if isPreallocatedTail(stream[off:]) {
				break
			}
			if int64(len(stream)-off) <= segSize {
				break
			}
			endOfWAL(off, baseOffset, "invalid record header", err)
			break
		}
		total := int(h.TotLen)
		if total < xlogRecordHeaderSize {
			if isPreallocatedTail(stream[off:]) {
				break
			}
			if int64(len(stream)-off) <= segSize {
				break
			}
			endOfWAL(off, baseOffset, "bad xlog total length", fmt.Errorf("xl_tot_len=%d", total))
			break
		}
		paddedTotal := maxAlignXLog(total)
		fullBytes, consumed := extractRecordBytes(stream[off:], pos, segSize, paddedTotal)
		if len(fullBytes) < total {
			break
		}
		decoded, err := decodeRecordXLogDetailed(fullBytes)
		if err != nil {
			tailStart := off + consumed
			if tailStart > len(stream) {
				tailStart = len(stream)
			}
			if isPreallocatedTail(stream[tailStart:]) {
				break
			}
			if int64(len(stream)-off) <= segSize {
				break
			}
			endOfWAL(off, baseOffset, "record failed to decode", err)
			break
		}
		if decoded.Consumed != len(fullBytes) {
			tailStart := off + consumed
			if tailStart > len(stream) {
				tailStart = len(stream)
			}
			if isPreallocatedTail(stream[tailStart:]) {
				break
			}
			if int64(len(stream)-off) <= segSize {
				break
			}
			endOfWAL(off, baseOffset, "record size mismatch",
				fmt.Errorf("decoded %d of %d bytes", decoded.Consumed, len(fullBytes)))
			break
		}
		start := baseOffset + uint64(off) + 1
		end := baseOffset + uint64(off) + uint64(consumed)
		records = append(records, Record{StartLSN: start, EndLSN: end, Payload: decoded.Payload, XLog: decoded.XLog})
		off += consumed
	}
	return records, nil
}

// (afterCorruptIsZeroTail was removed in A9 with the legacy ReadAll walk; the
// PG-frame page-aware walk uses isPreallocatedTail directly.)

// endOfWAL logs where the record walk stopped and why. root-0032: reaching a
// record that fails validation ENDS recovery, it is not a fatal error — the
// same rule upstream applies in report_invalid_record/ReadRecord, which log the
// reason and finish redo at the last valid record
// (postgres/src/backend/access/transam/xlogrecovery.c). goopg used to return
// the decode error from ReadAll, which initdb.Open turned into
// "goopg: wal replay: ..." and a refused start — a permanently unstartable
// cluster.
//
// The tail past a crash is routinely unreadable: WAL bytes are written by
// concurrent appenders, so a SIGKILL can leave the first 8 bytes of a record
// header on disk at the end of one segment while the page header that would
// have flagged the continuation was never written. Reproduced exactly that way
// (segment 0x09 ending in `5a00 0800 …`, segment 0x0A's long page header
// carrying info=XLP_LONG_HEADER with xlp_rem_len=0 — no contrecord flag), which
// surfaced as "invalid record header: padding bytes nonzero"/"unknown rmid=N"
// depending on which stale bytes followed.
//
// Everything before the stop point is replayed; a record whose bytes are not
// intact was never durable, so nothing replayable is dropped.
func endOfWAL(off int, baseOffset uint64, reason string, cause error) {
	slog.Warn("wal: end of WAL reached during replay",
		"reason", reason,
		"detail", cause,
		"lsn", baseOffset+uint64(off)+1,
		"stream_offset", off)
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

// firstAvailableSegment scans walDir and returns the first segment number of
// the LIVE segment run — the longest contiguous run of segment files that ends
// at the highest-numbered one — or -1 when no WAL segments exist.
//
// root-0032: this used to return the globally smallest segment number, which
// made any hole in pg_wal fatal (readStreamFrom stops at the hole, so replay
// saw only the orphaned prefix, and detectWritePos raised "gap at segment N"
// and the server refused to start). Holes are a normal post-crash state:
// removeOldSegments walks the obsolete segments newest-first (recycling the
// newest into future slots before unlinking the rest), so a SIGKILL in the
// middle of a checkpoint's retention pass leaves the OLDEST obsolete segments
// behind with a hole above them — reproduced as {1,2} surviving while 3..0x0B
// were gone and the live stream ran 0x0C..0x30.
//
// Skipping the orphaned prefix is safe by construction: retention only ever
// removes segments below the checkpoint keep point, so the existence of a hole
// proves every segment below it was already checkpointed and is not needed for
// recovery. The leftovers are unlinked by the next checkpoint's retention pass
// (removeOldSegments re-lists the directory and removes everything < keepSeg).
// Upstream never has to make this choice because StartupXLOG begins replay at
// the checkpoint's REDO location rather than at the first retained segment
// (postgres/src/backend/access/transam/xlog.c); goopg replays the whole
// retained stream, so it has to select the run explicitly.
func firstAvailableSegment(walDir string) (int64, error) {
	entries, err := os.ReadDir(walDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return -1, nil
		}
		return -1, fmt.Errorf("wal: list %s: %w", walDir, err)
	}
	segNos := make([]uint64, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		segNo, ok := parseSegmentName(e.Name())
		if !ok {
			continue
		}
		segNos = append(segNos, segNo)
	}
	if len(segNos) == 0 {
		return -1, nil
	}
	sort.Slice(segNos, func(i, j int) bool { return segNos[i] < segNos[j] })
	return int64(liveSegmentRunStart(segNos)), nil
}

// IsValidWalSegSize mirrors upstream's IsValidWalSegSize
// (postgres/src/include/access/xlog_internal.h): a power of two between
// 1 MB and 1 GB. Used to sanity-check a wal_segment_size read out of an
// on-disk long page header before trusting it.
func IsValidWalSegSize(size int64) bool {
	return size >= 1<<20 && size <= 1<<30 && size&(size-1) == 0
}

// detectSegmentSizeAt returns the cluster's wal_segment_size as recorded in
// the long page header at the head of segment segNo, or 0 when it cannot be
// determined (unreadable file, short read, non-long header, or a value that
// fails IsValidWalSegSize).
//
// Every segment file begins on a segment boundary, so buildPageHeader always
// emits the LONG form there with xlp_seg_size set to the writer's configured
// segment size — the same cross-check field upstream validates in
// XLogReaderValidatePageHeader and the same one pg_waldump's search_directory()
// uses to discover WalSegSz before it has any cluster context. Reading it back
// is therefore the authoritative answer for a caller that only has a directory
// path, and it costs one 40-byte read.
func detectSegmentSizeAt(walDir string, segNo uint64) int64 {
	f, err := os.Open(filepath.Join(walDir, formatSegmentName(segNo)))
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, SizeOfXLogLongPHD)
	if n, rerr := io.ReadFull(f, buf); rerr != nil || n != SizeOfXLogLongPHD {
		return 0
	}
	long, err := DecodeXLogLongPageHeader(buf)
	if err != nil {
		return 0
	}
	size := int64(long.SegSize)
	if !IsValidWalSegSize(size) {
		return 0
	}
	return size
}

// liveSegmentRunStart returns the first segment number of the longest
// contiguous run ending at the highest entry of segNos. segNos must be sorted
// ascending and non-empty. With no holes it returns segNos[0], so the common
// case is unchanged. See firstAvailableSegment for why the run — and not the
// smallest segment — defines the stream goopg replays.
func liveSegmentRunStart(segNos []uint64) uint64 {
	start := segNos[len(segNos)-1]
	for i := len(segNos) - 1; i > 0; i-- {
		if segNos[i-1]+1 != segNos[i] {
			break
		}
		start = segNos[i-1]
	}
	return start
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

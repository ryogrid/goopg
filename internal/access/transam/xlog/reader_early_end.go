package xlog

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

// ErrEarlyEndOfWAL reports that the replay walk stopped at a point that is NOT
// the end of the durable stream: past the stop point the WAL still holds at
// least one page that claims its own address AND a record whose own CRC checks
// out. Replaying only up to the stop point would therefore silently discard
// durable — possibly committed — work, and the first post-recovery append would
// overwrite it, making the loss permanent.
//
// M0131-S30.2. goopg used to treat every walk stop as a clean end-of-WAL: it
// logged a WARN (endOfWAL, reader.go) and reported a successful start. Both
// row-loss defects of the S30 series (S30.1, S30.1b) presented exactly that
// way — a WARN in the startup log, an apparently healthy cluster, and thousands
// of committed rows gone. Upstream cannot hit this case, because a record is
// durable only if every byte before it was flushed first, so "valid WAL after
// invalid WAL" is not a state PostgreSQL's insert/flush protocol can produce
// (ReserveXLogInsertLocation + XLogFlush,
// postgres/src/backend/access/transam/xlog.c). In goopg it means the WRITER
// left a hole, which is a defect that must be loud, not a WARN.
var ErrEarlyEndOfWAL = errors.New("wal: early end of WAL")

// allowEarlyEndOfWALEnv is the operator escape hatch. Refusing to start is the
// right default (a truncated replay is silent data loss), but it must not brick
// a cluster whose only remaining option is "start and accept the loss" — the
// role pg_resetwal plays upstream. Setting GOOPG_WAL_ALLOW_EARLY_END to a true
// boolean value downgrades the refusal back to a loud WARN.
const allowEarlyEndOfWALEnv = "GOOPG_WAL_ALLOW_EARLY_END"

func allowEarlyEndOfWAL() bool {
	v := os.Getenv(allowEarlyEndOfWALEnv)
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}

// earlyEndOfWAL classifies a walk stop at stream offset stopOff. It returns a
// non-nil ErrEarlyEndOfWAL when durable WAL exists past the stop point, and nil
// when the stop is a genuine end of stream (the normal crash tail) or when the
// operator has set GOOPG_WAL_ALLOW_EARLY_END.
func earlyEndOfWAL(stream []byte, stopOff int, segSize int64, baseOffset uint64, reason string, cause error) error {
	lsn, ok := durableWALAfter(stream, stopOff, segSize, baseOffset)
	if !ok {
		return nil
	}
	err := fmt.Errorf("%w: replay stopped at lsn %d (%s: %v) but durable WAL continues at lsn %d — "+
		"replaying only to the stop point would discard committed records; "+
		"set %s=1 to start anyway and accept the loss",
		ErrEarlyEndOfWAL, baseOffset+uint64(stopOff)+1, reason, cause, lsn, allowEarlyEndOfWALEnv)
	if allowEarlyEndOfWAL() {
		slog.Warn("wal: early end of WAL overridden by operator — committed records are being discarded",
			"detail", err, "env", allowEarlyEndOfWALEnv)
		return nil
	}
	return err
}

// durableWALAfter reports the LSN of the first durable record found after
// stream offset stopOff, scanning page by page.
//
// A page counts as durable evidence only when BOTH halves hold:
//
//   - its header validates against the address it is stored at
//     (xlogPageValidator.check — magic, long/short header placement, seg size
//     and, decisively, xlp_pageaddr), which rejects the stale contents of a
//     recycled segment; and
//   - the first record position on that page carries a record whose own
//     xl_crc checks out over the bytes extractRecordBytes assembles
//     (recordStartsAt).
//
// Both together are the same evidence durableUnknownRecord uses to tell a real
// record from crash garbage, and they are what makes a false positive cost a
// 2^-32 CRC collision on top of a self-consistent page header. A false NEGATIVE
// is the status quo ante: a silent, permanent loss of committed work.
//
// The scan starts at the first page boundary at or after stopOff — bytes inside
// the stopping page are exactly the torn tail the walk already rejected. Zeroed
// pages (preallocated or recycled-and-zero-filled) are skipped rather than
// treated as the end, because a hole is precisely what this is looking for.
func durableWALAfter(stream []byte, stopOff int, segSize int64, baseOffset uint64) (uint64, bool) {
	// Sub-page "segments" appear only in unit fixtures; there is no page
	// framing to walk, so there is no second opinion to offer.
	if segSize < XLOGBlockSize {
		return 0, false
	}
	if stopOff < 0 {
		stopOff = 0
	}
	p := (int64(stopOff) + XLOGBlockSize - 1) / XLOGBlockSize * XLOGBlockSize
	for ; p < int64(len(stream)); p += XLOGBlockSize {
		hsize := pageHeaderSizeAt(p, segSize)
		if int(p)+hsize > len(stream) {
			return 0, false
		}
		buf := stream[p : int(p)+hsize]
		if isZeroBytes(buf) {
			continue
		}
		var v xlogPageValidator
		v.segSize = segSize
		if err := v.check(buf, baseOffset+uint64(p)); err != nil {
			continue
		}
		hdr, err := DecodeXLogPageHeader(buf)
		if err != nil {
			continue
		}
		recOff := int(p) + hsize
		if hdr.Info&XLPFirstIsContRecord != 0 {
			// The page opens with the tail of a record whose header lives on an
			// earlier page; the first record that STARTS here follows it.
			recOff += maxAlignXLog(int(hdr.RemLen))
		}
		// A continuation that fills the whole page leaves no record start here.
		if recOff >= len(stream) || int64(recOff) >= p+XLOGBlockSize {
			continue
		}
		if recordStartsAt(stream, recOff, int64(recOff), segSize) {
			return baseOffset + uint64(recOff) + 1, true
		}
	}
	return 0, false
}

// errPreallocatedPage is the "cause" reported when the walk ends on an all-zero
// page header. That is the ordinary end of a preallocated segment, not a fault
// — it only becomes one when durable pages follow it (see durableWALAfter).
var errPreallocatedPage = errors.New("all-zero page header (preallocated tail)")

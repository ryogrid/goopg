package xlog

// predictEmittedSize returns the byte counts that emitWithPageHeaders
// would produce if asked to lay out a record of length recordLen at
// startPos in a segment of size segSize. The function does NO I/O and
// NO allocation: it is a pure mirror of emitWithPageHeaders' byte
// arithmetic, suitable for sizing the LSN reservation BEFORE the
// record bytes have been encoded.
//
// This is foundation 17 for the M0107-0007 slice B call-site rewrite.
// PG-compat appends face a chicken-and-egg between the LSN reservation
// (which must be sized in advance — `stripeAppendBuild` only encodes
// the bytes after the reservation grants a stable prev LSN) and the
// page-header insertion (which adds bytes that depend on the
// reservation's start position). predictEmittedSize closes the loop:
// the call-site rewrite will (a) compute the unwrapped record length
// via `encodeRecordXLog`'s known-size pre-amble, (b) snapshot
// `posTracker.curr` under posMu, (c) call predictEmittedSize to learn
// the exact emitted size at that startPos, (d) reserve that size via
// `core.AppendBuilt`, (e) inside the build closure, encode with the
// returned prev and emit the bytes via emitWithPageHeaders at the
// known startPos. Because steps (b)–(d) all happen under posMu (the
// inner posTracker mutex), no peer reservation can land between the
// prediction and the reserve, so the prediction is exact.
//
// Returned values:
//   - total is the count of bytes that would be appended to the
//     output buffer — equal to leading + recordLen + sum of inserted
//     contrecord page headers.
//   - leading is the size of the optional leading page header that
//     emitWithPageHeaders emits when startPos sits on a page
//     boundary; 0 when startPos is mid-page.
//
// Behavior is identical to emitWithPageHeaders for valid inputs;
// invalid inputs (recordLen <= 0, segSize <= 0, startPos < 0) return
// (0, 0) — emitWithPageHeaders also produces nothing useful in those
// cases (it would loop zero times or panic on a modulo-by-zero). The
// pure-prediction nature of this function makes it nil-free; no
// receiver to guard.
//
// pageHeaderSizeAt is the shared sizing helper defined alongside
// emitWithPageHeaders / buildPageHeader; we reuse it so any future
// header-layout change applies everywhere atomically.
func predictEmittedSize(recordLen int, startPos int64, segSize int64) (total int, leading int) {
	if recordLen <= 0 || segSize <= 0 || startPos < 0 {
		return 0, 0
	}
	pos := startPos
	if pos%XLOGBlockSize == 0 {
		hdr := pageHeaderSizeAt(pos, segSize)
		leading = hdr
		total += hdr
		pos += int64(hdr)
	}
	consumed := 0
	for consumed < recordLen {
		space := XLOGBlockSize - int(pos%XLOGBlockSize)
		chunk := recordLen - consumed
		if chunk > space {
			chunk = space
		}
		total += chunk
		pos += int64(chunk)
		consumed += chunk
		if consumed < recordLen && pos%XLOGBlockSize == 0 {
			hdr := pageHeaderSizeAt(pos, segSize)
			total += hdr
			pos += int64(hdr)
		}
	}
	return total, leading
}

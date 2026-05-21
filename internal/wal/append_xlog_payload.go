package wal

// appendXLogPayload is the top-level PG-compat WAL append composer
// for slice B. Closes the foundation chain by combining
// [[0107-0007ad]] predictXLogRecordLen (pre-reservation size
// prediction) with [[0107-0007ab]] AppendBuiltEmitted (reserve →
// build → ring write under one stripe lock) plus the canonical
// encode + page-header emit primitives that today's
// `state.append` PG-compat path uses:
//
//	realRecLen, paddedLen := predictXLogRecordLen(payload)
//	core.AppendBuiltEmitted(procNum, paddedLen,
//	    func(start, prev uint64, total, leading int) ([]byte, error) {
//	        record, _, err := encodeRecordXLog(payload, prev)
//	        if err != nil { return nil, err }
//	        out, _ := emitWithPageHeaders(record, realRecLen,
//	            int64(start), segSize, sysID, tli)
//	        return out, nil
//	    })
//
// Foundation 22 for M0107-0007 slice B. The twenty-one earlier
// foundations ([[0107-0007i]] through [[0107-0007ad]], minus the
// dead-code-removed [[0107-0007h]]) landed the individual primitives
// in isolation; this foundation packages them into the single
// entry-point the slice B call-site rewrite mounts at
// `state.append` / `state.tryAppend` / `state.appendBatch`'s
// PG-compat write path. The call-site rewrite is multi-loop scope
// (splitting `state.appendMu`'s four invariants — writePos / walBuf /
// memRing / writeLSN — into per-stripe local state vs. shared
// state) and is NOT in this loop.
//
// PG-compat contract. The composer is byte-identical to today's
// `state.append` PG-compat path:
//
//   - encodeRecordXLog produces a MAXALIGN-padded XLogRecord with
//     the post-reservation `prev` stamped into xl_prev; the xl_crc
//     field covers `(wrapped || header[:20])`.
//   - emitWithPageHeaders stamps standard / long page headers at
//     `start`-relative page boundaries with the cluster's sysID +
//     tli, identical to `wrapXLogMainData` + emitWithPageHeaders'
//     output today.
//   - Cross-segment crossings emit an XLOG_NOOP pad record (built by
//     [[0107-0007j]] buildSegmentPadRecord, dropped under posMu by
//     [[0107-0007s]] emitSegmentPad in the onCrossSegment hook of
//     [[0107-0007k]] insertPosTracker) at the gap, and the triggering
//     reservation lands at the boundary with a long PHD — identical
//     to today's `state.append` behaviour modulo concurrency.
//
// The contract pins that the predicted `paddedLen` (passed to
// AppendBuiltEmitted as `recordLen`) equals `len(encodeRecordXLog
// (payload, prev))` for ANY value of `prev` — `encodeRecordXLog`'s
// output length is `maxAlignXLog(xlogRecordHeaderSize + wrappedLen)`,
// independent of `prev`'s value, which is why predict-then-reserve
// is sound. The matching `len(out) == total` assertion inside
// stripeAppendBuiltEmitted catches any silent drift between the
// predict path and the encode path.
//
// Nil-safety. Nil receiver returns errStripeWriterCoreNil (matches
// AppendBuiltEmitted's contract); nil payload returns
// errStripeAppendEmptyRecord (predictXLogRecordLen returns (0, 0)
// for nil input, which AppendBuiltEmitted then rejects as
// recordLen<=0). Empty-but-non-nil payloads (`[]byte{}`) are
// legitimate WAL records (e.g., body-less framework records) and
// proceed normally with paddedLen = maxAlignXLog(xlogRecordHeaderSize
// + 2).
//
// Lock-ordering tier — inherits AppendBuiltEmitted's chain
// ([[0107-0007ab]]) verbatim; no additional locks taken:
//
//	core.AppendXLogPayload(procNum, payload, segSize, sysID, tli)
//	  → core.AppendBuiltEmitted(procNum, paddedLen, build)
//	    → appendLockSet.lockByProcNum               (one of 8 stripes)
//	      → insertPosTracker.reserveEmittedAndPublish (posMu held)
//	          → (rare) onCrossSegment(start, boundary, gapPrev)
//	              → emitSegmentPad → buildSegmentPadRecord +
//	                walBuffer.writeReserved + MemRing.WriteReserved
//	          → insertionTracker.setInsertingAt(stripe, start)
//	        (posMu released)
//	      → build closure runs:
//	          encodeRecordXLog(payload, prev)
//	          emitWithPageHeaders(record, realRecLen, start, segSize,
//	                              sysID, tli)
//	      → walBuffer.writeReserved
//	      → MemRing.WriteReserved
//	      → insertionTracker.setInsertingAt(stripe, lsnIdle)
//	    → drop stripe lock
//
// Dead code until the slice B call-site rewrite mounts
// `core.AppendXLogPayload` at the PG-compat write entry points;
// foundation-first pattern matches slice C ([[0107-0007b]] /
// [[0107-0007c]] / [[0107-0007d]] before [[0107-0007e]] /
// [[0107-0007f]] / [[0107-0007g]]) and the twenty-one earlier
// slice B foundations.
func appendXLogPayload(c *stripeWriterCore, procNum int32, payload []byte, segSize int64, sysID uint64, tli uint32) (start, prev uint64, total, leading int, err error) {
	if c == nil {
		return 0, 0, 0, 0, errStripeWriterCoreNil
	}
	_, paddedLen := predictXLogRecordLen(payload)
	return c.AppendBuiltEmitted(procNum, paddedLen, func(start, prev uint64, total, leading int) ([]byte, error) {
		record, realRecLen, eerr := encodeRecordXLog(payload, prev)
		if eerr != nil {
			return nil, eerr
		}
		out, _ := emitWithPageHeaders(record, realRecLen, int64(start), segSize, sysID, tli)
		return out, nil
	})
}

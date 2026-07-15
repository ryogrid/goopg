package wal

// predictXLogRecordLen returns the byte counts that encodeRecordXLog would
// produce for the given payload WITHOUT allocating or encoding anything.
//
// Returns:
//   - realRecLen: the un-padded record length stamped into the WAL record
//     header (XLogRecord.TotLen). This is what emitWithPageHeaders needs for
//     its `realRecLen` argument (used to compute xlp_rem_len on contrecord
//     page-header insertions).
//   - paddedLen: the MAXALIGN-rounded byte count that encodeRecordXLog
//     actually emits (== len(out)). This is what the slice B call-site
//     rewrite needs as the `recordLen` argument to
//     stripeWriterCore.AppendBuiltEmitted, which feeds it to
//     reserveEmittedAndPublish under posMu.
//
// Pure mirror of wrapXLogMainData + encodeRecordXLog's byte arithmetic so
// the caller can decide the reservation size before encoding. Mirrors the
// foundation-first pattern used throughout slice B: [[0107-0007z]]
// predictEmittedSize mirrors emitWithPageHeaders' byte arithmetic;
// predictXLogRecordLen mirrors encodeRecordXLog's byte arithmetic.
//
// Pairing for the slice B call-site rewrite:
//
//	realRecLen, paddedLen := predictXLogRecordLen(payload)
//	_, _, _, _, err := core.AppendBuiltEmitted(procNum, paddedLen,
//	    func(start, prev uint64, total, leading int) ([]byte, error) {
//	        record, _, err := encodeRecordXLog(payload, prev)
//	        if err != nil {
//	            return nil, err
//	        }
//	        out, _ := emitWithPageHeaders(record, realRecLen,
//	            int64(start), segSize, sysID, tli)
//	        return out, nil
//	    })
//
// Without this helper the call-site rewrite would need to call
// encodeRecordXLog twice (once to learn the size for the reservation, once
// inside the build closure with the assigned prev) or stash the encoded
// record across the reservation boundary — both costly on the hot path.
//
// Invalid input (nil payload) returns (0, 0). encodeRecordXLog itself
// produces a 32-byte header + 2-byte short chunk for an empty payload but
// the slice B caller has no useful "empty WAL insert" semantics; the
// degenerate case is documented for completeness, not to be relied upon.
func predictXLogRecordLen(payload []byte) (realRecLen, paddedLen int) {
	if payload == nil {
		return 0, 0
	}

	// Mirror of wrapXLogMainData: returns the byte count of the wrapped
	// main-data section. The short/long block-ID branches match PG's
	// xlrBlockIDDataShort / xlrBlockIDDataLong wrapping.
	var wrappedLen int
	switch {
	case len(payload) <= 0xFF:
		wrappedLen = 2 + len(payload)
	default:
		wrappedLen = 5 + len(payload)
	}

	realRecLen = xlogRecordHeaderSize + wrappedLen
	paddedLen = maxAlignXLog(realRecLen)
	return realRecLen, paddedLen
}

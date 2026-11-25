package xlog

import "fmt"

// SwitchSegment is RequestXLogSwitch (xlog.c): it inserts an XLOG_SWITCH
// record and consumes the rest of the WAL segment, so the next record starts
// at the next segment's first page. The remainder of the segment — including
// the page headers it would have carried — is zero-filled, exactly as
// CopyXLogRecordToWAL fills it, and the switch is flushed (XLogInsertRecord's
// WALINSERT_SPECIAL_SWITCH branch).
//
// It returns pg_switch_wal's value: the end of the switch record
// (StartPos + SizeOfXLogRecord, plus the page header the record straddled,
// if any), or — when nothing has been written into the current segment —
// the segment boundary, writing nothing (ReserveXLogSwitch's early return).
// M0149-0076.
func (w *Writer) SwitchSegment() (uint64, error) {
	st := w.stateRef
	if st == nil {
		return 0, ErrClosed
	}
	select {
	case <-w.done:
		return 0, ErrClosed
	default:
	}
	ret, flushTo, inserted, err := st.appendSwitch()
	if err != nil {
		return 0, err
	}
	if !inserted {
		return ret, nil
	}
	w.walRecords.Add(1)
	w.walBytes.Add(int64(SizeOfXLogRecord))
	if st.onAppend != nil {
		st.onAppend()
	}
	if err := w.FlushUpTo(flushTo); err != nil {
		return 0, err
	}
	return ret, nil
}

// appendSwitch writes the XLOG_SWITCH record and the zero fill to the
// segment boundary in one stream write, then moves the insert position to
// the boundary with xl_prev pointing at the switch record. It takes the same
// exclusive section as appendPGCompat's Path A.
func (s *state) appendSwitch() (ret, end uint64, inserted bool, err error) {
	s.writeMu.lock()
	defer s.writeMu.release()
	s.appendMu.Lock()
	defer s.appendMu.Unlock()
	if s.walBuf != nil && s.walBuf.resident() > 0 {
		curr, _ := s.core.Load()
		s.core.PublishUpTo(int64(curr))
		if err := s.drainBufferBytes(s.walBuf.resident(), drainReasonOverflow); err != nil {
			return 0, 0, false, err
		}
	}
	segSize := s.cfg.SegmentSize
	if segSize <= 0 {
		return 0, 0, false, fmt.Errorf("wal: switch: no segment size")
	}
	trackerCurr, trackerPrev := s.core.Load()
	writePos := int64(trackerCurr)
	if writePos%segSize == 0 {
		// Nothing inserted into this segment yet.
		return uint64(writePos), 0, false, nil
	}
	payload := framePGAssembled(RmgrXLog, xlogXLogSwitch, 0, nil)
	record, realRecLen, err := encodeRecordXLog(payload, trackerPrev)
	if err != nil {
		return 0, 0, false, err
	}
	stream, leading := emitWithPageHeaders(record, realRecLen, writePos, segSize, s.sysID, s.tli.Load())
	recStart := uint64(writePos) + uint64(leading) // PG LSN of the record
	recEnd := uint64(writePos) + uint64(len(stream))
	boundary := (int64(recEnd) + segSize - 1) / segSize * segSize
	out := stream
	if gap := boundary - int64(recEnd); gap > 0 {
		out = make([]byte, len(stream)+int(gap))
		copy(out, stream)
	}
	if err := s.writeAt(writePos, out); err != nil {
		return 0, 0, false, err
	}
	s.writePos = boundary
	if s.walBuf != nil {
		s.walBuf.reset(boundary)
	}
	s.memRing.Append(writePos, out)
	s.drainedLSN = uint64(boundary)
	s.writeLSN = uint64(boundary)
	if s.writeLSNMirror != nil {
		s.writeLSNMirror.Store(uint64(boundary))
	}
	// The next record's xl_prev is the switch record (ReserveXLogSwitch sets
	// PrevBytePos to its start); the tracker keeps the goopg 1-based start
	// minus one, i.e. the PG LSN.
	s.core.resetPosition(uint64(boundary), recStart)
	ret = recStart + uint64(SizeOfXLogRecord)
	if recStart/XLOGBlockSize != ret/XLOGBlockSize {
		if ret%uint64(segSize) == ret%XLOGBlockSize {
			ret += uint64(pageHeaderSizeAt(0, segSize))
		} else {
			ret += uint64(pageHeaderSizeAt(XLOGBlockSize, segSize))
		}
	}
	return ret, uint64(boundary), true, nil
}

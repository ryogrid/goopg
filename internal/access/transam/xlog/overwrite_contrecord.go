package xlog

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Missing continuation records at the end of crash recovery (M0149-0017a).
//
// When the last record of the WAL starts on one page and its continuation
// never reached disk — the next page is absent (its segment is gone) or is
// not that record's continuation — xlogreader.c's assembled-record error
// exit records abortedRecPtr (the record's start) and missingContrecPtr (the
// page where the continuation should have begun). Crash recovery then makes
// EndOfLog = missingContrecPtr and writes XLOG_OVERWRITE_CONTRECORD as the
// first record of that page, whose header carries
// XLP_FIRST_IS_OVERWRITE_CONTRECORD (xlog.c StartupXLOG,
// CreateOverwriteContrecordRecord). A downstream reader that has the
// partial record meets the flag where it expected the continuation, drops
// the partial record and resumes at that page (026_overwrite_contrecord.pl).
//
// Archive recovery never does this (xlogrecovery.c: the new timeline only
// copies WAL up to the last complete record).

// MissingContrecord describes a record whose continuation is missing at
// the end of the WAL.
type MissingContrecord struct {
	AbortedRecPtr     uint64 // start of the incomplete record
	MissingContrecPtr uint64 // page where its continuation should start
	PrevRecPtr        uint64 // start of the last complete record (xl_prev of what follows)
	TLI               uint32 // timeline of the page holding the record
	SysID             uint64 // xlp_sysid of the segment holding the record
}

// FindMissingContrecord walks the WAL from the segment holding redoLSN and
// reports a record that starts there but whose continuation is missing, as
// xlogreader.c's assembled-record error does. ok is false for an ordinary
// end of WAL (a zero or invalid record header, or a record that fits on its
// page).
func FindMissingContrecord(walDir string, segSize int64, redoLSN uint64) (MissingContrecord, bool, error) {
	var mc MissingContrecord
	if segSize <= 0 {
		segSize = DefaultSegmentSize
	}
	firstSeg := redoLSN / uint64(segSize)
	stream, err := readStreamFrom(walDir, segSize, firstSeg)
	if err != nil || len(stream) == 0 {
		return mc, false, err
	}
	base := firstSeg * uint64(segSize)
	recs, _ := readAllPageAware(stream, segSize, base)
	if len(recs) == 0 {
		return mc, false, nil
	}
	last := recs[len(recs)-1]
	next := last.EndLSN
	if last.XLog != nil && isXLogSwitchHeader(last.XLog.Header) {
		return mc, false, nil // the rest of the segment is the switch's
	}
	if next < base || next%XLOGBlockSize == 0 {
		// A record starting at a page boundary has its header on the next
		// page: a missing page there is an ordinary end of WAL.
		return mc, false, nil
	}
	off := next - base
	pageStart := next - next%XLOGBlockSize
	if off+4 > uint64(len(stream)) {
		return mc, false, nil
	}
	totLen := uint64(binary.LittleEndian.Uint32(stream[off : off+4]))
	avail := pageStart + XLOGBlockSize - next
	if totLen < uint64(xlogRecordHeaderSize) || totLen <= avail || avail < uint64(xlogRecordHeaderSize) {
		return mc, false, nil
	}
	tli := uint32(1)
	if so := pageStart - (pageStart % uint64(segSize)) - base; so+8 <= uint64(len(stream)) {
		if t := binary.LittleEndian.Uint32(stream[so+4 : so+8]); t != 0 {
			tli = t
		}
	}
	var sysID uint64
	if so := pageStart - (pageStart % uint64(segSize)) - base; so+SizeOfXLogLongPHD <= uint64(len(stream)) {
		if long, lerr := DecodeXLogLongPageHeader(stream[so : so+SizeOfXLogLongPHD]); lerr == nil {
			sysID = long.SysID
		}
	}
	remaining := totLen - avail
	pv := xlogPageValidator{segSize: segSize}
	for page := pageStart + XLOGBlockSize; ; page += XLOGBlockSize {
		po := page - base
		hsize := uint64(pageHeaderSizeAt(int64(page), segSize))
		missing := po+hsize > uint64(len(stream)) || isZeroBytes(stream[po:po+hsize]) ||
			pv.check(stream[po:po+hsize], page) != nil
		if !missing {
			hdr, derr := DecodeXLogPageHeader(stream[po : po+hsize])
			missing = derr != nil || hdr.Info&XLPFirstIsContRecord == 0 || uint64(hdr.RemLen) != remaining
		}
		if missing {
			mc = MissingContrecord{AbortedRecPtr: next, MissingContrecPtr: page, PrevRecPtr: last.StartLSN - 1, TLI: tli, SysID: sysID}
			return mc, true, nil
		}
		chunk := uint64(XLOGBlockSize) - hsize
		if remaining <= chunk {
			return mc, false, nil // complete record; it failed for another reason
		}
		remaining -= chunk
	}
}

// overwriteContrecordInfo is XLOG_OVERWRITE_CONTRECORD.
const overwriteContrecordInfo = xlogXLogOverwriteContrecord

// WriteOverwriteContrecord is CreateOverwriteContrecordRecord for goopg's
// startup: it writes, at mc.MissingContrecPtr, the page header flagged
// XLP_FIRST_IS_OVERWRITE_CONTRECORD followed by XLOG_OVERWRITE_CONTRECORD
// (xl_overwrite_contrecord{overwritten_lsn, overwrite_time}) with xl_prev
// pointing at the last complete record, and makes it durable before the
// WAL writer opens. The rest of that segment is zeroed so the writer's
// resume scan stops right after the record. It returns the record's end.
func WriteOverwriteContrecord(walDir string, segSize int64, mc MissingContrecord, overwriteTime int64) (uint64, error) {
	if segSize <= 0 {
		segSize = DefaultSegmentSize
	}
	page := mc.MissingContrecPtr
	if page%XLOGBlockSize != 0 {
		return 0, fmt.Errorf("wal: invalid position for missing continuation record %X/%X", uint32(page>>32), uint32(page))
	}
	hdr := buildPageHeader(int64(page), segSize, mc.SysID, mc.TLI, false, 0)
	binary.LittleEndian.PutUint16(hdr[2:4], binary.LittleEndian.Uint16(hdr[2:4])|XLPFirstIsOverwriteContRecord)

	mainData := make([]byte, 16)
	binary.LittleEndian.PutUint64(mainData[0:8], mc.AbortedRecPtr)
	binary.LittleEndian.PutUint64(mainData[8:16], uint64(overwriteTime))
	body, err := assembleXLogRecordInto(nil, mainData, nil)
	if err != nil {
		return 0, err
	}
	rec, realLen, err := encodeAssembledXLog(body, RmgrXLog, overwriteContrecordInfo, 0, mc.PrevRecPtr)
	if err != nil {
		return 0, err
	}

	segNo := page / uint64(segSize)
	segOff := int64(page % uint64(segSize))
	path := filepath.Join(walDir, FormatSegmentNameTLI(segNo, mc.TLI))
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0, fmt.Errorf("wal: open %s: %w", path, err)
	}
	defer f.Close()
	buf := make([]byte, segSize-segOff)
	copy(buf, hdr)
	copy(buf[len(hdr):], rec)
	if _, err := f.WriteAt(buf, segOff); err != nil {
		return 0, fmt.Errorf("wal: write overwrite contrecord to %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("wal: fsync %s: %w", path, err)
	}
	if d, err := os.Open(walDir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return page + uint64(len(hdr)) + uint64(realLen), nil
}

// RemoveSegmentsAfterGap ends crash recovery's WAL at the first segment
// missing after the redo point. Recovery reads forward from redo
// (xlogrecovery.c ReadRecord) and stops where the next segment cannot be
// opened. Whatever lies beyond that hole is not part of this WAL: PG
// recycles those files and the new WAL written from the hole overwrites
// them. goopg's reader and WAL writer instead locate the stream by the
// segment files present (liveSegmentRunStart keeps the LAST contiguous
// run), so a stale segment past the hole made crash recovery skip the
// redo segment entirely — replaying nothing — and resume writing in the
// stale file (026_overwrite_contrecord, M0149-0017a). Removing them restores
// PG's end of WAL. It returns the removed file names. Only for crash
// recovery: under archive recovery or as a standby a missing segment is
// fetched, not the end of WAL.
func RemoveSegmentsAfterGap(walDir string, segSize int64, redoLSN uint64) ([]string, error) {
	if segSize <= 0 {
		segSize = DefaultSegmentSize
	}
	entries, err := os.ReadDir(walDir)
	if err != nil {
		return nil, err
	}
	present := make(map[uint64][]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		_, segNo, ok := ParseXLogFileName(e.Name(), 0)
		if !ok {
			continue
		}
		present[segNo] = append(present[segNo], e.Name())
	}
	redoSeg := redoLSN / uint64(segSize)
	if len(present[redoSeg]) == 0 {
		return nil, nil
	}
	end := redoSeg
	for len(present[end+1]) > 0 {
		end++
	}
	var removed []string
	for segNo, names := range present {
		if segNo <= end+1 {
			continue
		}
		for _, n := range names {
			if err := os.Remove(filepath.Join(walDir, n)); err != nil {
				return removed, fmt.Errorf("wal: remove %s past the end of WAL: %w", n, err)
			}
			removed = append(removed, n)
		}
	}
	return removed, nil
}

// overwritePageWithin reports whether a record of total bytes starting at
// in-segment offset off (absolute position pos) runs onto a page whose
// header carries XLP_FIRST_IS_OVERWRITE_CONTRECORD, and returns that page's
// offset. Pages are checked in order while the record's bytes would still
// need them: each continuation page holds XLOG_BLCKSZ minus its header.
func overwritePageWithin(data []byte, off int, pos int64, total int, segSize int64) (int, bool) {
	avail := int(XLOGBlockSize - pos%XLOGBlockSize)
	if total <= avail {
		return 0, false
	}
	remaining := total - avail
	pageOff := off + avail
	pagePos := pos + int64(avail)
	for remaining > 0 {
		hsize := pageHeaderSizeAt(pagePos, segSize)
		if pageOff+hsize > len(data) {
			return 0, false
		}
		hdr, err := DecodeXLogPageHeader(data[pageOff : pageOff+hsize])
		if err != nil {
			return 0, false
		}
		if hdr.Info&XLPFirstIsOverwriteContRecord != 0 && hdr.PageAddr == uint64(pagePos) {
			return pageOff, true
		}
		remaining -= int(XLOGBlockSize) - hsize
		pageOff += int(XLOGBlockSize)
		pagePos += XLOGBlockSize
	}
	return 0, false
}

// verifyOverwriteContrecord is xlogrecovery.c xlogrecovery_redo's
// XLOG_OVERWRITE_CONTRECORD arm: the record's overwritten_lsn must be the
// partial record the reader dropped to reach it (FATAL "mismatching
// overwritten LSN" otherwise), and the skip is logged. M0149-0017b.
func verifyOverwriteContrecord(rec Record) error {
	if rec.XLog == nil || rec.XLog.Header.Rmid != RmgrXLog ||
		rec.XLog.Header.Info&^XLRInfoMask != overwriteContrecordInfo || len(rec.XLog.MainData) < 16 {
		return nil
	}
	lsn := binary.LittleEndian.Uint64(rec.XLog.MainData[0:8])
	ts := int64(binary.LittleEndian.Uint64(rec.XLog.MainData[8:16]))
	if lsn != rec.OverwrittenRecPtr {
		return fmt.Errorf("mismatching overwritten LSN %X/%X -> %X/%X",
			uint32(lsn>>32), uint32(lsn), uint32(rec.OverwrittenRecPtr>>32), uint32(rec.OverwrittenRecPtr))
	}
	slog.Default().Info(fmt.Sprintf("successfully skipped missing contrecord at %X/%X, overwritten at %s",
		uint32(lsn>>32), uint32(lsn), timestamptzToStr(ts)))
	return nil
}

// timestamptzToStr renders a TimestampTz as timestamptz_to_str does in the
// ISO style: "2026-11-24 12:00:00.123456+09".
func timestamptzToStr(ts int64) string {
	t := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(ts) * time.Microsecond).Local()
	s := t.Format("2006-01-02 15:04:05.000000-07")
	if _, off := t.Zone(); off%3600 != 0 {
		s = t.Format("2006-01-02 15:04:05.000000-07:00")
	}
	return s
}

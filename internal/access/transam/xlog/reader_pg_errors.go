package xlog

import (
	"encoding/binary"
	"fmt"
)

// pgDecodeFailure reproduces the checks XLogDecodeNextRecord
// (access/transam/xlogreader.c) makes for the record at the PG LSN
// recPtr = baseOffset+off, given the PG LSN of the previous record, and
// returns the error text it would report — the message PostgreSQL logs when
// crash recovery reaches the end of WAL there — or "" when the record reads
// cleanly. stream holds the WAL from baseOffset (a whole multiple of segSize)
// onward. tli names the segment file in page-header messages.
//
// goopg's own walk decides where replay stops by its own rules (a zero record
// header, a preallocated tail, a torn record); this function only produces
// PostgreSQL's message for that point, in PostgreSQL's check order
// (XLogReaderValidatePageHeader, ValidXLogRecordHeader, the contrecord
// checks, ValidXLogRecord). M0149-0076.
func pgDecodeFailure(stream []byte, off int, segSize int64, baseOffset, prevRecPtr uint64, tli uint32) string {
	const blcksz = XLOGBlockSize
	lsn := func(v uint64) string { return fmt.Sprintf("%X/%X", uint32(v>>32), uint32(v)) }
	at := func(abs uint64) int64 { return int64(abs - baseOffset) }
	have := func(abs uint64, n int) bool {
		o := at(abs)
		return o >= 0 && o+int64(n) <= int64(len(stream))
	}
	validatePage := func(page uint64) string {
		if !have(page, SizeOfXLogShortPHD) {
			return ""
		}
		o := at(page)
		h, _ := DecodeXLogPageHeader(stream[o : o+SizeOfXLogShortPHD])
		segno := page / uint64(segSize)
		segOff := page % uint64(segSize)
		fname := XLogFileName(tli, segno, segSize)
		if h.Magic != XLOGPageMagic {
			return fmt.Sprintf("invalid magic number %04X in WAL segment %s, LSN %s, offset %d", h.Magic, fname, lsn(page), segOff)
		}
		if h.Info&^0x000F != 0 {
			return fmt.Sprintf("invalid info bits %04X in WAL segment %s, LSN %s, offset %d", h.Info, fname, lsn(page), segOff)
		}
		if h.Info&XLPLongHeader == 0 && segOff == 0 {
			return fmt.Sprintf("invalid info bits %04X in WAL segment %s, LSN %s, offset %d", h.Info, fname, lsn(page), segOff)
		}
		if h.PageAddr != page {
			return fmt.Sprintf("unexpected pageaddr %s in WAL segment %s, LSN %s, offset %d", lsn(h.PageAddr), fname, lsn(page), segOff)
		}
		return ""
	}
	pageHdrSize := func(page uint64) int {
		o := at(page)
		if binary.LittleEndian.Uint16(stream[o+2:o+4])&XLPLongHeader != 0 {
			return SizeOfXLogLongPHD
		}
		return SizeOfXLogShortPHD
	}
	validHeader := func(rec []byte, recPtr uint64) string {
		totLen := binary.LittleEndian.Uint32(rec[0:4])
		if totLen < SizeOfXLogRecord {
			return fmt.Sprintf("invalid record length at %s: expected at least %d, got %d", lsn(recPtr), SizeOfXLogRecord, totLen)
		}
		if rmid := rec[17]; rmid > 21 && rmid < 128 {
			return fmt.Sprintf("invalid resource manager ID %d at %s", rmid, lsn(recPtr))
		}
		if prev := binary.LittleEndian.Uint64(rec[8:16]); prevRecPtr != unknownPrevRecPtr && prev != prevRecPtr {
			return fmt.Sprintf("record with incorrect prev-link %s at %s", lsn(prev), lsn(recPtr))
		}
		return ""
	}

	recPtr := baseOffset + uint64(off)
	page := recPtr - recPtr%blcksz
	if msg := validatePage(page); msg != "" {
		return msg
	}
	if !have(page, SizeOfXLogShortPHD) {
		return ""
	}
	hdrSize := pageHdrSize(page)
	targetRecOff := recPtr % blcksz
	if targetRecOff == 0 {
		recPtr += uint64(hdrSize)
		targetRecOff = uint64(hdrSize)
	} else if targetRecOff < uint64(hdrSize) {
		return fmt.Sprintf("invalid record offset at %s: expected at least %d, got %d", lsn(recPtr), hdrSize, targetRecOff)
	}
	o := at(page)
	if binary.LittleEndian.Uint16(stream[o+2:o+4])&XLPFirstIsContRecord != 0 && targetRecOff == uint64(hdrSize) {
		return fmt.Sprintf("contrecord is requested by %s", lsn(recPtr))
	}
	if !have(recPtr, 4) {
		return ""
	}
	totalLen := binary.LittleEndian.Uint32(stream[at(recPtr):])
	gotHeader := false
	if targetRecOff <= blcksz-SizeOfXLogRecord {
		if !have(recPtr, SizeOfXLogRecord) {
			return ""
		}
		if msg := validHeader(stream[at(recPtr):at(recPtr)+SizeOfXLogRecord], recPtr); msg != "" {
			return msg
		}
		gotHeader = true
	} else if totalLen < SizeOfXLogRecord {
		return fmt.Sprintf("invalid record length at %s: expected at least %d, got %d", lsn(recPtr), SizeOfXLogRecord, totalLen)
	}

	// Assemble the record as XLogDecodeNextRecord does.
	firstLen := uint32(blcksz - recPtr%blcksz)
	var buf []byte
	if totalLen > firstLen {
		if !have(recPtr, int(firstLen)) {
			return ""
		}
		buf = append(buf, stream[at(recPtr):at(recPtr)+int64(firstLen)]...)
		gotLen := firstLen
		for gotLen < totalLen {
			page += blcksz
			if msg := validatePage(page); msg != "" {
				return msg
			}
			if !have(page, SizeOfXLogShortPHD) {
				return ""
			}
			h, _ := DecodeXLogPageHeader(stream[at(page) : at(page)+SizeOfXLogShortPHD])
			if h.Info&XLPFirstIsContRecord == 0 {
				return fmt.Sprintf("there is no contrecord flag at %s", lsn(recPtr))
			}
			if h.RemLen == 0 || totalLen != h.RemLen+gotLen {
				return fmt.Sprintf("invalid contrecord length %d (expected %d) at %s", h.RemLen, int64(totalLen)-int64(gotLen), lsn(recPtr))
			}
			ph := pageHdrSize(page)
			n := uint32(blcksz) - uint32(ph)
			if h.RemLen < n {
				n = h.RemLen
			}
			if !have(page+uint64(ph), int(n)) {
				return ""
			}
			buf = append(buf, stream[at(page)+int64(ph):at(page)+int64(ph)+int64(n)]...)
			gotLen += n
			if !gotHeader && len(buf) >= SizeOfXLogRecord {
				if msg := validHeader(buf[:SizeOfXLogRecord], recPtr); msg != "" {
					return msg
				}
				gotHeader = true
			}
		}
	} else {
		if !have(recPtr, int(totalLen)) {
			return ""
		}
		buf = stream[at(recPtr) : at(recPtr)+int64(totalLen)]
	}
	// ValidXLogRecord: the CRC over the body, then the header up to xl_crc.
	want := binary.LittleEndian.Uint32(buf[20:24])
	if VerifyXLogRecordCRC(buf[:SizeOfXLogRecord], buf[SizeOfXLogRecord:totalLen], want) != nil {
		return fmt.Sprintf("incorrect resource manager data checksum in record at %s", lsn(recPtr))
	}
	return ""
}

// unknownPrevRecPtr disables pgDecodeFailure's prev-link check when the walk
// decoded no record before the failing point.
const unknownPrevRecPtr = ^uint64(0)

// PGEndOfWALMessage returns the message PostgreSQL's crash recovery logs
// where goopg's replay of walDir stops: the decode error XLogDecodeNextRecord
// reports for the record after the last one replayed ("invalid record
// length at 0/1530A28: expected at least 24, got 0" for an ordinary zero
// tail). "" when there is no WAL or the reader cannot say. M0149-0076.
func PGEndOfWALMessage(walDir string, segmentSize int64) string {
	firstSegNoI, err := firstAvailableSegment(walDir)
	if err != nil || firstSegNoI < 0 {
		return ""
	}
	firstSegNo := uint64(firstSegNoI)
	if segmentSize <= 0 {
		segmentSize = detectSegmentSizeAt(walDir, firstSegNo)
	}
	if segmentSize <= 0 {
		segmentSize = WALSegmentSize()
	}
	stream, err := readStreamFrom(walDir, segmentSize, firstSegNo)
	if err != nil || len(stream) == 0 {
		return ""
	}
	baseOffset := firstSegNo * uint64(segmentSize)
	recs, _ := readAllPageAware(stream, segmentSize, baseOffset)
	if len(recs) == 0 {
		return ""
	}
	last := recs[len(recs)-1]
	next := last.EndLSN // PG NextRecPtr: the end of the MAXALIGNed record
	if last.XLog != nil && isXLogSwitchHeader(last.XLog.Header) {
		if rem := next % uint64(segmentSize); rem != 0 {
			next += uint64(segmentSize) - rem
		}
	}
	if next < baseOffset {
		return ""
	}
	tli := uint32(1)
	segStart := next - next%uint64(segmentSize)
	if o := int64(segStart - baseOffset); o+8 <= int64(len(stream)) {
		if t := binary.LittleEndian.Uint32(stream[o+4 : o+8]); t != 0 {
			tli = t
		}
	} else {
		return ""
	}
	return pgDecodeFailure(stream, int(next-baseOffset), segmentSize, baseOffset, last.StartLSN-1, tli)
}

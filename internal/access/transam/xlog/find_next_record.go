package xlog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FindNextRecord is XLogFindNextRecord (xlogreader.c) for a page-aligned PG
// LSN: it returns the PG LSN of the first record that starts on the page at
// lsn or after it, read from timeline tli's segment files under walDir.
//
// A page whose header carries XLP_FIRST_IS_CONTRECORD begins with the tail
// of a record started earlier, whose header may be in a segment that is
// gone; its xlp_rem_len bytes are stepped over, a whole page at a time while
// the tail fills the page, as upstream does. The returned position may still
// be page padding when that tail ends a page's last record; a reader skips
// the padding as it would anywhere else.
//
// ok is false when no record is there: the segment file is missing or ends,
// or the page is an all-zero preallocated page.
func FindNextRecord(walDir string, tli uint32, segSize int64, lsn uint64) (rec uint64, ok bool, err error) {
	if segSize <= 0 {
		segSize = DefaultSegmentSize
	}
	if lsn%XLOGBlockSize != 0 {
		return 0, false, fmt.Errorf("wal: FindNextRecord: %X/%X is not page-aligned", uint32(lsn>>32), uint32(lsn))
	}
	var f *os.File
	var fSeg uint64
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()
	hdrBuf := make([]byte, SizeOfXLogLongPHD)
	for page := lsn; ; page += XLOGBlockSize {
		segno := page / uint64(segSize)
		if f == nil || fSeg != segno {
			if f != nil {
				_ = f.Close()
				f = nil
			}
			f, err = os.Open(filepath.Join(walDir, XLogFileName(tli, segno, segSize)))
			if errors.Is(err, os.ErrNotExist) {
				return 0, false, nil
			}
			if err != nil {
				return 0, false, err
			}
			fSeg = segno
		}
		hsize := pageHeaderSizeAt(int64(page), segSize)
		if _, err := f.ReadAt(hdrBuf[:hsize], int64(page%uint64(segSize))); err != nil {
			if errors.Is(err, io.EOF) {
				return 0, false, nil
			}
			return 0, false, err
		}
		if isZeroBytes(hdrBuf[:hsize]) {
			return 0, false, nil
		}
		hdr, err := DecodeXLogPageHeader(hdrBuf[:SizeOfXLogShortPHD])
		if err != nil {
			return 0, false, err
		}
		if hdr.PageAddr != page {
			return 0, false, fmt.Errorf("wal: page at %X/%X claims address %X/%X", uint32(page>>32), uint32(page), uint32(hdr.PageAddr>>32), uint32(hdr.PageAddr))
		}
		if hdr.Info&XLPFirstIsContRecord == 0 {
			return page + uint64(hsize), true, nil
		}
		if uint64(hdr.RemLen) < uint64(XLOGBlockSize-hsize) {
			return page + uint64(hsize) + uint64(maxAlignXLog(int(hdr.RemLen))), true, nil
		}
	}
}

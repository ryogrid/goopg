package xlog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SwitchTimeline ends recovery on the writer's timeline and continues WAL on
// newTLI from endOfLog, the end of the last record recovery replayed. It is
// the writer half of upstream StartupXLOG's end of archive recovery
// (xlog.c):
//
//   - WAL insertion resumes at EndOfLog, and the next record's xl_prev names
//     the last replayed record (prevRecPtr, its 0-based start). Bytes the
//     walreceiver appended past endOfLog are a partially transmitted record,
//     which recovery treats as end of WAL (xlogrecovery.c ReadRecord); the
//     next record overwrites them, as PG's insertion at EndOfLog does.
//   - XLogInitNewTimeline: when endOfLog is inside a segment, that segment
//     is copied to the new timeline's name up to endOfLog and zero-filled
//     after it (XLogFileCopy). At a segment boundary the new timeline's
//     first segment is created when the next record needs it.
//   - RemoveNonParentXlogFiles: old-timeline segments after the one holding
//     endOfLog are removed. They are preallocated files or WAL that is not
//     part of the new timeline's history. The old timeline's last segment
//     stays (PG renames it .partial only when archiving).
//
// Afterwards new segment names and page headers carry newTLI. Readers that
// open a segment by number pick its newest timeline (openSegmentFile,
// RecordIterator), so pre-switch segments stay readable under the old name.
//
// Precondition, as in PG ("Writing WAL isn't allowed yet, so there are no
// locking considerations"): nothing appends concurrently. Promotion calls
// this after the walreceiver and the replayer have stopped and before the
// runtime accepts writes.
func (w *Writer) SwitchTimeline(newTLI uint32, endOfLog, prevRecPtr uint64) error {
	s := w.stateRef
	if s == nil {
		return ErrClosed
	}
	oldTLI := s.tli.Load()
	if newTLI <= oldTLI {
		return fmt.Errorf("wal: switch timeline: new timeline %d is not after %d", newTLI, oldTLI)
	}
	if written := w.WrittenLSN(); endOfLog == 0 || endOfLog > written {
		return fmt.Errorf("wal: switch timeline: end of log %X outside the written WAL (%X)", endOfLog, written)
	}
	// Everything received is drained and durable before the files are
	// copied; the copy then reads complete bytes from the old segment.
	if err := w.FlushUpTo(w.WrittenLSN()); err != nil {
		return fmt.Errorf("wal: switch timeline: flush: %w", err)
	}
	s.eagerWG.Wait()

	s.writeMu.lock()
	defer s.writeMu.release()
	s.appendMu.Lock()
	defer s.appendMu.Unlock()

	// Resume insertion at endOfLog. The tracker, the WAL buffer, the
	// sender ring and the published watermark all restart there; with no
	// insertion in flight none of them holds a byte past it that matters.
	if endOfLog < uint64(s.writePos) {
		s.writePos = int64(endOfLog)
		s.memRing.ResetToPos(int64(endOfLog))
	}
	s.writeLSN = endOfLog
	s.drainedLSN = min(s.drainedLSN, endOfLog)
	s.flushedLSN = min(s.flushedLSN, endOfLog)
	if s.writeLSNMirror != nil {
		s.writeLSNMirror.Store(endOfLog)
	}
	if s.drainedLSNMirror != nil {
		s.drainedLSNMirror.Store(s.drainedLSN)
	}
	if s.flushedLSNMirror != nil {
		s.flushedLSNMirror.Store(s.flushedLSN)
	}
	if s.walBuf != nil {
		s.walBuf.reset(int64(endOfLog))
	}
	s.prevRecPtr = prevRecPtr
	s.core.resetPosition(endOfLog, prevRecPtr)
	s.core.resetPublished(int64(endOfLog))

	// Every open segment file is named for the old timeline.
	for segNo, f := range s.files {
		_ = f.Close()
		delete(s.files, segNo)
	}
	clear(s.dirty)

	segSize := uint64(s.cfg.SegmentSize)
	endSeg := endOfLog / segSize
	if off := endOfLog % segSize; off != 0 {
		from := filepath.Join(s.cfg.WALDir, FormatSegmentNameTLI(endSeg, oldTLI))
		to := filepath.Join(s.cfg.WALDir, FormatSegmentNameTLI(endSeg, newTLI))
		if err := copySegmentPrefix(from, to, int64(off), s.cfg.SegmentSize); err != nil {
			return fmt.Errorf("wal: switch timeline: %w", err)
		}
	} else {
		endSeg-- // the last old-timeline segment is the previous one
	}
	if err := removeSegmentsAfter(s.cfg.WALDir, oldTLI, endSeg); err != nil {
		return fmt.Errorf("wal: switch timeline: %w", err)
	}
	s.tli.Store(newTLI)
	return syncDir(s.cfg.WALDir)
}

// InsertPosition returns where the next record goes and the 0-based start
// of the record before it (its xl_prev). After walreceiver appends the prev
// half still names the last record the writer itself framed: AppendRaw
// copies bytes without tracking their record boundaries.
func (w *Writer) InsertPosition() (curr, prev uint64) {
	if w.core == nil {
		return w.WrittenLSN(), 0
	}
	return w.core.Load()
}

// copySegmentPrefix writes the first upto bytes of segment from into a new
// segment file to, zero-filled to segSize — XLogFileCopy. The file is built
// under a temporary name and renamed into place, so a crash never leaves a
// half-written segment under the real name.
func copySegmentPrefix(from, to string, upto, segSize int64) error {
	src, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("open %s: %w", from, err)
	}
	defer src.Close()
	tmp := to + ".tmp"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	fail := func(err error) error {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return err
	}
	if n, err := io.Copy(dst, io.LimitReader(src, upto)); err != nil {
		return fail(fmt.Errorf("copy %s: %w", from, err))
	} else if n != upto {
		return fail(fmt.Errorf("copy %s: read %d of %d bytes", from, n, upto))
	}
	zeros := make([]byte, 1<<16)
	for pos := upto; pos < segSize; {
		n := min(int64(len(zeros)), segSize-pos)
		if _, err := dst.Write(zeros[:n]); err != nil {
			return fail(fmt.Errorf("zero-fill %s: %w", tmp, err))
		}
		pos += n
	}
	if err := dst.Sync(); err != nil {
		return fail(fmt.Errorf("fsync %s: %w", tmp, err))
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, to); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}

// removeSegmentsAfter removes timeline tli's segments numbered above lastSeg
// (RemoveNonParentXlogFiles).
func removeSegmentsAfter(walDir string, tli uint32, lastSeg uint64) error {
	entries, err := os.ReadDir(walDir)
	if err != nil {
		return fmt.Errorf("readdir %s: %w", walDir, err)
	}
	for _, e := range entries {
		segTLI, segNo, ok := ParseXLogFileName(e.Name(), 0)
		if !ok || segTLI != tli || segNo <= lastSeg {
			continue
		}
		if err := os.Remove(filepath.Join(walDir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", e.Name(), err)
		}
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

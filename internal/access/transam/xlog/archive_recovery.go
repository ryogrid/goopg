// Archive recovery: fetch missing WAL segments via restore_command,
// replay them, and promote when the archive is exhausted.
//
// RunArchiveRecovery is called at startup when recovery.signal is
// present. It mirrors upstream PostgreSQL's archive-recovery loop in
// xlogrecovery.c: the server reads local WAL segments first, then
// fetches any missing segments from the archive via restore_command,
// replays them, and promotes to a new timeline when the archive is
// exhausted.
//
// See docs/design/0130-0009-recovery-signal-archive-recovery.md.

package xlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/goopg/goopg/internal/storage"
)

// RunArchiveRecovery executes the archive-recovery loop:
//  1. Determine the starting segment from the highest local segment.
//  2. For each segment from that point onward:
//     a. If the segment does not exist locally, fetch it via restore_command.
//     b. If restore_command fails, the archive is exhausted — exit.
//     c. Read the segment's records and replay them into mgr.
//  3. Return nil when the archive is exhausted (the caller then promotes).
//
// segmentSize of 0 means "derive from the cluster's own WAL" (via
// detectSegmentSizeAt / DefaultSegmentSize).
func RunArchiveRecovery(mgr *storage.Manager, dataDir, restoreCommand string, segmentSize int64) error {
	walDir := filepath.Join(dataDir, "pg_wal")

	// Determine the highest local segment so we know where to resume.
	// Crash recovery (ReplayFromDirWithMgr) already replayed everything
	// locally available; we fetch segments beyond that.
	highestSeg, err := highestLocalSegment(walDir)
	if err != nil {
		return fmt.Errorf("wal: archive recovery: scan local segments: %w", err)
	}

	if segmentSize <= 0 {
		var firstSeg uint64
		if highestSeg >= 0 {
			firstSeg = uint64(highestSeg)
		}
		segmentSize = detectSegmentSizeAt(walDir, firstSeg)
		if segmentSize <= 0 {
			segmentSize = DefaultSegmentSize
		}
	}

	// Start from the segment after the highest local one. If no local
	// segments exist (fresh basebackup), start from segment 0.
	nextSeg := uint64(0)
	if highestSeg >= 0 {
		nextSeg = uint64(highestSeg) + 1
	}

	for {
		segName := FormatSegmentNameTLI(nextSeg, 1) // TLI=1 for archive fetch

		// Check if the segment already exists locally.
		localPath := filepath.Join(walDir, segName)
		if _, err := os.Stat(localPath); os.IsNotExist(err) {
			// Segment not local — fetch from archive.
			if err := RestoreArchivedFile(restoreCommand, walDir, segName); err != nil {
				// Archive exhausted or restore_command not configured.
				// This is the normal termination condition.
				return nil
			}
		} else if err != nil {
			return fmt.Errorf("wal: archive recovery: stat %s: %w", localPath, err)
		}

		// Read and decode records from this segment.
		records, err := ReadSegmentRecords(walDir, nextSeg, segmentSize)
		if err != nil {
			return fmt.Errorf("wal: archive recovery: read segment %s: %w", segName, err)
		}

		// Replay the records. ReplayRecords is idempotent via pd_lsn, so
		// any already-applied records (from the initial crash recovery pass
		// or a prior iteration) are silently skipped.
		if len(records) > 0 {
			if _, err := ReplayRecords(mgr, records); err != nil {
				return fmt.Errorf("wal: archive recovery: replay segment %s: %w", segName, err)
			}
		}

		nextSeg++
	}
}

// highestLocalSegment returns the highest segment number found in walDir
// by scanning for files matching the WAL segment naming pattern.
// Returns -1 when no segments exist (fresh basebackup with empty pg_wal).
func highestLocalSegment(walDir string) (int, error) {
	entries, err := os.ReadDir(walDir)
	if err != nil {
		if os.IsNotExist(err) {
			return -1, nil
		}
		return -1, fmt.Errorf("wal: readdir %s: %w", walDir, err)
	}
	highest := -1
	for _, e := range entries {
		name := e.Name()
		// WAL segment names are 24 hex digits (8 TLI + 8 log + 8 seg).
		if len(name) != 24 {
			continue
		}
		// Extract segment number (last 8 hex digits).
		if segNo, err := strconv.ParseUint(name[16:24], 16, 64); err == nil {
			if int(segNo) > highest {
				highest = int(segNo)
			}
		}
	}
	return highest, nil
}

// ReadSegmentRecords reads and decodes WAL records from a single
// segment at segNo in walDir. The records carry absolute LSNs
// (including the base offset from segNo * segmentSize), matching the
// values the WAL writer assigned and the page pd_lsn headers use for
// idempotency.
func ReadSegmentRecords(walDir string, segNo uint64, segmentSize int64) ([]Record, error) {
	stream, err := readStreamFrom(walDir, segmentSize, segNo)
	if err != nil {
		return nil, err
	}
	if len(stream) == 0 {
		return nil, nil
	}
	baseOffset := segNo * uint64(segmentSize)
	return readAllPageAware(stream, segmentSize, baseOffset)
}

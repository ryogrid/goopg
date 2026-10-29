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

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/storage"
)

// TimelineSpan is one timeline on the expected history of archive recovery:
// its ID and the WAL position it began at (0 for the oldest).
type TimelineSpan struct {
	TLI   uint32
	Begin uint64
}

// ArchiveRecoveryResult is what archive recovery hands the promotion that
// follows it.
type ArchiveRecoveryResult struct {
	// EndOfLog is the end of the last record replayed — PG's EndOfLog, the
	// timeline switch point the promotion writes into the new timeline's
	// history file (xlog.c StartupXLOG → writeTimeLineHistory) and where the
	// new timeline's WAL begins. LastStart is that record's StartLSN; the
	// first record on the new timeline names LastStart-1 as its xl_prev
	// (0-based), as the streaming promotion does (StreamReplayer.LastApplied).
	// Both are 0 when nothing was replayed.
	EndOfLog, LastStart uint64
	// EndTLI is the timeline of the last segment replayed (PG's
	// EndOfLogTLI); 0 when nothing was replayed.
	EndTLI uint32
	// NewestTLI is the newest timeline whose history file exists locally or
	// in the archive (findNewestTimeLine). The promotion's new timeline is
	// NewestTLI+1, so it cannot collide with one already in the archive.
	NewestTLI uint32
	// Timelines is the expected history, oldest first (expectedTLEs).
	Timelines []TimelineSpan
}

// RunArchiveRecovery executes the archive-recovery loop, following the
// timeline history as PG's recovery does with recovery_target_timeline =
// 'latest' (the default):
//  1. The start timeline is pg_control's checkpoint timeline. The newest
//     timeline is found by probing <tli>.history files upward from it,
//     locally or through restore_command (findNewestTimeLine), and the
//     expected history is that timeline's history file (readTimeLineHistory)
//     — the start timeline must be on it.
//  2. From the highest local segment onward, each segment is read from the
//     newest expected timeline that has it, skipping a timeline that began
//     after the segment and never going back to an older timeline than the
//     last segment's (XLogFileReadAnyTLI). A missing segment is fetched with
//     restore_command; when no timeline has it, the archive is exhausted.
//  3. Each segment's records are replayed into mgr (idempotent via pd_lsn).
//
// Starting at the highest local segment, not after it, picks up a newer
// timeline's copy of the segment holding a timeline switch.
//
// segmentSize of 0 means "derive from the cluster's own WAL" (via
// detectSegmentSizeAt / DefaultSegmentSize).
func RunArchiveRecovery(mgr *storage.Manager, dataDir, restoreCommand string, segmentSize int64) (ArchiveRecoveryResult, error) {
	var res ArchiveRecoveryResult
	walDir := filepath.Join(dataDir, "pg_wal")

	highestSeg, err := highestLocalSegment(walDir)
	if err != nil {
		return res, fmt.Errorf("wal: archive recovery: scan local segments: %w", err)
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

	startTLI := uint32(1)
	if cd, err := control.ReadControlFile(dataDir); err == nil && cd != nil && cd.CheckPointCopyThisTLI != 0 {
		startTLI = cd.CheckPointCopyThisTLI
	}
	have := func(name string) bool {
		if _, err := os.Stat(filepath.Join(walDir, name)); err == nil {
			return true
		}
		return RestoreArchivedFile(restoreCommand, walDir, name) == nil
	}
	res.NewestTLI = startTLI
	for probe := startTLI + 1; have(TimelineHistoryFileName(probe)); probe++ {
		res.NewestTLI = probe
	}
	res.Timelines = []TimelineSpan{{TLI: startTLI}}
	if res.NewestTLI != startTLI {
		entries, err := ReadHistory(walDir, res.NewestTLI)
		if err != nil {
			return res, fmt.Errorf("wal: archive recovery: %w", err)
		}
		res.Timelines = res.Timelines[:0]
		var begin uint64
		onHistory := false
		for _, e := range entries {
			res.Timelines = append(res.Timelines, TimelineSpan{TLI: e.TLI, Begin: begin})
			begin = e.SwitchLSN
			onHistory = onHistory || e.TLI == startTLI
		}
		res.Timelines = append(res.Timelines, TimelineSpan{TLI: res.NewestTLI, Begin: begin})
		if !onHistory {
			return res, fmt.Errorf("wal: archive recovery: requested timeline %d is not a child of this server's history (timeline %d)", res.NewestTLI, startTLI)
		}
	}

	seg := uint64(0)
	if highestSeg >= 0 {
		seg = uint64(highestSeg)
	}
	curTLI := startTLI
	for {
		chosen := uint32(0)
		for i := len(res.Timelines) - 1; i >= 0; i-- {
			sp := res.Timelines[i]
			if sp.TLI < curTLI {
				break // don't bother looking at too-old timelines
			}
			if sp.Begin != 0 && seg < sp.Begin/uint64(segmentSize) {
				continue // the segment predates this timeline
			}
			if have(FormatSegmentNameTLI(seg, sp.TLI)) {
				chosen = sp.TLI
				break
			}
		}
		if chosen == 0 {
			return res, nil // archive exhausted — the normal end
		}
		curTLI = chosen

		records, err := ReadSegmentRecords(walDir, seg, segmentSize)
		if err != nil {
			return res, fmt.Errorf("wal: archive recovery: read segment %s: %w", FormatSegmentNameTLI(seg, chosen), err)
		}
		if len(records) > 0 {
			if _, err := ReplayRecords(mgr, records); err != nil {
				return res, fmt.Errorf("wal: archive recovery: replay segment %s: %w", FormatSegmentNameTLI(seg, chosen), err)
			}
			if last := records[len(records)-1]; last.EndLSN > res.EndOfLog {
				res.EndOfLog, res.LastStart, res.EndTLI = last.EndLSN, last.StartLSN, chosen
			}
		}
		seg++
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

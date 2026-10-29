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
	"context"
	"errors"
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
	// in the archive, probing from the target (findNewestTimeLine). The
	// promotion's new timeline is NewestTLI+1, so it cannot collide with one
	// already in the archive.
	NewestTLI uint32
	// TargetTLI is the recovery target timeline (recoveryTargetTLI): the
	// timeline whose history recovery follows.
	TargetTLI uint32
	// TargetHistory is TargetTLI's history file as read (nil for timeline 1
	// or when it has none). The history file is read through RECOVERYHISTORY
	// and not kept in pg_wal, so the promotion builds the new timeline's
	// history from these entries.
	TargetHistory []TimelineHistoryEntry
	// Timelines is the expected history, oldest first (expectedTLEs).
	Timelines []TimelineSpan
}

// TargetTimelineGoal is recovery_target_timeline's kind
// (RecoveryTargetTimeLineGoal); the zero value is 'latest', the default.
type TargetTimelineGoal int

const (
	TargetTimelineLatest  TargetTimelineGoal = iota // 'latest': findNewestTimeLine
	TargetTimelineCurrent                           // 'current': pg_control's timeline
	TargetTimelineNumeric                           // an explicit timeline ID
)

// TargetTimeline is recovery_target_timeline: the goal and, for
// TargetTimelineNumeric, the requested timeline.
type TargetTimeline struct {
	Goal TargetTimelineGoal
	TLI  uint32
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
	return RunArchiveRecoveryWith(context.Background(), mgr, dataDir, func() string { return restoreCommand }, segmentSize, TargetTimeline{})
}

// RunArchiveRecoveryWith is RunArchiveRecovery with restore_command read
// through restoreCommand before every archive fetch. restore_command is
// PGC_SIGHUP and RestoreArchivedFile reads recoveryRestoreCommand at each
// call, so a reload processed while recovery runs (the startup process's
// ProcessStartupProcInterrupts) changes the command the next fetch uses.
//
// ctx is the shutdown request: it is checked before every fetch and every
// segment replay (ProcessStartupProcInterrupts' shutdown_requested) and
// cancels a running restore_command (StartupProcShutdownHandler while
// in_restore_command); either way the result wraps ErrRecoveryShutdown and
// recovery is left unfinished. A *RestoreFatalError from a fetch aborts
// recovery instead of ending it, as upstream's FATAL does.
func RunArchiveRecoveryWith(ctx context.Context, mgr *storage.Manager, dataDir string, restoreCommand func() string, segmentSize int64, target TargetTimeline) (ArchiveRecoveryResult, error) {
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
	// %r: the file holding the oldest restartpoint's redo
	// (GetOldestRestartPoint — pg_control's checkPointCopy). Start-time
	// recovery takes no restartpoints, so it is fixed for the whole pass.
	lastRestartPointFname := XLogFileName(0, 0, segmentSize)
	if cd, err := control.ReadControlFile(dataDir); err == nil && cd != nil && cd.CheckPointCopyThisTLI != 0 {
		startTLI = cd.CheckPointCopyThisTLI
		lastRestartPointFname = XLogFileName(cd.CheckPointCopyThisTLI, cd.CheckPointCopyRedo/uint64(segmentSize), segmentSize)
	}
	// have reports whether name was restored from the archive or, failing
	// that, is in pg_wal — XLogFileReadAnyTLI tries XLOG_FROM_ARCHIVE
	// before XLOG_FROM_PG_WAL because the local copy may be an old or
	// partly-filled version restored with the base backup; a restored copy
	// replaces it. A shutdown request or a FATAL restore failure is
	// returned as the error.
	have := func(name, recoverName string, expectedSize int64) (bool, error) {
		if ctx.Err() != nil {
			return false, ErrRecoveryShutdown
		}
		if cmd := restoreCommand(); cmd != "" {
			err := RestoreArchivedFileContext(ctx, cmd, walDir, name, recoverName, expectedSize, lastRestartPointFname)
			if err == nil {
				return true, nil
			}
			var fatal *RestoreFatalError
			if errors.Is(err, ErrRecoveryShutdown) || errors.As(err, &fatal) {
				return false, err
			}
		}
		if _, err := os.Stat(filepath.Join(walDir, name)); err == nil {
			return true, nil
		}
		return false, nil
	}
	// FinishWalRecovery: a partial RECOVERYXLOG or a leftover
	// RECOVERYHISTORY is removed when archive recovery ends.
	defer func() {
		_ = os.Remove(filepath.Join(walDir, RecoveryXLogName))
		_ = os.Remove(filepath.Join(walDir, RecoveryHistoryName))
	}()
	// historyPath finds tli's history file the way readTimeLineHistory and
	// existsTimeLineHistory do in archive recovery: restored from the
	// archive into RECOVERYHISTORY (not kept in pg_wal), else the pg_wal
	// copy. ok is false when neither has it.
	historyPath := func(tli uint32) (string, bool, error) {
		if ctx.Err() != nil {
			return "", false, ErrRecoveryShutdown
		}
		name := TimelineHistoryFileName(tli)
		if cmd := restoreCommand(); cmd != "" {
			err := RestoreArchivedFileTemp(ctx, cmd, walDir, name, RecoveryHistoryName, lastRestartPointFname)
			if err == nil {
				return filepath.Join(walDir, RecoveryHistoryName), true, nil
			}
			var fatal *RestoreFatalError
			if errors.Is(err, ErrRecoveryShutdown) || errors.As(err, &fatal) {
				return "", false, err
			}
		}
		local := filepath.Join(walDir, name)
		if _, err := os.Stat(local); err == nil {
			return local, true, nil
		}
		return "", false, nil
	}
	// findNewestTimeLine: the newest timeline whose history file exists,
	// probing upward from start.
	findNewest := func(start uint32) (uint32, error) {
		newest := start
		for probe := start + 1; ; probe++ {
			_, ok, err := historyPath(probe)
			if err != nil {
				return 0, err
			}
			if !ok {
				return newest, nil
			}
			newest = probe
		}
	}
	// validateRecoveryParameters' recovery_target_timeline step.
	targetTLI := startTLI
	switch target.Goal {
	case TargetTimelineNumeric:
		// Timeline 1 does not have a history file, all else should.
		if target.TLI != 1 {
			_, ok, err := historyPath(target.TLI)
			if err != nil {
				return res, fmt.Errorf("wal: archive recovery: %w", err)
			}
			if !ok {
				return res, &RestoreFatalError{Msg: fmt.Sprintf("recovery target timeline %d does not exist", target.TLI)}
			}
		}
		targetTLI = target.TLI
	case TargetTimelineLatest:
		newest, err := findNewest(startTLI)
		if err != nil {
			return res, fmt.Errorf("wal: archive recovery: %w", err)
		}
		targetTLI = newest
	}
	res.TargetTLI = targetTLI
	// The promotion's new timeline is findNewestTimeLine(recoveryTargetTLI)
	// + 1 whatever the target, so it cannot collide with an archived one.
	res.NewestTLI = targetTLI
	if target.Goal != TargetTimelineLatest {
		newest, err := findNewest(targetTLI)
		if err != nil {
			return res, fmt.Errorf("wal: archive recovery: %w", err)
		}
		res.NewestTLI = newest
	}
	// expectedTLEs = readTimeLineHistory(recoveryTargetTLI), read through
	// RECOVERYHISTORY; the entries are kept verbatim for the promotion's
	// writeTimeLineHistory, which copies the parent's history the same way.
	// The start timeline must be on it (InitWalRecovery's "not a child"
	// FATAL).
	res.Timelines = []TimelineSpan{{TLI: startTLI}}
	if targetTLI != 1 {
		path, ok, err := historyPath(targetTLI)
		if err != nil {
			return res, fmt.Errorf("wal: archive recovery: %w", err)
		}
		var entries []TimelineHistoryEntry
		if ok {
			if entries, err = ReadHistoryFile(path); err != nil {
				return res, fmt.Errorf("wal: archive recovery: %w", err)
			}
		}
		res.TargetHistory = entries
	}
	if targetTLI != startTLI {
		res.Timelines = res.Timelines[:0]
		var begin uint64
		onHistory := false
		for _, e := range res.TargetHistory {
			res.Timelines = append(res.Timelines, TimelineSpan{TLI: e.TLI, Begin: begin})
			begin = e.SwitchLSN
			onHistory = onHistory || e.TLI == startTLI
		}
		res.Timelines = append(res.Timelines, TimelineSpan{TLI: targetTLI, Begin: begin})
		if !onHistory {
			return res, &RestoreFatalError{Msg: fmt.Sprintf("requested timeline %d is not a child of this server's history", targetTLI)}
		}
	}
	// restoreTimeLineHistoryFiles(checkpoint TLI, recoveryTargetTLI)
	// (StartupXLOG): the history files of the timelines between the start
	// and the target — not the target's own — are copied into pg_wal for a
	// cascading standby or a later archive.
	for tli := startTLI; tli < targetTLI; tli++ {
		if tli == 1 {
			continue
		}
		err := RestoreArchivedFileContext(ctx, restoreCommand(), walDir, TimelineHistoryFileName(tli), RecoveryHistoryName, 0, lastRestartPointFname)
		var fatal *RestoreFatalError
		if errors.Is(err, ErrRecoveryShutdown) || errors.As(err, &fatal) {
			return res, fmt.Errorf("wal: archive recovery: %w", err)
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
			ok, err := have(FormatSegmentNameTLI(seg, sp.TLI), RecoveryXLogName, segmentSize)
			if err != nil {
				return res, fmt.Errorf("wal: archive recovery: %w", err)
			}
			if ok {
				chosen = sp.TLI
				break
			}
		}
		if chosen == 0 {
			return res, nil // archive exhausted — the normal end
		}
		curTLI = chosen
		if ctx.Err() != nil {
			return res, fmt.Errorf("wal: archive recovery: %w", ErrRecoveryShutdown)
		}

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

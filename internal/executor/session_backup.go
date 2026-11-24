package executor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/optimizer"
)

// Session-level (non-exclusive) online backups: pg_backup_start /
// pg_backup_stop (xlogfuncs.c) over do_pg_backup_start / do_pg_backup_stop
// (xlog.c). PG keeps the state in the backend-local sessionBackupState and
// backup_state; goopg keys it by the connection's ProcArray slot (ProcNum),
// which is fixed for the connection's lifetime. BASE_BACKUP on a
// replication=database connection consults the same state
// (SessionBackupRunning) to refuse a second backup in one session.
// M0149-0077k.

// sessionBackup is the in-progress backup of one session.
type sessionBackup struct {
	label     string
	startLSN  uint64 // PG byte LSN of the checkpoint's redo point
	ckptLSN   uint64 // PG byte LSN of the checkpoint record
	tli       uint32
	startTime time.Time
}

var sessionBackups sync.Map // int32 ProcNum → *sessionBackup

// maxBackupLabelLen is MAXPGPATH: do_pg_backup_start rejects longer labels.
const maxBackupLabelLen = 1024

// SessionBackupRunning reports whether the session in ProcArray slot procNum
// has called pg_backup_start without pg_backup_stop (SESSION_BACKUP_RUNNING).
func SessionBackupRunning(procNum int32) bool {
	_, ok := sessionBackups.Load(procNum)
	return ok
}

// AbortSessionBackup forgets the session's backup, reporting whether one was
// running. The connection teardown calls it: PG's do_pg_abort_backup runs at
// backend exit with "aborting backup due to backend exiting before
// pg_backup_stop was called".
func AbortSessionBackup(procNum int32) bool {
	_, ok := sessionBackups.LoadAndDelete(procNum)
	return ok
}

// BuildBackupLabel renders backup_label the way do_pg_backup_start's
// build_backup_content does for a streamed backup. startLSN and ckptLSN are
// PG byte LSNs.
func BuildBackupLabel(label string, startLSN, ckptLSN uint64, tli uint32, segSize int64, startTime time.Time) []byte {
	if ckptLSN == 0 {
		ckptLSN = startLSN
	}
	segno := startLSN / uint64(segSize)
	walFile := xlog.XLogFileName(tli, segno, segSize)
	var b bytes.Buffer
	fmt.Fprintf(&b, "START WAL LOCATION: %s (file %s)\n", formatPgLSN(startLSN), walFile)
	fmt.Fprintf(&b, "CHECKPOINT LOCATION: %s\n", formatPgLSN(ckptLSN))
	fmt.Fprintf(&b, "BACKUP METHOD: streamed\n")
	fmt.Fprintf(&b, "BACKUP FROM: primary\n")
	fmt.Fprintf(&b, "START TIME: %s\n", startTime.UTC().Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintf(&b, "LABEL: %s\n", label)
	fmt.Fprintf(&b, "START TIMELINE: %d\n", tli)
	return b.Bytes()
}

// evalPgBackupStart implements pg_backup_start(label text, fast boolean
// DEFAULT false) → pg_lsn: refuse a second backup in the session, take a
// checkpoint (immediate either way here — goopg's CheckpointNow has no
// spread mode), and return the checkpoint's redo LSN.
func evalPgBackupStart(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) < 1 || len(x.Args) > 2 {
		return Datum{}, &ExecError{Code: "42883", Message: "function pg_backup_start does not exist"}
	}
	labelD, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if labelD.IsNull() {
		return NullDatum, nil // STRICT
	}
	if backupGUC(ctx, "wal_level") == "minimal" {
		return Datum{}, &ExecError{Code: "55000",
			Message: "WAL level not sufficient for making an online backup",
			Hint:    "\"wal_level\" must be set to \"replica\" or \"logical\" at server start."}
	}
	if len(x.Args) == 2 {
		fastD, err := evalExprSlot(x.Args[1], slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if fastD.IsNull() {
			return NullDatum, nil
		}
	}
	label := labelD.StringValue()
	if SessionBackupRunning(ctx.ProcNum) {
		return Datum{}, &ExecError{Code: "55000", Message: "a backup is already in progress in this session"}
	}
	if len(label) >= maxBackupLabelLen {
		return Datum{}, &ExecError{Code: "22023",
			Message: fmt.Sprintf("backup label too long (max %d bytes)", maxBackupLabelLen)}
	}
	if ctx.Checkpointer != nil {
		if err := ctx.Checkpointer.CheckpointNow(); err != nil {
			return Datum{}, &ExecError{Code: "XX000", Message: fmt.Sprintf("checkpoint request failed: %v", err)}
		}
	}
	// The checkpointer reports goopg's 1-based LSNs; backup_label and the
	// function result carry PG byte LSNs (BASE_BACKUP converts the same way).
	var redo, rec uint64
	if ctx.Checkpointer != nil {
		redo = ctx.Checkpointer.CheckpointRedoLSN()
		rec = ctx.Checkpointer.LastCheckpointRecordLSN()
	}
	if redo == 0 && ctx.WAL != nil {
		redo = ctx.WAL.WrittenLSN()
	}
	if rec == 0 {
		rec = redo
	}
	b := &sessionBackup{label: label, startTime: time.Now()}
	if redo > 0 {
		b.startLSN = redo - 1
	}
	if rec > 0 {
		b.ckptLSN = rec - 1
	}
	if ctx.WAL != nil {
		b.tli = ctx.WAL.TimelineID()
	}
	if b.tli == 0 {
		b.tli = 1
	}
	sessionBackups.Store(ctx.ProcNum, b)
	return NewStringDatum(formatPgLSN(b.startLSN)), nil
}

// evalPgBackupStop implements pg_backup_stop(wait_for_archive boolean
// DEFAULT true) → record (lsn pg_lsn, labelfile text, spcmapfile text):
// without a running session backup it is "backup is not in progress";
// otherwise it ends the backup, switches to a new WAL segment (do_pg_backup_stop's
// RequestXLogSwitch, so the backup's last segment can be archived) and
// returns the stop LSN with the backup_label and tablespace_map contents.
func evalPgBackupStop(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) > 1 {
		return Datum{}, &ExecError{Code: "42883", Message: "function pg_backup_stop does not exist"}
	}
	waitForArchive := true
	if len(x.Args) == 1 {
		d, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if d.IsNull() {
			return NullDatum, nil // STRICT
		}
		waitForArchive = d.BoolValue()
	}
	v, ok := sessionBackups.Load(ctx.ProcNum)
	if !ok {
		return Datum{}, &ExecError{Code: "55000", Message: "backup is not in progress",
			Hint: "Did you call pg_backup_start()?"}
	}
	b := v.(*sessionBackup)
	var stop uint64
	stopTLI := b.tli
	segSize := xlog.WALSegmentSize()
	walDir := ""
	if ctx.DataDir != "" {
		walDir = filepath.Join(ctx.DataDir, "pg_wal")
	}
	if ctx.WAL != nil {
		segSize = ctx.WAL.SegmentSize()
		stop = ctx.WAL.WrittenLSN()
		if !ctx.IsStandby {
			// do_pg_backup_stop on a primary: the XLOG_BACKUP_END record —
			// recovery from the backup is consistent once it replays it —
			// whose end is the stop point, then a segment switch so the
			// backup's last segment can be archived, then the backup
			// history file, announced to the archiver.
			rec, err := xlog.EncodeBackupEndPG(b.startLSN)
			if err != nil {
				return Datum{}, &ExecError{Code: "XX000", Message: err.Error()}
			}
			_, end, err := ctx.WAL.Append(rec)
			if err != nil {
				return Datum{}, &ExecError{Code: "XX000", Message: err.Error()}
			}
			stop, stopTLI = end, ctx.WAL.TimelineID()
			if _, err := ctx.WAL.SwitchSegment(); err != nil {
				return Datum{}, &ExecError{Code: "XX000", Message: err.Error()}
			}
			if walDir != "" {
				hist := xlog.BackupHistoryFileName(stopTLI, b.startLSN, segSize)
				content := BuildBackupHistory(b.label, b.startLSN, b.ckptLSN, b.tli, stop, stopTLI, segSize, b.startTime, time.Now())
				if err := os.WriteFile(filepath.Join(walDir, hist), content, 0o600); err != nil {
					return Datum{}, &ExecError{Code: "58030", Message: fmt.Sprintf("could not write file \"%s\": %v", filepath.Join("pg_wal", hist), err)}
				}
				ctx.WAL.Archiver().Notify(hist)
			}
		}
	}
	sessionBackups.Delete(ctx.ProcNum)
	mode := backupGUC(ctx, "archive_mode")
	archiving := (!ctx.IsStandby && (mode == "on" || mode == "always")) || (ctx.IsStandby && mode == "always")
	if waitForArchive && archiving && walDir != "" && ctx.WAL != nil {
		// Wait until the backup's last segment and its history file are
		// archived; the alphabetic order of WAL file names covers the
		// earlier segments.
		last := xlog.XLogFileName(stopTLI, (stop-1)/uint64(segSize), segSize)
		hist := xlog.BackupHistoryFileName(stopTLI, b.startLSN, segSize)
		if err := waitForBackupArchive(ctx, walDir, last, hist); err != nil {
			return Datum{}, err
		}
		ctx.AddNotice("all required WAL segments have been archived")
	} else if waitForArchive {
		ctx.AddNotice("WAL archiving is not enabled; you must ensure that all required WAL segments are copied through other means to complete the backup")
	}
	labelFile := BuildBackupLabel(b.label, b.startLSN, b.ckptLSN, b.tli, segSize, b.startTime)
	return NewStringDatum("(" + formatPgLSN(stop) + "," + recordOutField(string(labelFile)) + "," + recordOutField("") + ")"), nil
}

// waitForBackupArchive is do_pg_backup_stop's archive wait: poll once a
// second until neither file is busy, with PG's NOTICE after five waits and
// WARNING after 60 s (doubling). A cancelled statement ends the wait.
func waitForBackupArchive(ctx *Context, walDir, last, hist string) error {
	waits, warnAfter, reported := 0, 60, false
	for xlog.XLogArchiveIsBusy(walDir, last) || xlog.XLogArchiveIsBusy(walDir, hist) {
		if !reported && waits > 5 {
			ctx.AddNotice("base backup done, waiting for required WAL segments to be archived")
			reported = true
		}
		if ctx.Ctx != nil {
			select {
			case <-ctx.Ctx.Done():
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			case <-time.After(time.Second):
			}
		} else {
			time.Sleep(time.Second)
		}
		waits++
		if waits >= warnAfter {
			warnAfter *= 2
			ctx.AddWarning(fmt.Sprintf("still waiting for all required WAL segments to be archived (%d seconds elapsed)", waits))
		}
	}
	return nil
}

// BuildBackupHistory renders the backup history file do_pg_backup_stop
// writes into pg_wal: the backup_label contents plus the stop location
// (build_backup_content with ishistoryfile).
func BuildBackupHistory(label string, startLSN, ckptLSN uint64, tli uint32, stopLSN uint64, stopTLI uint32, segSize int64, startTime, stopTime time.Time) []byte {
	lines := strings.SplitAfter(string(BuildBackupLabel(label, startLSN, ckptLSN, tli, segSize, startTime)), "\n")
	stopFile := xlog.XLogFileName(stopTLI, stopLSN/uint64(segSize), segSize)
	var b bytes.Buffer
	b.WriteString(lines[0])
	fmt.Fprintf(&b, "STOP WAL LOCATION: %s (file %s)\n", formatPgLSN(stopLSN), stopFile)
	for _, l := range lines[1:] {
		b.WriteString(l)
	}
	fmt.Fprintf(&b, "STOP TIME: %s\n", stopTime.UTC().Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintf(&b, "STOP TIMELINE: %d\n", stopTLI)
	return b.Bytes()
}

// backupGUC reads a GUC's current value ("" when the context has no settings).
func backupGUC(ctx *Context, name string) string {
	if ctx.GetSetting == nil {
		return ""
	}
	v, _ := ctx.GetSetting(name)
	return v
}

// recordOutField quotes one field of a composite's text form the way
// record_out does: an empty string or one holding a double quote,
// backslash, parenthesis, comma or whitespace is double-quoted, with
// quotes and backslashes doubled.
func recordOutField(s string) string {
	if s != "" && !strings.ContainsAny(s, "\"\\(),\t\n\v\f\r ") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\\' {
			b.WriteByte(c)
		}
		b.WriteByte(c)
	}
	b.WriteByte('"')
	return b.String()
}

package executor

import (
	"fmt"
	"log/slog"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/optimizer"
)

// WAL position functions (xlogfuncs.c): pg_current_wal_lsn reports
// GetXLogWriteRecPtr (written to the kernel), pg_current_wal_insert_lsn
// GetXLogInsertRecPtr (the end of the last inserted record) and
// pg_current_wal_flush_lsn GetFlushRecPtr (durable). All three refuse to run
// during recovery.
//
// goopg's Writer reports the same three positions as PG LSNs (byte offsets of
// a record's end): WrittenLSN is everything appended, DrainedLSN what reached
// the segment files, FlushedLSN what is durable. They are clamped so that
// flush <= write <= insert holds as it does in PG, even when two reads race an
// append or a flush.

func evalPgCurrentWALLSN(name string, ctx *Context) (Datum, error) {
	if ctx.IsStandby {
		return Datum{}, &ExecError{
			Code:    "55000",
			Message: "recovery is in progress",
			Hint:    "WAL control functions cannot be executed during recovery.",
		}
	}
	var flush, write, insert uint64
	if w := ctx.WAL; w != nil {
		flush = w.FlushedLSN()
		write = max(w.DrainedLSN(), flush)
		insert = max(w.WrittenLSN(), write)
	}
	switch name {
	case "pg_current_wal_insert_lsn":
		return NewStringDatum(formatPgLSN(insert)), nil
	case "pg_current_wal_flush_lsn":
		return NewStringDatum(formatPgLSN(flush)), nil
	default:
		return NewStringDatum(formatPgLSN(write)), nil
	}
}

// evalPgLastWALLSN implements pg_last_wal_receive_lsn and
// pg_last_wal_replay_lsn (xlogfuncs.c). The first is the WAL the standby's
// walreceiver has received (GetWalRcvFlushRecPtr); the second the end of the
// last record replayed (GetXLogReplayRecPtr, lastReplayedEndRecPtr). Each is
// NULL when its position is 0: no streaming yet, or a server that never ran
// recovery. Unlike pg_current_wal_*, both may run during recovery.
func evalPgLastWALLSN(name string, ctx *Context) Datum {
	if ctx.RecoveryLSNs == nil {
		return NullDatum
	}
	receive, replay := ctx.RecoveryLSNs()
	lsn := receive
	if name == "pg_last_wal_replay_lsn" {
		lsn = replay
	}
	if lsn == 0 {
		return NullDatum
	}
	return NewStringDatum(formatPgLSN(lsn))
}

// evalPgSwitchWAL implements pg_switch_wal (xlogfuncs.c): it refuses to run
// during recovery and returns RequestXLogSwitch's position, the end of the
// XLOG_SWITCH record (or the segment boundary when the current segment is
// still empty). M0149-0076.
func evalPgSwitchWAL(ctx *Context) (Datum, error) {
	if ctx.IsStandby {
		return Datum{}, &ExecError{
			Code:    "55000",
			Message: "recovery is in progress",
			Hint:    "WAL control functions cannot be executed during recovery.",
		}
	}
	if ctx.WAL == nil {
		return NewStringDatum(formatPgLSN(0)), nil
	}
	lsn, err := ctx.WAL.SwitchSegment()
	if err != nil {
		return Datum{}, &ExecError{Code: "XX000", Message: err.Error()}
	}
	return NewStringDatum(formatPgLSN(lsn)), nil
}

// evalPgWalReplayPause implements pg_wal_replay_pause, pg_wal_replay_resume
// and pg_get_wal_replay_pause_state (xlogfuncs.c). Outside recovery they raise
// 55000 "recovery is not in progress"; once promotion is triggered pause and
// resume raise "standby promotion is ongoing". M0149-0013.
func evalPgWalReplayPause(name string, ctx *Context) (Datum, error) {
	if !ctx.IsStandby || ctx.RecoveryPause == nil {
		return Datum{}, &ExecError{
			Code:    "55000",
			Message: "recovery is not in progress",
			Hint:    "Recovery control functions can only be executed during recovery.",
		}
	}
	action := "state"
	switch name {
	case "pg_wal_replay_pause":
		action = "pause"
	case "pg_wal_replay_resume":
		action = "resume"
	}
	state, promoting := ctx.RecoveryPause(action)
	if promoting && action != "state" {
		return Datum{}, &ExecError{
			Code:    "55000",
			Message: "standby promotion is ongoing",
			Hint:    name + "() cannot be executed after promotion is triggered.",
		}
	}
	if action == "state" {
		return NewStringDatum(state), nil
	}
	return NewStringDatum(""), nil
}

// evalPgWalfileName is xlogfuncs.c pg_walfile_name: the name of the WAL
// file holding lsn (XLByteToSeg, as PG 18 computes it, so an LSN exactly at
// a segment boundary names the segment that starts there) on the current
// insert timeline.
// Like the other WAL control functions it refuses to run during recovery.
// M0149-0017 (026_overwrite_contrecord).
func evalPgWalfileName(d Datum, ctx *Context) (Datum, error) {
	if ctx.IsStandby {
		return Datum{}, &ExecError{
			Code:    "55000",
			Message: "recovery is in progress",
			Hint:    "pg_walfile_name() cannot be executed during recovery.",
		}
	}
	lsn, err := parsePgLSN(d.StringValue())
	if err != nil {
		return Datum{}, &ExecError{Code: "22P02", Message: err.Error()}
	}
	segSize := uint64(16 * 1024 * 1024)
	tli := uint32(1)
	if w := ctx.WAL; w != nil {
		if s := w.SegmentSize(); s > 0 {
			segSize = uint64(s)
		}
		if t := w.TimelineID(); t != 0 {
			tli = t
		}
	}
	return NewStringDatum(xlogFileName(tli, lsn/segSize, segSize)), nil
}

// xlogFileName is xlog_internal.h XLogFileName.
func xlogFileName(tli uint32, segno, segSize uint64) string {
	perID := uint64(0x100000000) / segSize
	return fmt.Sprintf("%08X%08X%08X", tli, uint32(segno/perID), uint32(segno%perID))
}

// evalPgCreateRestorePoint implements pg_create_restore_point(text) →
// pg_lsn (xlogfuncs.c): it writes an XLOG_RESTORE_POINT record carrying the
// name — a recovery_target_name archive recovery and standbys can stop at —
// logs "restore point ... created at ...", and returns the record's end.
// M0149-0023a.
func evalPgCreateRestorePoint(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 1 {
		return Datum{}, &ExecError{Code: "42883", Message: "function pg_create_restore_point does not exist"}
	}
	v, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil || v.IsNull() {
		return NullDatum, err // STRICT
	}
	if ctx.IsStandby {
		return Datum{}, &ExecError{
			Code:    "55000",
			Message: "recovery is in progress",
			Hint:    "WAL control functions cannot be executed during recovery.",
		}
	}
	if backupGUC(ctx, "wal_level") == "minimal" {
		return Datum{}, &ExecError{
			Code:    "55000",
			Message: "WAL level not sufficient for creating a restore point",
			Hint:    `"wal_level" must be set to "replica" or "logical" at server start.`,
		}
	}
	name := v.StringValue()
	if len(name) > xlog.MaxRestorePointName {
		return Datum{}, &ExecError{
			Code:    "22023",
			Message: fmt.Sprintf("value too long for restore point (maximum %d characters)", xlog.MaxRestorePointName),
		}
	}
	if ctx.WAL == nil {
		return NewStringDatum(formatPgLSN(0)), nil
	}
	rec, err := xlog.EncodeRestorePointPG(name, xlog.TimestampTzNow())
	if err != nil {
		return Datum{}, &ExecError{Code: "XX000", Message: err.Error()}
	}
	_, end, err := ctx.WAL.Append(rec)
	if err != nil {
		return Datum{}, &ExecError{Code: "XX000", Message: err.Error()}
	}
	slog.Info(fmt.Sprintf("restore point \"%s\" created at %s", name, formatPgLSN(end)))
	return NewStringDatum(formatPgLSN(end)), nil
}

package xlog

import (
	"encoding/binary"
	"log/slog"
	"sync/atomic"
)

// LogLevelFatal is the server log level of a FATAL report: slog has no
// level above ERROR, so FATAL is ERROR+4 (the text handler prints
// "level=ERROR+4"; the TAP harness renders it as PG's "FATAL:").
// M0149-0020.
const LogLevelFatal = slog.LevelError + 4

// RecoveryFatalError is an ereport(FATAL) raised while replaying WAL during
// archive recovery: the server must stop rather than continue. Msg, Detail
// and Hint are PG's texts.
type RecoveryFatalError struct {
	Msg, Detail, Hint string
}

func (e *RecoveryFatalError) Error() string { return e.Msg }

// archiveRecoveryRequested is ArchiveRecoveryRequested (xlogrecovery.c): a
// recovery.signal or standby.signal was present at startup. Crash recovery
// alone leaves it false.
var archiveRecoveryRequested atomic.Bool

// SetArchiveRecoveryRequested records whether startup found a recovery or
// standby signal file.
func SetArchiveRecoveryRequested(on bool) { archiveRecoveryRequested.Store(on) }

// sizeOfXlParameterChange is sizeof(xl_parameter_change): six int32, two
// bools, padded to 4.
const sizeOfXlParameterChange = 28

// EncodeParameterChangePG builds the RM_XLOG XLOG_PARAMETER_CHANGE record
// XLogReportParameters writes when a server starts with settings that differ
// from pg_control's (xlog.c).
func EncodeParameterChangePG(p GUCParameters) ([]byte, error) {
	main := make([]byte, sizeOfXlParameterChange)
	le := binary.LittleEndian
	le.PutUint32(main[0:], uint32(p.MaxConnections))
	le.PutUint32(main[4:], uint32(p.MaxWorkerProcesses))
	le.PutUint32(main[8:], uint32(p.MaxWalSenders))
	le.PutUint32(main[12:], uint32(p.MaxPreparedXacts))
	le.PutUint32(main[16:], uint32(p.MaxLocksPerXact))
	le.PutUint32(main[20:], uint32(p.WalLevel))
	if p.WalLogHints {
		main[24] = 1
	}
	if p.TrackCommitTimestamp {
		main[25] = 1
	}
	return framedAssemble(RmgrXLog, xlogXLogParameterChange, 0, main, nil)
}

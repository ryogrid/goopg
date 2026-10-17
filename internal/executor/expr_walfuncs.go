package executor

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

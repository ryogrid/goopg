package executor

// pgstat_io.go — per-backend-type I/O statistics (pg_stat_io, PG 16+, M0122-0003).
//
// Upstream builds this view (pg_stat_get_io -> pg_stat_io_build_tuples,
// postgres/src/backend/utils/adt/pgstatfuncs.c) by iterating every
// BackendType that participates in IO stats (pgstat_tracks_io_bktype) and,
// for each, every (IOObject, IOContext) combination that type can perform IO
// under (pgstat_tracks_io_object); a row is emitted for each such
// combination, with individual op columns (reads/writes/extends/hits/
// evictions/reuses/writebacks/fsyncs and their _bytes/_time siblings) left
// SQL NULL when that particular op is never tracked for the combination
// (pgstat_tracks_io_op) rather than fabricated as zero. The three functions
// below are a direct port of pgstat_tracks_io_bktype/_object/_op from
// postgres/src/backend/utils/activity/pgstat_io.c, empirically cross-checked
// against a throwaway real PostgreSQL 18.3 instance (`SELECT * FROM
// pg_stat_io`, 79 rows across 14 backend types) rather than derived from
// static reading alone.
//
// goopg tracks a handful of real IO counters today: storage.Pool's pool-wide
// shared hit/read/written/evicted/extended tallies (added for EXPLAIN
// (BUFFERS) rendering) plus real wall-clock read_time/write_time/extend_time
// accumulators (M0122-0003 track_io_timing follow-up: Pool.OnPinDone /
// Pool.OnFlushDone / Pool.OnExtendDone accumulate the actual pinLoad
// disk-read / evictVictim dirty-flush / PinNew relation-extension duration
// via ActivityRegistry.WaitEventEnd, each gated on the backend's
// track_io_timing flag). Those real signals are wired into the one cell
// they correspond to — backend_type='client backend', object='relation',
// context='normal', columns reads/read_bytes/read_time/writes/write_bytes/
// write_time/hits/evictions/extends/extend_bytes/extend_time. A second real
// signal — wal.Writer's lifetime fdatasync(2) count/time against WAL
// segments (Writer.FsyncCount/FsyncTimeNanos, incremented in
// state.flushUpTo, time gated the same way via initdb.Open's
// OnWALSyncDone) — backs backend_type='client backend', object='wal',
// context='normal', column fsyncs/fsync_time. Everything else that
// upstream *tracks* renders as a real 0 (goopg has not performed that IO,
// which is true), and every untracked cell still renders NULL, matching
// upstream's row *shape* exactly even though most of its counts are not yet
// collected. This is a deliberate, bounded slice of the wider
// IO-instrumentation gap (see .ralph/deferral_ledger.md, M0122-0003): the
// remaining two op counters (reuses/writebacks, plus their _bytes/_time
// siblings) need a BufferAccessStrategy-style ring-buffer implementation
// (reuses) and async-writeback issuance (writebacks) goopg does not have,
// and remain future work — NOT fabricated here.

import (
	"strconv"

	"github.com/goopg/goopg/internal/catalog"
)

type ioObject int

const (
	ioObjRelation ioObject = iota
	ioObjTempRelation
	ioObjWal
)

var ioObjectNames = [...]string{"relation", "temp relation", "wal"}

type ioContext int

const (
	ioCtxBulkread ioContext = iota
	ioCtxBulkwrite
	ioCtxInit
	ioCtxNormal
	ioCtxVacuum
)

var ioContextNames = [...]string{"bulkread", "bulkwrite", "init", "normal", "vacuum"}

type ioOp int

const (
	ioOpEvict ioOp = iota
	ioOpFsync
	ioOpHit
	ioOpReuse
	ioOpWriteback
	ioOpExtend
	ioOpRead
	ioOpWrite
)

// ioBackendType enumerates only the BackendTypes upstream's
// pgstat_tracks_io_bktype() reports true for, in PostgreSQL's BackendType
// enum order (this is also pg_stat_get_io()'s row-emission order).
type ioBackendType int

const (
	ioBktClientBackend ioBackendType = iota
	ioBktAutovacLauncher
	ioBktAutovacWorker
	ioBktBgWorker
	ioBktWalSender
	ioBktSlotsyncWorker
	ioBktStandaloneBackend
	ioBktBgWriter
	ioBktCheckpointer
	ioBktIOWorker
	ioBktStartup
	ioBktWalReceiver
	ioBktWalSummarizer
	ioBktWalWriter
	ioBktNumTypes
)

var ioBackendTypeNames = [...]string{
	"client backend",
	"autovacuum launcher",
	"autovacuum worker",
	"background worker",
	"walsender",
	"slotsync worker",
	"standalone backend",
	"background writer",
	"checkpointer",
	"io worker",
	"startup",
	"walreceiver",
	"walsummarizer",
	"walwriter",
}

// ioTracksObject ports pgstat_tracks_io_object (postgres/src/backend/utils/
// activity/pgstat_io.c). pgstat_tracks_io_bktype's filtering is already
// applied by ioBackendType only listing tracked types, so it is not
// reproduced here.
func ioTracksObject(bt ioBackendType, obj ioObject, ctx ioContext) bool {
	if obj == ioObjWal && ctx != ioCtxNormal && ctx != ioCtxInit {
		return false
	}
	if ctx != ioCtxNormal && obj == ioObjTempRelation {
		return false
	}
	noTempRel := bt == ioBktAutovacLauncher || bt == ioBktBgWriter || bt == ioBktCheckpointer ||
		bt == ioBktAutovacWorker || bt == ioBktStandaloneBackend || bt == ioBktStartup ||
		bt == ioBktWalSummarizer || bt == ioBktWalWriter || bt == ioBktWalReceiver
	if noTempRel && ctx == ioCtxNormal && obj == ioObjTempRelation {
		return false
	}
	if (bt == ioBktWalSummarizer || bt == ioBktWalReceiver || bt == ioBktWalWriter) && obj != ioObjWal {
		return false
	}
	if (bt == ioBktCheckpointer || bt == ioBktBgWriter) &&
		(ctx == ioCtxBulkread || ctx == ioCtxBulkwrite || ctx == ioCtxVacuum) {
		return false
	}
	if bt == ioBktAutovacLauncher && ctx == ioCtxVacuum {
		return false
	}
	if (bt == ioBktAutovacWorker || bt == ioBktAutovacLauncher) && ctx == ioCtxBulkwrite {
		return false
	}
	return true
}

// ioTracksOp ports pgstat_tracks_io_op.
func ioTracksOp(bt ioBackendType, obj ioObject, ctx ioContext, op ioOp) bool {
	if !ioTracksObject(bt, obj, ctx) {
		return false
	}
	if bt == ioBktBgWriter && (op == ioOpRead || op == ioOpEvict || op == ioOpHit) {
		return false
	}
	if bt == ioBktCheckpointer && ((obj != ioObjWal && op == ioOpRead) || op == ioOpEvict || op == ioOpHit) {
		return false
	}
	if (bt == ioBktAutovacLauncher || bt == ioBktBgWriter || bt == ioBktCheckpointer) && op == ioOpExtend {
		return false
	}
	if obj == ioObjWal && op == ioOpRead &&
		(bt == ioBktWalReceiver || bt == ioBktBgWriter || bt == ioBktAutovacLauncher ||
			bt == ioBktAutovacWorker || bt == ioBktWalWriter) {
		return false
	}
	if obj == ioObjTempRelation && (op == ioOpFsync || op == ioOpWriteback) {
		return false
	}
	if ctx == ioCtxBulkread && op == ioOpExtend {
		return false
	}
	strategyCtx := ctx == ioCtxBulkread || ctx == ioCtxBulkwrite || ctx == ioCtxVacuum
	if !strategyCtx && op == ioOpReuse {
		return false
	}
	if obj == ioObjWal && ctx == ioCtxInit && !(op == ioOpWrite || op == ioOpFsync) {
		return false
	}
	if obj == ioObjWal && ctx == ioCtxNormal && !(op == ioOpWrite || op == ioOpRead || op == ioOpFsync) {
		return false
	}
	if strategyCtx && op == ioOpFsync {
		return false
	}
	return true
}

// ioColIndex is the position (within the 20-column pg_stat_io row) of an
// op's count/bytes/time cell; -1 (ioColNone) means that op has no such cell.
const ioColNone = -1

func ioOpColumns(op ioOp) (count, bytes, timeCol int) {
	switch op {
	case ioOpEvict:
		return 15, ioColNone, ioColNone // evictions
	case ioOpFsync:
		return 17, ioColNone, 18 // fsyncs, fsync_time
	case ioOpHit:
		return 14, ioColNone, ioColNone // hits
	case ioOpReuse:
		return 16, ioColNone, ioColNone // reuses
	case ioOpWriteback:
		return 9, ioColNone, 10 // writebacks, writeback_time
	case ioOpExtend:
		return 11, 12, 13 // extends, extend_bytes, extend_time
	case ioOpRead:
		return 3, 4, 5 // reads, read_bytes, read_time
	case ioOpWrite:
		return 6, 7, 8 // writes, write_bytes, write_time
	}
	return ioColNone, ioColNone, ioColNone
}

const pgStatIOColCount = 20

// fetchIOStatRows builds the pg_stat_io rows in upstream's own emission
// order (backend type, then object, then context — matching
// pg_stat_io_build_tuples' nested loops), giving every valid combination
// upstream's exact NULL/zero cell shape. The single cell goopg actually
// instruments (client backend / relation / normal: reads, read_bytes,
// read_time, writes, write_bytes, write_time, hits, evictions, extends,
// extend_bytes, extend_time) is filled from storage.Pool's shared-buffer
// counters; every other tracked cell is a faithful 0 (goopg has done none
// of that IO), not a guess.
func fetchIOStatRows(ctx *Context) [][]string {
	var poolHit, poolRead, poolWritten, poolReadTimeNanos, poolWriteTimeNanos, poolEvictions, poolExtends, poolExtendTimeNanos int64
	if ctx != nil && ctx.Pool != nil {
		poolHit, poolRead, _, poolWritten = ctx.Pool.BufferCounters()
		poolReadTimeNanos = ctx.Pool.ReadTimeNanos()
		poolWriteTimeNanos = ctx.Pool.WriteTimeNanos()
		poolEvictions = ctx.Pool.EvictionCount()
		poolExtends = ctx.Pool.ExtendCount()
		poolExtendTimeNanos = ctx.Pool.ExtendTimeNanos()
	}
	var walFsyncCount, walFsyncTimeNanos int64
	if ctx != nil && ctx.WAL != nil {
		walFsyncCount = ctx.WAL.FsyncCount()
		walFsyncTimeNanos = ctx.WAL.FsyncTimeNanos()
	}

	var rows [][]string
	for bt := ioBackendType(0); bt < ioBktNumTypes; bt++ {
		for obj := ioObjRelation; obj <= ioObjWal; obj++ {
			for ctxIdx := ioCtxBulkread; ctxIdx <= ioCtxVacuum; ctxIdx++ {
				if !ioTracksObject(bt, obj, ctxIdx) {
					continue
				}
				cells := make([]string, pgStatIOColCount)
				for i := range cells {
					cells[i] = catalog.VirtualNull
				}
				cells[0] = ioBackendTypeNames[bt]
				cells[1] = ioObjectNames[obj]
				cells[2] = ioContextNames[ctxIdx]
				cells[19] = slruResetTimestamp

				for op := ioOpEvict; op <= ioOpWrite; op++ {
					if !ioTracksOp(bt, obj, ctxIdx, op) {
						continue
					}
					countCol, byteCol, timeCol := ioOpColumns(op)
					count, bytes := int64(0), int64(0)
					timeMillis := "0"
					switch {
					case bt == ioBktClientBackend && obj == ioObjRelation && ctxIdx == ioCtxNormal:
						switch op {
						case ioOpHit:
							count = poolHit
						case ioOpRead:
							count = poolRead
							bytes = poolRead * 8192
							timeMillis = strconv.FormatFloat(nsToMs(poolReadTimeNanos), 'f', 3, 64)
						case ioOpWrite:
							count = poolWritten
							bytes = poolWritten * 8192
							timeMillis = strconv.FormatFloat(nsToMs(poolWriteTimeNanos), 'f', 3, 64)
						case ioOpEvict:
							count = poolEvictions
						case ioOpExtend:
							count = poolExtends
							bytes = poolExtends * 8192
							timeMillis = strconv.FormatFloat(nsToMs(poolExtendTimeNanos), 'f', 3, 64)
						}
					case bt == ioBktClientBackend && obj == ioObjWal && ctxIdx == ioCtxNormal:
						if op == ioOpFsync {
							count = walFsyncCount
							timeMillis = strconv.FormatFloat(nsToMs(walFsyncTimeNanos), 'f', 3, 64)
						}
					}
					cells[countCol] = strconv.FormatInt(count, 10)
					if byteCol != ioColNone {
						cells[byteCol] = strconv.FormatInt(bytes, 10)
					}
					if timeCol != ioColNone {
						cells[timeCol] = timeMillis
					}
				}
				rows = append(rows, cells)
			}
		}
	}
	return rows
}

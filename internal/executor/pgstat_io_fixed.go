package executor

import (
	"encoding/binary"
	"sync"
	"time"
)

// pgBackendTypeCount is BACKEND_NUM_TYPES (miscadmin.h BackendType, B_INVALID
// through B_LOGGER): PgStat_IO keeps one PgStat_BktypeIO per BackendType.
const pgBackendTypeCount = 18

// pgBackendTypeOf maps goopg's tracked backend types to PG's BackendType
// values, which index PgStat_IO.stats.
var pgBackendTypeOf = [ioBktNumTypes]int{
	ioBktClientBackend:     1,  // B_BACKEND
	ioBktAutovacLauncher:   3,  // B_AUTOVAC_LAUNCHER
	ioBktAutovacWorker:     4,  // B_AUTOVAC_WORKER
	ioBktBgWorker:          5,  // B_BG_WORKER
	ioBktWalSender:         6,  // B_WAL_SENDER
	ioBktSlotsyncWorker:    7,  // B_SLOTSYNC_WORKER
	ioBktStandaloneBackend: 8,  // B_STANDALONE_BACKEND
	ioBktBgWriter:          10, // B_BG_WRITER
	ioBktCheckpointer:      11, // B_CHECKPOINTER
	ioBktIOWorker:          12, // B_IO_WORKER
	ioBktStartup:           13, // B_STARTUP
	ioBktWalReceiver:       14, // B_WAL_RECEIVER
	ioBktWalSummarizer:     15, // B_WAL_SUMMARIZER
	ioBktWalWriter:         16, // B_WAL_WRITER
}

const (
	ioNumObjects  = 3
	ioNumContexts = 5
	ioNumOps      = 8
)

// pgBktypeIO is PgStat_BktypeIO: bytes, counts and times (microseconds),
// each [IOOBJECT_NUM_TYPES][IOCONTEXT_NUM_TYPES][IOOP_NUM_TYPES].
type pgBktypeIO struct {
	bytes, counts, times [ioNumObjects][ioNumContexts][ioNumOps]int64
}

// ioCumulative is the persisted part of pg_stat_io (PgStat_IO): IO that
// goopg attributes to a backend type other than a client backend's live
// buffer-pool counters — the standalone backend of `goopg init` and the
// startup process. It is written to and restored from the IO chunk of
// pg_stat/pgstat.stat. M0149-0007.
var ioCumulative struct {
	mu    sync.Mutex
	stats [pgBackendTypeCount]pgBktypeIO
	reset time.Time
}

// countIO is pgstat_count_io_op_time for a backend type goopg runs inline.
func countIO(bt ioBackendType, obj ioObject, ctx ioContext, op ioOp, cnt, bytes int64) {
	ioCumulative.mu.Lock()
	defer ioCumulative.mu.Unlock()
	s := &ioCumulative.stats[pgBackendTypeOf[bt]]
	s.counts[obj][ctx][op] += cnt
	s.bytes[obj][ctx][op] += bytes
}

// ioCumulativeGet returns the persisted counters of one row cell.
func ioCumulativeGet(bt ioBackendType, obj ioObject, ctx ioContext, op ioOp) (cnt, bytes int64) {
	ioCumulative.mu.Lock()
	defer ioCumulative.mu.Unlock()
	s := &ioCumulative.stats[pgBackendTypeOf[bt]]
	return s.counts[obj][ctx][op], s.bytes[obj][ctx][op]
}

// pgstatIOChunkLen is sizeof(PgStat_IO): stat_reset_timestamp, then the
// per-BackendType PgStat_BktypeIO array.
const pgstatIOChunkLen = 8 + pgBackendTypeCount*3*ioNumObjects*ioNumContexts*ioNumOps*8

func encodeIOChunk() []byte {
	ioCumulative.mu.Lock()
	defer ioCumulative.mu.Unlock()
	return encodeIOStats(&ioCumulative.stats, ioCumulative.reset)
}

func encodeIOStats(stats *[pgBackendTypeCount]pgBktypeIO, reset time.Time) []byte {
	b := make([]byte, pgstatIOChunkLen)
	le := binary.LittleEndian
	le.PutUint64(b[0:], uint64(timestampTz(reset)))
	off := 8
	for bt := range stats {
		s := &stats[bt]
		for _, arr := range []*[ioNumObjects][ioNumContexts][ioNumOps]int64{&s.bytes, &s.counts, &s.times} {
			for o := 0; o < ioNumObjects; o++ {
				for c := 0; c < ioNumContexts; c++ {
					for op := 0; op < ioNumOps; op++ {
						le.PutUint64(b[off:], uint64(arr[o][c][op]))
						off += 8
					}
				}
			}
		}
	}
	return b
}

func decodeIOChunk(b []byte) (stats [pgBackendTypeCount]pgBktypeIO, reset time.Time) {
	le := binary.LittleEndian
	reset = fromTimestampTz(int64(le.Uint64(b[0:])))
	off := 8
	for bt := range stats {
		s := &stats[bt]
		for _, arr := range []*[ioNumObjects][ioNumContexts][ioNumOps]int64{&s.bytes, &s.counts, &s.times} {
			for o := 0; o < ioNumObjects; o++ {
				for c := 0; c < ioNumContexts; c++ {
					for op := 0; op < ioNumOps; op++ {
						arr[o][c][op] = int64(le.Uint64(b[off:]))
						off += 8
					}
				}
			}
		}
	}
	return stats, reset
}

func resetIOCumulative() {
	ioCumulative.mu.Lock()
	ioCumulative.stats = [pgBackendTypeCount]pgBktypeIO{}
	ioCumulative.reset = time.Now()
	ioCumulative.mu.Unlock()
}

// WriteInitStatsFile writes the statistics file `goopg init` leaves behind,
// as PG's initdb does when its standalone backend exits: no entries, and the
// standalone backend's IO for the bootstrap WAL segment it created — one
// write and one fsync of the whole segment in the init context
// (XLogFileInitInternal's pgstat_count_io_op_time). It is built from a clean
// slate, not from this process's stores. M0149-0007.
func WriteInitStatsFile(dataDir string, segmentBytes int64, now time.Time) error {
	var io [pgBackendTypeCount]pgBktypeIO
	sa := &io[pgBackendTypeOf[ioBktStandaloneBackend]]
	sa.counts[ioObjWal][ioCtxInit][ioOpWrite] = 1
	sa.bytes[ioObjWal][ioCtxInit][ioOpWrite] = segmentBytes
	sa.counts[ioObjWal][ioCtxInit][ioOpFsync] = 1
	return writeStatsFileRaw(dataDir, func(kind uint32, n int) []byte {
		switch kind {
		case pgstatKindIO:
			return encodeIOStats(&io, now)
		case pgstatKindCheckpointer:
			return encodeCheckpointerChunk(PgStatCheckpointer{StatResetTimestamp: now})
		case pgstatKindWAL:
			return encodeWalChunk(PgStatWal{StatResetTimestamp: now})
		}
		return make([]byte, n)
	})
}

// RecordStartupWALReads counts the WAL the startup process reads to find the
// redo point and replay up to the end of WAL: the XLOG_BLCKSZ pages from the
// page holding redoLSN through the page holding endLSN. M0149-0007.
func RecordStartupWALReads(redoLSN, endLSN uint64) {
	const xlogBlcksz = 8192
	if endLSN < redoLSN {
		endLSN = redoLSN
	}
	pages := int64(endLSN/xlogBlcksz-redoLSN/xlogBlcksz) + 1
	countIO(ioBktStartup, ioObjWal, ioCtxNormal, ioOpRead, pages, pages*xlogBlcksz)
}

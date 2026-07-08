package wal

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/stats"
)

// groupFlushReq represents a single caller's flush request in the
// group-commit queue. M0098-0002.
type groupFlushReq struct {
	lsn  uint64
	done chan struct{} // closed by writer goroutine when flush is complete
	err  error         // set before done is closed
}

// flushGroup holds the shared queue and signal channel for WAL group
// commit (M0098-0002). Multiple concurrent FlushUpTo callers append
// their requests and the writer goroutine drains the entire batch in
// one fdatasync.
type flushGroup struct {
	mu     sync.Mutex
	queue  []*groupFlushReq
	signal chan struct{} // capacity-1 channel; non-blocking send triggers writer
}

const (
	// DefaultSegmentSize matches PostgreSQL's default WAL segment size.
	DefaultSegmentSize int64 = 16 * 1024 * 1024

	// commitDelayUs is the number of microseconds handleGroupFlush sleeps
	// after the initial drain to accumulate more concurrent FlushUpTo callers
	// into the same fdatasync batch. Mirrors PostgreSQL's commit_delay GUC.
	// Re-enabled after the state.append Path A race was fixed (M0099).
	commitDelayUs = 1000

	// commitSiblings is the minimum number of concurrent waiters required
	// before applying the commitDelayUs sleep. Below this threshold we flush
	// immediately to avoid adding latency to low-concurrency workloads.
	commitSiblings = 5
)

var (
	ErrClosed        = errors.New("wal: writer closed")
	ErrLSNNotWritten = errors.New("wal: requested LSN is beyond written WAL")
	// ErrEmptyPayload guards the EOS sentinel: a zero
	// (len=0, crc=0) header is reserved as "no record here yet"
	// in preallocated segments. See
	// docs/design/0007-0001-wal-segment-preallocation.md.
	ErrEmptyPayload = errors.New("wal: empty payload not allowed")
	// ErrWALFormatMismatch is returned by NewWriter when the
	// existing WAL directory's on-disk format conflicts with the
	// writer's configured `PageHeaders` mode (M0014-0004 step 2):
	// e.g. the data dir holds legacy frames but the operator
	// requested pg-compat output, or vice versa. Mixing the two
	// formats inside one WAL stream would corrupt recovery, so the
	// writer refuses to open. Operators must either match the
	// config to the existing format or migrate the data dir.
	ErrWALFormatMismatch = errors.New("wal: configured format does not match existing data dir")
	// ErrUnsupportedSyncMethod is returned by NewWriter when
	// Config.SyncMethod names a wal_sync_method value this build
	// doesn't implement (open_sync / open_datasync / fsync_writethrough
	// require O_SYNC/O_DSYNC open-time flags or a Windows/macOS-only
	// primitive — see the wal_sync_method GUC registration comment in
	// internal/config/defaults.go). Mirrors internal/aio's
	// ErrUnsupportedMethod precedent for io_method=io_uring.
	ErrUnsupportedSyncMethod = errors.New("wal: unsupported wal_sync_method")
)

// Config controls writer and reader behavior.
type Config struct {
	WALDir      string
	SegmentSize int64

	// Preallocate, when true, zero-fills new segment files to
	// SegmentSize and fsyncs them at creation time so subsequent
	// commit-path syncs don't pay for inode metadata updates and
	// don't trigger filesystem allocations on the hot path.
	// Mirrors upstream's `wal_init_zero` GUC. Default false keeps
	// the legacy "grow lazily" behaviour for callers that haven't
	// migrated. See docs/design/0007-0001-wal-segment-preallocation.md.
	Preallocate bool

	// AIO is the optional AIO engine seam. When set,
	// state.writeAt submits per-segment writes through it so
	// pg_stat_aio / pg_aios show WAL writes alongside heap
	// writes. nil falls back to direct f.WriteAt — preserving
	// pre-AIO behaviour. The commit-path durability barrier
	// (`flushUpTo` → `dataSync`) remains a synchronous
	// fdatasync regardless of this field. Defined as a narrow
	// interface to avoid the wal → aio import (the engine pulls
	// in a heavier dependency graph that wal can't take on).
	AIO AIOEngine

	// WALBuffers sizes (in bytes) the bounded in-memory WAL
	// buffer Appends land in before they hit segment files.
	// 0 disables the buffer (every Append flows straight
	// through to writeAt — legacy / pre-M0013 behaviour).
	// Mirrors upstream's wal_buffers GUC. See
	// docs/design/0013-0001-wal-buffers-architecture.md.
	WALBuffers int64

	// SenderMemoryBuffer sizes (in bytes) the in-memory ring
	// of recent WAL bytes used by the walsender hot-path.
	// 0 disables the ring entirely; iterators always pread
	// from disk. >0 allocates a fixed-size byte ring that
	// captures every successful state.writeAt — RecordIterator
	// consults the ring before reaching for a segment file.
	// Mirrors the M0010 milestone's `wal_sender_memory_buffer`
	// GUC. Default 0 keeps the legacy disk-only path. See
	// docs/design/0010-0002-walsender-in-memory-wal-handoff.md.
	SenderMemoryBuffer int64

	// PageHeaders flips the writer's append path onto the
	// PG-compatible XLOG layout (M0014-0003): every
	// 8 KiB page boundary gets a 24-byte short page header, every
	// segment boundary gets a 40-byte long page header, and records
	// are framed as XLogRecord headers with xl_prev chaining and a
	// wrapped main-data payload so pg_waldump can parse emitted WAL.
	// Records crossing a page boundary stamp XLP_FIRST_IS_CONTRECORD
	// + xlp_rem_len on the next page. Default false keeps the legacy
	// flat len+CRC32-IEEE stream for pre-M0014 clusters/tests. See
	// docs/design/0014-0001-xlog-page-and-segment-layout-compat.md.
	PageHeaders bool

	// SystemID is the cluster's pg_control system identifier,
	// stamped into every long-form page header's xlp_sysid for
	// pg_waldump cross-check. Caller (cmd/goopg start) plumbs the
	// pg_control value through. Only consulted when PageHeaders=true.
	SystemID uint64

	// TimelineID is the cluster's current timeline, stamped into
	// every page header's xlp_tli. Defaults to 1 when zero — the
	// initial timeline a fresh cluster starts on. Only consulted
	// when PageHeaders=true.
	TimelineID uint32

	// OnLoopStart / OnLoopEnd are optional hooks called at the
	// beginning and end of the WAL state-loop goroutine, so the
	// caller can register/deregister the WAL writer goroutine in
	// the activity registry via RegisterCurrentGoroutine.
	OnLoopStart func()
	OnLoopEnd   func()

	// OnWALWrite is an optional hook called before each WAL page
	// write in the state loop.  Set by initdb.Open so the activity
	// registry can report WALWrite wait events.
	OnWALWrite func()

	// Logger, when non-nil, is used to emit INFO-level log lines
	// for each WAL flush (M0057-0001). Defaults to slog.Default()
	// when nil.
	Logger *slog.Logger

	// SyncMethod selects the commit-path durability barrier
	// flushUpTo's dataSync stage uses, mirroring upstream's
	// `wal_sync_method` GUC. "fdatasync" (default, empty string
	// resolves here) skips inode-metadata flushes now that
	// preallocated segments have a fixed size between commits;
	// "fsync" flushes inode metadata too. "open_sync"/"open_datasync"
	// are recognized (upstream allows them on Linux) but not yet
	// implemented — they need O_SYNC/O_DSYNC at segment-open time
	// across every WAL open site, not just this flush barrier — so
	// NewWriter rejects them with ErrUnsupportedSyncMethod, matching
	// internal/aio's io_method=io_uring precedent. See
	// docs/design/0007-0002-fdatasync-commit-path.md.
	SyncMethod string

	// MinWALSize mirrors upstream's `min_wal_size` GUC (bytes). It sets
	// the floor on how many WAL segments RemoveOldSegments keeps around
	// as pre-zeroed spares by renaming obsolete segments into fresh
	// future slots (recycle-by-rename) instead of unlinking them —
	// avoiding the create+zero-fill+directory-fsync cost a later
	// openSegment would otherwise pay for a brand-new file. <= 0 (the
	// default) disables recycling entirely: every obsolete segment is
	// unlinked, matching pre-M0122-0009 behaviour. See
	// docs/design/0122-0009-wal-segment-recycling.md.
	MinWALSize int64
}

// AIOEngine is the wal-side seam onto an AIO engine. Mirrors
// storage.AIOEngine; the adapter in initdb.Open wraps the same
// underlying *aio.Engine so reads, heap writes, and WAL writes
// all flow through one pool.
type AIOEngine interface {
	Submit(op AIOSubmitOp) AIOHandle
}

// AIOSubmitOp is the wal-side op shape — read/write directional
// against an AIOFile + offset. Buffer is the source for writes
// (and the destination for reads, though wal currently only
// submits writes).
type AIOSubmitOp struct {
	File      AIOFile
	Buffer    []byte
	Offset    int64
	Direction AIODirection
	// Target is a free-form descriptor (typically the WAL
	// segment path) the engine surfaces through
	// pg_aios.target_desc. Empty leaves the column blank.
	Target string
}

// AIOFile mirrors storage.AIOFile: a pread/pwrite-shaped
// handle. The wal writer passes its underlying *os.File through.
type AIOFile interface {
	ReadAt(p []byte, off int64) (int, error)
	WriteAt(p []byte, off int64) (int, error)
}

// AIOHandle mirrors storage.AIOHandle.
type AIOHandle interface {
	Wait() (n int, err error)
}

// AIODirection mirrors aio.Direction without taking the import.
type AIODirection int

const (
	AIODirRead AIODirection = iota
	AIODirWrite
)

func (c *Config) withDefaults() {
	if c.WALDir == "" {
		c.WALDir = filepath.Join(".", "pg_wal")
	}
	if c.SegmentSize <= 0 {
		c.SegmentSize = DefaultSegmentSize
	}
	if c.PageHeaders && c.TimelineID == 0 {
		c.TimelineID = 1
	}
	if c.SyncMethod == "" {
		c.SyncMethod = "fdatasync"
	}
}

type opKind int

const (
	opAppend opKind = iota
	opAppendRaw
	opFlush
	opRecycle
	opClose
	// opWALBufStat is a read-only op the M0013-0003 observability
	// surface uses to snapshot walBuf cap + resident under the
	// writer goroutine's serialisation (avoids racing append /
	// drain). The snapshot lands in result.walBufStat.
	opWALBufStat
)

type op struct {
	kind    opKind
	payload []byte
	lsn     uint64
	resp    chan result
}

type walBufStatSnapshot struct {
	cap      int64
	resident int64
}

type result struct {
	startLSN   uint64
	endLSN     uint64
	removed    int
	recycled   int
	walBufStat walBufStatSnapshot
	err        error
}

// Writer serializes all WAL writes through one internal goroutine.
//
// LSN is represented as a 1-based byte position in the WAL stream:
// the first byte written has LSN 1, and the zero value means
// "no WAL record assigned".
type Writer struct {
	ops  chan op
	done chan struct{}

	// writeLSNAtomic mirrors state.writeLSN so external observers
	// (notably the checkpointer's max_wal_size trigger) can read
	// the current write position without serialising through the
	// op channel.
	writeLSNAtomic atomic.Uint64

	// subscribers receive a non-blocking wake-up after every
	// successful Append. Used by RecordIterator (and walsender
	// goroutines) to block until more WAL is available instead of
	// polling WrittenLSN. Subscribers maintain their own buffer
	// channel; Writer drops the wake-up if the channel is full —
	// the iterator will catch up on the next event or by a manual
	// re-poll.
	subMu       sync.Mutex
	subscribers map[chan<- struct{}]struct{}

	// memRing is shared with state.memRing (one allocation,
	// two pointers). Exposed via MemRing() so the iterator
	// and observability code can read it without serialising
	// through the writer's loop. nil when
	memRing *MemRing

	// stateRef is a reference to the state struct, needed by
	// Writer.Append to call encodeAndBuffer directly (M0026
	// concurrent append path).
	stateRef *state

	// walBufferCounters shares atomics with state for the
	// M0013-0003 wal_buffers_* columns of pg_stat_wal_io.
	walBufferCounters *walBufferCounters

	// pageHeaders mirrors Config.PageHeaders so RecordIterator
	// (and any other reader holding a *Writer reference) can
	// decide whether to skip page-header bytes when streaming
	// records. Static for the Writer's lifetime.
	pageHeaders bool
	// segmentSize mirrors Config.SegmentSize so iterators can
	// distinguish a long-form (segment-boundary) page header
	// from a short-form one without having to thread cfg in.
	segmentSize int64

	// core is the M0107-0007 slice B WAL-insert striping packaging
	// struct (`docs/design/0107-0007v-wal-stripe-writer-core.md`).
	// Mounted here so the eventual call-site rewrite can replace
	// `state.append`'s body with `s.core.Append(procNum, encoded)`
	// and the drain goroutine's per-tick prelude with
	// `s.core.PublishUpTo(...)` — see
	// `docs/design/0107-0007w-wal-stripe-writer-core-mount.md`. The
	// core is constructed in `NewWriter` after `loadState` so it
	// borrows the same `walBuf` / `memRing` rings as the legacy path
	// (one allocation, two consumers — duplicating the rings would
	// fork on-disk visibility). Dead code until the call-site
	// rewrite consumes it; access in tests is guarded by the
	// `stripeCore()` accessor below.
	core *stripeWriterCore

	// OnWALWrite and OnWALSync are optional hooks called before
	// blocking WAL write / fdatasync operations.  The caller
	// (initdb.Open) sets them via activity.LookupGoroutine so
	// pg_stat_activity can report WALWrite / WALSync wait events.
	OnWALWrite func()
	OnWALSync  func()

	// OnWALSyncDone pairs with OnWALSync — called immediately after
	// the fdatasync completes (success or error) so observability
	// code can balance every WaitEventStart with a WaitEventEnd.
	// (M0058-0006.)
	OnWALSyncDone func()

	// fg is the group-commit queue. FlushUpTo appends to fg.queue
	// and signals fg.signal; the writer goroutine drains the entire
	// batch in one fdatasync. M0098-0002.
	fg *flushGroup

	// sharedFsyncTimeNanos accumulates real wall-clock time spent inside
	// FlushUpTo's fdatasync wait, backing pg_stat_io's (client backend,
	// wal, normal) fsync_time column (M0122-0003 track_io_timing
	// follow-up). initdb.Open's OnWALSyncDone hook calls
	// AddFsyncTimeNanos only when the calling backend's own
	// track_io_timing is on (via ActivityRegistry.LookupTrackedGoroutine),
	// mirroring storage.Pool's OnPinDone/AddReadTimeNanos gating exactly —
	// see AddFsyncTimeNanos's doc comment. Plain atomic.Int64 (not a
	// sharded stats.Counter) because fsyncs are already serialised through
	// group commit, unlike per-buffer read/write/extend ops.
	sharedFsyncTimeNanos atomic.Int64
}

type state struct {
	cfg Config

	writePos   int64
	writeLSN   uint64
	flushedLSN uint64

	// drainedLSN is the last byte position that has been written
	// to a segment file. With wal_buffers > 0, drainedLSN can lag
	// writeLSN by up to walBuf.cap bytes — those bytes live only
	// in walBuf until an overflow drain or flushUpTo Stage 1
	// pushes them to disk. With walBuf nil, drainedLSN tracks
	// writeLSN exactly. See M0013-0001.
	drainedLSN uint64

	// walBuf is the M0013-0001 in-memory WAL buffer. nil when
	// Config.WALBuffers == 0 (legacy behaviour: every Append
	// calls writeAt directly).
	walBuf *walBuffer

	// writeLSNMirror, when non-nil, gets the current writeLSN
	// stored after each successful append. The Writer reads it
	// without locking.
	writeLSNMirror *atomic.Uint64

	// onAppend, when non-nil, is invoked after every successful
	// append to wake subscribers (RecordIterators, walsender
	// goroutines). Non-blocking by contract.
	onAppend func()

	files map[uint64]*os.File
	dirty map[uint64]bool

	// aio mirrors Config.AIO so writeAt can submit per-segment
	// writes through the engine. nil → direct f.WriteAt path
	// (legacy / no-engine deployments).
	aio AIOEngine

	// appendMu coordinates access between concurrent tryAppend goroutines
	// and the state-loop's exclusive drain/Path-A paths.
	//
	// RLock (multiple holders OK): tryAppend PG-compat Path B — stripe
	//   writes are internally serialised by per-stripe locks; walBuf
	//   bytes are written to disjoint LSN ranges; head/base are stable.
	// Lock (exclusive): appendPGCompat Path A (direct disk write +
	//   resetPosition), appendPGCompat Path B (may drain walBuf),
	//   appendRaw (may drain + resetPosition), append non-pageHeaders,
	//   DrainedLSN/readBufferedAt readers that observe drainedLSN.
	appendMu sync.RWMutex

	// memRing mirrors recently-written WAL bytes in RAM so
	// walsender's RecordIterator can stream without paying for
	// disk reads. nil when Config.SenderMemoryBuffer == 0.
	memRing *MemRing

	// walBufferCounters mirrors Writer.walBufferCounters via
	// pointer — drainBufferBytes bumps these (M0013-0003).
	walBufferCounters *walBufferCounters

	// pageHeaders, when true, makes state.append interleave
	// PG-compatible page headers into the on-disk byte stream.
	// Mirrors Config.PageHeaders. See M0014-0001 step 2 in
	// docs/design/0014-0001-xlog-page-and-segment-layout-compat.md.
	pageHeaders bool
	// prevRecPtr stores the upstream-style 0-based RecPtr of the
	// most recently appended record (the byte offset where its
	// XLogRecord header begins). Written verbatim into the next
	// record's xl_prev so pg_waldump's prev-link check passes.
	// 0 means "no previous record" (first record on the chain).
	prevRecPtr uint64
	sysID      uint64
	tli        uint32

	// onLoopStart / onLoopEnd are optional hooks called at the
	// beginning and end of the state-loop goroutine, respectively.
	// Set by NewWriter from the Writer's OnLoopStart / OnLoopEnd.
	onLoopStart func()
	onLoopEnd   func()

	// onWALWrite is an optional hook called before each WAL page
	// write in writeAt. Set by NewWriter from the Writer's OnWALWrite.
	onWALWrite func()

	// core is the slice B stripe writer core, mounted by NewWriter and
	// shared with Writer.core (same pointer). state.append's PG-compat
	// Path B calls core.AppendXLogPayload instead of the old
	// encodeRecordXLog + emitWithPageHeaders + walBuf.append sequence.
	// nil before NewWriter completes; nil-safe across all core methods.
	core *stripeWriterCore

	// fg is the group-commit flush queue, shared with Writer. M0098-0002.
	fg *flushGroup
}

// walBufferCounters holds the lifetime drain counters that
// pg_stat_wal_io's M0013-0003 columns surface. The two drain
// buckets are kept separate so an operator can tell a sizing
// problem (high overflowDrainBytes) from natural commit /
// eviction durability cost (high flushDrainBytes). Single
// allocation owned by Writer; shared with state via pointer.
// Counters are per-P sharded via stats.Counter (M0107-0008
// loop 11) so the lines do not bounce across cores even when
// multiple backend goroutines drive the drain serially under
// state.appendMu.
type walBufferCounters struct {
	overflowDrainBytes stats.Counter
	flushDrainBytes    stats.Counter

	// fsyncCount is the lifetime count of real fdatasync(2) calls made by
	// state.flushUpTo against WAL segment files — pg_stat_io's
	// (client backend, wal, normal, fsyncs) cell (M0122-0003 follow-up).
	// Incremented once per segment actually synced, unconditionally (real
	// op counts are never gated on track_io_timing, only their _time
	// siblings are — see Writer.sharedFsyncTimeNanos).
	fsyncCount stats.Counter

	// segmentsPreallocated and preallocatedBytes are the lifetime count
	// of new WAL segments zero-filled by preallocateSegment and the
	// total bytes written doing so — the "Counters / observability"
	// follow-up deferred by 0007-0001-wal-segment-preallocation.md
	// (wal_segments_preallocated_total / wal_init_zero_bytes_total).
	// Both are bumped together in state.openSegment, only on the
	// wasNew branch (a re-open of an existing segment never
	// preallocates and must not double-count).
	segmentsPreallocated stats.Counter
	preallocatedBytes    stats.Counter
}

// drainReason classifies which counter a drainBufferBytes call
// should bump. Kept inside the package — callers funnel through
// the two top-level helpers (drainBufferBytes /
// drainBufferUpToForFlush) rather than naming the enum directly.
type drainReason int

const (
	drainReasonOverflow drainReason = iota // append-path overflow
	drainReasonFlush                       // flushUpTo Stage 1
)

// NewWriter creates a segmented WAL writer rooted at cfg.WALDir.
func NewWriter(cfg Config) (*Writer, error) {
	cfg.withDefaults()
	switch cfg.SyncMethod {
	case "fdatasync", "fsync":
		// implemented — dispatched in flushUpTo via state.doSync.
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedSyncMethod, cfg.SyncMethod)
	}
	if err := os.MkdirAll(cfg.WALDir, 0o700); err != nil {
		return nil, fmt.Errorf("wal: mkdir %s: %w", cfg.WALDir, err)
	}

	st, err := loadState(cfg)
	if err != nil {
		return nil, err
	}

	bufCounters := &walBufferCounters{}
	st.walBufferCounters = bufCounters
	fg := &flushGroup{
		signal: make(chan struct{}, 1),
	}
	w := &Writer{
		ops:               make(chan op),
		done:              make(chan struct{}),
		memRing:           st.memRing,
		stateRef:          st,
		walBufferCounters: bufCounters,
		pageHeaders:       cfg.PageHeaders,
		segmentSize:       cfg.SegmentSize,
		fg:                fg,
	}
	w.writeLSNAtomic.Store(uint64(st.writePos))
	st.writeLSNMirror = &w.writeLSNAtomic
	st.onAppend = w.notifyAppend
	st.onLoopStart = cfg.OnLoopStart
	st.onLoopEnd = cfg.OnLoopEnd
	st.onWALWrite = cfg.OnWALWrite
	st.fg = fg
	w.core = newStripeWriterCore(
		uint64(cfg.SegmentSize),
		uint64(st.writePos),
		st.prevRecPtr,
		st.walBuf,
		st.memRing,
	)
	st.core = w.core // share core pointer so state.append can call AppendXLogPayload
	go st.loop(w.ops, w.done)
	return w, nil
}

// PageHeadersEnabled reports whether the writer is emitting
// PG-compatible XLOG page headers (M0014-0001 step 2). Returns the
// Config.PageHeaders value the writer was constructed with — static
// for the lifetime of the Writer. RecordIterator consults this so
// it knows whether the on-disk segments contain interleaved page
// headers it must skip.
func (w *Writer) PageHeadersEnabled() bool { return w.pageHeaders }

// Format returns the active on-disk WAL format the writer is
// emitting. Mirrors the Config.PageHeaders flag the writer was
// constructed with — static for the lifetime of the Writer.
// Surfaced for runtime observability (M0014 DoD #9 — WAL format
// mode/version exposed at runtime).
func (w *Writer) Format() WALFormatVersion {
	if w.pageHeaders {
		return WALFormatPGCompat
	}
	return WALFormatLegacy
}

// MemRing returns the in-memory WAL ring. nil when
// `wal_sender_memory_buffer = 0` (or absent). RecordIterator
// consults this on every read to decide between RAM and disk;
// observability code reads its Hits()/Misses() counters. The
// returned pointer is stable for the writer's lifetime; callers
// must not mutate the ring's internal state.
func (w *Writer) MemRing() *MemRing { return w.memRing }

// WALBuffersCapacity returns the wal_buffers GUC value the writer
// was configured with. 0 when the buffer is disabled. Static for
// the writer's lifetime. Surfaced as
// `wal_buffers_capacity_bytes` in pg_stat_wal_io (M0013-0003).
func (w *Writer) WALBuffersCapacity() int64 {
	resp := make(chan result, 1)
	if err := w.send(op{kind: opWALBufStat, resp: resp}); err != nil {
		return 0
	}
	return (<-resp).walBufStat.cap
}

// WALBuffersBytesResident returns the live byte count currently
// held in the in-memory WAL buffer (i.e. not yet drained to a
// segment file). Surfaced as `wal_buffers_bytes_resident` in
// pg_stat_wal_io. Reads serialise on the writer goroutine to
// avoid racing with append/drain.
func (w *Writer) WALBuffersBytesResident() int64 {
	resp := make(chan result, 1)
	if err := w.send(op{kind: opWALBufStat, resp: resp}); err != nil {
		return 0
	}
	return (<-resp).walBufStat.resident
}

// WALBuffersOverflowDrainBytes returns the lifetime total bytes
// drained from the buffer because an Append would have overflowed
// it. High values indicate wal_buffers is too small for the
// workload's append rate. Atomic load — does not enqueue an op.
func (w *Writer) WALBuffersOverflowDrainBytes() uint64 {
	if w.walBufferCounters == nil {
		return 0
	}
	return uint64(w.walBufferCounters.overflowDrainBytes.Sum())
}

// WALBuffersFlushDrainBytes returns the lifetime total bytes
// drained from the buffer by FlushUpTo's Stage 1 (commits,
// eviction, checkpoint). High values are typical and healthy —
// the buffer's natural cost of WAL durability. Atomic load.
func (w *Writer) WALBuffersFlushDrainBytes() uint64 {
	if w.walBufferCounters == nil {
		return 0
	}
	return uint64(w.walBufferCounters.flushDrainBytes.Sum())
}

// FsyncCount returns the lifetime count of real fdatasync(2) calls the
// writer has made against WAL segment files, backing pg_stat_io's
// (client backend, wal, normal) fsyncs column (M0122-0003 follow-up).
// Atomic load — does not enqueue an op.
func (w *Writer) FsyncCount() int64 {
	if w.walBufferCounters == nil {
		return 0
	}
	return w.walBufferCounters.fsyncCount.Sum()
}

// AddFsyncTimeNanos accumulates n nanoseconds of real wall-clock time
// spent waiting on a WAL fdatasync, backing pg_stat_io's fsync_time
// column. Callers must only add real elapsed time measured while the
// calling backend has track_io_timing enabled (see initdb.Open's
// OnWALSyncDone wiring, which mirrors storage.Pool's OnPinDone /
// AddReadTimeNanos gating exactly — "zero unless track_io_timing is
// enabled" pg_stat_io.*_time semantics). Non-positive n is ignored.
func (w *Writer) AddFsyncTimeNanos(n int64) {
	if n > 0 {
		w.sharedFsyncTimeNanos.Add(n)
	}
}

// FsyncTimeNanos returns the cumulative real time spent in WAL
// fdatasync calls by backends with track_io_timing on, backing
// pg_stat_io's fsync_time column. Atomic load.
func (w *Writer) FsyncTimeNanos() int64 {
	return w.sharedFsyncTimeNanos.Load()
}

// SegmentsPreallocated returns the lifetime count of new WAL segments
// zero-filled by preallocateSegment (wal_segments_preallocated_total).
// Zero when Config.Preallocate is off, since openSegment's wasNew
// branch — the only call site — never runs. Atomic load.
func (w *Writer) SegmentsPreallocated() int64 {
	if w.walBufferCounters == nil {
		return 0
	}
	return w.walBufferCounters.segmentsPreallocated.Sum()
}

// PreallocatedBytes returns the lifetime total bytes written by
// preallocateSegment zero-filling new WAL segments
// (wal_init_zero_bytes_total). Atomic load.
func (w *Writer) PreallocatedBytes() int64 {
	if w.walBufferCounters == nil {
		return 0
	}
	return w.walBufferCounters.preallocatedBytes.Sum()
}

// WrittenLSN returns the LSN of the last byte the writer has
// appended (durable or not). Cheap and lock-free; suitable for
// the checkpointer's max_wal_size trigger.
func (w *Writer) WrittenLSN() uint64 {
	return w.writeLSNAtomic.Load()
}

// DrainedLSN returns the last byte position already written into segment
// files. Bytes between DrainedLSN and WrittenLSN may still live only in the
// in-memory WAL buffer.
func (w *Writer) DrainedLSN() uint64 {
	if st := w.stateRef; st != nil {
		st.appendMu.Lock()
		defer st.appendMu.Unlock()
		return st.drainedLSN
	}
	return w.WrittenLSN()
}

func (w *Writer) readBufferedAt(pos int64, out []byte) int {
	if len(out) == 0 {
		return 0
	}
	st := w.stateRef
	if st == nil || st.walBuf == nil {
		return 0
	}
	st.appendMu.Lock()
	defer st.appendMu.Unlock()
	return st.walBuf.readAt(pos, out)
}

// Subscribe registers ch to receive a non-blocking wake-up after
// every successful Append. Caller is expected to use a buffered
// channel of capacity ≥ 1 — the Writer drops the wake-up if the
// channel is full, since "WAL has advanced" is an idempotent signal
// (subscribers re-poll WrittenLSN, the actual advance is observable
// regardless of how many wake-ups were collapsed).
func (w *Writer) Subscribe(ch chan<- struct{}) {
	w.subMu.Lock()
	if w.subscribers == nil {
		w.subscribers = map[chan<- struct{}]struct{}{}
	}
	w.subscribers[ch] = struct{}{}
	w.subMu.Unlock()
}

// Unsubscribe removes ch from the wake-up set. Safe to call even if
// ch was never subscribed.
func (w *Writer) Unsubscribe(ch chan<- struct{}) {
	w.subMu.Lock()
	delete(w.subscribers, ch)
	w.subMu.Unlock()
}

// notifyAppend wakes every active subscriber. Called by the writer
// goroutine after a successful append. Non-blocking sends so a stuck
// subscriber can't back-pressure the WAL writer.
func (w *Writer) notifyAppend() {
	w.subMu.Lock()
	for ch := range w.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	w.subMu.Unlock()
}

// Append writes one record and returns its [startLSN, endLSN].
//
// M0026: when a WAL buffer is configured, the calling goroutine
// encodes and buffers the record directly under state.appendMu,
// bypassing the state-loop goroutine.  This eliminates the
// serial round-trip in the common case (buffer not full).
// When the buffer overflows (uncommon), the call falls back to
// the state-loop path (legacy behaviour).
//
// Empty payloads are rejected because the encoded zero header
// (len=0, crc=0) is reserved as the EOS sentinel in preallocated
// segments — see
// docs/design/0007-0001-wal-segment-preallocation.md. No
// production caller emits empty records.
func (w *Writer) Append(payload []byte) (uint64, uint64, error) {
	if len(payload) == 0 {
		return 0, 0, ErrEmptyPayload
	}

	// Fast path: WAL buffer available — encode and buffer under
	// appendMu, sending an opAppend to the state loop only when
	// the buffer overflows.
	if st := w.stateRef; st != nil && st.walBuf != nil {
		if start, end, ok, err := st.tryAppend(payload); ok {
			return start, end, err
		}
	}

	// Slow path: no WAL buffer, or buffer overflow — fall back
	// to the state-loop goroutine (legacy serialised path).
	resp := make(chan result, 1)
	buf := make([]byte, len(payload))
	copy(buf, payload)
	if err := w.send(op{kind: opAppend, payload: buf, resp: resp}); err != nil {
		return 0, 0, err
	}
	r := <-resp
	return r.startLSN, r.endLSN, r.err
}

// AppendRaw writes an already-encoded WAL byte stream verbatim.
//
// Unlike Append, this does not wrap or re-encode the input as a new
// record. Physical walreceiver uses it to persist PostgreSQL WAL bytes
// exactly as streamed by the primary so the local iterator sees the
// original page headers and record framing.
func (w *Writer) AppendRaw(stream []byte) (uint64, uint64, error) {
	if len(stream) == 0 {
		return 0, 0, ErrEmptyPayload
	}
	resp := make(chan result, 1)
	buf := make([]byte, len(stream))
	copy(buf, stream)
	if err := w.send(op{kind: opAppendRaw, payload: buf, resp: resp}); err != nil {
		return 0, 0, err
	}
	r := <-resp
	return r.startLSN, r.endLSN, r.err
}

// FlushUpTo persists WAL bytes up to lsn with fdatasync semantics.
// M0098-0002: group-commit path — multiple concurrent callers are
// batched into a single fdatasync by the writer goroutine.
func (w *Writer) FlushUpTo(lsn uint64) error {
	if lsn == 0 {
		return nil
	}
	if w.OnWALSync != nil {
		w.OnWALSync()
	}
	if w.OnWALSyncDone != nil {
		defer w.OnWALSyncDone()
	}
	req := &groupFlushReq{lsn: lsn, done: make(chan struct{})}
	w.fg.mu.Lock()
	w.fg.queue = append(w.fg.queue, req)
	w.fg.mu.Unlock()
	// Signal writer goroutine. Non-blocking: if signal is already
	// pending the writer will pick up our request on the next drain.
	select {
	case w.fg.signal <- struct{}{}:
	default:
	}
	// Wait for the writer to complete our flush (or the writer to close).
	select {
	case <-req.done:
		return req.err
	case <-w.done:
		return ErrClosed
	}
}

// RemoveOldSegments retires any WAL segment file whose contents end
// strictly before the segment that contains keepLSN. The segment
// containing keepLSN — and every segment after it — is preserved, so
// the caller can pass `min(checkpointLSN, min(slot.RestartLSN))` and
// be sure no record needed for crash recovery or for an attached
// standby is removed.
//
// Up to `ceil(Config.MinWALSize/SegmentSize)` of the newest obsolete
// segments are recycled (zero-filled and renamed into fresh future
// segment slots) rather than unlinked, so a later openSegment for
// that slot skips its own create+zero-fill+directory-fsync. The rest
// are unlinked as before. Config.MinWALSize <= 0 disables recycling
// (every obsolete segment is unlinked, the pre-M0122-0009 behaviour).
//
// keepLSN == 0 is a no-op (no records have been written yet).
//
// The op runs on the writer's serialised loop so it cannot race with
// an in-flight Append or Flush. Open file handles for retired segments
// are closed before the unlink/rename and dropped from the writer's
// cache so subsequent writes never re-touch the old file.
//
// Returns the number of segment files unlinked and the number recycled.
func (w *Writer) RemoveOldSegments(keepLSN uint64) (removed, recycled int, err error) {
	if keepLSN == 0 {
		return 0, 0, nil
	}
	resp := make(chan result, 1)
	if err := w.send(op{kind: opRecycle, lsn: keepLSN, resp: resp}); err != nil {
		return 0, 0, err
	}
	r := <-resp
	return r.removed, r.recycled, r.err
}

// Close flushes dirty segments, closes files, and stops the worker.
func (w *Writer) Close() error {
	resp := make(chan result, 1)
	if err := w.send(op{kind: opClose, resp: resp}); err != nil {
		if errors.Is(err, ErrClosed) {
			return nil
		}
		return err
	}
	r := <-resp
	<-w.done
	return r.err
}

func (w *Writer) send(req op) error {
	select {
	case <-w.done:
		return ErrClosed
	case w.ops <- req:
		return nil
	}
}

func loadState(cfg Config) (*state, error) {
	// M0014-0004 step 2: fail fast if the existing data dir holds
	// WAL in a format the configured writer cannot append to.
	// Detection is read-only and skips CRC validation. Unknown
	// (empty / fresh / unclassifiable) is treated as "no mismatch
	// signal" — both modes can claim the dir, and detectWritePos's
	// later record-level scan will catch any deeper incompatibility.
	detected, _ := DetectWALFormat(cfg.WALDir)
	if detected == WALFormatLegacy && cfg.PageHeaders {
		return nil, fmt.Errorf("%w: data dir contains %s WAL but PageHeaders=true requests pgcompat output",
			ErrWALFormatMismatch, detected)
	}
	if detected == WALFormatPGCompat && !cfg.PageHeaders {
		return nil, fmt.Errorf("%w: data dir contains %s WAL but PageHeaders=false requests legacy output",
			ErrWALFormatMismatch, detected)
	}
	writePos, prevRecPtr, err := detectWritePos(cfg.WALDir, cfg.SegmentSize, cfg.PageHeaders)
	if err != nil {
		return nil, err
	}
	st := &state{
		cfg:         cfg,
		writePos:    writePos,
		writeLSN:    uint64(writePos),
		flushedLSN:  uint64(writePos),
		drainedLSN:  uint64(writePos),
		files:       make(map[uint64]*os.File),
		dirty:       make(map[uint64]bool),
		aio:         cfg.AIO,
		memRing:     NewMemRing(cfg.SenderMemoryBuffer),
		walBuf:      newWALBuffer(cfg.WALBuffers),
		pageHeaders: cfg.PageHeaders,
		prevRecPtr:  prevRecPtr,
		sysID:       cfg.SystemID,
		tli:         cfg.TimelineID,
	}
	if st.walBuf != nil {
		st.walBuf.reset(writePos)
	}
	// Anchor the MemRing at writePos so ReadAt never returns zeros for WAL
	// positions written to disk by a prior session (e.g. initdb). Without this,
	// after the first Path-B append advances tail past an old segment boundary,
	// ReadAt(oldPos) returns true but yields zeros — the ring was never written
	// at that offset — causing the walsender to serve an all-zero page to PG
	// standbys ("invalid magic number 0000").
	if st.memRing != nil {
		st.memRing.ResetToPos(writePos)
	}
	return st, nil
}

func detectWritePos(walDir string, segSize int64, pageHeaders bool) (int64, uint64, error) {
	entries, err := os.ReadDir(walDir)
	if err != nil {
		return 0, 0, fmt.Errorf("wal: list %s: %w", walDir, err)
	}

	segSizes := make(map[uint64]int64)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		segNo, ok := parseSegmentName(e.Name())
		if !ok {
			continue
		}
		st, err := e.Info()
		if err != nil {
			return 0, 0, fmt.Errorf("wal: stat %s: %w", e.Name(), err)
		}
		if st.Size() < 0 {
			return 0, 0, fmt.Errorf("wal: invalid segment size %d for %s", st.Size(), e.Name())
		}
		// Skip segments whose size doesn't match the configured segment size.
		// This handles the case where goopg init always creates a 16MB bootstrap
		// segment but the server may be configured with a different segment size
		// (e.g. in tests). Pre-fix, parseSegmentName silently skipped PG-format
		// names; now we skip explicitly on size mismatch. M0106-0010 batched-35.
		if st.Size() > segSize {
			continue
		}
		segSizes[segNo] = st.Size()
	}

	if len(segSizes) == 0 {
		return 0, 0, nil
	}

	segNos := make([]uint64, 0, len(segSizes))
	for seg := range segSizes {
		segNos = append(segNos, seg)
	}
	sort.Slice(segNos, func(i, j int) bool { return segNos[i] < segNos[j] })

	// M0045-0001: Accept a non-zero first segment. Normal WAL
	// retention deletes pre-checkpoint segments, so after one full
	// retention cycle segment 0 is gone. Rejecting any first segment
	// other than 0 contradicts the retention contract.
	//
	// The old check `if segNos[0] != 0 { return error }` is dropped.
	// Gap detection (missing segment in the retained range) is still
	// done correctly by comparing consecutive entries.
	firstSegNo := segNos[0]

	// Start writePos at the absolute LSN of the first retained segment
	// so the result is an absolute byte offset, not one relative to
	// segment 0. Without this, the caller would treat the returned
	// position as if it started at byte 0 instead of firstSegNo*segSize.
	var writePos int64 = int64(firstSegNo) * segSize
	var prevRecPtr uint64
	for i := 0; i < len(segNos); i++ {
		expected := firstSegNo + uint64(i)
		if segNos[i] != expected {
			return 0, 0, fmt.Errorf("wal: gap at segment %s", formatSegmentName(expected))
		}
		sz := segSizes[expected]
		if i < len(segNos)-1 && sz != segSize {
			return 0, 0, fmt.Errorf("wal: non-final segment %s has size %d, expected %d", formatSegmentName(expected), sz, segSize)
		}
		if i < len(segNos)-1 {
			writePos += sz
			continue
		}
		// Last segment: scan for the first EOS sentinel (zero
		// header) to find the actual end of records. Works for
		// both legacy lazy-grown segments (writePos == sz, no
		// trailing zeros) and preallocated full-size segments
		// (writePos < sz, zero-fill tail).
		usedBytes, lastRecPtr, scanErr := scanLastSegmentEnd(walDir, expected, sz, segSize, pageHeaders)
		if scanErr != nil {
			return 0, 0, scanErr
		}
		writePos += usedBytes
		// scanLastSegmentEnd returns the *1-based* start-LSN of the last
		// record (goopg's public RecPtr convention, matching state.append's
		// `start = writePos + leading + 1`). prevRecPtr is the *0-based*
		// RecPtr (the header byte offset written verbatim into the next
		// record's xl_prev), so convert by -1 — exactly mirroring the live
		// append path's `resetPosition(end, start-1)`. Without this, the
		// first record appended after a restart/boot inherits a +1 xl_prev
		// and pg_waldump aborts the chain with "incorrect prev-link"
		// (goopg's own recovery never validates xl_prev, so this was
		// silently wrong until the pg_waldump oracle surfaced it).
		if lastRecPtr > 0 {
			prevRecPtr = lastRecPtr - 1
		} else {
			prevRecPtr = 0
		}
	}

	return writePos, prevRecPtr, nil
}

// scanLastSegmentEnd reads the last segment from disk and returns
// the byte offset of the first EOS sentinel plus the start-LSN of
// the final complete record found in this segment.
//
// EOS markers:
//   - legacy mode: zero 8-byte record header
//   - page-headers mode: all-zero page header at a page boundary,
//     or zero XLogRecord header mid-page
//
// Used by detectWritePos to recover the true write position from a
// preallocated full-size last segment and to reconstruct xl_prev
// chaining state on restart.
//
// `cfgSegSize` is the configured WAL segment size (controls the
// long-vs-short page header size at segment boundaries).
// `pageHeaders=true` walks the file's interleaved page headers and
// XLogRecord-framed bytes; `false` keeps the legacy flat record-stream
// walk.
func scanLastSegmentEnd(walDir string, segNo uint64, segSize int64, cfgSegSize int64, pageHeaders bool) (int64, uint64, error) {
	path := filepath.Join(walDir, formatSegmentName(segNo))
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("wal: read %s: %w", path, err)
	}
	if int64(len(data)) > segSize {
		return 0, 0, fmt.Errorf("wal: segment %s too large: %d > %d", path, len(data), segSize)
	}
	if !pageHeaders {
		off := 0
		var lastRecPtr uint64
		for off < len(data) {
			if isZeroHeader(data[off:]) {
				return int64(off), lastRecPtr, nil
			}
			lastRecPtr = uint64(int64(segNo)*cfgSegSize+int64(off)) + 1
			_, n, err := decodeRecord(data[off:])
			if err != nil {
				// Corrupt record in the last segment —
				// treat as EOS (unclean shutdown).
				return int64(off), lastRecPtr, nil
			}
			off += n
		}
		return int64(off), lastRecPtr, nil
	}

	// Page-aware walk: at every page boundary skip the page
	// header (or stop on an all-zero header — the preallocated
	// tail). Inside a page, decode records that may continue
	// onto the next page (the next page's header sits between
	// the head and tail of those bytes).
	streamBase := int64(segNo) * cfgSegSize
	off := 0
	var lastRecPtr uint64
	for off < len(data) {
		pos := streamBase + int64(off)
		if pos%XLOGBlockSize == 0 {
			hsize := pageHeaderSizeAt(pos, cfgSegSize)
			if off+hsize > len(data) {
				return int64(off), lastRecPtr, nil
			}
			if isZeroBytes(data[off : off+hsize]) {
				return int64(off), lastRecPtr, nil
			}
			off += hsize
			continue
		}
		// Mid-page record. In PG-compatible framing the first
		// 24 bytes are the XLogRecord header; zero means EOS.
		if off+xlogRecordHeaderSize > len(data) {
			return int64(off), lastRecPtr, nil
		}
		header, _ := extractRecordBytes(data[off:], pos, cfgSegSize, xlogRecordHeaderSize)
		if len(header) < xlogRecordHeaderSize {
			return int64(off), lastRecPtr, nil
		}
		if isZeroXLogRecordHeader(header) {
			return int64(off), lastRecPtr, nil
		}
		h, err := DecodeXLogRecordHeader(header)
		if err != nil {
			return int64(off), lastRecPtr, nil
		}
		total := int(h.TotLen)
		if total < xlogRecordHeaderSize {
			return int64(off), lastRecPtr, nil
		}
		// Request MAXALIGN-aligned bytes so we also pick up the
		// trailing pad bytes (zero-filled by the encoder). The
		// scanner advances by `consumed` so the next iteration
		// lands on the next record header.
		paddedTotal := maxAlignXLog(total)
		fullBytes, consumed := extractRecordBytes(data[off:], pos, cfgSegSize, paddedTotal)
		if len(fullBytes) < total {
			// Truncated tail — treat as EOS so we don't
			// consume a partially-written record.
			return int64(off), lastRecPtr, nil
		}
		decoded, err := decodeRecordXLogDetailed(fullBytes)
		if err != nil || decoded.Consumed != len(fullBytes) {
			// Corrupt record in the last segment —
			// treat as EOS (unclean shutdown).
			return int64(off), lastRecPtr, nil
		}
		lastRecPtr = uint64(pos) + 1
		off += consumed
	}
	return int64(off), lastRecPtr, nil
}

func (s *state) loop(ops <-chan op, done chan<- struct{}) {
	// Pin this goroutine to its OS thread to eliminate migration
	// overhead on the fdatasync hot path. M0098-0002.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if s.onLoopStart != nil {
		s.onLoopStart()
	}
	if s.onLoopEnd != nil {
		defer s.onLoopEnd()
	}
	defer close(done)
	// flushSig is s.fg.signal; using a local avoids a nil-check on
	// every select iteration when fg is absent (tests that build a
	// state without a Writer use fg=nil).
	var flushSig <-chan struct{}
	if s.fg != nil {
		flushSig = s.fg.signal
	}
	for {
		select {
		case req, ok := <-ops:
			if !ok {
				return
			}
			switch req.kind {
			case opAppend:
				start, end, err := s.append(req.payload)
				req.resp <- result{startLSN: start, endLSN: end, err: err}
				if err == nil && s.onAppend != nil {
					s.onAppend()
				}
			case opAppendRaw:
				start, end, err := s.appendRaw(req.payload)
				req.resp <- result{startLSN: start, endLSN: end, err: err}
				if err == nil && s.onAppend != nil {
					s.onAppend()
				}
			case opFlush:
				// Legacy path kept for backward compatibility.
				// New code routes through flushSig via FlushUpTo.
				req.resp <- result{err: s.flushUpTo(req.lsn)}
			case opRecycle:
				n, rc, err := s.removeOldSegments(req.lsn)
				req.resp <- result{removed: n, recycled: rc, err: err}
			case opClose:
				// Drain any pending group-flush requests before closing.
				s.handleGroupFlush()
				err := s.close()
				req.resp <- result{err: err}
				return
			case opWALBufStat:
				snap := walBufStatSnapshot{}
				if s.walBuf != nil {
					snap.cap = s.walBuf.cap
					snap.resident = s.walBuf.resident()
				}
				req.resp <- result{walBufStat: snap}
			default:
				req.resp <- result{err: fmt.Errorf("wal: unknown operation %d", req.kind)}
			}
		case _, ok := <-flushSig:
			if !ok {
				return
			}
			s.handleGroupFlush()
		}
	}
}

// handleGroupFlush drains the entire pending flush queue, issues one
// fdatasync for the maximum LSN requested, then notifies all waiters.
// Called only from the writer goroutine. M0098-0002.
//
// M0099-0003: when commitSiblings or more concurrent waiters are already
// in the queue, sleep commitDelayUs microseconds before flushing so that
// additional FlushUpTo callers can arrive and be served by the same
// fdatasync. This mirrors PostgreSQL's commit_delay / commit_siblings
// semantics and increases the average batch size under high concurrency.
func (s *state) handleGroupFlush() {
	if s.fg == nil {
		return
	}
	s.fg.mu.Lock()
	queue := s.fg.queue
	s.fg.queue = nil
	s.fg.mu.Unlock()
	if len(queue) == 0 {
		return
	}
	// Batching delay (M0099-0003, re-enabled after Path A race fix M0099):
	// if commitSiblings or more concurrent waiters are queued, sleep
	// commitDelayUs so additional callers can arrive and be served by the
	// same fdatasync. The sleep runs on the OS-thread-pinned writer goroutine
	// (runtime.LockOSThread, M0098-0002). The state.append Path A race that
	// previously caused stale writePos under concurrency is now fixed.
	if commitDelayUs > 0 && len(queue) >= commitSiblings {
		time.Sleep(commitDelayUs * time.Microsecond)
		s.fg.mu.Lock()
		extra := s.fg.queue
		s.fg.queue = nil
		s.fg.mu.Unlock()
		if len(extra) > 0 {
			queue = append(queue, extra...)
		}
	}
	// Find the highest LSN across all requests; one flush satisfies all.
	var maxLSN uint64
	for _, req := range queue {
		if req.lsn > maxLSN {
			maxLSN = req.lsn
		}
	}
	err := s.flushUpTo(maxLSN)
	// Notify all callers. req.err is set before close(req.done) so the
	// caller's read of req.err after <-req.done is race-free (close is
	// a happens-before barrier in Go).
	for _, req := range queue {
		req.err = err
		close(req.done)
	}
}

func (s *state) append(payload []byte) (uint64, uint64, error) {
	// PG-compat (pageHeaders) path: use stripe B (core.AppendXLogPayload)
	// for Path B, and the old encode path for Path A (direct disk write).
	if s.pageHeaders {
		return s.appendPGCompat(payload)
	}

	// Non-pageHeaders (legacy binary format): original encodeRecord path.
	record := encodeRecord(payload)
	realRecLen := len(record)

	// Path A: WAL buffer disabled, OR record larger than the
	// entire buffer. Bypass and write straight to disk.
	if s.walBuf == nil || !s.walBuf.canHold(len(record)) {
		s.appendMu.Lock()
		if s.walBuf != nil && s.walBuf.resident() > 0 {
			if err := s.drainBufferBytes(s.walBuf.resident(), drainReasonOverflow); err != nil {
				s.appendMu.Unlock()
				return 0, 0, err
			}
		}
		writePos := s.writePos
		start := uint64(writePos) + 1
		s.writePos = writePos + int64(len(record))
		end := uint64(s.writePos)
		s.appendMu.Unlock()

		if err := s.writeAt(writePos, record); err != nil {
			s.appendMu.Lock()
			if s.writePos == writePos+int64(len(record)) {
				s.writePos = writePos
			}
			s.appendMu.Unlock()
			return 0, 0, err
		}

		s.appendMu.Lock()
		if s.walBuf != nil {
			s.walBuf.reset(int64(end))
		}
		s.memRing.Append(writePos, record)
		s.drainedLSN = end
		s.writeLSN = end
		if s.writeLSNMirror != nil {
			s.writeLSNMirror.Store(end)
		}
		s.appendMu.Unlock()
		return start, end, nil
	}

	// Path B: buffered append under appendMu.
	s.appendMu.Lock()
	defer s.appendMu.Unlock()

	writePos := s.writePos
	start := uint64(writePos) + 1

	need := int64(realRecLen) - s.walBuf.free()
	if need > 0 {
		if err := s.drainBufferBytes(need, drainReasonOverflow); err != nil {
			return 0, 0, err
		}
	}
	s.walBuf.append(record)
	s.memRing.Append(writePos, record)
	s.writePos = writePos + int64(len(record))
	end := uint64(s.writePos)
	s.writeLSN = end
	if s.writeLSNMirror != nil {
		s.writeLSNMirror.Store(end)
	}
	return start, end, nil
}

// appendPGCompat implements state.append for the PG-compat pageHeaders path
// using the slice B stripe writer core (core.AppendXLogPayload). Path A
// (walBuf disabled or record too large) still uses the legacy encode path and
// resyncs the core's posTracker afterwards. Path B uses core.AppendXLogPayload
// and a synchronous PublishUpTo so walBuf.tail stays correct for drain.
//
// Called exclusively when s.pageHeaders == true.
func (s *state) appendPGCompat(payload []byte) (uint64, uint64, error) {
	// Conservative emitted size: paddedLen + max page-header overhead
	// (40-byte long PHD at a segment boundary + 24-byte contrecord header).
	_, paddedLen := predictXLogRecordLen(payload)
	conservativeSize := paddedLen + 64
	// Worst-case WAL-buffer footprint including a possible segment-boundary
	// pad. A record whose stripe reservation straddles a segment boundary is
	// re-landed at the boundary by reserveEmittedAndPublish, which first emits
	// an XLOG_NOOP pad over the gap via emitSegmentPad → writeReserved; pad +
	// record together occupy strictly less than 2*conservativeSize ring bytes
	// (the gap is < the record's emitted size by the crossing predicate). The
	// Path B buffered append below also goes through AppendXLogPayload, so it
	// must keep that much headroom too — otherwise a crossing while the buffer
	// is near-full returns errWALBufferReservedOutOfRange. See tryAppend.
	reserveSize := 2 * conservativeSize

	// Path A: walBuf disabled, record physically won't fit in ring, OR the
	// buffer needs draining. The drain case falls here (rather than Path B)
	// so that appendMu.Lock() is held across drain + write: concurrent
	// tryAppend goroutines (RLock) are frozen and cannot consume the freed
	// space between the drain and the write, avoiding errWALBufferReservedOutOfRange.
	needsDrain := s.walBuf != nil && int64(reserveSize) > s.walBuf.free()-s.walBuf.reservedBytes.Load()
	if s.walBuf == nil || !s.walBuf.canHold(reserveSize) || needsDrain {
		s.appendMu.Lock()
		// Publish + drain any buffered bytes before the direct write.
		if s.walBuf != nil && s.walBuf.resident() > 0 {
			curr, _ := s.core.Load()
			s.core.PublishUpTo(int64(curr))
			if err := s.drainBufferBytes(s.walBuf.resident(), drainReasonOverflow); err != nil {
				s.appendMu.Unlock()
				return 0, 0, err
			}
		}
		// Derive writePos and prevRecPtr from the tracker so that
		// concurrent tryAppend goroutines (under RLock) that advanced
		// the tracker before we acquired Lock are accounted for.
		trackerCurr, trackerPrev := s.core.Load()
		writePos := int64(trackerCurr)
		record, realRecLen, err := encodeRecordXLog(payload, trackerPrev)
		if err != nil {
			s.appendMu.Unlock()
			return 0, 0, err
		}
		stream, leading := emitWithPageHeaders(record, realRecLen, writePos, s.cfg.SegmentSize, s.sysID, s.tli)
		start := uint64(writePos) + uint64(leading) + 1
		s.writePos = writePos + int64(len(stream))
		end := uint64(s.writePos)
		s.appendMu.Unlock()

		if err := s.writeAt(writePos, stream); err != nil {
			s.appendMu.Lock()
			if s.writePos == writePos+int64(len(stream)) {
				s.writePos = writePos
			}
			s.appendMu.Unlock()
			return 0, 0, err
		}

		s.appendMu.Lock()
		if s.walBuf != nil {
			s.walBuf.reset(int64(end))
		}
		s.memRing.Append(writePos, stream)
		s.drainedLSN = end
		s.writeLSN = end
		if s.writeLSNMirror != nil {
			s.writeLSNMirror.Store(end)
		}
		// Resync core posTracker so subsequent stripe Path B appends and
		// concurrent tryAppend goroutines start at the correct position.
		// prevRecPtr field update dropped: tracker prev is authoritative.
		s.core.resetPosition(end, start-1)
		s.appendMu.Unlock()
		return start, end, nil
	}

	// Path B: stripe-B buffered append. No outer lock needed.
	//
	// drain runs lock-free: the state-loop goroutine is the sole caller of
	// drainBufferBytes → advanceHead (single writer of head/base, now atomic).
	// Concurrent tryAppend goroutines under RLock write to LSN ranges strictly
	// above tail (checked under RLock), which are disjoint from the drain
	// window [head, head+need). The safety argument is in
	// docs/design/0107-0007ai-wal-buffer-head-base-atomic-async-drain.md.
	//
	// AppendXLogPayload acquires its own stripe lock internally (no outer lock).
	// PublishUpTo is an atomic tail advance (no lock).

	// Phase 1+2: atomically claim reserveSize bytes via tryReserve — the same
	// CAS-protected reservedBytes accounting tryAppend's fast path uses — then
	// stripe-locked append. A plain `reserveSize > free()` check here (as this
	// code used to do) races against concurrent tryAppend fast-path callers:
	// they can hold reservedBytes bytes that have not yet advanced tail/resident,
	// so a free()-only check on this (state-loop) path can conclude "enough
	// room" while a fast-path caller's in-flight reservation pushes the actual
	// combined footprint past cap, and AppendXLogPayload's subsequent
	// writeReserved fails with errWALBufferReservedOutOfRange (AI-20260707-000712-001:
	// reproduces reliably under GOMAXPROCS=2 concurrency, where scheduling gaps
	// widen the window between this check and the reservation). tryReserve's
	// CAS loop closes that window: only writers whose combined reservations
	// stay within cap proceed. Loop because a lost CAS or a concurrent
	// fast-path reservation can require another drain pass before there is
	// room to claim.
	for {
		reserved := s.walBuf.reservedBytes.Load()
		need := int64(reserveSize) - (s.walBuf.free() - reserved)
		if need > 0 {
			// Publish pending bytes before draining so drain can read them.
			curr, _ := s.core.Load()
			s.core.PublishUpTo(int64(curr))
			if err := s.drainBufferBytes(need, drainReasonOverflow); err != nil {
				return 0, 0, err
			}
		}
		if rerr := s.walBuf.tryReserve(int64(reserveSize)); rerr == nil {
			break
		}
		// Lost the race to a concurrent reservation (fast-path tryAppend or
		// another retry of this same loop elsewhere is impossible — this is
		// the single state-loop goroutine — but tryAppend callers can claim
		// the space we just drained before our tryReserve runs). Yield so a
		// racing holder's imminent releaseReservation can make progress
		// instead of busy-spinning when there is nothing left to drain.
		runtime.Gosched()
	}

	procNum := s.stripeNum()
	start0, _, total, leading, err := s.core.AppendXLogPayload(procNum, payload, s.cfg.SegmentSize, s.sysID, s.tli)
	if err != nil {
		s.walBuf.releaseReservation(int64(reserveSize))
		return 0, 0, err
	}

	// Phase 3: publish (atomic tail advance; no lock), then release the
	// buffer-capacity reservation now that tail covers these bytes (they are
	// counted in resident() rather than reservedBytes from here on).
	s.core.PublishUpTo(int64(start0) + int64(total))
	s.walBuf.releaseReservation(int64(reserveSize))

	// Update state-loop bookkeeping. writePos/writeLSN are state-loop-only
	// fields (plain assignment is safe — no concurrent writes from the state
	// loop). writeLSNMirror uses storeMaxLSN because concurrent tryAppend
	// goroutines under RLock may update it simultaneously.
	s.writePos = int64(start0) + int64(total)
	end := uint64(s.writePos)
	s.writeLSN = end
	if s.writeLSNMirror != nil {
		storeMaxLSN(s.writeLSNMirror, end)
	}
	start := start0 + uint64(leading) + 1 // 1-based record start (goopg convention)
	return start, end, nil
}

func (s *state) appendRaw(stream []byte) (uint64, uint64, error) {
	if len(stream) == 0 {
		return 0, 0, ErrEmptyPayload
	}

	if s.walBuf == nil || !s.walBuf.canHold(len(stream)) {
		s.appendMu.Lock()
		if s.walBuf != nil && s.walBuf.resident() > 0 {
			if err := s.drainBufferBytes(s.walBuf.resident(), drainReasonOverflow); err != nil {
				s.appendMu.Unlock()
				return 0, 0, err
			}
		}
		// Read writePos from the tracker so concurrent tryAppend goroutines
		// (under RLock) that advanced it before we acquired Lock are accounted for.
		trackerCurr, trackerPrev := s.core.Load()
		writePos := int64(trackerCurr)
		start := uint64(writePos) + 1
		s.writePos = writePos + int64(len(stream))
		end := uint64(s.writePos)
		s.appendMu.Unlock()

		if err := s.writeAt(writePos, stream); err != nil {
			s.appendMu.Lock()
			if s.writePos == writePos+int64(len(stream)) {
				s.writePos = writePos
			}
			s.appendMu.Unlock()
			return 0, 0, err
		}

		s.appendMu.Lock()
		if s.walBuf != nil {
			s.walBuf.reset(int64(end))
		}
		s.memRing.Append(writePos, stream)
		s.drainedLSN = end
		s.writeLSN = end
		if s.writeLSNMirror != nil {
			s.writeLSNMirror.Store(end)
		}
		// Sync the stripe tracker so subsequent tryAppend/AppendXLogPayload
		// calls start at the correct position. Use the pre-write tracker prev
		// (appendRaw bytes are pre-encoded from the primary and bypass the
		// regular xl_prev chain tracking).
		s.core.resetPosition(end, trackerPrev)
		s.appendMu.Unlock()
		return start, end, nil
	}

	s.appendMu.Lock()
	defer s.appendMu.Unlock()

	// Read writePos from the tracker; concurrent tryAppend goroutines under
	// RLock may have advanced it beyond s.writePos since the last Lock path.
	trackerCurr, trackerPrev := s.core.Load()
	writePos := int64(trackerCurr)
	start := uint64(writePos) + 1
	need := int64(len(stream)) - s.walBuf.free()
	if need > 0 {
		if err := s.drainBufferBytes(need, drainReasonOverflow); err != nil {
			return 0, 0, err
		}
	}
	s.walBuf.append(stream)
	s.memRing.Append(writePos, stream)
	s.writePos = writePos + int64(len(stream))
	end := uint64(s.writePos)
	s.writeLSN = end
	if s.writeLSNMirror != nil {
		s.writeLSNMirror.Store(end)
	}
	// Sync the stripe tracker (same reasoning as Path A above).
	s.core.resetPosition(end, trackerPrev)
	return start, end, nil
}

// tryAppend attempts a fast-path append directly on the calling
// goroutine.  Returns ok=true when the record was buffered without
// I/O; returns ok=false when the caller must fall back to the
// state-loop path (buffer overflow or other condition requiring
// disk write).  M0026: this eliminates the state-loop round-trip
// for the common case (buffer not full).
func (s *state) tryAppend(payload []byte) (start, end uint64, ok bool, err error) {
	// PG-compat path: use stripe B.
	if s.pageHeaders {
		// Conservative emitted-size check before acquiring appendMu or the
		// stripe lock — avoids the lock round-trip when walBuf has no room.
		_, paddedLen := predictXLogRecordLen(payload)
		conservativeSize := paddedLen + 64 // max PHD + contrecord overhead
		// Worst-case WAL-buffer footprint of this one append. When a
		// reservation would straddle a segment boundary, reserveEmittedAndPublish
		// re-lands the record at the boundary AFTER first emitting an XLOG_NOOP
		// pad over the gap [curr, boundary) via emitSegmentPad → writeReserved.
		// The pad and the re-landed record are TWO separate writeReserved calls
		// into the ring, so the reservation must cover BOTH. The crossing
		// predicate (curr+total > boundary) means the gap is strictly smaller
		// than the record's emitted size, and the re-landed record's emitted
		// size is ≤ conservativeSize, so pad+record together occupy strictly
		// less than 2*conservativeSize ring bytes. Reserving that worst case
		// unconditionally keeps writeReserved inside the ring window regardless
		// of where the (concurrently-advancing) LSN cursor actually lands —
		// without it, a crossing while the buffer is near-full returns
		// errWALBufferReservedOutOfRange (M0118-0001: tripped ~50% of the
		// multiple-row-versions 1,000,000-row bulk-insert setup).
		reserveSize := 2 * conservativeSize
		if s.walBuf == nil || !s.walBuf.canHold(reserveSize) {
			return 0, 0, false, nil // Path A territory; let slow path handle I/O
		}

		// RLock allows multiple concurrent stripe writers in parallel.
		// Lock() paths (Path A, appendRaw) are exclusive and wait for
		// all RLock holders to complete before running.
		s.appendMu.RLock()
		defer s.appendMu.RUnlock()

		// Atomically claim reserveSize bytes in the WAL buffer before
		// reserving LSN space. Multiple concurrent stripe writers under
		// RLock could each see free() > 0 and all proceed to reserve LSN
		// space, collectively overflowing the buffer and causing
		// walBuffer.writeReserved to return errWALBufferReservedOutOfRange.
		// tryReserve uses a CAS loop so that the check-and-claim is
		// atomic: only writers whose combined reservations stay within cap
		// proceed to AppendXLogPayload.
		if rerr := s.walBuf.tryReserve(int64(reserveSize)); rerr != nil {
			return 0, 0, false, nil // overflow; slow path drains then appends
		}

		procNum := s.stripeNum()
		var start0 uint64
		var total, leading int
		start0, _, total, leading, err = s.core.AppendXLogPayload(procNum, payload, s.cfg.SegmentSize, s.sysID, s.tli)
		if err != nil {
			// LSN reservation failed or encoding error; release the buffer
			// reservation so capacity is not permanently consumed.
			s.walBuf.releaseReservation(int64(reserveSize))
			return 0, 0, false, err
		}

		// Synchronous publish: advance walBuf.tail and memRing.tail so
		// drain goroutine and flushUpTo see these bytes.
		s.core.PublishUpTo(int64(start0) + int64(total))
		// Release the reservation now that tail has advanced: the bytes are
		// now counted in resident() rather than reservedBytes.
		s.walBuf.releaseReservation(int64(reserveSize))

		end = uint64(int64(start0) + int64(total))
		// CAS-max: multiple concurrent RLock holders may call this
		// simultaneously; we want the highest end value to persist.
		if s.writeLSNMirror != nil {
			storeMaxLSN(s.writeLSNMirror, end)
		}
		// s.writePos and s.prevRecPtr are NOT updated here: Lock() paths
		// (appendPGCompat Path A, appendRaw) derive them from core.Load()
		// after all RLock holders have released. s.writeLSN is left for
		// the state loop's own Lock-path tracking (flushUpTo reads the
		// atomic writeLSNMirror instead of s.writeLSN).
		start = start0 + uint64(leading) + 1 // 1-based record start
		if s.onAppend != nil {
			s.onAppend()
		}
		return start, end, true, nil
	}

	// Non-pageHeaders (legacy binary format): original path.
	record := encodeRecord(payload)
	realRecLen := len(record)

	s.appendMu.Lock()
	defer s.appendMu.Unlock()

	writePos := s.writePos
	start = uint64(writePos) + 1

	// Path A: record larger than the buffer — can't handle without I/O.
	if !s.walBuf.canHold(realRecLen) {
		return 0, 0, false, nil
	}

	// Path B: buffered append.  If we'd overflow, fall back to the
	// state loop (which will drain first).
	if int64(realRecLen) > s.walBuf.free() {
		return 0, 0, false, nil
	}

	s.walBuf.append(record)
	s.memRing.Append(writePos, record)
	s.writePos = writePos + int64(len(record))
	end = uint64(s.writePos)
	s.writeLSN = end
	if s.writeLSNMirror != nil {
		s.writeLSNMirror.Store(end)
	}
	if s.onAppend != nil {
		s.onAppend()
	}
	return start, end, true, nil
}

// drainBufferBytes writes at least `n` bytes from walBuf's head to
// segment files via state.writeAt, advances walBuf.head, and bumps
// drainedLSN. Drained segments are added to s.dirty (already
// happens inside writeAt via openSegment's dirty-flag bookkeeping)
// so the next flushUpTo's dataSync includes them — the "sync debt"
// the milestone calls out.
//
// The caller has already verified walBuf is non-nil and the record
// being appended is ≤ walBuf.cap. `reason` classifies the drain
// for the M0013-0003 observability counters (overflow vs flush).
func (s *state) drainBufferBytes(n int64, reason drainReason) error {
	if n <= 0 || s.walBuf == nil {
		return nil
	}
	if n > s.walBuf.resident() {
		n = s.walBuf.resident()
	}
	first, second := s.walBuf.readForDrain(n)
	pos := int64(s.drainedLSN)
	if len(first) > 0 {
		if err := s.writeAt(pos, first); err != nil {
			return err
		}
		pos += int64(len(first))
	}
	if len(second) > 0 {
		if err := s.writeAt(pos, second); err != nil {
			return err
		}
		pos += int64(len(second))
	}
	s.walBuf.advanceHead(n)
	s.drainedLSN = uint64(pos)
	if c := s.walBufferCounters; c != nil {
		switch reason {
		case drainReasonOverflow:
			c.overflowDrainBytes.Add(n)
		case drainReasonFlush:
			c.flushDrainBytes.Add(n)
		}
	}
	return nil
}

// stripeNum returns the caller's process number for stripe selection in
// core.AppendXLogPayload. Falls back to 0 for goroutines that are not
// registered in the activity registry (initdb, checkpointer, walreceiver,
// tests) — they all land on stripe 0, which is always valid.
func (s *state) stripeNum() int32 {
	_, procNum, ok := activity.LookupCurrentGoroutine()
	if !ok {
		return 0
	}
	return procNum
}

// drainBufferUpTo drains every byte from walBuf.head through (but
// not past) targetLSN. Used by flushUpTo's Stage 1 — every byte
// ≤ targetLSN must land in segment files before the dataSync
// barrier runs. No-op if walBuf is nil or already drained past
// targetLSN.
func (s *state) drainBufferUpTo(targetLSN uint64) error {
	if s.walBuf == nil || targetLSN <= s.drainedLSN {
		return nil
	}
	need := int64(targetLSN - s.drainedLSN)
	return s.drainBufferBytes(need, drainReasonFlush)
}

// doSync runs the commit-path durability barrier selected by
// Config.SyncMethod (wal_sync_method). withDefaults/NewWriter
// already normalized+validated the value, so this is a closed
// two-way switch — "fsync" flushes inode metadata too, everything
// else (the "fdatasync" default) skips it.
func (s *state) doSync(f *os.File) error {
	if s.cfg.SyncMethod == "fsync" {
		return fullSync(f)
	}
	return dataSync(f)
}

func (s *state) flushUpTo(lsn uint64) error {
	if lsn == 0 {
		return nil
	}
	// Use writeLSNMirror (atomic) so LSNs written by concurrent
	// tryAppend goroutines under RLock are visible here without holding appendMu.
	writtenLSN := s.writeLSN
	if s.writeLSNMirror != nil {
		if m := s.writeLSNMirror.Load(); m > writtenLSN {
			writtenLSN = m
		}
	}
	if lsn > writtenLSN {
		return fmt.Errorf("%w: have %d, need %d", ErrLSNNotWritten, writtenLSN, lsn)
	}
	if lsn <= s.flushedLSN {
		return nil
	}

	// Stage 1 (M0013-0001): drain walBuf bytes ≤ lsn to segment
	// files so Stage 2's dataSync covers every byte the caller
	// asked for. No-op when walBuf is nil or already drained.
	if err := s.drainBufferUpTo(lsn); err != nil {
		return err
	}

	targetSeg := segmentForLSN(lsn, s.cfg.SegmentSize)
	dirty := make([]uint64, 0, len(s.dirty))
	for seg := range s.dirty {
		if seg <= targetSeg {
			dirty = append(dirty, seg)
		}
	}
	sort.Slice(dirty, func(i, j int) bool { return dirty[i] < dirty[j] })

	for _, seg := range dirty {
		f, err := s.openSegment(seg)
		if err != nil {
			return err
		}
		// wal_sync_method dispatch: fdatasync on Linux (skips inode
		// metadata that hasn't changed thanks to preallocation) is
		// the default; fsync additionally flushes inode metadata.
		// See docs/design/0007-0002-fdatasync-commit-path.md.
		if err := s.doSync(f); err != nil {
			return fmt.Errorf("wal: %s %s: %w", s.cfg.SyncMethod, f.Name(), err)
		}
		if s.walBufferCounters != nil {
			s.walBufferCounters.fsyncCount.Add(1)
		}
		delete(s.dirty, seg)
	}

	s.flushedLSN = lsn
	// M0057-0001: LOG each WAL flush so benchmark runs show WAL writer activity.
	l := s.cfg.Logger
	if l == nil {
		l = slog.Default()
	}
	l.Info("walwriter flush", "lsn", lsn, "segments_fsynced", len(dirty))
	return nil
}

// removeOldSegments retires every segment whose final byte sits
// strictly before the segment containing keepLSN, either by
// unlinking it or — for up to `ceil(Config.MinWALSize/SegmentSize)`
// of the newest obsolete segments — recycling it into a fresh future
// segment slot. Runs on the loop goroutine so it serialises with
// append/flush and won't race with openSegment.
//
// Behaviour notes:
//   - The segment that contains keepLSN is preserved (the standby /
//     recovery still needs to read records inside it).
//   - Open file handles for retired segments are closed and dropped
//     from s.files so a subsequent writeAt that somehow targets a
//     stale segment number reopens fresh (it shouldn't — keepLSN
//     guarantees we only retire fully-superseded segments — but the
//     defensive close keeps the invariant explicit).
//   - dirty flags for retired segments are dropped: the bytes were
//     superseded, no fdatasync owes them anymore.
//   - Missing files are silently skipped so a partially-cleaned
//     directory (e.g. a manual rm during testing) doesn't wedge the
//     loop.
//   - Recycling targets the lowest-numbered free segment slot at or
//     after keepSeg (mirrors upstream InstallXLogFileSegment's
//     find_free scan), so it never clobbers a segment that already
//     exists. The recycled file is zero-filled after the rename —
//     unlike upstream, which leaves the old content in place — because
//     decodeRecord's graceful-EOS heuristic (docs/design/0007-0001-wal-segment-preallocation.md)
//     distinguishes "end of valid WAL" from "corruption" by checking
//     whether the bytes past a bad/absent record are all zero. A
//     recycled segment's leftover content is a real, well-formed
//     record from its previous life (valid CRC and all), so leaving
//     it in place would let recovery misread stale history as live
//     WAL. See docs/design/0122-0009-wal-segment-recycling.md.
func (s *state) removeOldSegments(keepLSN uint64) (removed, recycled int, err error) {
	if keepLSN == 0 {
		return 0, 0, nil
	}
	keepSeg := segmentForLSN(keepLSN, s.cfg.SegmentSize)
	if keepSeg == 0 {
		return 0, 0, nil
	}

	entries, err := os.ReadDir(s.cfg.WALDir)
	if err != nil {
		return 0, 0, fmt.Errorf("wal: list %s: %w", s.cfg.WALDir, err)
	}

	type staleSeg struct {
		segNo uint64
		name  string
	}
	existing := make(map[uint64]bool, len(entries))
	var stale []staleSeg
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		segNo, ok := parseSegmentName(e.Name())
		if !ok {
			continue
		}
		existing[segNo] = true
		if segNo < keepSeg {
			stale = append(stale, staleSeg{segNo, e.Name()})
		}
	}

	spares := 0
	if s.cfg.MinWALSize > 0 && s.cfg.SegmentSize > 0 {
		spares = int((s.cfg.MinWALSize + s.cfg.SegmentSize - 1) / s.cfg.SegmentSize)
	}

	// Recycle the newest obsolete segments first (mirrors upstream's
	// RemoveOldXlogFiles, which walks toward the retention cutoff from
	// the current end of WAL).
	sort.Slice(stale, func(i, j int) bool { return stale[i].segNo > stale[j].segNo })

	nextTarget := keepSeg
	anyRecycled := false
	for _, o := range stale {
		if f, open := s.files[o.segNo]; open {
			_ = f.Close()
			delete(s.files, o.segNo)
		}
		delete(s.dirty, o.segNo)
		oldPath := filepath.Join(s.cfg.WALDir, o.name)

		if recycled < spares {
			for existing[nextTarget] {
				nextTarget++
			}
			newPath := filepath.Join(s.cfg.WALDir, formatSegmentName(nextTarget))
			if rerr := recycleSegmentFile(oldPath, newPath, s.cfg.SegmentSize); rerr != nil {
				if errors.Is(rerr, os.ErrNotExist) {
					continue
				}
				return removed, recycled, rerr
			}
			delete(existing, o.segNo)
			existing[nextTarget] = true
			nextTarget++
			recycled++
			anyRecycled = true
			continue
		}

		if rerr := os.Remove(oldPath); rerr != nil {
			if errors.Is(rerr, os.ErrNotExist) {
				continue
			}
			return removed, recycled, fmt.Errorf("wal: remove %s: %w", oldPath, rerr)
		}
		removed++
	}

	if anyRecycled {
		// Directory fsync makes the renamed dirents durable, mirroring
		// openSegment's post-creation fsync for brand-new segments.
		if dir, derr := os.Open(s.cfg.WALDir); derr == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
	}
	return removed, recycled, nil
}

// recycleSegmentFile renames an obsolete WAL segment into a fresh
// future segment slot and zero-fills it. See removeOldSegments for
// why the zero-fill (absent from upstream's equivalent
// InstallXLogFileSegment) is required here.
func recycleSegmentFile(oldPath, newPath string, size int64) error {
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("wal: recycle rename %s -> %s: %w", oldPath, newPath, err)
	}
	f, err := os.OpenFile(newPath, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("wal: recycle open %s: %w", newPath, err)
	}
	defer f.Close()
	return preallocateSegment(f, size)
}

func (s *state) writeAt(pos int64, buf []byte) error {
	if s.onWALWrite != nil {
		s.onWALWrite()
	}
	for len(buf) > 0 {
		segNo := uint64(pos / s.cfg.SegmentSize)
		segOff := pos % s.cfg.SegmentSize
		space := int(s.cfg.SegmentSize - segOff)
		chunk := len(buf)
		if chunk > space {
			chunk = space
		}

		f, err := s.openSegment(segNo)
		if err != nil {
			return err
		}

		// Per-segment write. Two dispatch paths:
		//   1. AIO engine attached: pwrite goes through
		//      Engine.Submit + Wait — observable in pg_aios /
		//      pg_stat_aio alongside heap writes.
		//   2. Neither: direct f.WriteAt (buffered by the OS).
		var n int
		if s.aio != nil {
			h := s.aio.Submit(AIOSubmitOp{
				File:      f,
				Buffer:    buf[:chunk],
				Offset:    segOff,
				Direction: AIODirWrite,
				Target:    f.Name(),
			})
			n, err = h.Wait()
		} else {
			n, err = f.WriteAt(buf[:chunk], segOff)
		}
		if err != nil {
			return fmt.Errorf("wal: write %s at %d: %w", f.Name(), segOff, err)
		}
		if n != chunk {
			return fmt.Errorf("wal: short write to %s: %d of %d", f.Name(), n, chunk)
		}

		s.dirty[segNo] = true
		pos += int64(chunk)
		buf = buf[chunk:]
	}
	return nil
}

func (s *state) openSegment(segNo uint64) (*os.File, error) {
	if f, ok := s.files[segNo]; ok {
		return f, nil
	}
	path := filepath.Join(s.cfg.WALDir, formatSegmentName(segNo))

	// Detect first-time creation so the preallocator only zero-fills
	// new segments. Existing files (legacy lazy mode, or a re-open
	// after server restart) are left alone — the EOS-sentinel rule
	// in decodeRecord handles their trailing zeros if any.
	wasNew := false
	if s.cfg.Preallocate {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			wasNew = true
		} else if err != nil {
			return nil, fmt.Errorf("wal: stat %s: %w", path, err)
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("wal: open %s: %w", path, err)
	}

	if wasNew {
		if err := preallocateSegment(f, s.cfg.SegmentSize); err != nil {
			_ = f.Close()
			return nil, err
		}
		if s.walBufferCounters != nil {
			s.walBufferCounters.segmentsPreallocated.Add(1)
			s.walBufferCounters.preallocatedBytes.Add(s.cfg.SegmentSize)
		}
		// Directory fsync makes the new dirent durable. Mirrors
		// upstream's fsync_fname behaviour.
		if dir, derr := os.Open(s.cfg.WALDir); derr == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
	}

	s.files[segNo] = f
	return f, nil
}

// preallocateSegment zero-fills f to size and fsyncs it. Mirrors
// upstream's XLogFileInitInternal zero-write loop. The simple
// loop is portable across filesystems; posix_fallocate is a
// follow-up optimisation.
func preallocateSegment(f *os.File, size int64) error {
	const chunk = 1 << 16 // 64 KiB
	zero := make([]byte, chunk)
	written := int64(0)
	for written < size {
		n := int64(chunk)
		if size-written < n {
			n = size - written
		}
		if _, err := f.WriteAt(zero[:n], written); err != nil {
			return fmt.Errorf("wal: preallocate %s: %w", f.Name(), err)
		}
		written += n
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("wal: preallocate fsync %s: %w", f.Name(), err)
	}
	return nil
}

// storeMaxLSN atomically advances *ptr to val if val > ptr.Load().
// Required when multiple goroutines holding RLock concurrently update
// writeLSNMirror — plain Store would let a goroutine with a lower LSN
// overwrite a higher one written by a peer that finished first.
func storeMaxLSN(ptr *atomic.Uint64, val uint64) {
	for {
		cur := ptr.Load()
		if val <= cur {
			return
		}
		if ptr.CompareAndSwap(cur, val) {
			return
		}
	}
}

func (s *state) close() error {
	var firstErr error
	closeLSN := s.writeLSN
	if s.writeLSNMirror != nil {
		if m := s.writeLSNMirror.Load(); m > closeLSN {
			closeLSN = m
		}
	}
	if closeLSN > 0 {
		if err := s.flushUpTo(closeLSN); err != nil {
			firstErr = err
		}
	}
	for _, f := range s.files {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.files = nil
	s.dirty = nil
	return firstErr
}

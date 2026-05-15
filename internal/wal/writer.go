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
)

// groupFlushReq represents a single caller's flush request in the
// group-commit queue. M0098-0002.
type groupFlushReq struct {
	lsn  uint64
	done chan struct{} // closed by writer goroutine when flush is complete
	err  error        // set before done is closed
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
}

type opKind int

const (
	opAppend opKind = iota
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

	// appendMu serialises access to walBuf / memRing / writePos /
	// writeLSN / drainedLSN so that Writer.Append (M0026 concurrent
	// append) and the state loop's drain path do not race.
	appendMu sync.Mutex

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

	// fg is the group-commit flush queue, shared with Writer. M0098-0002.
	fg *flushGroup
}

// walBufferCounters holds the atomic lifetime counters that
// pg_stat_wal_io's M0013-0003 columns surface. The two drain
// buckets are kept separate so an operator can tell a sizing
// problem (high overflowDrainBytes) from natural commit /
// eviction durability cost (high flushDrainBytes). Single
// allocation owned by Writer; shared with state via pointer.
type walBufferCounters struct {
	overflowDrainBytes atomic.Uint64
	flushDrainBytes    atomic.Uint64
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
	return w.walBufferCounters.overflowDrainBytes.Load()
}

// WALBuffersFlushDrainBytes returns the lifetime total bytes
// drained from the buffer by FlushUpTo's Stage 1 (commits,
// eviction, checkpoint). High values are typical and healthy —
// the buffer's natural cost of WAL durability. Atomic load.
func (w *Writer) WALBuffersFlushDrainBytes() uint64 {
	if w.walBufferCounters == nil {
		return 0
	}
	return w.walBufferCounters.flushDrainBytes.Load()
}

// WrittenLSN returns the LSN of the last byte the writer has
// appended (durable or not). Cheap and lock-free; suitable for
// the checkpointer's max_wal_size trigger.
func (w *Writer) WrittenLSN() uint64 {
	return w.writeLSNAtomic.Load()
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

// RemoveOldSegments unlinks any WAL segment file whose contents end
// strictly before the segment that contains keepLSN. The segment
// containing keepLSN — and every segment after it — is preserved, so
// the caller can pass `min(checkpointLSN, min(slot.RestartLSN))` and
// be sure no record needed for crash recovery or for an attached
// standby is removed.
//
// keepLSN == 0 is a no-op (no records have been written yet).
//
// The op runs on the writer's serialised loop so it cannot race with
// an in-flight Append or Flush. Open file handles for removed segments
// are closed before the unlink and dropped from the writer's cache so
// subsequent writes never re-touch a deleted file.
//
// Returns the number of segment files that were removed.
func (w *Writer) RemoveOldSegments(keepLSN uint64) (int, error) {
	if keepLSN == 0 {
		return 0, nil
	}
	resp := make(chan result, 1)
	if err := w.send(op{kind: opRecycle, lsn: keepLSN, resp: resp}); err != nil {
		return 0, err
	}
	r := <-resp
	return r.removed, r.err
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
		cfg:        cfg,
		writePos:   writePos,
		writeLSN:   uint64(writePos),
		flushedLSN: uint64(writePos),
		drainedLSN: uint64(writePos),
		files:      make(map[uint64]*os.File),
		dirty:      make(map[uint64]bool),
		aio:        cfg.AIO,
		memRing:    NewMemRing(cfg.SenderMemoryBuffer),
		walBuf:     newWALBuffer(cfg.WALBuffers),
		pageHeaders: cfg.PageHeaders,
		prevRecPtr: prevRecPtr,
		sysID:      cfg.SystemID,
		tli:        cfg.TimelineID,
	}
	if st.walBuf != nil {
		st.walBuf.reset(writePos)
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
		if st.Size() < 0 || st.Size() > segSize {
			return 0, 0, fmt.Errorf("wal: invalid segment size %d for %s", st.Size(), e.Name())
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
		prevRecPtr = lastRecPtr
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
			case opFlush:
				// Legacy path kept for backward compatibility.
				// New code routes through flushSig via FlushUpTo.
				req.resp <- result{err: s.flushUpTo(req.lsn)}
			case opRecycle:
				n, err := s.removeOldSegments(req.lsn)
				req.resp <- result{removed: n, err: err}
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
	record := encodeRecord(payload)
	realRecLen := len(record)
	if s.pageHeaders {
		var err error
		record, realRecLen, err = encodeRecordXLog(payload, s.prevRecPtr)
		if err != nil {
			return 0, 0, err
		}
	}

	// Path A: WAL buffer disabled, OR record larger than the
	// entire buffer. Bypass and write straight to disk.
	//
	// M0099: The original code read s.writePos before acquiring appendMu,
	// allowing concurrent tryAppend callers to advance s.writePos in the
	// window between the read and the appendMu-protected update. Path A
	// would then clobber s.writePos with a stale value, producing backwards
	// LSN accounting and eventual FlushUpTo(uint64_max) errors.
	//
	// Fix: acquire appendMu first to read the authoritative writePos, then
	// advance s.writePos as a reservation BEFORE releasing the lock (so
	// concurrent tryAppend callers write their records AFTER this one),
	// then do I/O outside the lock, then finalise under appendMu.
	//
	// Note: walBuf == nil check must be done before acquiring appendMu
	// (walBuf is set once at init, immutable thereafter).
	if s.walBuf == nil || !s.walBuf.canHold(len(record)) {
		// Re-encode under appendMu so writePos is consistent with stream.
		s.appendMu.Lock()
		writePos := s.writePos // authoritative read
		stream := record
		leading := 0
		if s.pageHeaders {
			stream, leading = emitWithPageHeaders(record, realRecLen, writePos, s.cfg.SegmentSize, s.sysID, s.tli)
		}
		start := uint64(writePos) + uint64(leading) + 1
		// Reserve the position: advance writePos so concurrent tryAppend
		// callers write their records after this large record.
		s.writePos = writePos + int64(len(stream))
		end := uint64(s.writePos)
		s.writeLSN = end // optimistic; rolled back below on I/O error
		if s.writeLSNMirror != nil {
			s.writeLSNMirror.Store(end)
		}
		s.appendMu.Unlock()

		if err := s.writeAt(writePos, stream); err != nil {
			// Roll back the reservation on I/O error (rare; WAL errors
			// are typically fatal, but restore state for callers that
			// handle errors gracefully).
			s.appendMu.Lock()
			if s.writePos == writePos+int64(len(stream)) {
				s.writePos = writePos
				s.writeLSN = uint64(writePos)
				if s.writeLSNMirror != nil {
					s.writeLSNMirror.Store(s.writeLSN)
				}
			}
			s.appendMu.Unlock()
			return 0, 0, err
		}

		s.appendMu.Lock()
		s.memRing.Append(writePos, stream)
		s.drainedLSN = end
		if s.pageHeaders {
			s.prevRecPtr = start - 1
		}
		s.appendMu.Unlock()
		return start, end, nil
	}

	// Path B: buffered append. Acquire appendMu first to read writePos
	// authoritatively (same reason as Path A fix above), then compute
	// stream layout, drain if needed, and append to walBuf.

	// Path B: buffered append. Serialise buffer access and LSN
	// update under appendMu so we don't race with tryAppend.
	s.appendMu.Lock()
	defer s.appendMu.Unlock()

	// Re-read writePos under appendMu to get the authoritative position.
	writePos := s.writePos
	stream := record
	leading := 0
	if s.pageHeaders {
		stream, leading = emitWithPageHeaders(record, realRecLen, writePos, s.cfg.SegmentSize, s.sysID, s.tli)
	}
	start := uint64(writePos) + uint64(leading) + 1

	need := int64(len(stream)) - s.walBuf.free()
	if need > 0 {
		if err := s.drainBufferBytes(need, drainReasonOverflow); err != nil {
			return 0, 0, err
		}
	}
	s.walBuf.append(stream)
	// Walsender's MemRing must see every record, regardless of
	// whether it's in walBuf or already on a segment — keeps the
	// streaming-from-RAM invariant unchanged from M0010-0002.
	s.memRing.Append(writePos, stream)
	s.writePos = writePos + int64(len(stream))
	end := uint64(s.writePos)
	s.writeLSN = end
	if s.writeLSNMirror != nil {
		s.writeLSNMirror.Store(end)
	}
	if s.pageHeaders {
		// prevRecPtr tracks the 0-based PostgreSQL RecPtr of this record's
		// start so the NEXT record's xl_prev field is filled correctly.
		// start is 1-based (goopg convention), so RecPtr = start - 1.
		s.prevRecPtr = start - 1
	}
	return start, end, nil
}

// tryAppend attempts a fast-path append directly on the calling
// goroutine.  Returns ok=true when the record was buffered without
// I/O; returns ok=false when the caller must fall back to the
// state-loop path (buffer overflow or other condition requiring
// disk write).  M0026: this eliminates the state-loop round-trip
// for the common case (buffer not full).
func (s *state) tryAppend(payload []byte) (start, end uint64, ok bool, err error) {
	record := encodeRecord(payload)
	realRecLen := len(record)
	if s.pageHeaders {
		record, realRecLen, err = encodeRecordXLog(payload, s.prevRecPtr)
		if err != nil {
			return 0, 0, false, err
		}
	}

	s.appendMu.Lock()
	defer s.appendMu.Unlock()

	writePos := s.writePos
	stream := record
	leading := 0
	if s.pageHeaders {
		stream, leading = emitWithPageHeaders(record, realRecLen,
			writePos, s.cfg.SegmentSize, s.sysID, s.tli)
	}
	start = uint64(writePos) + uint64(leading) + 1

	// Path A: record larger than the buffer — can't handle without I/O.
	if !s.walBuf.canHold(len(stream)) {
		return 0, 0, false, nil
	}

	// Path B: buffered append.  If we'd overflow, fall back to the
	// state loop (which will drain first).
	if int64(len(stream)) > s.walBuf.free() {
		return 0, 0, false, nil
	}

	s.walBuf.append(stream)
	s.memRing.Append(writePos, stream)
	s.writePos = writePos + int64(len(stream))
	end = uint64(s.writePos)
	s.writeLSN = end
	if s.writeLSNMirror != nil {
		s.writeLSNMirror.Store(end)
	}
	if s.pageHeaders {
		// prevRecPtr tracks the 0-based PostgreSQL RecPtr of this record's
		// start so the NEXT record's xl_prev field is filled correctly.
		// start is 1-based (goopg convention), so RecPtr = start - 1.
		s.prevRecPtr = start - 1
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
			c.overflowDrainBytes.Add(uint64(n))
		case drainReasonFlush:
			c.flushDrainBytes.Add(uint64(n))
		}
	}
	return nil
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

func (s *state) flushUpTo(lsn uint64) error {
	if lsn == 0 {
		return nil
	}
	if lsn > s.writeLSN {
		return fmt.Errorf("%w: have %d, need %d", ErrLSNNotWritten, s.writeLSN, lsn)
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
		// fdatasync on Linux (skips inode metadata that hasn't
		// changed thanks to preallocation), full fsync fallback
		// elsewhere. See
		// docs/design/0007-0002-fdatasync-commit-path.md.
		if err := dataSync(f); err != nil {
			return fmt.Errorf("wal: fdatasync %s: %w", f.Name(), err)
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

// removeOldSegments unlinks every segment whose final byte sits
// strictly before the segment containing keepLSN. Runs on the loop
// goroutine so it serialises with append/flush and won't race with
// openSegment.
//
// Behaviour notes:
//   - The segment that contains keepLSN is preserved (the standby /
//     recovery still needs to read records inside it).
//   - Open file handles for removed segments are closed and dropped
//     from s.files so a subsequent writeAt that somehow targets a
//     stale segment number reopens fresh (it shouldn't — keepLSN
//     guarantees we only delete fully-superseded segments — but the
//     defensive close keeps the invariant explicit).
//   - dirty flags for removed segments are dropped: the bytes were
//     superseded, no fdatasync owes them anymore.
//   - Missing files are silently skipped so a partially-cleaned
//     directory (e.g. a manual rm during testing) doesn't wedge the
//     loop.
func (s *state) removeOldSegments(keepLSN uint64) (int, error) {
	if keepLSN == 0 {
		return 0, nil
	}
	keepSeg := segmentForLSN(keepLSN, s.cfg.SegmentSize)
	if keepSeg == 0 {
		return 0, nil
	}

	entries, err := os.ReadDir(s.cfg.WALDir)
	if err != nil {
		return 0, fmt.Errorf("wal: list %s: %w", s.cfg.WALDir, err)
	}

	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		segNo, ok := parseSegmentName(e.Name())
		if !ok {
			continue
		}
		if segNo >= keepSeg {
			continue
		}
		if f, open := s.files[segNo]; open {
			_ = f.Close()
			delete(s.files, segNo)
		}
		delete(s.dirty, segNo)
		path := filepath.Join(s.cfg.WALDir, e.Name())
		if err := os.Remove(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return removed, fmt.Errorf("wal: remove %s: %w", path, err)
		}
		removed++
	}
	return removed, nil
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

func (s *state) close() error {
	var firstErr error
	if s.writeLSN > 0 {
		if err := s.flushUpTo(s.writeLSN); err != nil {
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

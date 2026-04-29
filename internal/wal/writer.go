package wal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
)

const (
	// DefaultSegmentSize matches PostgreSQL's default WAL segment size.
	DefaultSegmentSize int64 = 16 * 1024 * 1024
)

var (
	ErrClosed        = errors.New("wal: writer closed")
	ErrLSNNotWritten = errors.New("wal: requested LSN is beyond written WAL")
	// ErrEmptyPayload guards the EOS sentinel: a zero
	// (len=0, crc=0) header is reserved as "no record here yet"
	// in preallocated segments. See
	// docs/design/0007-0001-wal-segment-preallocation.md.
	ErrEmptyPayload = errors.New("wal: empty payload not allowed")
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

	// DirectIO requests Linux O_DIRECT writes for WAL segment
	// pwrite/pwritev calls so newly-written WAL bytes don't
	// pollute the OS page cache (mitigates the cache-pressure
	// pathology M0010 documents on write-heavy primaries).
	// Mirrors upstream's `wal_direct_io` GUC. The writer
	// probes the WALDir filesystem for O_DIRECT support at
	// construction time; on filesystems / kernels that don't
	// honour the flag (tmpfs, overlayfs, FUSE, every
	// non-Linux GOOS) it transparently downgrades to buffered
	// writes and surfaces the reason via
	// `Writer.DirectIOFallbackReason()`. Default false keeps
	// the legacy buffered behaviour. See
	// docs/design/0010-0001-wal-direct-io-write-path.md.
	//
	// Phase 1 (M0010-0001a, landed): the probe + fallback
	// reporting + plumbing surface this field flips. The
	// segment-open flag flip + per-write alignment path
	// (RMW for partial trailing blocks) lands in M0010-0001b.
	// Setting DirectIO=true today is a no-op at the syscall
	// layer beyond the probe; the GUC value flows through the
	// startup log so an operator can verify the rollout
	// without surprising correctness regressions while Phase
	// 2 is in flight.
	DirectIO bool

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

	// directIORequested mirrors Config.DirectIO; cached on the
	// Writer so cmd/goopg start can read it without reaching
	// into state. directIOFallbackReason captures the probe
	// outcome — empty when (a) DirectIO=false, or (b)
	// DirectIO=true and the probe succeeded; non-empty when
	// the probe rejected O_DIRECT. See
	// docs/design/0010-0001-wal-direct-io-write-path.md.
	directIORequested      bool
	directIOFallbackReason string

	// memRing is shared with state.memRing (one allocation,
	// two pointers). Exposed via MemRing() so the iterator
	// and observability code can read it without serialising
	// through the writer's loop. nil when
	// Config.SenderMemoryBuffer == 0. See
	// docs/design/0010-0002-walsender-in-memory-wal-handoff.md.
	memRing *MemRing

	// directIOCounters shares atomics with state for the
	// pg_stat_wal_io view. Allocated once at construction; never
	// reseated. Read by Writer.DirectIOWrites() /
	// Writer.TailRMWWrites().
	directIOCounters *directIOCounters

	// walBufferCounters shares atomics with state for the
	// M0013-0003 wal_buffers_* columns of pg_stat_wal_io.
	walBufferCounters *walBufferCounters
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

	// directIORequested mirrors Config.DirectIO.
	directIORequested bool

	// directIOFallbackReason is non-empty when Config.DirectIO
	// was true but the filesystem probe rejected O_DIRECT
	// (tmpfs / overlayfs / non-Linux). The cmd/goopg start
	// logger surfaces it as `event=wal_direct_io_fallback` so
	// an operator can grep for the misconfiguration. Empty
	// when (a) DirectIO=false, or (b) DirectIO=true and the
	// probe succeeded.
	directIOFallbackReason string

	// directIOActive is true when the operator requested direct
	// I/O AND the probe succeeded — the only state in which
	// `state.openSegment` flips O_DIRECT on the segment fd and
	// `state.writeAt` routes through `writeAtDirectIO`'s aligned
	// RMW path. Snapshot of (`directIORequested && fallback==""`)
	// captured at writer construction.
	directIOActive bool

	// directIOBlockSize is the alignment / minimum-write
	// granularity O_DIRECT enforces on this filesystem. ext4 and
	// XFS use 4 KiB by default; the value is hard-coded here
	// because every modern Linux DIO-capable FS satisfies a
	// 4 KiB boundary. STATX_DIOALIGN-driven detection is a
	// future optimisation — wrong-but-larger alignment never
	// causes EINVAL, only wastes a few extra RMW bytes.
	directIOBlockSize int64

	// directIOScratch is a page-aligned mmap'd buffer used by
	// writeAtDirectIO's RMW (read aligned region → overlay user
	// bytes → write aligned region back). Lazy-allocated on
	// first directIO write, freed at close. Sized at
	// directIOScratchCap so a typical record write needs one
	// pread + one pwrite (no scratch-loop iterations).
	directIOScratch []byte

	// memRing mirrors recently-written WAL bytes in RAM so
	// walsender's RecordIterator can stream without paying for
	// disk reads (especially important when wal_direct_io=on
	// keeps WAL out of the OS page cache). nil when
	// Config.SenderMemoryBuffer == 0; iterators always go to
	// disk in that case.
	memRing *MemRing

	// directIOCounters mirrors the Writer.directIOCounters
	// pointer so the writeAtDirectIO hot path can bump the
	// observability counters without indirecting through
	// Writer (state is the layer that owns the actual writes).
	// Pointer-shared with Writer so both ends see the same
	// atomics. Read by the pg_stat_wal_io virtual view.
	directIOCounters *directIOCounters

	// walBufferCounters mirrors Writer.walBufferCounters via
	// pointer — drainBufferBytes bumps these (M0013-0003).
	walBufferCounters *walBufferCounters
}

// directIOCounters holds the atomic counters wal-direct-I/O
// accumulates for the pg_stat_wal_io view. Single allocation
// shared between Writer and state.
type directIOCounters struct {
	directWrites  atomic.Uint64 // every writeAtDirectIO region
	tailRMWWrites atomic.Uint64 // subset where the user range wasn't block-aligned
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

// directIOScratchCap caps the per-call RMW scratch buffer at
// 1 MiB. WAL writes are usually much smaller (one append =
// header + payload ≤ a few KiB); the 1 MiB ceiling keeps the
// mmap'd region small while still handling outsized batches in
// one pread/pwrite. writeAtDirectIO loops when the requested
// region exceeds the scratch.
const directIOScratchCap = 1 << 20

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

	counters := &directIOCounters{}
	st.directIOCounters = counters
	bufCounters := &walBufferCounters{}
	st.walBufferCounters = bufCounters
	w := &Writer{
		ops:                    make(chan op),
		done:                   make(chan struct{}),
		directIORequested:      st.directIORequested,
		directIOFallbackReason: st.directIOFallbackReason,
		memRing:                st.memRing,
		directIOCounters:       counters,
		walBufferCounters:      bufCounters,
	}
	w.writeLSNAtomic.Store(uint64(st.writePos))
	st.writeLSNMirror = &w.writeLSNAtomic
	st.onAppend = w.notifyAppend
	go st.loop(w.ops, w.done)
	return w, nil
}

// DirectIOFallbackReason reports the human-readable reason the
// O_DIRECT probe rejected `wal_direct_io=on`, or the empty string
// when (a) the operator did not request direct I/O, or (b) the
// probe succeeded. cmd/goopg start consults this and emits
// `event=wal_direct_io_fallback` at startup when it's non-empty.
// Returned value is stable for the lifetime of the Writer (the
// probe only runs once at construction).
func (w *Writer) DirectIOFallbackReason() string {
	return w.directIOFallbackReason
}

// DirectIORequested reports whether the operator asked for direct
// I/O via `wal_direct_io=on`. Independent of whether the probe
// succeeded — combine with DirectIOFallbackReason to determine the
// effective mode (requested && !fallback → direct I/O active in
// Phase 2; otherwise buffered).
func (w *Writer) DirectIORequested() bool {
	return w.directIORequested
}

// MemRing returns the in-memory WAL ring. nil when
// `wal_sender_memory_buffer = 0` (or absent). RecordIterator
// consults this on every read to decide between RAM and disk;
// observability code reads its Hits()/Misses() counters. The
// returned pointer is stable for the writer's lifetime; callers
// must not mutate the ring's internal state.
func (w *Writer) MemRing() *MemRing { return w.memRing }

// DirectIOWrites returns the lifetime count of writeAtDirectIO
// regions written successfully — observable via the
// pg_stat_wal_io view's `direct_writes` column. Always 0 when
// `wal_direct_io=off` or the probe fell back. Atomic load.
func (w *Writer) DirectIOWrites() uint64 {
	if w.directIOCounters == nil {
		return 0
	}
	return w.directIOCounters.directWrites.Load()
}

// TailRMWWrites returns the subset of DirectIOWrites where the
// user range wasn't already block-aligned and the writer paid
// for a real read-modify-write (head pad and/or tail pad).
// Operators watch this to gauge alignment-induced overhead;
// when it equals DirectIOWrites every direct-I/O write is RMW.
func (w *Writer) TailRMWWrites() uint64 {
	if w.directIOCounters == nil {
		return 0
	}
	return w.directIOCounters.tailRMWWrites.Load()
}

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
// Empty payloads are rejected because the encoded zero header
// (len=0, crc=0) is reserved as the EOS sentinel in preallocated
// segments — see
// docs/design/0007-0001-wal-segment-preallocation.md. No
// production caller emits empty records.
func (w *Writer) Append(payload []byte) (uint64, uint64, error) {
	if len(payload) == 0 {
		return 0, 0, ErrEmptyPayload
	}
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
func (w *Writer) FlushUpTo(lsn uint64) error {
	if lsn == 0 {
		return nil
	}
	resp := make(chan result, 1)
	if err := w.send(op{kind: opFlush, lsn: lsn, resp: resp}); err != nil {
		return err
	}
	return (<-resp).err
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
	writePos, err := detectWritePos(cfg.WALDir, cfg.SegmentSize)
	if err != nil {
		return nil, err
	}
	// Direct-I/O probe: only runs when the operator actually
	// requested it via `wal_direct_io=on`. Probe cost is one
	// open + close + unlink; cheap enough to do unconditionally
	// at startup. Failure modes:
	//   - empty reason  → probe succeeded; Phase 2 may flip
	//     O_DIRECT on segment opens.
	//   - non-empty     → fallback to buffered writes; the
	//     startup logger surfaces the reason.
	//   - error return  → unexpected failure (permission denied,
	//     mkdir race); fail fast so misconfiguration is loud.
	var fallback string
	if cfg.DirectIO {
		fallback, err = probeDirectIO(cfg.WALDir)
		if err != nil {
			return nil, err
		}
	}
	st := &state{
		cfg:                    cfg,
		writePos:               writePos,
		writeLSN:               uint64(writePos),
		flushedLSN:             uint64(writePos),
		drainedLSN:             uint64(writePos),
		files:                  make(map[uint64]*os.File),
		dirty:                  make(map[uint64]bool),
		aio:                    cfg.AIO,
		directIORequested:      cfg.DirectIO,
		directIOFallbackReason: fallback,
		directIOActive:         cfg.DirectIO && fallback == "",
		directIOBlockSize:      4096,
		memRing:                NewMemRing(cfg.SenderMemoryBuffer),
		walBuf:                 newWALBuffer(cfg.WALBuffers),
	}
	if st.walBuf != nil {
		st.walBuf.reset(writePos)
	}
	return st, nil
}

func detectWritePos(walDir string, segSize int64) (int64, error) {
	entries, err := os.ReadDir(walDir)
	if err != nil {
		return 0, fmt.Errorf("wal: list %s: %w", walDir, err)
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
			return 0, fmt.Errorf("wal: stat %s: %w", e.Name(), err)
		}
		if st.Size() < 0 || st.Size() > segSize {
			return 0, fmt.Errorf("wal: invalid segment size %d for %s", st.Size(), e.Name())
		}
		segSizes[segNo] = st.Size()
	}

	if len(segSizes) == 0 {
		return 0, nil
	}

	segNos := make([]uint64, 0, len(segSizes))
	for seg := range segSizes {
		segNos = append(segNos, seg)
	}
	sort.Slice(segNos, func(i, j int) bool { return segNos[i] < segNos[j] })

	if segNos[0] != 0 {
		return 0, fmt.Errorf("wal: first segment is %s, expected %s", formatSegmentName(segNos[0]), formatSegmentName(0))
	}

	var writePos int64
	for i := 0; i < len(segNos); i++ {
		expected := uint64(i)
		if segNos[i] != expected {
			return 0, fmt.Errorf("wal: gap at segment %s", formatSegmentName(expected))
		}
		sz := segSizes[expected]
		if i < len(segNos)-1 && sz != segSize {
			return 0, fmt.Errorf("wal: non-final segment %s has size %d, expected %d", formatSegmentName(expected), sz, segSize)
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
		usedBytes, scanErr := scanLastSegmentEnd(walDir, expected, sz)
		if scanErr != nil {
			return 0, scanErr
		}
		writePos += usedBytes
	}

	return writePos, nil
}

// scanLastSegmentEnd reads the last segment from disk and returns
// the byte offset of the first EOS sentinel (a zero record header
// — `len=0 && crc=0`). Used by detectWritePos to recover the true
// write position from a preallocated full-size last segment, and
// also serves the legacy short-segment case (where the segment
// has no trailing zeros, and the scan returns the full size).
func scanLastSegmentEnd(walDir string, segNo uint64, segSize int64) (int64, error) {
	path := filepath.Join(walDir, formatSegmentName(segNo))
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("wal: read %s: %w", path, err)
	}
	if int64(len(data)) > segSize {
		return 0, fmt.Errorf("wal: segment %s too large: %d > %d", path, len(data), segSize)
	}
	off := 0
	for off < len(data) {
		if isZeroHeader(data[off:]) {
			return int64(off), nil
		}
		_, n, err := decodeRecord(data[off:])
		if err != nil {
			return 0, fmt.Errorf("wal: scan %s at offset %d: %w", path, off, err)
		}
		off += n
	}
	return int64(off), nil
}

func (s *state) loop(ops <-chan op, done chan<- struct{}) {
	defer close(done)
	for req := range ops {
		switch req.kind {
		case opAppend:
			start, end, err := s.append(req.payload)
			req.resp <- result{startLSN: start, endLSN: end, err: err}
			if err == nil && s.onAppend != nil {
				s.onAppend()
			}
		case opFlush:
			req.resp <- result{err: s.flushUpTo(req.lsn)}
		case opRecycle:
			n, err := s.removeOldSegments(req.lsn)
			req.resp <- result{removed: n, err: err}
		case opClose:
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
	}
}

func (s *state) append(payload []byte) (uint64, uint64, error) {
	record := encodeRecord(payload)
	start := uint64(s.writePos) + 1
	writePos := s.writePos

	// Path A: WAL buffer disabled, OR record larger than the
	// entire buffer. Bypass and write straight to disk —
	// fragmenting a single record across multiple drains would
	// complicate the contiguous-stream invariant for no benefit.
	if s.walBuf == nil || !s.walBuf.canHold(len(record)) {
		if err := s.writeAt(writePos, record); err != nil {
			return 0, 0, err
		}
		s.memRing.Append(writePos, record)
		s.writePos = writePos + int64(len(record))
		end := uint64(s.writePos)
		s.writeLSN = end
		s.drainedLSN = end
		if s.writeLSNMirror != nil {
			s.writeLSNMirror.Store(end)
		}
		return start, end, nil
	}

	// Path B: buffered append. If we'd overflow, drain the head
	// of the buffer to segment files first to make room.
	need := int64(len(record)) - s.walBuf.free()
	if need > 0 {
		if err := s.drainBufferBytes(need, drainReasonOverflow); err != nil {
			return 0, 0, err
		}
	}
	s.walBuf.append(record)
	// Walsender's MemRing must see every record, regardless of
	// whether it's in walBuf or already on a segment — keeps the
	// streaming-from-RAM invariant unchanged from M0010-0002.
	s.memRing.Append(writePos, record)
	s.writePos = writePos + int64(len(record))
	end := uint64(s.writePos)
	s.writeLSN = end
	if s.writeLSNMirror != nil {
		s.writeLSNMirror.Store(end)
	}
	return start, end, nil
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

		// Per-segment write. Three dispatch paths:
		//   1. directIOActive: writeAtDirectIO does an aligned
		//      RMW (read aligned region → overlay → write back).
		//      Bypasses the AIO engine — its Submit path holds
		//      onto the user's buffer pointer, which isn't
		//      page-aligned. AIO+DirectIO interaction is a
		//      perf-only follow-up; correctness already lands
		//      via the synchronous RMW.
		//   2. AIO engine attached (no DirectIO): pwrite goes
		//      through Engine.Submit + Wait — observable in
		//      pg_aios / pg_stat_aio alongside heap writes.
		//   3. Neither: legacy direct f.WriteAt path.
		// The wal writer's loop is single-threaded so every
		// Wait / RMW is inline; deferring across appends
		// requires restructuring the writer loop and is a
		// future slice.
		var n int
		switch {
		case s.directIOActive:
			n, err = s.writeAtDirectIO(f, segOff, buf[:chunk])
		case s.aio != nil:
			h := s.aio.Submit(AIOSubmitOp{
				File:      f,
				Buffer:    buf[:chunk],
				Offset:    segOff,
				Direction: AIODirWrite,
				Target:    f.Name(),
			})
			n, err = h.Wait()
		default:
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

// writeAtDirectIO writes buf to f at offset segOff under O_DIRECT
// alignment rules: the (offset, length, buffer) triple passed to
// pwrite must each be a multiple of directIOBlockSize. We satisfy
// this with a per-write read-modify-write through a page-aligned
// mmap'd scratch buffer:
//
//  1. Compute the aligned region [alignDown(segOff),
//     alignUp(segOff+len(buf))) covering the destination.
//  2. pread the existing region into scratch. Past-EOF bytes
//     (legacy lazy-grow case without `wal_init_zero=on`) are
//     padded with zeros — preserves the bytes-written-so-far
//     invariant. With `wal_init_zero=on` (M0007-0001) the segment
//     body is fully allocated and the pread always returns full.
//  3. Overlay buf onto scratch[segOff-regionStart:].
//  4. pwrite the full region back. O_DIRECT honours this because
//     scratch is page-aligned (mmap MAP_PRIVATE|MAP_ANONYMOUS),
//     regionStart is block-aligned by construction, and regionLen
//     is a multiple of directIOBlockSize.
//
// When the requested region exceeds directIOScratchCap, the loop
// processes one scratch-sized aligned slice at a time. Returns the
// number of user bytes written (always len(buf) on success) and
// the first error encountered.
//
// Trade-off: per-call RMW pays for two block-aligned syscalls per
// write. A future Phase 2.b slice will introduce write-buffering
// (accumulate user bytes in scratch until block-aligned, drain at
// flushUpTo) which amortises the read cost — see Phase 2 sketch in
// docs/design/0010-0001-wal-direct-io-write-path.md.
func (s *state) writeAtDirectIO(f *os.File, segOff int64, buf []byte) (int, error) {
	if s.directIOScratch == nil {
		scratch, err := allocAlignedScratch(directIOScratchCap)
		if err != nil {
			return 0, fmt.Errorf("wal: alloc DIO scratch: %w", err)
		}
		s.directIOScratch = scratch
	}

	bs := s.directIOBlockSize
	alignDown := func(x int64) int64 { return x &^ (bs - 1) }
	alignUp := func(x int64) int64 { return alignDown(x + bs - 1) }

	written := 0
	for written < len(buf) {
		userStart := segOff + int64(written)
		userEndAll := segOff + int64(len(buf))

		regionStart := alignDown(userStart)
		// Cap the region at directIOScratchCap. Then snap
		// the cap back down to a block boundary so pwrite
		// length stays aligned.
		userEnd := userEndAll
		if userEnd-regionStart > int64(directIOScratchCap) {
			userEnd = regionStart + int64(directIOScratchCap)
			userEnd = alignDown(userEnd)
			if userEnd <= userStart {
				return written, fmt.Errorf(
					"wal: DIO scratch %d too small for block size %d",
					directIOScratchCap, bs)
			}
		}
		regionEnd := alignUp(userEnd)
		regionLen := int(regionEnd - regionStart)
		scratch := s.directIOScratch[:regionLen]

		// Read the aligned region. Short reads (past-EOF) are
		// expected for legacy lazy-grow segments; zero-fill the
		// tail. With wal_init_zero=on the segment is preallocated
		// to SegmentSize so this branch is rarely taken — but it's
		// the correct behaviour for both modes.
		n, err := f.ReadAt(scratch, regionStart)
		if err != nil && err != io.EOF {
			return written, fmt.Errorf("wal: DIO RMW read %s at %d: %w",
				f.Name(), regionStart, err)
		}
		for i := n; i < regionLen; i++ {
			scratch[i] = 0
		}

		// Overlay the user bytes.
		copy(scratch[userStart-regionStart:], buf[written:int(userEnd-segOff)])

		// Write the aligned region back.
		wn, werr := f.WriteAt(scratch, regionStart)
		if werr != nil {
			return written, fmt.Errorf("wal: DIO RMW write %s at %d: %w",
				f.Name(), regionStart, werr)
		}
		if wn != regionLen {
			return written, fmt.Errorf("wal: DIO RMW short write to %s: %d of %d",
				f.Name(), wn, regionLen)
		}

		// M0010-0003 observability: bump direct-write counter
		// always; bump RMW counter only when the user range
		// wasn't already block-aligned (head pad OR tail pad
		// non-empty), which is when we paid for the read+overlay
		// dance rather than a clean aligned write.
		if c := s.directIOCounters; c != nil {
			c.directWrites.Add(1)
			if userStart != regionStart || userEnd != regionEnd {
				c.tailRMWWrites.Add(1)
			}
		}

		written = int(userEnd - segOff)
	}
	return written, nil
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

	// Phase 2 of M0010-0001: flip O_DIRECT on the live fd AFTER
	// preallocation finishes. Preallocation's 64-KiB heap-buffer
	// zero-fill writes can't satisfy O_DIRECT alignment (the
	// `make([]byte, chunk)` slice isn't page-aligned), so we
	// open buffered, lay the body down, then fcntl(F_SETFL) the
	// flag in. Subsequent pread/pwrite go through writeAtDirectIO's
	// RMW path which uses the per-state aligned scratch buffer.
	// See docs/design/0010-0001-wal-direct-io-write-path.md.
	if s.directIOActive {
		if err := enableDirectIO(f); err != nil {
			_ = f.Close()
			return nil, err
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
	if s.directIOScratch != nil {
		if err := freeAlignedScratch(s.directIOScratch); err != nil && firstErr == nil {
			firstErr = err
		}
		s.directIOScratch = nil
	}
	return firstErr
}

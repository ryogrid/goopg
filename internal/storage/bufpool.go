package storage

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Slot is one buffer-pool entry holding one Page. Callers receive
// *Slot from Pin and return it via Unpin. Direct field access is
// permitted under contentMu (held implicitly by the pin guarantee
// for single-writer flows).
type Slot struct {
	page Page // alias into the arena

	// Tag identifies the (rel, fork, block) the slot currently holds.
	// Mutated only with the pool mutex held.
	tag BufferTag

	// valid means the slot's page bytes correspond to disk content.
	// false during eviction, between the moment we drop the lookup
	// and the moment we finish reading from disk.
	valid bool

	// dirty means the page bytes have been mutated and must be
	// written out before reassignment.
	dirty bool

	// pinCount is incremented on Pin, decremented on Unpin.
	pinCount int32

	// usageCount is the clock-sweep "second chance" counter.
	usageCount uint8

	// fpiSinceCheckpoint records whether a full-page-image WAL
	// record has already been emitted for this page in the current
	// checkpoint epoch. Mutated only with the pool mutex held.
	// The checkpointer clears it across all slots after a
	// successful checkpoint via Pool.ResetCheckpointEpoch.
	fpiSinceCheckpoint bool

	// contentMu guards the Page bytes for read/write. The pool mutex
	// is dropped before we acquire this so I/O doesn't block lookups.
	contentMu sync.RWMutex
}

// Page returns the Page-typed view of the slot's storage. The caller
// must hold a pin on the slot.
func (s *Slot) Page() Page { return s.page }

// Tag returns the (rel, fork, block) the slot currently holds.
func (s *Slot) Tag() BufferTag { return s.tag }

// Lock acquires the page's exclusive content lock. Callers that
// mutate the page bytes (e.g. PageAddHeapTuple, PageSetHeapTupleXmax)
// must hold this between the start of the modification and the
// MarkDirty/Unlock sequence. Without it, two concurrent writers can
// race on the same line-pointer / upper-region update and produce
// corrupted tuples (upstream PostgreSQL holds a buffer content
// lock for the same window — see postgres/src/backend/storage/buffer/
// bufmgr.c LockBuffer(BUFFER_LOCK_EXCLUSIVE)).
func (s *Slot) Lock()    { s.contentMu.Lock() }
func (s *Slot) Unlock()  { s.contentMu.Unlock() }
func (s *Slot) RLock()   { s.contentMu.RLock() }
func (s *Slot) RUnlock() { s.contentMu.RUnlock() }

// Pool is the buffer manager. It is goroutine-safe.
type Pool struct {
	mgr   *Manager
	arena *arena
	slots []*Slot
	wal   WALFlusher
	// logFPI emits a full-page-image WAL record and returns the
	// record's end LSN. nil disables FPI emission. Set via
	// PoolConfig.LogPageImage.
	logFPI func(rel RelFileNode, blk BlockNumber, page Page) (LSN, error)
	// logBtreeSplit emits an atomic B-tree split WAL record
	// covering both halves of a split. Pulled out via
	// LogBtreeSplit() for the btree access method.
	logBtreeSplit LogBtreeSplitFunc
	// logHeapInsert emits a logical heap-insert WAL record.
	// Pulled out via LogHeapInsert() for the executor's
	// writeHeapRow path. nil disables the optimisation —
	// MarkDirtyChangeRecord falls back to FPI emission.
	logHeapInsert LogHeapInsertFunc
	// logBtreeInsert emits a logical B-tree non-split insert
	// WAL record. Pulled out via LogBtreeInsert() for the btree
	// access method. nil disables the optimisation.
	logBtreeInsert LogBtreeInsertFunc
	// logHeapDelete emits a logical heap-delete (xmax stamp)
	// WAL record. Used by the executor's UPDATE / DELETE paths.
	logHeapDelete LogHeapDeleteFunc
	// logHeapLock emits a logical heap-lock (lock-only xmax +
	// lock-strength) WAL record. Used by the SELECT FOR
	// UPDATE / FOR SHARE executor (M0021 tuple-level locking
	// step 2). nil disables the optimisation —
	// MarkDirtyChangeRecord falls back to FPI emission.
	logHeapLock LogHeapLockFunc
	// logHeapVacuum emits a logical heap-vacuum (page prune) WAL
	// record. Used by VACUUM to capture the dead-slot list rather
	// than a full-page image on each pruned page.
	logHeapVacuum LogHeapVacuumFunc
	// logHeapHotUpdate emits an atomic HOT-update WAL record
	// (M0046-0001): old-slot xmax stamp + new tuple insert on the
	// same page in one record. nil disables the optimisation and
	// the executor falls back to separate delete+insert FPIs.
	logHeapHotUpdate LogHeapHotUpdateFunc
	// logHeapPruneOpt emits an opportunistic page-pruning WAL record
	// (M0046-0002). nil disables the optimisation (FPI fallback).
	logHeapPruneOpt LogHeapPruneOptFunc
	// logSmgrCreate emits a relation-file creation WAL record
	// (RecordKindSmgrCreate) when Pool.PinNew creates block 0 of
	// a new relation. Set via PoolConfig.LogSmgrCreate.
	logSmgrCreate func(rel RelFileNode) error
	// logChangeRecord emits a pre-encoded change record (e.g.
	// RecordKindCreateIndex) on behalf of the executor without
	// requiring an executor → wal direct dependency. Set via
	// PoolConfig.LogChangeRecord. Exposed via
	// Pool.LogChangeRecord. (M0079-0001.)
	logChangeRecord func(payload []byte) (LSN, error)
	// fullPageWrites gates FPI emission. The wire/admin layer can
	// flip it at runtime to mirror upstream's full_page_writes
	// SIGHUP-context GUC.
	fullPageWrites atomic.Bool
	// logger is used to surface non-fatal FPI emission failures
	// without changing MarkDirty's signature.
	logger *slog.Logger

	// prefetchEnabled gates Pool.Prefetch. When false (the
	// default), Prefetch is a fast no-op so the synchronous
	// fallback path doesn't pay for an inline pread we'd have
	// to repeat on the subsequent Pin. The lifecycle owner
	// (initdb.Open) flips it on after attaching an AIO engine
	// to the storage Manager, since only then does Prefetch
	// run async.
	prefetchEnabled atomic.Bool

	// asyncFlushBatchSize controls FlushAllPaced's batching.
	// 0 / 1 reduces to the legacy per-slot serial loop. >1
	// batches the WAL flush + the AIO write submissions
	// across that many dirty slots so the engine's worker
	// pool pipelines the writes. The lifecycle owner sets
	// this after attaching an AIO engine; values are clamped
	// in flushBatchSize.
	asyncFlushBatchSize atomic.Int32

	// poolMu guards byTag, slots[*].tag/valid/dirty, slots[*].pinCount,
	// slots[*].usageCount, slots[*].fpiSinceCheckpoint, clockHand,
	// and ioByTag.
	// It does NOT guard the page bytes — those are guarded by
	// Slot.contentMu.
	poolMu    sync.Mutex
	byTag     map[BufferTag]int
	clockHand int

	// ioByTag tracks BufferTags currently being read from disk
	// (BM_IO_IN_PROGRESS, M0048-0001). A goroutine requesting a tag
	// that is already in ioByTag waits on ioCond instead of starting
	// a second concurrent disk read. This ensures smgr.ReadBlock is
	// called exactly once per page miss regardless of concurrency.
	// Both fields are guarded by poolMu.
	ioByTag map[BufferTag]struct{}
	ioCond  *sync.Cond

	// OnPinWait is an optional hook called when Pool.Pin performs an
	// actual disk read (the one goroutine that wins the I/O race).
	// initdb.Open sets this via activity.LookupGoroutine to record
	// BufferPin wait events in pg_stat_activity.
	OnPinWait func()

	// OnPinDone pairs with OnPinWait — called immediately after the
	// disk-read finishes so observability code can balance every
	// WaitEventStart with a WaitEventEnd. (M0058-0006.)
	OnPinDone func()

	// OnBufferIOWait is an optional hook called when a goroutine must
	// wait for an in-flight read issued by another goroutine to complete
	// (BM_IO_IN_PROGRESS, M0048-0001). Fires once per waiting goroutine
	// before it blocks on ioCond. initdb.Open can set this to record
	// BufferIO wait events in pg_stat_activity.
	OnBufferIOWait func()

	// OnFlushAll is an optional hook called at the start of FlushAll
	// and FlushAllPaced before any I/O.  initdb.Open wires an
	// assertion that these callers are checkpointer or walwriter
	// goroutines — never client-backend goroutines (M0042-0004).
	// nil disables the check (tests that don't set up activity
	// goroutine registration).
	OnFlushAll func()

	// Dirty-victim instrumentation (M0048-0003): counts how many times
	// the foreground victim-search (evictLocked) encounters a dirty page
	// that must be flushed synchronously, vs. total victims processed.
	// Used to measure bgwriter effectiveness (DoD: dirty rate ≤ 5%).
	// Protected by poolMu.
	dirtyVictimCount int64
	totalVictimCount int64

	// bgwriterHand is the clock-hand position for the bgwriter's
	// independent pass through the slot array (M0048-0003). Separate from
	// clockHand so the bgwriter doesn't interfere with the eviction sweep.
	// Guarded by poolMu.
	bgwriterHand int
}

// PoolConfig controls Pool sizing.
type PoolConfig struct {
	Slots int

	// WAL, when non-nil, is asked to flush up to the page's pd_lsn
	// before any dirty page is written to data files.
	WAL WALFlusher

	// LogPageImage, when non-nil, is invoked by MarkDirty on the
	// first mutation of each page after every checkpoint to emit
	// a full-page-image WAL record. The returned LSN is stamped
	// into the page header so the existing flush-before-write
	// ordering covers it. Failures are logged but not propagated
	// to the mutation site (matches upstream's PANIC stance only
	// in spirit; v0 logs and continues — see milestone 0002).
	LogPageImage func(rel RelFileNode, blk BlockNumber, page Page) (LSN, error)

	// FullPageWrites mirrors upstream's full_page_writes GUC. When
	// false, no FPI is emitted regardless of LogPageImage.
	FullPageWrites bool

	// LogBtreeSplit, when non-nil, is exposed via Pool.LogBtreeSplit
	// so the B-tree access method can emit an atomic split record
	// (Landing 3a of milestone 0002 — see
	// docs/design/0002-0002-btree-concurrency.md). nil disables the
	// hook and the btree falls back to per-page FPI on splits.
	LogBtreeSplit LogBtreeSplitFunc

	// LogHeapInsert, when non-nil, is exposed via
	// Pool.LogHeapInsert so the executor's heap-insert path can
	// emit a logical change record instead of a full FPI on
	// every dirty (see docs/design/0002-0003-redo-records.md).
	LogHeapInsert LogHeapInsertFunc

	// LogBtreeInsert, when non-nil, is exposed via
	// Pool.LogBtreeInsert so the B-tree access method's
	// non-split insert path can emit a logical change record.
	LogBtreeInsert LogBtreeInsertFunc

	// LogHeapDelete, when non-nil, is exposed via
	// Pool.LogHeapDelete so the executor's UPDATE / DELETE
	// xmax-stamp paths can emit a logical change record.
	LogHeapDelete LogHeapDeleteFunc

	// LogHeapLock, when non-nil, is exposed via
	// Pool.LogHeapLock so the SELECT FOR UPDATE / FOR SHARE
	// executor can emit a row-lock change record (M0021
	// tuple-level locking step 2 — producer wiring).
	LogHeapLock LogHeapLockFunc

	// LogHeapVacuum, when non-nil, is exposed via
	// Pool.LogHeapVacuum so the VACUUM path can emit a
	// dead-slot-list change record instead of a full FPI on
	// each pruned page.
	LogHeapVacuum LogHeapVacuumFunc

	// LogHeapHotUpdate, when non-nil, is exposed via
	// Pool.LogHeapHotUpdate so the executor's HOT-update path can
	// emit a single atomic change record (old-stamp + new-insert on
	// the same page) instead of separate delete+insert FPIs.
	LogHeapHotUpdate LogHeapHotUpdateFunc

	// LogHeapPruneOpt, when non-nil, is exposed via
	// Pool.LogHeapPruneOpt so the opportunistic pruning path can
	// emit a logical prune record instead of a full FPI.
	LogHeapPruneOpt LogHeapPruneOptFunc

	// LogSmgrCreate, when non-nil, is called by PinNew when it
	// creates block 0 of a relation (the first Extend). The hook
	// should emit a RecordKindSmgrCreate WAL record so crash
	// recovery can recreate the relfile before replaying data pages.
	// Failures are logged but not propagated, matching the FPI hook
	// behaviour. See docs/design/0030-0002-ddl-wal-records.md.
	LogSmgrCreate func(rel RelFileNode) error

	// LogChangeRecord, when non-nil, is exposed via
	// Pool.LogChangeRecord so DDL-emitting executors can append a
	// pre-encoded WAL record (e.g. RecordKindCreateIndex) without
	// importing the wal package. The function returns the record's
	// end LSN; failures are propagated so the executor can roll back
	// the in-memory catalog mutation that produced the record.
	// (M0079-0001 / pgbench recovery fix.)
	LogChangeRecord func(payload []byte) (LSN, error)

	// Logger receives FPI emission failures. nil means
	// slog.Default().
	Logger *slog.Logger
}

// WALFlusher is the minimal WAL contract the buffer pool needs to
// enforce write-ahead ordering.
type WALFlusher interface {
	FlushUpTo(lsn uint64) error
}

// LogBtreeSplitFunc emits one atomic WAL record covering a B-tree
// page split (left's post-image + right's full image) and returns
// the record's end LSN. The signature lives in storage so packages
// like internal/access/btree can consume it without an import
// cycle through internal/wal.
type LogBtreeSplitFunc func(rel RelFileNode, leftBlk, rightBlk BlockNumber, leftPage, rightPage Page) (LSN, error)

// LogHeapInsertFunc emits one logical heap-insert redo record and
// returns its end LSN. Used by the executor's writeHeapRow path
// (and exposed via Pool.LogHeapInsert) to bypass full-page-image
// emission on subsequent dirties of the same page in an epoch.
// See docs/design/0002-0003-redo-records.md.
type LogHeapInsertFunc func(rel RelFileNode, blk BlockNumber, lineSlot uint16, tuple []byte) (LSN, error)

// LogBtreeInsertFunc emits one logical B-tree non-split insert
// redo record. The `item` payload is the raw bytes the caller
// stored on the page — for v0's btree, internal/access/btree's
// item.marshal output. See docs/design/0002-0003-redo-records.md.
type LogBtreeInsertFunc func(rel RelFileNode, blk BlockNumber, item []byte) (LSN, error)

// LogHeapDeleteFunc emits one logical heap-delete (xmax stamp)
// redo record. Used by the executor's UPDATE / DELETE paths to
// avoid an FPI on subsequent dirties of the same page in an
// epoch. See docs/design/0002-0003-redo-records.md.
type LogHeapDeleteFunc func(rel RelFileNode, blk BlockNumber, lineSlot uint16, xmax TransactionID) (LSN, error)

// LogHeapLockFunc emits one row-lock WAL record (M0021 tuple-
// level locking step 3) and returns the record's end LSN. Used
// by the SELECT FOR UPDATE / FOR SHARE executor to capture
// the lock-only xmax + lock-strength infomask change in WAL
// so crash recovery can re-stamp the affected tuple. Mirrors
// upstream's xl_heap_lock emission point inside heap_lock_tuple.
type LogHeapLockFunc func(rel RelFileNode, blk BlockNumber, lineSlot uint16, xmax TransactionID, lockStrength uint16) (LSN, error)

// LogHeapVacuumFunc emits one logical heap-vacuum (page prune)
// redo record carrying the 1-based LP_NORMAL slot numbers being
// reclaimed. Replay calls VacuumHeapPageBySlots with the same
// list, so the prune is bit-exact whether it's the original
// write or recovery. See docs/design/0002-0003-redo-records.md.
type LogHeapVacuumFunc func(rel RelFileNode, blk BlockNumber, deadSlots []uint16) (LSN, error)

// LogHeapHotUpdateFunc emits one atomic HOT-update redo record
// (M0046-0001). The record encodes the old-slot xmax stamp +
// HeapHotUpdated infomask + CTID chain + the new tuple bytes (which
// carry HeapOnlyTuple in their infomask) — all on the same heap page.
// Replay inserts the new tuple, derives newSlot, then stamps the old
// slot. See docs/design/0046-0001-hot-updates.md.
type LogHeapHotUpdateFunc func(rel RelFileNode, blk BlockNumber, oldSlot uint16, xmax TransactionID, tupleBytes []byte) (LSN, error)

// LogHeapPruneOptFunc emits one opportunistic page-pruning redo record
// (M0046-0002, RecordKindHeapPruneOpt). Carries redirect pairs (for HOT
// chain roots converted to ItemIDRedirect) and unused slot numbers (for
// HOT-only and standalone dead tuples). Replay applies both operations to
// bring the page back to the pruned state.
type LogHeapPruneOptFunc func(rel RelFileNode, blk BlockNumber, redirects [][2]uint16, unused []uint16) (LSN, error)

// NewPool allocates a Pool of cfg.Slots fixed buffers backed by a
// Go-heap arena. Returns an error if slots <= 0 or the arena alloc
// fails.
func NewPool(mgr *Manager, cfg PoolConfig) (*Pool, error) {
	if cfg.Slots <= 0 {
		return nil, fmt.Errorf("pool: Slots must be > 0, got %d", cfg.Slots)
	}
	a, err := newArena(cfg.Slots)
	if err != nil {
		return nil, err
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	p := &Pool{
		mgr:            mgr,
		arena:          a,
		slots:          make([]*Slot, cfg.Slots),
		byTag:          make(map[BufferTag]int, cfg.Slots),
		ioByTag:        make(map[BufferTag]struct{}),
		wal:            cfg.WAL,
		logFPI:         cfg.LogPageImage,
		logBtreeSplit:  cfg.LogBtreeSplit,
		logHeapInsert:  cfg.LogHeapInsert,
		logBtreeInsert: cfg.LogBtreeInsert,
		logHeapDelete:    cfg.LogHeapDelete,
		logHeapLock:      cfg.LogHeapLock,
		logHeapVacuum:    cfg.LogHeapVacuum,
		logHeapHotUpdate: cfg.LogHeapHotUpdate,
		logHeapPruneOpt:  cfg.LogHeapPruneOpt,
		logSmgrCreate:    cfg.LogSmgrCreate,
		logChangeRecord:  cfg.LogChangeRecord,
		logger:         logger,
	}
	p.fullPageWrites.Store(cfg.FullPageWrites)
	p.ioCond = sync.NewCond(&p.poolMu)
	for i := range p.slots {
		p.slots[i] = &Slot{page: a.slot(i)}
	}
	return p, nil
}

// LogBtreeSplit returns the configured atomic split-record hook,
// or nil if none was wired. Callers (the B-tree access method)
// are expected to fall back to per-page FPI when nil.
func (p *Pool) LogBtreeSplit() LogBtreeSplitFunc { return p.logBtreeSplit }

// LogPageImage returns the configured full-page-image hook,
// or nil if none was wired.
func (p *Pool) LogPageImage() func(rel RelFileNode, blk BlockNumber, page Page) (LSN, error) {
	return p.logFPI
}

// LogHeapInsert returns the configured heap-insert change-record
// hook, or nil if none was wired. Callers fall back to per-page
// FPI via MarkDirty when nil.
func (p *Pool) LogHeapInsert() LogHeapInsertFunc { return p.logHeapInsert }

// LogChangeRecord appends a pre-encoded WAL change record. Used
// by DDL paths in the executor (CREATE / DROP INDEX) to emit
// catalog-only records without an executor → wal hard
// dependency. Returns 0 / nil when no hook is wired (the
// executor's caller-side check decides whether to roll back the
// in-memory mutation). (M0079-0001.)
func (p *Pool) LogChangeRecord(payload []byte) (LSN, error) {
	if p == nil || p.logChangeRecord == nil {
		return 0, nil
	}
	return p.logChangeRecord(payload)
}

// LogBtreeInsert returns the configured btree non-split insert
// change-record hook, or nil if none was wired. Callers fall
// back to per-page FPI via MarkDirty when nil.
func (p *Pool) LogBtreeInsert() LogBtreeInsertFunc { return p.logBtreeInsert }

// LogHeapDelete returns the configured heap-delete change-record
// hook, or nil if none was wired. Callers fall back to per-page
// FPI via MarkDirty when nil.
func (p *Pool) LogHeapDelete() LogHeapDeleteFunc { return p.logHeapDelete }

// LogHeapLock returns the configured row-lock change-record
// emitter (M0021 tuple-level locking step 2). nil disables the
// optimisation; the executor falls back to MarkDirty + FPI
// emission for crash safety.
func (p *Pool) LogHeapLock() LogHeapLockFunc { return p.logHeapLock }

// LogHeapVacuum returns the configured heap-vacuum change-record
// hook, or nil if none was wired. Callers fall back to per-page
// FPI via MarkDirty when nil.
func (p *Pool) LogHeapVacuum() LogHeapVacuumFunc { return p.logHeapVacuum }

// LogHeapHotUpdate returns the configured HOT-update change-record
// hook (M0046-0001), or nil if none was wired. Callers fall back to
// per-page FPI via MarkDirty when nil.
func (p *Pool) LogHeapHotUpdate() LogHeapHotUpdateFunc { return p.logHeapHotUpdate }

// LogHeapPruneOpt returns the configured opportunistic-pruning
// change-record hook (M0046-0002), or nil if none was wired.
func (p *Pool) LogHeapPruneOpt() LogHeapPruneOptFunc { return p.logHeapPruneOpt }

// SetFullPageWrites toggles full-page-image emission at runtime.
// Mirrors upstream's full_page_writes SIGHUP-context GUC.
func (p *Pool) SetFullPageWrites(on bool) { p.fullPageWrites.Store(on) }

// FullPageWrites reports the current setting.
func (p *Pool) FullPageWrites() bool { return p.fullPageWrites.Load() }

// ResetCheckpointEpoch clears the per-slot "FPI emitted" flag.
// The checkpointer calls this after a successful checkpoint so
// the next mutation of each page emits a fresh full-page image.
func (p *Pool) ResetCheckpointEpoch() {
	p.poolMu.Lock()
	defer p.poolMu.Unlock()
	for _, s := range p.slots {
		s.fpiSinceCheckpoint = false
	}
}

// Close flushes every dirty slot through smgr and releases the
// page-aligned arena. Dirty pages must reach disk before the
// process exits or callers see data loss across restarts; the
// storage manager itself is closed separately by the caller.
func (p *Pool) Close() error {
	flushErr := p.FlushAll()
	closeErr := p.arena.close()
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

// NBlocks reports the number of blocks currently in rel. Pass-through
// to the underlying smgr; callers (e.g. vacuum) need this to drive a
// full-relation walk.
func (p *Pool) NBlocks(rel RelFileNode) (BlockNumber, error) {
	return p.mgr.NBlocks(rel)
}

// Manager exposes the underlying storage manager so DDL operators can
// drop or truncate relation files. Pinning still flows through Pool.
func (p *Pool) Manager() *Manager { return p.mgr }

// SetPrefetchEnabled toggles Pool.Prefetch's behaviour. When
// false, Prefetch is a no-op. Idempotent. The wiring layer
// (initdb.Open) sets this true once an AIO engine is attached
// to the storage Manager — only then does Prefetch run on a
// background goroutine rather than blocking the caller with
// an inline pread.
func (p *Pool) SetPrefetchEnabled(on bool) { p.prefetchEnabled.Store(on) }

// SetAsyncFlushBatchSize controls how many dirty slots
// FlushAllPaced batches per WAL-flush + AIO-submit + Wait
// cycle. 0 or 1 means the legacy per-slot serial loop. Larger
// values pipeline writes through the AIO worker pool — the
// caller (initdb.Open) sets this after attaching an AIO
// engine. Values >MaxFlushBatchSize are clamped.
func (p *Pool) SetAsyncFlushBatchSize(n int) {
	if n > MaxFlushBatchSize {
		n = MaxFlushBatchSize
	}
	if n < 0 {
		n = 0
	}
	p.asyncFlushBatchSize.Store(int32(n))
}

// MaxFlushBatchSize caps the per-batch dirty-slot count so a
// pathologically large config can't allocate unbounded handle
// arrays or hold too many contentMu read locks at once.
// Independent of the engine's global MaxConcurrency cap (which
// still applies on top via Engine.Submit's queue-fullness
// backpressure).
const MaxFlushBatchSize = 256

// flushBatchSize returns the effective batch size for
// FlushAllPaced. Treats 0 as 1 (serial), so callers that
// haven't opted in see the legacy behaviour.
func (p *Pool) flushBatchSize() int {
	n := int(p.asyncFlushBatchSize.Load())
	if n < 1 {
		return 1
	}
	return n
}

// Prefetch is a hint that the caller is about to Pin tag. When
// prefetching is enabled and tag isn't already cached, the page
// is read on the AIO engine's background workers so the
// kernel page cache is warm by the time Pin's synchronous read
// runs. Buffer-pool slot allocation still happens at Pin time;
// Prefetch only warms the kernel side.
//
// Drops the AIO handle on the floor — the engine's worker
// goroutine completes the read in the background; the throwaway
// buffer warms the page cache and is then GC'd.
//
// Errors are intentionally swallowed: a failed prefetch must
// not impact correctness. The subsequent Pin will surface any
// real I/O error.
func (p *Pool) Prefetch(tag BufferTag) {
	if !p.prefetchEnabled.Load() {
		return
	}
	p.poolMu.Lock()
	_, cached := p.byTag[tag]
	p.poolMu.Unlock()
	if cached {
		return
	}
	buf := make([]byte, BlockSize)
	_, _ = p.mgr.PrefetchBlock(tag.Rel, tag.Block, buf)
}

// InvalidateRel evicts every slot currently bound to a buffer of rel.
// DROP TABLE / TRUNCATE TABLE call this after committing the
// catalog-level change so a subsequent Pin returns a fresh page
// rather than a stale cached one. Pinned slots are skipped — v0
// requires DDL to run without concurrent pinning of the dropped
// relation, matching upstream's AccessExclusiveLock requirement.
func (p *Pool) InvalidateRel(rel RelFileNode) {
	p.poolMu.Lock()
	defer p.poolMu.Unlock()
	for tag, idx := range p.byTag {
		if tag.Rel != rel {
			continue
		}
		s := p.slots[idx]
		if s.pinCount > 0 {
			continue
		}
		s.valid = false
		s.dirty = false
		delete(p.byTag, tag)
	}
}

// ErrNoBuffer is returned when every slot is pinned and the clock
// sweep can't find a victim.
var ErrNoBuffer = errors.New("no available buffer (all pinned)")

// PinNew pins the next-after-end block of rel for writing without
// reading from disk. Used by relation extension. Returns the block
// number that the slot now represents.
func (p *Pool) PinNew(rel RelFileNode) (*Slot, BlockNumber, error) {
	p.poolMu.Lock()
	slotIdx, err := p.evictLocked()
	if err != nil {
		p.poolMu.Unlock()
		return nil, InvalidBlockNumber, err
	}
	s := p.slots[slotIdx]
	// Provisionally take the slot offline and release the pool lock so
	// I/O happens unlocked. We hold contentMu in write mode for the
	// extend.
	if s.valid && s.dirty {
		oldTag := s.tag
		s.contentMu.Lock()
		p.poolMu.Unlock()
		err := p.flushSlot(oldTag, s.page)
		s.contentMu.Unlock()
		if err != nil {
			return nil, InvalidBlockNumber, fmt.Errorf("flush victim: %w", err)
		}
		p.poolMu.Lock()
	}
	delete(p.byTag, s.tag)
	s.valid = false
	s.dirty = false
	// M0056-0001: reserve the slot BEFORE releasing poolMu so a
	// concurrent `Pin` call's `evictLocked` skips it. Without
	// this reservation the slot is observable to evictLocked
	// (pinCount==0, usageCount==0) and could be stolen mid-I/O,
	// trampling our subsequent tag/pinCount publication. Mirrors
	// the regular `Pin` path's pre-publication reservation at
	// line ~717.
	s.tag = BufferTag{}
	s.pinCount = 1
	s.usageCount = 1
	p.poolMu.Unlock()

	s.contentMu.Lock()
	if err := InitPage(s.page); err != nil {
		s.contentMu.Unlock()
		// Roll back the reservation — slot returns to the free
		// pool with pinCount=0.
		p.poolMu.Lock()
		s.pinCount = 0
		s.usageCount = 0
		p.poolMu.Unlock()
		return nil, InvalidBlockNumber, err
	}
	blk, err := p.mgr.Extend(rel, s.page)
	s.contentMu.Unlock()
	if err != nil {
		p.poolMu.Lock()
		s.pinCount = 0
		s.usageCount = 0
		p.poolMu.Unlock()
		return nil, InvalidBlockNumber, err
	}
	// Emit SmgrCreate WAL record on first block creation so crash
	// recovery can recreate the relfile before replaying data pages.
	if blk == 0 && p.logSmgrCreate != nil {
		if emitErr := p.logSmgrCreate(rel); emitErr != nil {
			p.logger.Error("SmgrCreate WAL emission failed", "rel", rel, "err", emitErr)
			// Don't propagate — storage mutation already committed.
		}
	}
	tag := BufferTag{Rel: rel, Block: blk}

	p.poolMu.Lock()
	if idx, ok := p.byTag[tag]; ok {
		// Another goroutine already loaded/published this freshly
		// extended block while we were outside poolMu. Reuse that
		// slot and release ours back to the free pool. Our
		// reserved pinCount=1 from the I/O window is decremented
		// to 0 here; the existing slot's pinCount is incremented.
		existing := p.slots[idx]
		existing.pinCount++
		if existing.usageCount < maxUsageCount {
			existing.usageCount++
		}
		s.tag = BufferTag{}
		s.valid = false
		s.dirty = false
		s.pinCount = 0
		s.usageCount = 0
		p.poolMu.Unlock()
		return existing, blk, nil
	}
	s.tag = tag
	s.valid = true
	s.dirty = true // Extend wrote it but the in-memory page was just initialised; flag dirty so any subsequent mutation flushes
	// M0056-0001: pinCount/usageCount were already set to 1 as
	// the reservation before we released poolMu. Don't overwrite
	// them here; the caller's first Unpin will balance the
	// reservation.
	p.byTag[tag] = slotIdx
	p.poolMu.Unlock()
	return s, blk, nil
}

// Pin returns the slot holding tag, reading from disk if necessary.
// The slot's pinCount is incremented; the caller MUST call Unpin.
//
// BM_IO_IN_PROGRESS (M0048-0001): when two goroutines miss the cache for
// the same tag simultaneously, only the first one issues a disk read.
// The others wait on p.ioCond until the read completes, then pick up the
// cached result. This guarantees smgr.ReadBlock is called exactly once
// per cache miss regardless of concurrency.
func (p *Pool) Pin(tag BufferTag) (*Slot, error) {
	p.poolMu.Lock()

	// Outer loop: a goroutine may need to wait multiple times if spurious
	// wakeups occur or if ioCond is signalled before the tag is published.
	for {
		// Fast path: tag is already cached.
		if idx, ok := p.byTag[tag]; ok {
			s := p.slots[idx]
			s.pinCount++
			if s.usageCount < maxUsageCount {
				s.usageCount++
			}
			p.poolMu.Unlock()
			return s, nil
		}

		// If another goroutine is already reading this exact tag from disk,
		// wait for it to finish rather than issuing a duplicate read.
		if _, inFlight := p.ioByTag[tag]; inFlight {
			if p.OnBufferIOWait != nil {
				p.OnBufferIOWait()
			}
			p.ioCond.Wait() // atomically releases poolMu and sleeps
			continue        // re-check cache hit and in-flight status
		}

		// No in-flight read for this tag — we win the I/O race.
		break
	}

	// Mark this tag as in-flight BEFORE evicting a victim slot or
	// releasing poolMu. Subsequent goroutines requesting the same tag
	// will see ioByTag[tag] and wait instead of starting a duplicate read.
	p.ioByTag[tag] = struct{}{}

	slotIdx, err := p.evictLocked()
	if err != nil {
		delete(p.ioByTag, tag)
		p.ioCond.Broadcast()
		p.poolMu.Unlock()
		return nil, err
	}
	s := p.slots[slotIdx]
	if s.valid && s.dirty {
		oldTag := s.tag
		s.contentMu.Lock()
		p.poolMu.Unlock()
		err := p.flushSlot(oldTag, s.page)
		s.contentMu.Unlock()
		if err != nil {
			p.poolMu.Lock()
			delete(p.ioByTag, tag)
			p.ioCond.Broadcast()
			p.poolMu.Unlock()
			return nil, fmt.Errorf("flush victim: %w", err)
		}
		p.poolMu.Lock()
	}
	delete(p.byTag, s.tag)
	s.valid = false
	s.dirty = false
	// Reserve this slot while I/O runs outside poolMu.
	s.tag = BufferTag{}
	s.pinCount = 1
	s.usageCount = 1
	p.poolMu.Unlock()

	s.contentMu.Lock()
	if p.OnPinWait != nil {
		p.OnPinWait()
	}
	err = p.mgr.ReadBlock(tag.Rel, tag.Block, s.page)
	if p.OnPinDone != nil {
		p.OnPinDone()
	}
	s.contentMu.Unlock()

	// Remove from ioByTag and wake waiters regardless of success or
	// failure so they can either pick up the cached page or retry.
	p.poolMu.Lock()
	delete(p.ioByTag, tag)
	p.ioCond.Broadcast()

	if err != nil {
		s.tag = BufferTag{}
		s.pinCount = 0
		s.usageCount = 0
		s.valid = false
		s.dirty = false
		p.poolMu.Unlock()
		return nil, err
	}
	if idx, ok := p.byTag[tag]; ok {
		// Another goroutine published this tag while we were reading
		// (e.g. via PinNew). Use that slot and release ours.
		existing := p.slots[idx]
		existing.pinCount++
		if existing.usageCount < maxUsageCount {
			existing.usageCount++
		}
		s.tag = BufferTag{}
		s.pinCount = 0
		s.usageCount = 0
		s.valid = false
		s.dirty = false
		p.poolMu.Unlock()
		return existing, nil
	}
	s.tag = tag
	s.valid = true
	s.dirty = false
	p.byTag[tag] = slotIdx
	p.poolMu.Unlock()
	return s, nil
}

// Capacity returns the total number of buffer slots in the pool.
// Used by scan strategies to decide whether to activate a ring buffer.
func (p *Pool) Capacity() int { return len(p.slots) }

// TryPin returns the pool slot for tag if it is already cached,
// incrementing pinCount. Returns (nil, false) when the tag is not in
// the pool — no disk read is attempted. Used by ScanRing to detect
// cache hits without evicting other pages on misses (M0048-0002).
func (p *Pool) TryPin(tag BufferTag) (*Slot, bool) {
	p.poolMu.Lock()
	defer p.poolMu.Unlock()
	idx, ok := p.byTag[tag]
	if !ok {
		return nil, false
	}
	s := p.slots[idx]
	s.pinCount++
	if s.usageCount < maxUsageCount {
		s.usageCount++
	}
	return s, true
}

// Unpin decrements the slot's pin count.
func (p *Pool) Unpin(s *Slot) {
	p.poolMu.Lock()
	defer p.poolMu.Unlock()
	if s.pinCount <= 0 {
		panic(fmt.Sprintf("unpin underflow on tag %v", s.tag))
	}
	s.pinCount--
}

// MarkDirty flags the slot as having been mutated. Caller must hold
// a pin and (for write paths) the slot's exclusive content lock so
// a full-page-image snapshot taken below sees a stable image.
//
// When full_page_writes is on and this is the first mutation since
// the last checkpoint, an FPI record is emitted via the configured
// LogPageImage callback and the resulting LSN is stamped into the
// page header so the existing flush-before-write ordering
// (`flushSlot` -> `wal.FlushUpTo(pd_lsn)`) covers it.
func (p *Pool) MarkDirty(s *Slot) {
	p.maybeEmitFPI(s)
	p.poolMu.Lock()
	s.dirty = true
	p.poolMu.Unlock()
}

// MarkDirtyWithLSN records an explicit page LSN and marks the slot
// dirty. Used when the caller has already issued a WAL record and
// wants the buffer pool to track its LSN for the flush ordering.
// FPI emission is skipped — the caller's record is the durability
// anchor.
//
// Callers that DON'T already hold s.contentMu (e.g. test code that
// pins the slot but never explicitly latches it) should use this
// entry point. Callers that ARE in the middle of an exclusive
// content-latch hold (e.g. the B-tree split path) must use
// MarkDirtyWithLSNLocked to avoid the obvious self-deadlock.
func (p *Pool) MarkDirtyWithLSN(s *Slot, lsn LSN) {
	s.contentMu.Lock()
	MustHeader(s.page).SetLSN(lsn)
	s.contentMu.Unlock()
	p.markDirtyWithLSNCommon(s)
}

// MarkDirtyWithLSNLocked is the variant for callers that already
// hold s.contentMu in exclusive mode. The page-header LSN write
// happens without re-taking the latch.
func (p *Pool) MarkDirtyWithLSNLocked(s *Slot, lsn LSN) {
	MustHeader(s.page).SetLSN(lsn)
	p.markDirtyWithLSNCommon(s)
}

func (p *Pool) markDirtyWithLSNCommon(s *Slot) {
	p.poolMu.Lock()
	s.dirty = true
	// Stamping our own LSN supersedes any prior FPI for this
	// epoch — the WAL record at this LSN is what redo will pick up.
	s.fpiSinceCheckpoint = true
	p.poolMu.Unlock()
}

// MarkDirtyChangeRecord is the change-record-aware variant of
// MarkDirty. The first dirty in each checkpoint epoch emits an
// FPI as the baseline (and ignores `emitter`); subsequent dirties
// in the same epoch invoke `emitter` to append the caller's
// logical change record and stamp its end LSN onto pd_lsn. This
// gives upstream's "FPI then change records" replay invariant
// while keeping the once-per-epoch FPI optimisation alive for
// migrated paths. Caller must hold s.contentMu exclusive.
//
// Paths that haven't been migrated to logical records can still
// use MarkDirty for one-off page initialisation writes; hot
// paths that may dirty the same page repeatedly in one checkpoint
// epoch should use this API so subsequent mutations are logged.
// See docs/design/0002-0003-redo-records.md.
func (p *Pool) MarkDirtyChangeRecord(s *Slot, emitter func() (LSN, error)) error {
	p.poolMu.Lock()
	needFPI := !s.fpiSinceCheckpoint
	tag := s.tag
	p.poolMu.Unlock()

	if needFPI {
		// Baseline: emit the post-mutation page image.
		if p.logFPI != nil && p.fullPageWrites.Load() {
			pageCopy := make(Page, BlockSize)
			copy(pageCopy, s.page)
			lsn, err := p.logFPI(tag.Rel, tag.Block, pageCopy)
			if err != nil {
				return err
			}
			MustHeader(s.page).SetLSN(lsn)
		} else if emitter != nil {
			// No FPI hook (or full_page_writes off): fall back
			// to the logical record so the change is at least
			// captured in WAL.
			lsn, err := emitter()
			if err != nil {
				return err
			}
			MustHeader(s.page).SetLSN(lsn)
		}
	} else {
		// Already FPI'd this epoch — emit logical record only.
		if emitter == nil {
			return fmt.Errorf("MarkDirtyChangeRecord: emitter required after first dirty")
		}
		lsn, err := emitter()
		if err != nil {
			return err
		}
		MustHeader(s.page).SetLSN(lsn)
	}

	p.poolMu.Lock()
	s.dirty = true
	s.fpiSinceCheckpoint = true
	p.poolMu.Unlock()
	return nil
}

// maybeEmitFPI runs the FPI side-effect of MarkDirty for the
// first mutation in each checkpoint epoch. Caller must hold
// Slot.contentMu exclusive so the page bytes are stable.
func (p *Pool) maybeEmitFPI(s *Slot) {
	if p.logFPI == nil || !p.fullPageWrites.Load() {
		return
	}
	p.poolMu.Lock()
	if s.fpiSinceCheckpoint {
		p.poolMu.Unlock()
		return
	}
	tag := s.tag
	p.poolMu.Unlock()

	pageCopy := make(Page, BlockSize)
	copy(pageCopy, s.page)
	lsn, err := p.logFPI(tag.Rel, tag.Block, pageCopy)
	if err != nil {
		// Best-effort in v0: log and continue. Upstream PANICs
		// here; surfacing that requires changing MarkDirty's
		// signature, which is a follow-up loop's task.
		p.logger.Warn("full-page-image WAL emit failed",
			"rel", tag.Rel, "block", tag.Block, "err", err)
		return
	}
	MustHeader(s.page).SetLSN(lsn)
	p.poolMu.Lock()
	s.fpiSinceCheckpoint = true
	p.poolMu.Unlock()
}

// FlushAll writes every dirty slot through smgr and clears the dirty
// bit. Equivalent to FlushAllPaced with a no-op pacer; convenient
// for callers that don't want to spread the work over time
// (CheckpointNow, Pool.Close).
//
// FlushAll is reserved for the checkpointer goroutine. Client-backend
// goroutines must never call it (M0042-0004 invariant). The OnFlushAll
// hook fires here for assertions; see Pool.OnFlushAll.
func (p *Pool) FlushAll() error {
	if p.OnFlushAll != nil {
		p.OnFlushAll()
	}
	return p.FlushAllPaced(nil)
}

// FlushAllPaced writes every dirty slot through smgr and invokes
// `pacer(progress)` after each batch, where progress is the
// fraction of work already completed in [0, 1]. The pacer can
// sleep to spread the I/O over a target wall-clock window — this
// is the path the spread checkpoint writes (milestone 0002) take.
// A nil pacer behaves like FlushAll.
//
// The dirty set is snapshotted at entry; pages dirtied after the
// snapshot are not flushed in this pass and stay dirty for the
// next checkpoint.
//
// When SetAsyncFlushBatchSize is >1 and an AIO engine is
// attached to the storage Manager, dirty slots are flushed in
// batches: one combined wal.FlushUpTo for the batch's max
// pd_lsn, then parallel AIO writes via Manager.WriteBlockAIO,
// then waits on every handle. The contentMu RLock is held for
// the full batch so writers can't tear the bytes the engine is
// pwrite-ing. With batch=1 the loop is bit-identical to the
// legacy per-slot serial flush.
func (p *Pool) FlushAllPaced(pacer func(progress float64) error) error {
	if p.OnFlushAll != nil {
		p.OnFlushAll()
	}
	p.poolMu.Lock()
	type pending struct {
		idx int
		tag BufferTag
	}
	var todo []pending
	for i, s := range p.slots {
		if s.valid && s.dirty {
			todo = append(todo, pending{i, s.tag})
		}
	}
	p.poolMu.Unlock()

	total := len(todo)
	if total == 0 {
		return nil
	}
	batchSize := p.flushBatchSize()
	for start := 0; start < total; start += batchSize {
		end := start + batchSize
		if end > total {
			end = total
		}
		// Per-batch flush: WAL barrier first, then AIO writes.
		// Build a parallel slice of slots so the helper doesn't
		// need to re-resolve idx → *Slot.
		batchSlots := make([]*Slot, 0, end-start)
		batchTags := make([]BufferTag, 0, end-start)
		for _, t := range todo[start:end] {
			batchSlots = append(batchSlots, p.slots[t.idx])
			batchTags = append(batchTags, t.tag)
		}
		if err := p.flushBatch(batchSlots, batchTags); err != nil {
			return err
		}
		if pacer != nil {
			progress := float64(end) / float64(total)
			if err := pacer(progress); err != nil {
				return err
			}
		}
	}
	return nil
}

// flushBatch flushes a batch of dirty slots: takes contentMu
// read-locks across the batch, runs one combined WAL flush at
// the batch's max pd_lsn, submits all AIO writes in parallel
// via Manager.WriteBlockAIO, waits on every handle, then
// clears dirty bits for slots whose tag still matches.
//
// At batch size 1 this reduces to the legacy per-slot serial
// flush (one RLock, one WAL FlushUpTo, one Submit+Wait).
func (p *Pool) flushBatch(slots []*Slot, tags []BufferTag) error {
	// Phase 1: lock + compute max LSN.
	for _, s := range slots {
		s.contentMu.RLock()
	}
	defer func() {
		for _, s := range slots {
			s.contentMu.RUnlock()
		}
	}()

	var maxLSN LSN
	for _, s := range slots {
		if lsn := MustHeader(s.page).LSN(); lsn > maxLSN {
			maxLSN = lsn
		}
	}

	// Phase 2: single WAL flush for the batch. WAL-before-data
	// is preserved: every page in the batch has pd_lsn ≤ maxLSN,
	// and the durability barrier covers all of them.
	if p.wal != nil && maxLSN != 0 {
		if err := p.wal.FlushUpTo(uint64(maxLSN)); err != nil {
			return fmt.Errorf("flush wal up to %d: %w", maxLSN, err)
		}
	}

	// Phase 3: submit every data write through the AIO engine.
	// With no engine attached, WriteBlockAIO returns a
	// pre-completed handle synchronously — the loop below then
	// runs writes serially, matching the legacy behaviour.
	handles := make([]AIOHandle, len(slots))
	for i, s := range slots {
		h, err := p.mgr.WriteBlockAIO(tags[i].Rel, tags[i].Block, s.page)
		if err != nil {
			// Wait on whatever has already been submitted so
			// the engine's InFlight counter doesn't leak.
			for j := 0; j < i; j++ {
				_, _ = handles[j].Wait()
			}
			return err
		}
		handles[i] = h
	}

	// Phase 4: wait for every write to land. Surface the first
	// error, but always drain the rest so the engine's
	// bookkeeping stays coherent.
	var firstErr error
	for _, h := range handles {
		if _, err := h.Wait(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}

	// Phase 5: clear dirty bits where the tag still matches.
	// Only clear dirty if the tag hasn't been reassigned and
	// nothing else has marked it dirty since the flush started.
	p.poolMu.Lock()
	for i, s := range slots {
		if s.tag == tags[i] {
			s.dirty = false
		}
	}
	p.poolMu.Unlock()
	return nil
}

func (p *Pool) flushSlot(tag BufferTag, page Page) error {
	if p.wal != nil {
		lsn := MustHeader(page).LSN()
		if lsn != 0 {
			if err := p.wal.FlushUpTo(uint64(lsn)); err != nil {
				return fmt.Errorf("flush wal up to %d: %w", lsn, err)
			}
		}
	}
	if err := p.mgr.WriteBlock(tag.Rel, tag.Block, page); err != nil {
		return err
	}
	return nil
}

// maxUsageCount caps usageCount; matches upstream's BM_MAX_USAGE_COUNT
// (postgres/src/backend/storage/buffer/freelist.c) at 5.
const maxUsageCount = 5

// evictLocked finds a victim slot. The pool mutex must be held.
// Returns the slot index, or ErrNoBuffer if every slot is pinned.
//
// Algorithm: clock sweep. In the worst case every unpinned slot starts
// at usageCount==maxUsageCount, so finding a victim can require one full
// sweep per decrement plus one final sweep to observe a zero.
// Bound probes at N*(maxUsageCount+1).
func (p *Pool) evictLocked() (int, error) {
	n := len(p.slots)
	if n == 0 {
		return 0, ErrNoBuffer
	}
	maxPasses := n * (int(maxUsageCount) + 1)
	for i := 0; i < maxPasses; i++ {
		idx := p.clockHand
		p.clockHand = (p.clockHand + 1) % n
		s := p.slots[idx]
		if s.pinCount > 0 {
			continue
		}
		if s.usageCount > 0 {
			s.usageCount--
			continue
		}
		// Track dirty-victim rate for bgwriter effectiveness (M0048-0003).
		p.totalVictimCount++
		if s.dirty {
			p.dirtyVictimCount++
		}
		return idx, nil
	}
	return 0, ErrNoBuffer
}

// DirtyVictimRate returns the fraction of foreground evictions that
// encountered a dirty page and had to flush it synchronously. Used to
// measure bgwriter effectiveness (DoD: rate ≤ 5%). Acquires poolMu.
func (p *Pool) DirtyVictimRate() float64 {
	p.poolMu.Lock()
	defer p.poolMu.Unlock()
	if p.totalVictimCount == 0 {
		return 0
	}
	return float64(p.dirtyVictimCount) / float64(p.totalVictimCount)
}

// ResetVictimStats resets the dirty-victim counters to zero.
func (p *Pool) ResetVictimStats() {
	p.poolMu.Lock()
	defer p.poolMu.Unlock()
	p.dirtyVictimCount = 0
	p.totalVictimCount = 0
}

// WriteDirtyPages proactively flushes up to maxPages dirty slots in one
// bgwriter tick. It scans from p.bgwriterHand (independent from the
// eviction clockHand), flushes WAL-before-data ordering, and writes each
// dirty page to disk without fsync — the checkpointer handles durability.
// Returns the number of pages flushed.
//
// Called only by the bgwriter goroutine; must NOT be called by client
// backends (use FlushAll/FlushAllPaced for checkpoint-level flushes).
func (p *Pool) WriteDirtyPages(maxPages int) int {
	if maxPages <= 0 {
		return 0
	}

	// M0070-0002: bgwriter dominated mutex contention on Q9
	// (M0070 baseline pprof: ~93 % of contention coming from
	// this loop holding poolMu across all `n` slots while
	// foreground Pin / Unpin callers blocked). Snapshot
	// candidates by acquiring poolMu briefly per scanned slot
	// instead of holding the lock for the entire scan window.
	// Each slot's read is consistent (the brief acquire / release
	// pair sees the slot's metadata atomically) and the
	// per-victim re-validation at line ~1250 below catches any
	// staleness introduced between slots in the snapshot pass.
	type victim struct {
		idx int
		tag BufferTag
	}
	n := len(p.slots)
	p.poolMu.Lock()
	start := p.bgwriterHand
	p.bgwriterHand = (start + n) % n
	p.poolMu.Unlock()
	victims := make([]victim, 0, maxPages)
	for i := 0; i < n && len(victims) < maxPages; i++ {
		idx := (start + i) % n
		s := p.slots[idx]
		p.poolMu.Lock()
		ok := s.valid && s.dirty && s.pinCount == 0
		var tag BufferTag
		if ok {
			tag = s.tag
		}
		p.poolMu.Unlock()
		if ok {
			victims = append(victims, victim{idx: idx, tag: tag})
		}
	}

	// Flush each victim under its shared content lock (read lock is
	// sufficient — we only need stable page bytes, not exclusive access).
	written := 0
	for _, v := range victims {
		s := p.slots[v.idx]
		s.contentMu.RLock()

		// Re-check under poolMu that the slot is still the same dirty page.
		p.poolMu.Lock()
		stillValid := s.valid && s.tag == v.tag && s.dirty && s.pinCount == 0
		p.poolMu.Unlock()

		if stillValid {
			if err := p.flushSlot(v.tag, s.page); err == nil {
				// Clear dirty flag under poolMu; re-check tag to avoid
				// a race where the slot was reused for a different block.
				p.poolMu.Lock()
				if s.tag == v.tag {
					s.dirty = false
				}
				p.poolMu.Unlock()
				written++
			}
		}
		s.contentMu.RUnlock()
	}
	return written
}

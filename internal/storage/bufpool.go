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
	// logHeapVacuum emits a logical heap-vacuum (page prune) WAL
	// record. Used by VACUUM to capture the dead-slot list rather
	// than a full-page image on each pruned page.
	logHeapVacuum LogHeapVacuumFunc
	// fullPageWrites gates FPI emission. The wire/admin layer can
	// flip it at runtime to mirror upstream's full_page_writes
	// SIGHUP-context GUC.
	fullPageWrites atomic.Bool
	// logger is used to surface non-fatal FPI emission failures
	// without changing MarkDirty's signature.
	logger *slog.Logger

	// poolMu guards byTag, slots[*].tag/valid/dirty, slots[*].pinCount,
	// slots[*].usageCount, slots[*].fpiSinceCheckpoint, and clockHand.
	// It does NOT guard the page bytes — those are guarded by
	// Slot.contentMu.
	poolMu    sync.Mutex
	byTag     map[BufferTag]int
	clockHand int
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

	// LogHeapVacuum, when non-nil, is exposed via
	// Pool.LogHeapVacuum so the VACUUM path can emit a
	// dead-slot-list change record instead of a full FPI on
	// each pruned page.
	LogHeapVacuum LogHeapVacuumFunc

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

// LogHeapVacuumFunc emits one logical heap-vacuum (page prune)
// redo record carrying the 1-based LP_NORMAL slot numbers being
// reclaimed. Replay calls VacuumHeapPageBySlots with the same
// list, so the prune is bit-exact whether it's the original
// write or recovery. See docs/design/0002-0003-redo-records.md.
type LogHeapVacuumFunc func(rel RelFileNode, blk BlockNumber, deadSlots []uint16) (LSN, error)

// NewPool allocates a Pool of cfg.Slots fixed buffers backed by an
// mmap'd arena. Returns an error if slots <= 0 or the arena alloc
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
		wal:            cfg.WAL,
		logFPI:         cfg.LogPageImage,
		logBtreeSplit:  cfg.LogBtreeSplit,
		logHeapInsert:  cfg.LogHeapInsert,
		logBtreeInsert: cfg.LogBtreeInsert,
		logHeapDelete:  cfg.LogHeapDelete,
		logHeapVacuum:  cfg.LogHeapVacuum,
		logger:         logger,
	}
	p.fullPageWrites.Store(cfg.FullPageWrites)
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

// LogBtreeInsert returns the configured btree non-split insert
// change-record hook, or nil if none was wired. Callers fall
// back to per-page FPI via MarkDirty when nil.
func (p *Pool) LogBtreeInsert() LogBtreeInsertFunc { return p.logBtreeInsert }

// LogHeapDelete returns the configured heap-delete change-record
// hook, or nil if none was wired. Callers fall back to per-page
// FPI via MarkDirty when nil.
func (p *Pool) LogHeapDelete() LogHeapDeleteFunc { return p.logHeapDelete }

// LogHeapVacuum returns the configured heap-vacuum change-record
// hook, or nil if none was wired. Callers fall back to per-page
// FPI via MarkDirty when nil.
func (p *Pool) LogHeapVacuum() LogHeapVacuumFunc { return p.logHeapVacuum }

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
	p.poolMu.Unlock()

	s.contentMu.Lock()
	if err := InitPage(s.page); err != nil {
		s.contentMu.Unlock()
		return nil, InvalidBlockNumber, err
	}
	blk, err := p.mgr.Extend(rel, s.page)
	s.contentMu.Unlock()
	if err != nil {
		return nil, InvalidBlockNumber, err
	}
	tag := BufferTag{Rel: rel, Block: blk}

	p.poolMu.Lock()
	if idx, ok := p.byTag[tag]; ok {
		// Another goroutine already loaded/published this freshly
		// extended block while we were outside poolMu. Reuse that
		// slot and release ours back to the free pool.
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
	s.pinCount = 1
	s.usageCount = 1
	p.byTag[tag] = slotIdx
	p.poolMu.Unlock()
	return s, blk, nil
}

// Pin returns the slot holding tag, reading from disk if necessary.
// The slot's pinCount is incremented; the caller MUST call Unpin.
func (p *Pool) Pin(tag BufferTag) (*Slot, error) {
	p.poolMu.Lock()
	if idx, ok := p.byTag[tag]; ok {
		s := p.slots[idx]
		s.pinCount++
		if s.usageCount < maxUsageCount {
			s.usageCount++
		}
		p.poolMu.Unlock()
		return s, nil
	}
	slotIdx, err := p.evictLocked()
	if err != nil {
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
	err = p.mgr.ReadBlock(tag.Rel, tag.Block, s.page)
	s.contentMu.Unlock()
	if err != nil {
		p.poolMu.Lock()
		s.tag = BufferTag{}
		s.pinCount = 0
		s.usageCount = 0
		s.valid = false
		s.dirty = false
		p.poolMu.Unlock()
		return nil, err
	}
	p.poolMu.Lock()
	if idx, ok := p.byTag[tag]; ok {
		// Another goroutine published this tag while we were doing I/O.
		// Use that slot and release ours.
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
func (p *Pool) FlushAll() error {
	return p.FlushAllPaced(nil)
}

// FlushAllPaced writes every dirty slot through smgr and invokes
// `pacer(progress)` after each flush, where progress is the
// fraction of work already completed in [0, 1]. The pacer can
// sleep to spread the I/O over a target wall-clock window — this
// is the path the spread checkpoint writes (milestone 0002) take.
// A nil pacer behaves like FlushAll.
//
// The dirty set is snapshotted at entry; pages dirtied after the
// snapshot are not flushed in this pass and stay dirty for the
// next checkpoint.
func (p *Pool) FlushAllPaced(pacer func(progress float64) error) error {
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
	for i, t := range todo {
		s := p.slots[t.idx]
		s.contentMu.RLock()
		err := p.flushSlot(t.tag, s.page)
		s.contentMu.RUnlock()
		if err != nil {
			return err
		}
		p.poolMu.Lock()
		// Only clear dirty if the tag hasn't been reassigned and
		// nothing else has marked it dirty since the flush started.
		if s.tag == t.tag {
			s.dirty = false
		}
		p.poolMu.Unlock()
		if pacer != nil && total > 0 {
			progress := float64(i+1) / float64(total)
			if err := pacer(progress); err != nil {
				return err
			}
		}
	}
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
// Algorithm: clock sweep. We do at most 2*N+M passes where M==maxUsageCount,
// matching upstream's bound (each pass decrements usageCount by 1 and
// after maxUsageCount+1 passes any unpinned slot has usageCount==0).
func (p *Pool) evictLocked() (int, error) {
	n := len(p.slots)
	if n == 0 {
		return 0, ErrNoBuffer
	}
	maxPasses := 2*n + int(maxUsageCount)
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
		return idx, nil
	}
	return 0, ErrNoBuffer
}

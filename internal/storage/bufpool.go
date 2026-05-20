package storage

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Slot state bit layout (single 64-bit atomic word):
//
//	bits  0..21  pinCount    (22 bits → 4 M concurrent pins max)
//	bits 22..29  usageCount  (8 bits, saturating clock-sweep counter)
//	bit      30  dirty       (page has been mutated since last flush)
//	bit      31  valid       (slot holds a valid page for its tag)
//	bit      32  ioInflight  (a disk read is in progress for this slot)
//	bits 33..47  gen         (15 bits, bumped on eviction; ABA guard)
//	bits 48..63  reserved
const (
	slotPinMask    = uint64((1 << 22) - 1) // bits 0..21
	slotUsageShift = 22
	slotUsageMask  = uint64(((1 << 8) - 1) << slotUsageShift)
	slotDirtyBit   = uint64(1 << 30)
	slotValidBit   = uint64(1 << 31)
	slotIOBit      = uint64(1 << 32)
	slotGenShift   = 33
	slotGenMask    = uint64(((1 << 15) - 1) << slotGenShift)
	maxUsageCount  = 5
	maxPinCount    = (1 << 22) - 1
)

// slotState helpers — extract fields from a packed state word.
func statePin(st uint64) uint64    { return st & slotPinMask }
func stateUsage(st uint64) uint64  { return (st & slotUsageMask) >> slotUsageShift }
func stateGen(st uint64) uint32    { return uint32((st & slotGenMask) >> slotGenShift) }
func stateValid(st uint64) bool    { return st&slotValidBit != 0 }
func stateDirty(st uint64) bool    { return st&slotDirtyBit != 0 }
func stateIO(st uint64) bool       { return st&slotIOBit != 0 }

// slotIOCond is a per-slot condition variable for waiting on ioInflight.
type slotIOCond struct {
	mu   sync.Mutex
	cond *sync.Cond
}

// Slot is one buffer-pool entry holding one Page. Callers receive
// *Slot from Pin and return it via Unpin. Direct field access is
// permitted under contentMu (held implicitly by the pin guarantee
// for single-writer flows).
type Slot struct {
	page Page // alias into the arena

	// Tag identifies the (rel, fork, block) the slot currently holds.
	// Written only while ioInflight is set in state; readable once valid.
	tag BufferTag

	// state packs pinCount, usageCount, dirty, valid, ioInflight, gen
	// into a single 64-bit atomic word for lock-free Pin/Unpin.
	state atomic.Uint64

	// fpiSinceCheckpoint records whether a full-page-image WAL
	// record has already been emitted for this page in the current
	// checkpoint epoch. It is read/written atomically so that the FPI
	// path is safe to call while the caller already holds contentMu
	// (e.g. test pattern: s.Lock(); s.Page()[i] = b; MarkDirty(s);
	// s.Unlock()).  The checkpointer clears it across all slots after
	// a successful checkpoint via Pool.ResetCheckpointEpoch.
	fpiSinceCheckpoint atomic.Bool

	// contentMu guards the Page bytes for read/write. The pool does
	// not hold any global lock while this is taken.
	contentMu sync.RWMutex
}

// Page returns the Page-typed view of the slot's storage. The caller
// must hold a pin on the slot.
func (s *Slot) Page() Page { return s.page }

// Tag returns the (rel, fork, block) the slot currently holds.
func (s *Slot) Tag() BufferTag { return s.tag }

// Lock acquires the page's exclusive content lock.
func (s *Slot) Lock()    { s.contentMu.Lock() }
func (s *Slot) Unlock()  { s.contentMu.Unlock() }
func (s *Slot) RLock()   { s.contentMu.RLock() }
func (s *Slot) RUnlock() { s.contentMu.RUnlock() }

// valid is a read helper for tests/internal callers.
func (s *Slot) isValid() bool { return stateValid(s.state.Load()) }

// dirty is a read helper for tests/internal callers.
func (s *Slot) isDirty() bool { return stateDirty(s.state.Load()) }

// getPinCount returns the current pin count.
func (s *Slot) getPinCount() int32 { return int32(statePin(s.state.Load())) }

// Pool is the buffer manager. It is goroutine-safe.
type Pool struct {
	mgr   *Manager
	arena *arena
	slots []Slot
	wal   WALFlusher
	// logFPI emits a full-page-image WAL record and returns the
	// record's end LSN. nil disables FPI emission.
	logFPI func(rel RelFileNode, blk BlockNumber, page Page) (LSN, error)
	// logBtreeSplit emits an atomic B-tree split WAL record.
	logBtreeSplit LogBtreeSplitFunc
	// logHeapInsert emits a logical heap-insert WAL record.
	logHeapInsert LogHeapInsertFunc
	// logBtreeInsert emits a logical B-tree non-split insert WAL record.
	logBtreeInsert LogBtreeInsertFunc
	// logHeapDelete emits a logical heap-delete WAL record.
	logHeapDelete LogHeapDeleteFunc
	// logHeapLock emits a logical heap-lock WAL record.
	logHeapLock LogHeapLockFunc
	// logHeapVacuum emits a logical heap-vacuum WAL record.
	logHeapVacuum LogHeapVacuumFunc
	// logBtreeVacuum emits a logical btree-vacuum WAL record.
	logBtreeVacuum LogBtreeVacuumFunc
	// M0079-0003 logical records.
	logBtreeUnlinkPage       LogBtreeUnlinkPageFunc
	logBtreeNewRoot          LogBtreeNewRootFunc
	logBtreeMarkPageHalfDead LogBtreeMarkPageHalfDeadFunc
	// M0080-0001 logical record for VACUUM FREEZE.
	logHeapFreeze LogHeapFreezeFunc
	// logHeapHotUpdate emits an atomic HOT-update WAL record.
	logHeapHotUpdate LogHeapHotUpdateFunc
	// logHeapPruneOpt emits an opportunistic page-pruning WAL record.
	logHeapPruneOpt LogHeapPruneOptFunc
	// logSmgrCreate emits a relation-file creation WAL record.
	logSmgrCreate func(rel RelFileNode) error
	// logChangeRecord emits a pre-encoded change record.
	logChangeRecord func(payload []byte) (LSN, error)
	// fullPageWrites gates FPI emission.
	fullPageWrites atomic.Bool
	// logger surfaces non-fatal FPI failures.
	logger *slog.Logger

	// prefetchEnabled gates Pool.Prefetch.
	prefetchEnabled atomic.Bool

	// asyncFlushBatchSize controls FlushAllPaced batching.
	asyncFlushBatchSize atomic.Int32

	// bm is the lock-free buffer-tag → slot-index hash table.
	// Fast path (cache hit): lock-free Lookup + CAS on slot state.
	// Slow path (cache miss / IO-inflight wait): must hold pinMu.
	bm *bufmap

	// pinMu serialises the slow path: cache-miss eviction + insert.
	// The fast path (pin of a valid cached slot) never acquires this.
	// pinMu also serialises waits on IO-in-progress so only the right
	// goroutines get woken and re-check the correct slot.
	pinMu sync.Mutex

	// clockHand is the atomic clock-sweep hand for victim selection.
	clockHand atomic.Int64

	// bgwriterHand is the bgwriter's independent scan cursor.
	// Protected by bgwriterMu.
	bgwriterMu   sync.Mutex
	bgwriterHand int

	// compactMu serialises the rare tombstone-compaction rebuild.
	compactMu sync.Mutex

	// tombstones counts tombstone entries in bm; used to trigger compaction.
	tombstones atomic.Int64

	// pinCond is the condition variable for waiting on IO-in-progress.
	// Signalled whenever any slot transitions out of ioInflight.
	pinCond *sync.Cond

	// OnPinWait is called when Pool.Pin issues a disk read.
	OnPinWait func()

	// OnPinDone is called after the disk read finishes.
	OnPinDone func()

	// OnBufferIOWait is called when a goroutine waits for an in-flight read.
	OnBufferIOWait func()

	// OnFlushAll is called at the start of FlushAll/FlushAllPaced.
	OnFlushAll func()

	// Dirty-victim instrumentation (M0048-0003).
	// Atomic counters (no lock needed post-refactor).
	dirtyVictimCount atomic.Int64
	totalVictimCount atomic.Int64
}

// PoolConfig controls Pool sizing.
type PoolConfig struct {
	Slots int

	// WAL, when non-nil, is asked to flush up to the page's pd_lsn
	// before any dirty page is written to data files.
	WAL WALFlusher

	// LogPageImage, when non-nil, is invoked by MarkDirty on the
	// first mutation of each page after every checkpoint to emit
	// a full-page-image WAL record.
	LogPageImage func(rel RelFileNode, blk BlockNumber, page Page) (LSN, error)

	// FullPageWrites mirrors upstream's full_page_writes GUC.
	FullPageWrites bool

	LogBtreeSplit            LogBtreeSplitFunc
	LogHeapInsert            LogHeapInsertFunc
	LogBtreeInsert           LogBtreeInsertFunc
	LogHeapDelete            LogHeapDeleteFunc
	LogHeapLock              LogHeapLockFunc
	LogHeapVacuum            LogHeapVacuumFunc
	LogBtreeVacuum           LogBtreeVacuumFunc
	LogBtreeUnlinkPage       LogBtreeUnlinkPageFunc
	LogBtreeNewRoot          LogBtreeNewRootFunc
	LogBtreeMarkPageHalfDead LogBtreeMarkPageHalfDeadFunc
	LogHeapFreeze            LogHeapFreezeFunc
	LogHeapHotUpdate         LogHeapHotUpdateFunc
	LogHeapPruneOpt          LogHeapPruneOptFunc
	LogSmgrCreate            func(rel RelFileNode) error
	LogChangeRecord          func(payload []byte) (LSN, error)

	// Logger receives FPI emission failures. nil means slog.Default().
	Logger *slog.Logger
}

// WALFlusher is the minimal WAL contract the buffer pool needs to
// enforce write-ahead ordering.
type WALFlusher interface {
	FlushUpTo(lsn uint64) error
}

// LogBtreeSplitFunc emits one atomic WAL record covering a B-tree page split.
type LogBtreeSplitFunc func(rel RelFileNode, leftBlk, rightBlk BlockNumber, leftPage, rightPage Page) (LSN, error)

// LogHeapInsertFunc emits one logical heap-insert redo record.
type LogHeapInsertFunc func(rel RelFileNode, blk BlockNumber, lineSlot uint16, tuple []byte) (LSN, error)

// LogBtreeInsertFunc emits one logical B-tree non-split insert redo record.
type LogBtreeInsertFunc func(rel RelFileNode, blk BlockNumber, item []byte) (LSN, error)

// LogHeapDeleteFunc emits one logical heap-delete (xmax stamp) redo record.
type LogHeapDeleteFunc func(rel RelFileNode, blk BlockNumber, lineSlot uint16, xmax TransactionID, oldTuple []byte) (LSN, error)

// LogHeapLockFunc emits one row-lock WAL record.
type LogHeapLockFunc func(rel RelFileNode, blk BlockNumber, lineSlot uint16, xmax TransactionID, lockStrength uint16) (LSN, error)

// LogHeapVacuumFunc emits one logical heap-vacuum (page prune) redo record.
type LogHeapVacuumFunc func(rel RelFileNode, blk BlockNumber, deadSlots []uint16) (LSN, error)

// LogBtreeVacuumFunc emits one logical btree-vacuum redo record.
type LogBtreeVacuumFunc func(rel RelFileNode, blk BlockNumber, keptItems [][]byte, opaqueFlags uint16) (LSN, error)

// BtreeUnlinkPageRequest collects the 4-page mutation control info.
type BtreeUnlinkPageRequest struct {
	LeafBlk          BlockNumber
	LeafFlagsAfter   uint16
	HasLeftSib       bool
	LeftSibBlk       BlockNumber
	LeftSibNewNext   BlockNumber
	HasRightSib      bool
	RightSibBlk      BlockNumber
	RightSibNewPrev  BlockNumber
	HasParent        bool
	ParentBlk        BlockNumber
	ParentRemoveSlot uint16
}

// LogBtreeUnlinkPageFunc emits the M0079-0003 atomic page-deletion redo record.
type LogBtreeUnlinkPageFunc func(rel RelFileNode, req BtreeUnlinkPageRequest) (LSN, error)

// LogBtreeNewRootFunc emits the M0079-0003 root-replacement record.
type LogBtreeNewRootFunc func(rel RelFileNode, rootBlk BlockNumber, level uint32, items [][]byte) (LSN, error)

// LogBtreeMarkPageHalfDeadFunc emits the M0079-0003 leaf-only half-dead transition record.
type LogBtreeMarkPageHalfDeadFunc func(rel RelFileNode, leafBlk BlockNumber, flagsAfter uint16) (LSN, error)

// LogHeapFreezeFunc emits the M0080-0001 heap-freeze redo record.
type LogHeapFreezeFunc func(rel RelFileNode, blk BlockNumber, frozenSlots []uint16) (LSN, error)

// LogHeapHotUpdateFunc emits one atomic HOT-update redo record.
type LogHeapHotUpdateFunc func(rel RelFileNode, blk BlockNumber, oldSlot uint16, xmax TransactionID, tupleBytes []byte) (LSN, error)

// LogHeapPruneOptFunc emits one opportunistic page-pruning redo record.
type LogHeapPruneOptFunc func(rel RelFileNode, blk BlockNumber, redirects [][2]uint16, unused []uint16) (LSN, error)

// NewPool allocates a Pool of cfg.Slots fixed buffers backed by a
// Go-heap arena.
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
		mgr:                      mgr,
		arena:                    a,
		slots:                    make([]Slot, cfg.Slots),
		wal:                      cfg.WAL,
		logFPI:                   cfg.LogPageImage,
		logBtreeSplit:            cfg.LogBtreeSplit,
		logHeapInsert:            cfg.LogHeapInsert,
		logBtreeInsert:           cfg.LogBtreeInsert,
		logHeapDelete:            cfg.LogHeapDelete,
		logHeapLock:              cfg.LogHeapLock,
		logHeapVacuum:            cfg.LogHeapVacuum,
		logBtreeVacuum:           cfg.LogBtreeVacuum,
		logBtreeUnlinkPage:       cfg.LogBtreeUnlinkPage,
		logBtreeNewRoot:          cfg.LogBtreeNewRoot,
		logBtreeMarkPageHalfDead: cfg.LogBtreeMarkPageHalfDead,
		logHeapFreeze:            cfg.LogHeapFreeze,
		logHeapHotUpdate:         cfg.LogHeapHotUpdate,
		logHeapPruneOpt:          cfg.LogHeapPruneOpt,
		logSmgrCreate:            cfg.LogSmgrCreate,
		logChangeRecord:          cfg.LogChangeRecord,
		logger:                   logger,
		bm:                       newBufmap(cfg.Slots),
	}
	p.pinCond = sync.NewCond(&p.pinMu)
	p.fullPageWrites.Store(cfg.FullPageWrites)
	// Initialise per-slot page pointers.
	for i := range p.slots {
		p.slots[i].page = a.slot(i)
	}
	return p, nil
}

// LogBtreeSplit returns the configured atomic split-record hook.
func (p *Pool) LogBtreeSplit() LogBtreeSplitFunc { return p.logBtreeSplit }

// LogPageImage returns the configured full-page-image hook.
func (p *Pool) LogPageImage() func(rel RelFileNode, blk BlockNumber, page Page) (LSN, error) {
	return p.logFPI
}

// LogHeapInsert returns the configured heap-insert change-record hook.
func (p *Pool) LogHeapInsert() LogHeapInsertFunc { return p.logHeapInsert }

// LogChangeRecord appends a pre-encoded WAL change record.
func (p *Pool) LogChangeRecord(payload []byte) (LSN, error) {
	if p == nil || p.logChangeRecord == nil {
		return 0, nil
	}
	return p.logChangeRecord(payload)
}

// LogBtreeInsert returns the configured btree non-split insert change-record hook.
func (p *Pool) LogBtreeInsert() LogBtreeInsertFunc { return p.logBtreeInsert }

// LogHeapDelete returns the configured heap-delete change-record hook.
func (p *Pool) LogHeapDelete() LogHeapDeleteFunc { return p.logHeapDelete }

// LogHeapLock returns the configured row-lock change-record emitter.
func (p *Pool) LogHeapLock() LogHeapLockFunc { return p.logHeapLock }

// LogHeapVacuum returns the configured heap-vacuum change-record hook.
func (p *Pool) LogHeapVacuum() LogHeapVacuumFunc { return p.logHeapVacuum }

// LogBtreeVacuum returns the configured btree-vacuum kept-items change-record emitter.
func (p *Pool) LogBtreeVacuum() LogBtreeVacuumFunc { return p.logBtreeVacuum }

// LogBtreeUnlinkPage returns the configured atomic page-deletion change-record emitter.
func (p *Pool) LogBtreeUnlinkPage() LogBtreeUnlinkPageFunc { return p.logBtreeUnlinkPage }

// LogBtreeNewRoot returns the configured root-replacement change-record emitter.
func (p *Pool) LogBtreeNewRoot() LogBtreeNewRootFunc { return p.logBtreeNewRoot }

// LogBtreeMarkPageHalfDead returns the configured leaf-only half-dead transition emitter.
func (p *Pool) LogBtreeMarkPageHalfDead() LogBtreeMarkPageHalfDeadFunc {
	return p.logBtreeMarkPageHalfDead
}

// LogHeapFreeze returns the configured heap-freeze change-record emitter.
func (p *Pool) LogHeapFreeze() LogHeapFreezeFunc { return p.logHeapFreeze }

// LogHeapHotUpdate returns the configured HOT-update change-record hook.
func (p *Pool) LogHeapHotUpdate() LogHeapHotUpdateFunc { return p.logHeapHotUpdate }

// LogHeapPruneOpt returns the configured opportunistic-pruning change-record hook.
func (p *Pool) LogHeapPruneOpt() LogHeapPruneOptFunc { return p.logHeapPruneOpt }

// SetFullPageWrites toggles full-page-image emission at runtime.
func (p *Pool) SetFullPageWrites(on bool) { p.fullPageWrites.Store(on) }

// FullPageWrites reports the current setting.
func (p *Pool) FullPageWrites() bool { return p.fullPageWrites.Load() }

// ResetCheckpointEpoch clears the per-slot "FPI emitted" flag.
// The checkpointer calls this after a successful checkpoint.
func (p *Pool) ResetCheckpointEpoch() {
	for i := range p.slots {
		p.slots[i].fpiSinceCheckpoint.Store(false)
	}
}

// Close flushes every dirty slot through smgr and releases the arena.
func (p *Pool) Close() error {
	flushErr := p.FlushAll()
	closeErr := p.arena.close()
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

// NBlocks reports the number of blocks currently in rel.
func (p *Pool) NBlocks(rel RelFileNode) (BlockNumber, error) {
	return p.mgr.NBlocks(rel)
}

// Manager exposes the underlying storage manager.
func (p *Pool) Manager() *Manager { return p.mgr }

// SyncAllDataFiles fdatasyncs every open data file.
func (p *Pool) SyncAllDataFiles() error {
	if p.mgr == nil {
		return nil
	}
	return p.mgr.SyncAll()
}

// SetPrefetchEnabled toggles Pool.Prefetch's behaviour.
func (p *Pool) SetPrefetchEnabled(on bool) { p.prefetchEnabled.Store(on) }

// SetAsyncFlushBatchSize controls how many dirty slots FlushAllPaced batches.
func (p *Pool) SetAsyncFlushBatchSize(n int) {
	if n > MaxFlushBatchSize {
		n = MaxFlushBatchSize
	}
	if n < 0 {
		n = 0
	}
	p.asyncFlushBatchSize.Store(int32(n))
}

// MaxFlushBatchSize caps the per-batch dirty-slot count.
const MaxFlushBatchSize = 256

// flushBatchSize returns the effective batch size for FlushAllPaced.
func (p *Pool) flushBatchSize() int {
	n := int(p.asyncFlushBatchSize.Load())
	if n < 1 {
		return 1
	}
	return n
}

// Prefetch is a hint that the caller is about to Pin tag.
func (p *Pool) Prefetch(tag BufferTag) {
	if !p.prefetchEnabled.Load() {
		return
	}
	// Fast check: already cached?
	if slotIdx, _ := p.bm.Lookup(tag); slotIdx >= 0 {
		return
	}
	buf := make([]byte, BlockSize)
	_, _ = p.mgr.PrefetchBlock(tag.Rel, tag.Block, buf)
}

// InvalidateRel evicts every slot currently bound to rel.
func (p *Pool) InvalidateRel(rel RelFileNode) {
	for i := range p.slots {
		s := &p.slots[i]
		st := s.state.Load()
		if !stateValid(st) {
			continue
		}
		if s.tag.Rel != rel {
			continue
		}
		if statePin(st) > 0 {
			continue
		}
		// Try to atomically clear valid+dirty bits.
		// We only clear if the slot is still valid+unpinned.
		newSt := st &^ (slotValidBit | slotDirtyBit)
		if s.state.CompareAndSwap(st, newSt) {
			p.bm.Delete(s.tag, int32(i))
			p.tombstones.Add(1)
		}
	}
}

// InvalidateBlock evicts the slot bound to tag if it exists and is unpinned.
func (p *Pool) InvalidateBlock(tag BufferTag) {
	slotIdx, _ := p.bm.Lookup(tag)
	if slotIdx < 0 {
		return
	}
	s := &p.slots[slotIdx]
	st := s.state.Load()
	if !stateValid(st) || statePin(st) > 0 {
		return
	}
	newSt := st &^ (slotValidBit | slotDirtyBit)
	if s.state.CompareAndSwap(st, newSt) {
		p.bm.Delete(tag, slotIdx)
		p.tombstones.Add(1)
	}
}

// ErrNoBuffer is returned when every slot is pinned and the clock
// sweep can't find a victim.
var ErrNoBuffer = errors.New("no available buffer (all pinned)")

// tryPinSlot attempts to increment the pin count on slotIdx atomically,
// verifying the slot is valid, not in IO, and has the expected generation.
// Returns the slot on success, or nil on failure (caller should retry/fallback).
func (p *Pool) tryPinSlot(slotIdx int32, gen uint32) *Slot {
	s := &p.slots[slotIdx]
	for {
		old := s.state.Load()
		if !stateValid(old) || stateIO(old) {
			return nil
		}
		if stateGen(old) != gen {
			return nil // ABA
		}
		pinCount := statePin(old)
		if pinCount >= maxPinCount {
			return nil
		}
		usage := stateUsage(old)
		if usage < maxUsageCount {
			usage++
		}
		newSt := (old &^ (slotPinMask | slotUsageMask)) | (pinCount + 1) | (usage << slotUsageShift)
		if s.state.CompareAndSwap(old, newSt) {
			return s
		}
	}
}

// releaseVictimSlot returns a victim slot back to the free pool when we
// decide not to use it (e.g., because we found the tag already published
// by another goroutine). Called with pinMu held. Signals pinCond.
// MUST be called with pinMu held.
func (p *Pool) releaseVictimSlot(victimIdx int) {
	p.slots[victimIdx].state.Store(0)
	p.pinCond.Broadcast()
}

// claimVictim finds a victim slot using clock-sweep and atomically sets
// ioInflight, clearing valid+dirty. Returns (idx, wasDirty, oldTag).
// MUST be called with pinMu held.
func (p *Pool) claimVictim() (victimIdx int, wasDirty bool, oldTag BufferTag, err error) {
	n := len(p.slots)
	if n == 0 {
		return 0, false, BufferTag{}, ErrNoBuffer
	}
	maxProbes := n * (maxUsageCount + 2)
	for probe := 0; probe < maxProbes; probe++ {
		i := int(p.clockHand.Add(1)-1) % n
		s := &p.slots[i]
		old := s.state.Load()

		if statePin(old) != 0 {
			continue // pinned
		}
		if stateIO(old) {
			continue // another goroutine is evicting this slot
		}

		usage := stateUsage(old)
		if usage > 0 {
			// Decrement usage (second-chance). Best-effort.
			newSt := (old &^ slotUsageMask) | ((usage - 1) << slotUsageShift)
			s.state.CompareAndSwap(old, newSt)
			continue
		}

		wasValid := stateValid(old)
		dirty := stateDirty(old)

		// Track dirty-victim stats.
		if wasValid {
			p.totalVictimCount.Add(1)
			if dirty {
				p.dirtyVictimCount.Add(1)
			}
		}

		// Claim: set ioInflight, bump gen to invalidate in-flight fast-path CAS.
		curGen := stateGen(old)
		newGen := (curGen + 1) & 0x7FFF
		newSt := slotIOBit | (uint64(newGen) << slotGenShift)
		if s.state.CompareAndSwap(old, newSt) {
			tag := s.tag
			if !wasValid {
				tag = BufferTag{}
			}
			return i, wasValid && dirty, tag, nil
		}
		// Another goroutine modified this slot concurrently; try next.
	}
	return 0, false, BufferTag{}, ErrNoBuffer
}

// evictVictim flushes (if dirty) and removes the old tag from bufmap.
// MUST be called with pinMu held.
func (p *Pool) evictVictim(victimIdx int, wasDirty bool, oldTag BufferTag) error {
	if oldTag == (BufferTag{}) {
		return nil // slot was free
	}
	// Delete from bufmap BEFORE flushing to ensure no stale lookups.
	p.bm.Delete(oldTag, int32(victimIdx))
	p.tombstones.Add(1)
	p.maybeCompact()
	if wasDirty {
		s := &p.slots[victimIdx]
		// Release pinMu while doing IO so other goroutines can proceed.
		p.pinMu.Unlock()
		s.contentMu.Lock()
		flushErr := p.flushSlot(oldTag, s.page)
		s.contentMu.Unlock()
		p.pinMu.Lock()
		if flushErr != nil {
			return flushErr
		}
	}
	return nil
}

// PinNew pins the next-after-end block of rel for writing without
// reading from disk. Returns the block number the slot represents.
func (p *Pool) PinNew(rel RelFileNode) (*Slot, BlockNumber, error) {
	p.pinMu.Lock()

	victimIdx, wasDirty, oldTag, err := p.claimVictim()
	if err != nil {
		p.pinMu.Unlock()
		return nil, InvalidBlockNumber, err
	}
	s := &p.slots[victimIdx]

	if err := p.evictVictim(victimIdx, wasDirty, oldTag); err != nil {
		p.releaseVictimSlot(victimIdx)
		p.pinMu.Unlock()
		return nil, InvalidBlockNumber, fmt.Errorf("flush victim: %w", err)
	}

	// Initialize page and extend the relation. Release pinMu during IO.
	p.pinMu.Unlock()
	s.contentMu.Lock()
	if err := InitPage(s.page); err != nil {
		s.contentMu.Unlock()
		p.pinMu.Lock()
		p.releaseVictimSlot(victimIdx)
		p.pinMu.Unlock()
		return nil, InvalidBlockNumber, err
	}
	blk, err := p.mgr.Extend(rel, s.page)
	s.contentMu.Unlock()
	if err != nil {
		p.pinMu.Lock()
		p.releaseVictimSlot(victimIdx)
		p.pinMu.Unlock()
		return nil, InvalidBlockNumber, err
	}
	p.pinMu.Lock()

	// Emit SmgrCreate WAL record on first block creation.
	if blk == 0 && p.logSmgrCreate != nil {
		if emitErr := p.logSmgrCreate(rel); emitErr != nil {
			p.logger.Error("SmgrCreate WAL emission failed", "rel", rel, "err", emitErr)
		}
	}

	tag := BufferTag{Rel: rel, Block: blk}

	// Publish this slot as valid+dirty+pinned.
	gen := stateGen(s.state.Load())
	s.tag = tag
	newSt := slotValidBit | slotDirtyBit | uint64(1) | (uint64(1) << slotUsageShift) | (uint64(gen) << slotGenShift)
	s.state.Store(newSt)

	// Insert into bufmap. Under pinMu, no other goroutine can insert the same tag.
	if !p.bm.Insert(tag, int32(victimIdx), gen) {
		// Another goroutine published this block while we were in Extend.
		// Use their slot and release ours.
		if existingIdx, existingGen := p.bm.Lookup(tag); existingIdx >= 0 {
			if existing := p.tryPinSlot(existingIdx, existingGen); existing != nil {
				s.tag = BufferTag{}
				s.state.Store(0)
				p.pinCond.Broadcast()
				p.pinMu.Unlock()
				return existing, blk, nil
			}
		}
		// Fall through: keep our publication.
	}

	p.pinCond.Broadcast()
	p.pinMu.Unlock()
	return s, blk, nil
}

// Pin returns the slot holding tag, reading from disk if necessary.
// The slot's pinCount is incremented; the caller MUST call Unpin.
//
// BM_IO_IN_PROGRESS: when multiple goroutines miss the cache for the same
// tag simultaneously, only one issues a disk read (the winner). The others
// wait on pinCond under pinMu until the IO completes, then find the slot valid.
//
// Fast path (cache hit): lock-free Lookup + CAS, no mutex.
// Slow path (cache miss / IO wait): acquires pinMu.
func (p *Pool) Pin(tag BufferTag) (*Slot, error) {
	for {
		// Fast path: tag is cached and valid (no IO in flight).
		slotIdx, gen := p.bm.Lookup(tag)
		if slotIdx >= 0 {
			s := &p.slots[slotIdx]
			old := s.state.Load()

			if stateValid(old) && !stateIO(old) && stateGen(old) == gen {
				// Slot is valid and ready: try CAS to increment pin.
				pinCount := statePin(old)
				if pinCount >= maxPinCount {
					return nil, ErrNoBuffer
				}
				usage := stateUsage(old)
				if usage < maxUsageCount {
					usage++
				}
				newSt := (old &^ (slotPinMask | slotUsageMask)) | (pinCount + 1) | (usage << slotUsageShift)
				if s.state.CompareAndSwap(old, newSt) {
					return s, nil
				}
				// CAS failed (concurrent pin/unpin); retry fast path.
				continue
			}
		}

		// Slow path: cache miss, IO in flight, or transient state.
		if s, err := p.pinSlow(tag); s != nil || err != nil {
			return s, err
		}
		// pinSlow returned nil,nil: spurious wakeup or ABA; retry from top.
	}
}

// pinSlow is the slow path for Pin: acquires pinMu, re-checks the bufmap,
// waits for in-flight IO to complete, or performs a full cache-miss eviction+read.
//
// Returns (slot, nil) on success, (nil, err) on hard error,
// (nil, nil) to signal the caller to retry the fast path.
func (p *Pool) pinSlow(tag BufferTag) (*Slot, error) {
	p.pinMu.Lock()
	defer p.pinMu.Unlock()

	for {
		slotIdx, gen := p.bm.Lookup(tag)
		if slotIdx >= 0 {
			s := &p.slots[slotIdx]
			old := s.state.Load()

			if stateIO(old) {
				// IO in flight: wait for it to complete.
				if p.OnBufferIOWait != nil {
					p.OnBufferIOWait()
				}
				p.pinCond.Wait() // releases pinMu, sleeps, re-acquires
				continue         // re-check after wakeup
			}

			if stateValid(old) && stateGen(old) == gen {
				// Valid: try to pin.
				if s2 := p.tryPinSlot(slotIdx, gen); s2 != nil {
					return s2, nil
				}
				// tryPinSlot failed (race): retry
				continue
			}

			// Slot in unexpected state (just evicted?): retry Lookup.
			continue
		}

		// Cache miss: load the page from disk.
		return p.pinLoad(tag)
	}
}

// pinLoad loads tag from disk into a victim slot. Called under pinMu.
// Returns (slot, nil) on success, (nil, err) on error.
func (p *Pool) pinLoad(tag BufferTag) (*Slot, error) {
	// Find a victim.
	victimIdx, wasDirty, oldTag, err := p.claimVictim()
	if err != nil {
		return nil, err
	}
	s := &p.slots[victimIdx]

	// Evict the old contents.
	if err := p.evictVictim(victimIdx, wasDirty, oldTag); err != nil {
		p.releaseVictimSlot(victimIdx)
		return nil, fmt.Errorf("flush victim: %w", err)
	}

	// Re-check after eviction in case another goroutine already loaded this tag.
	if existingIdx, existingGen := p.bm.Lookup(tag); existingIdx >= 0 {
		if existing := p.tryPinSlot(existingIdx, existingGen); existing != nil {
			p.releaseVictimSlot(victimIdx)
			return existing, nil
		}
	}

	// Publish tag in bufmap with ioInflight set. Any concurrent Lookup will
	// see the slot and wait in the "stateIO" branch above.
	gen := stateGen(s.state.Load())
	s.tag = tag
	if !p.bm.Insert(tag, int32(victimIdx), gen) {
		// Another goroutine (in a concurrent PinNew?) published this tag.
		s.tag = BufferTag{}
		p.releaseVictimSlot(victimIdx)
		return nil, nil // caller retries
	}

	// Release pinMu during the disk read so other goroutines can proceed.
	p.pinMu.Unlock()
	s.contentMu.Lock()
	if p.OnPinWait != nil {
		p.OnPinWait()
	}
	ioErr := p.mgr.ReadBlock(tag.Rel, tag.Block, s.page)
	if p.OnPinDone != nil {
		p.OnPinDone()
	}
	s.contentMu.Unlock()
	p.pinMu.Lock()

	if ioErr != nil {
		p.bm.Delete(tag, int32(victimIdx))
		p.tombstones.Add(1)
		s.tag = BufferTag{}
		p.releaseVictimSlot(victimIdx) // also broadcasts
		return nil, ioErr
	}

	// Transition to valid+pinned. Under pinMu nobody else can modify this slot.
	newSt := slotValidBit | uint64(1) | (uint64(1) << slotUsageShift) | (uint64(gen) << slotGenShift)
	s.state.Store(newSt)
	p.pinCond.Broadcast() // wake goroutines waiting on this IO
	return s, nil
}

// Capacity returns the total number of buffer slots in the pool.
func (p *Pool) Capacity() int { return len(p.slots) }

// TryPin returns the pool slot for tag if it is already cached.
// Returns (nil, false) when the tag is not in the pool.
func (p *Pool) TryPin(tag BufferTag) (*Slot, bool) {
	slotIdx, gen := p.bm.Lookup(tag)
	if slotIdx < 0 {
		return nil, false
	}
	s := &p.slots[slotIdx]
	for {
		old := s.state.Load()
		if !stateValid(old) || stateIO(old) {
			return nil, false
		}
		if stateGen(old) != gen {
			return nil, false
		}
		pinCount := statePin(old)
		if pinCount >= maxPinCount {
			return nil, false
		}
		usage := stateUsage(old)
		if usage < maxUsageCount {
			usage++
		}
		newSt := (old &^ (slotPinMask | slotUsageMask)) | (pinCount + 1) | (usage << slotUsageShift)
		if s.state.CompareAndSwap(old, newSt) {
			return s, true
		}
	}
}

// Unpin decrements the slot's pin count via CAS.
func (p *Pool) Unpin(s *Slot) {
	for {
		old := s.state.Load()
		pinCount := statePin(old)
		if pinCount == 0 {
			panic(fmt.Sprintf("unpin underflow on tag %v", s.tag))
		}
		newSt := (old &^ slotPinMask) | (pinCount - 1)
		if s.state.CompareAndSwap(old, newSt) {
			return
		}
	}
}

// MarkDirty flags the slot as having been mutated.
func (p *Pool) MarkDirty(s *Slot) {
	p.maybeEmitFPI(s)
	// Atomically set dirty bit.
	for {
		old := s.state.Load()
		if old&slotDirtyBit != 0 {
			return // already dirty
		}
		if s.state.CompareAndSwap(old, old|slotDirtyBit) {
			return
		}
	}
}

// MarkDirtyWithLSN records an explicit page LSN and marks the slot dirty.
func (p *Pool) MarkDirtyWithLSN(s *Slot, lsn LSN) {
	s.contentMu.Lock()
	MustHeader(s.page).SetLSN(lsn)
	s.contentMu.Unlock()
	p.markDirtyWithLSNCommon(s)
}

// MarkDirtyWithLSNLocked is the variant for callers that already hold s.contentMu.
func (p *Pool) MarkDirtyWithLSNLocked(s *Slot, lsn LSN) {
	MustHeader(s.page).SetLSN(lsn)
	p.markDirtyWithLSNCommon(s)
}

func (p *Pool) markDirtyWithLSNCommon(s *Slot) {
	// Set dirty bit and mark fpiSinceCheckpoint.
	for {
		old := s.state.Load()
		if old&slotDirtyBit != 0 {
			// Already dirty; still need to update fpiSinceCheckpoint.
			break
		}
		if s.state.CompareAndSwap(old, old|slotDirtyBit) {
			break
		}
	}
	s.fpiSinceCheckpoint.Store(true)
}

// MarkDirtyForceFPI emits a fresh full-page image, overriding any stale FPI.
func (p *Pool) MarkDirtyForceFPI(s *Slot) {
	if p.logFPI == nil || !p.fullPageWrites.Load() {
		for {
			old := s.state.Load()
			if old&slotDirtyBit != 0 {
				return
			}
			if s.state.CompareAndSwap(old, old|slotDirtyBit) {
				return
			}
		}
	}
	tag := s.tag

	pageCopy := make(Page, BlockSize)
	copy(pageCopy, s.page)
	lsn, err := p.logFPI(tag.Rel, tag.Block, pageCopy)
	if err != nil {
		for {
			old := s.state.Load()
			if old&slotDirtyBit != 0 {
				return
			}
			if s.state.CompareAndSwap(old, old|slotDirtyBit) {
				return
			}
		}
	}
	MustHeader(s.page).SetLSN(lsn)
	for {
		old := s.state.Load()
		if old&slotDirtyBit != 0 {
			break
		}
		if s.state.CompareAndSwap(old, old|slotDirtyBit) {
			break
		}
	}
	s.fpiSinceCheckpoint.Store(true)
}

// MarkDirtyChangeRecord is the change-record-aware variant of MarkDirty.
func (p *Pool) MarkDirtyChangeRecord(s *Slot, emitter func() (LSN, error)) error {
	needFPI := !s.fpiSinceCheckpoint.Load()
	tag := s.tag

	if needFPI {
		if p.logFPI != nil && p.fullPageWrites.Load() {
			pageCopy := make(Page, BlockSize)
			copy(pageCopy, s.page)
			lsn, err := p.logFPI(tag.Rel, tag.Block, pageCopy)
			if err != nil {
				return err
			}
			MustHeader(s.page).SetLSN(lsn)
		} else if emitter != nil {
			lsn, err := emitter()
			if err != nil {
				return err
			}
			MustHeader(s.page).SetLSN(lsn)
		}
	} else {
		if emitter == nil {
			return fmt.Errorf("MarkDirtyChangeRecord: emitter required after first dirty")
		}
		lsn, err := emitter()
		if err != nil {
			return err
		}
		MustHeader(s.page).SetLSN(lsn)
	}

	for {
		old := s.state.Load()
		if old&slotDirtyBit != 0 {
			break
		}
		if s.state.CompareAndSwap(old, old|slotDirtyBit) {
			break
		}
	}
	s.fpiSinceCheckpoint.Store(true)
	return nil
}

// MarkDirtyLogicalChange is the logical-decoding-aware variant of MarkDirtyChangeRecord.
func (p *Pool) MarkDirtyLogicalChange(s *Slot, emitter func() (LSN, error)) error {
	if emitter == nil {
		return fmt.Errorf("MarkDirtyLogicalChange: emitter required")
	}
	needFPI := !s.fpiSinceCheckpoint.Load()
	tag := s.tag

	lsn, err := emitter()
	if err != nil {
		return err
	}
	MustHeader(s.page).SetLSN(lsn)

	if needFPI && p.logFPI != nil && p.fullPageWrites.Load() {
		pageCopy := make(Page, BlockSize)
		copy(pageCopy, s.page)
		fpiLSN, fpiErr := p.logFPI(tag.Rel, tag.Block, pageCopy)
		if fpiErr != nil {
			return fpiErr
		}
		MustHeader(s.page).SetLSN(fpiLSN)
	}

	for {
		old := s.state.Load()
		if old&slotDirtyBit != 0 {
			break
		}
		if s.state.CompareAndSwap(old, old|slotDirtyBit) {
			break
		}
	}
	s.fpiSinceCheckpoint.Store(true)
	return nil
}

// maybeEmitFPI runs the FPI side-effect of MarkDirty for the first
// mutation in each checkpoint epoch.
func (p *Pool) maybeEmitFPI(s *Slot) {
	if p.logFPI == nil || !p.fullPageWrites.Load() {
		return
	}
	if s.fpiSinceCheckpoint.Load() {
		return
	}

	tag := s.tag

	pageCopy := make(Page, BlockSize)
	copy(pageCopy, s.page)
	lsn, err := p.logFPI(tag.Rel, tag.Block, pageCopy)
	if err != nil {
		p.logger.Warn("full-page-image WAL emit failed",
			"rel", tag.Rel, "block", tag.Block, "err", err)
		return
	}
	MustHeader(s.page).SetLSN(lsn)
	s.fpiSinceCheckpoint.Store(true)
}

// FlushAll writes every dirty slot and clears the dirty bit.
func (p *Pool) FlushAll() error {
	if p.OnFlushAll != nil {
		p.OnFlushAll()
	}
	return p.FlushAllPaced(nil)
}

// FlushAllPaced writes every dirty slot and invokes pacer after each batch.
func (p *Pool) FlushAllPaced(pacer func(progress float64) error) error {
	if p.OnFlushAll != nil {
		p.OnFlushAll()
	}
	type pending struct {
		idx int
		tag BufferTag
	}
	var todo []pending
	for i := range p.slots {
		st := p.slots[i].state.Load()
		if stateValid(st) && stateDirty(st) {
			todo = append(todo, pending{i, p.slots[i].tag})
		}
	}

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
		batchSlots := make([]*Slot, 0, end-start)
		batchTags := make([]BufferTag, 0, end-start)
		for _, t := range todo[start:end] {
			batchSlots = append(batchSlots, &p.slots[t.idx])
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

// flushBatch flushes a batch of dirty slots.
func (p *Pool) flushBatch(slots []*Slot, tags []BufferTag) error {
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

	if p.wal != nil && maxLSN != 0 {
		if err := p.wal.FlushUpTo(uint64(maxLSN)); err != nil {
			return fmt.Errorf("flush wal up to %d: %w", maxLSN, err)
		}
	}

	handles := make([]AIOHandle, len(slots))
	for i, s := range slots {
		h, err := p.mgr.WriteBlockAIO(tags[i].Rel, tags[i].Block, s.page)
		if err != nil {
			for j := 0; j < i; j++ {
				_, _ = handles[j].Wait()
			}
			return err
		}
		handles[i] = h
	}

	var firstErr error
	for _, h := range handles {
		if _, err := h.Wait(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}

	// Clear dirty bits where tag still matches.
	for i, s := range slots {
		if s.tag == tags[i] {
			for {
				old := s.state.Load()
				if old&slotDirtyBit == 0 {
					break
				}
				if s.state.CompareAndSwap(old, old&^slotDirtyBit) {
					break
				}
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


// maybeCompact triggers a tombstone compaction if the tombstone ratio exceeds 25%.
func (p *Pool) maybeCompact() {
	n := int64(len(p.slots))
	ts := p.tombstones.Load()
	if ts*4 < n {
		return
	}
	if !p.compactMu.TryLock() {
		return // another goroutine is already compacting
	}
	defer p.compactMu.Unlock()
	p.bm.compact()
	p.tombstones.Store(0)
}

// DirtyVictimRate returns the fraction of foreground evictions that
// encountered a dirty page.
func (p *Pool) DirtyVictimRate() float64 {
	total := p.totalVictimCount.Load()
	if total == 0 {
		return 0
	}
	return float64(p.dirtyVictimCount.Load()) / float64(total)
}

// ResetVictimStats resets the dirty-victim counters to zero.
func (p *Pool) ResetVictimStats() {
	p.dirtyVictimCount.Store(0)
	p.totalVictimCount.Store(0)
}

// WriteDirtyPages proactively flushes up to maxPages dirty slots.
// Called only by the bgwriter goroutine.
func (p *Pool) WriteDirtyPages(maxPages int) int {
	if maxPages <= 0 {
		return 0
	}

	type victim struct {
		idx int
		tag BufferTag
	}
	n := len(p.slots)

	p.bgwriterMu.Lock()
	start := p.bgwriterHand
	p.bgwriterHand = (start + n) % n
	p.bgwriterMu.Unlock()

	victims := make([]victim, 0, maxPages)
	for i := 0; i < n && len(victims) < maxPages; i++ {
		idx := (start + i) % n
		s := &p.slots[idx]
		st := s.state.Load()
		if stateValid(st) && stateDirty(st) && statePin(st) == 0 {
			victims = append(victims, victim{idx: idx, tag: s.tag})
		}
	}

	written := 0
	for _, v := range victims {
		s := &p.slots[v.idx]
		s.contentMu.RLock()

		// Re-check under RLock that the slot is still dirty with same tag.
		st := s.state.Load()
		stillValid := stateValid(st) && s.tag == v.tag && stateDirty(st) && statePin(st) == 0

		if stillValid {
			if err := p.flushSlot(v.tag, s.page); err == nil {
				if s.tag == v.tag {
					for {
						old := s.state.Load()
						if old&slotDirtyBit == 0 {
							break
						}
						if s.state.CompareAndSwap(old, old&^slotDirtyBit) {
							break
						}
					}
					written++
				}
			}
		}
		s.contentMu.RUnlock()
	}
	return written
}

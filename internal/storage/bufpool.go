package storage

import (
	"errors"
	"fmt"
	"sync"
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

	// contentMu guards the Page bytes for read/write. The pool mutex
	// is dropped before we acquire this so I/O doesn't block lookups.
	contentMu sync.RWMutex
}

// Page returns the Page-typed view of the slot's storage. The caller
// must hold a pin on the slot.
func (s *Slot) Page() Page { return s.page }

// Tag returns the (rel, fork, block) the slot currently holds.
func (s *Slot) Tag() BufferTag { return s.tag }

// Pool is the buffer manager. It is goroutine-safe.
type Pool struct {
	mgr   *Manager
	arena *arena
	slots []*Slot
	wal   WALFlusher

	// poolMu guards byTag, slots[*].tag/valid/dirty, slots[*].pinCount,
	// slots[*].usageCount, and clockHand. It does NOT guard the page
	// bytes — those are guarded by Slot.contentMu.
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
}

// WALFlusher is the minimal WAL contract the buffer pool needs to
// enforce write-ahead ordering.
type WALFlusher interface {
	FlushUpTo(lsn uint64) error
}

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
	p := &Pool{
		mgr:   mgr,
		arena: a,
		slots: make([]*Slot, cfg.Slots),
		byTag: make(map[BufferTag]int, cfg.Slots),
		wal:   cfg.WAL,
	}
	for i := range p.slots {
		p.slots[i] = &Slot{page: a.slot(i)}
	}
	return p, nil
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
	// Provisionally bind the new tag so concurrent Pin of the same
	// tag finds the slot rather than picking another victim. We mark
	// it not-valid until the disk read completes.
	s.tag = tag
	s.pinCount = 1
	s.usageCount = 1
	p.byTag[tag] = slotIdx
	p.poolMu.Unlock()

	s.contentMu.Lock()
	err = p.mgr.ReadBlock(tag.Rel, tag.Block, s.page)
	s.contentMu.Unlock()
	if err != nil {
		// Roll back the tentative binding.
		p.poolMu.Lock()
		delete(p.byTag, tag)
		s.tag = BufferTag{}
		s.pinCount = 0
		s.valid = false
		p.poolMu.Unlock()
		return nil, err
	}
	p.poolMu.Lock()
	s.valid = true
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
// a pin.
func (p *Pool) MarkDirty(s *Slot) {
	p.poolMu.Lock()
	defer p.poolMu.Unlock()
	s.dirty = true
}

// MarkDirtyWithLSN records a page LSN and marks the slot dirty.
// Callers should use this when a page mutation is backed by WAL.
func (p *Pool) MarkDirtyWithLSN(s *Slot, lsn LSN) {
	s.contentMu.Lock()
	MustHeader(s.page).SetLSN(lsn)
	s.contentMu.Unlock()
	p.poolMu.Lock()
	s.dirty = true
	p.poolMu.Unlock()
}

// FlushAll writes every dirty slot through smgr and clears the dirty
// bit. Used by the checkpointer (when it lands).
func (p *Pool) FlushAll() error {
	// Snapshot the dirty set under the pool mutex, then flush each
	// slot under its own contentMu so we don't block lookups.
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

	for _, t := range todo {
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

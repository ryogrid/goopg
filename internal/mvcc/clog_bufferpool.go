package mvcc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// CLOG SLRU buffer-pool sizing constants, mirroring PostgreSQL
// (postgres/src/include/access/slru.h and
// postgres/src/backend/access/transam/clog.c).
const (
	// clogSLRUBankSize mirrors PG's SLRU_BANK_SIZE (slru.h): the buffer-count
	// granularity the auto-tuner rounds to and the floor for an explicit
	// transaction_buffers setting.
	clogSLRUBankSize = 16
	// clogMaxAllowedBuffers mirrors SLRU_MAX_ALLOWED_BUFFERS = 1 GiB / BLCKSZ
	// (slru.h:24). The largest resident-page budget the GUC will honor.
	clogMaxAllowedBuffers = (1024 * 1024 * 1024) / storage.BlockSize
)

// EffectiveCLOGBuffers resolves the resident-page budget (number of BLCKSZ
// CLOG pages held in memory at once) from the transaction_buffers GUC value,
// faithfully mirroring clog.c:CLOGShmemBuffers + slru.c:SimpleLruAutotuneBuffers:
//
//   - transactionBuffers == 0 → auto-tune: sharedBuffers/512, floored at one
//     SLRU bank (16), capped at 1024, rounded down to a bank multiple.
//   - transactionBuffers != 0 → Min(Max(16, transactionBuffers), 1GiB/BLCKSZ).
//
// transactionBuffers is the raw GUC integer (a count of BLCKSZ buffers; PG
// stores it with GUC_UNIT_BLOCKS where one block == one CLOG buffer, so the raw
// value is identical). sharedBuffers is the shared_buffers page count used only
// on the auto path; pass 0 to get the bank-size floor.
func EffectiveCLOGBuffers(transactionBuffers, sharedBuffers int) int {
	if transactionBuffers == 0 {
		const autoMax = 1024
		n := sharedBuffers / 512
		n -= n % clogSLRUBankSize
		n = max(n, clogSLRUBankSize)
		n = min(n, autoMax-(autoMax%clogSLRUBankSize))
		return n
	}
	n := max(transactionBuffers, 16)
	n = min(n, clogMaxAllowedBuffers)
	return n
}

// laneFromStatus maps a TxnStatus to its 2-bit on-disk SLRU lane value, or
// (0, false) if the status has no terminal lane (TxnStatusUnknown is the
// in-progress lane 0x00 and is not a storable terminal status here).
func laneFromStatus(status TxnStatus) (byte, bool) {
	switch status {
	case TxnStatusCommitted:
		return pgClogStatusCommitted, true
	case TxnStatusAborted:
		return pgClogStatusAborted, true
	case TxnStatusSubCommitted:
		return pgClogStatusSubCommitted, true
	default:
		return 0, false
	}
}

// statusFromLane maps a 2-bit SLRU lane value back to a TxnStatus. Mirrors the
// decode in loadFromSLRU so the two stay in lock-step (sibling paths).
func statusFromLane(lane byte) TxnStatus {
	switch lane & 0x3 {
	case pgClogStatusCommitted:
		return TxnStatusCommitted
	case pgClogStatusAborted:
		return TxnStatusAborted
	case pgClogStatusSubCommitted:
		return TxnStatusSubCommitted
	default:
		return TxnStatusUnknown
	}
}

// clogPageSlot is one resident buffer holding a single BLCKSZ CLOG page (the
// 2-bit-packed SLRU representation: clogXactsPerPage XIDs at clogBitsPerXact
// bits each). Mirrors one slot of PG's SimpleLruCtl shared buffer array.
type clogPageSlot struct {
	pageNo   int64  // SLRU page number = xid / clogXactsPerPage
	data     []byte // BLCKSZ bytes; nil until first use
	valid    bool   // false for an empty/never-faulted slot
	dirty    bool   // page modified since last writeback
	lastUsed uint64 // recency stamp for LRU victim selection
}

// clogBufferPool is a bounded LRU page cache over the 2-bit-packed CLOG SLRU
// representation, backed by the pg_xact/ segment files under dir. It is the
// memory-bounded replacement for CLog's fully-resident per-bank byte slices
// (gap G6): instead of holding one byte per XID for every XID ever assigned, it
// keeps at most nslots BLCKSZ pages resident (4 XIDs/byte ⇒ 16× denser than the
// legacy store) and faults pages in/out on demand, mirroring PG's SimpleLru
// buffer pool sized by transaction_buffers.
//
// This is the standalone, fully-tested component (M0117-0006 Part A). Wiring it
// into CLog.GetStatus/setStatus (replacing the banks) is the separately-gated
// Part B — see docs/design/0117-0006-clog-slru-buffer-pool.md.
//
// Concurrency: all state is guarded by a single mutex. Reads fault a page in
// (possibly evicting another), so GetStatus is not lock-free; this matches PG,
// where SimpleLruReadPage takes the SLRU bank lock. Bootstrap/frozen-XID
// short-circuiting (xid < FirstNormalTransactionID) is NOT done here — that is
// the CLog layer's job; the pool is a pure 2-bit store.
type clogBufferPool struct {
	mu      sync.Mutex
	dir     string // pg_xact/ segment directory (the on-disk backing store)
	nslots  int    // resident-page budget (EffectiveCLOGBuffers)
	slots   []clogPageSlot
	pageMap map[int64]int // pageNo → index into slots
	clock   uint64        // monotonic recency counter
}

// newCLOGBufferPool creates a pool over the segment directory dir holding at
// most nbuffers resident pages. nbuffers < 1 is clamped to 1.
func newCLOGBufferPool(dir string, nbuffers int) *clogBufferPool {
	if nbuffers < 1 {
		nbuffers = 1
	}
	return &clogBufferPool{
		dir:     dir,
		nslots:  nbuffers,
		slots:   make([]clogPageSlot, 0, nbuffers),
		pageMap: make(map[int64]int, nbuffers),
	}
}

// segPathForPage returns the segment file path and the byte offset of pageNo
// within that file.
func (p *clogBufferPool) segPathForPage(pageNo int64) (string, int64) {
	segNo := pageNo / slruPagesPerSegment
	pageInSeg := pageNo % slruPagesPerSegment
	return filepath.Join(p.dir, fmt.Sprintf("%04X", segNo)), pageInSeg * int64(storage.BlockSize)
}

// readPageFromDisk fills buf (len == BLCKSZ) with pageNo's bytes from its
// segment file. A missing segment file or a read that runs past EOF leaves the
// uncovered tail zero-filled (zero == in-progress lane), mirroring PG faulting
// a never-written page in as all-zero.
func (p *clogBufferPool) readPageFromDisk(pageNo int64, buf []byte) error {
	for i := range buf {
		buf[i] = 0
	}
	segPath, off := p.segPathForPage(pageNo)
	f, err := os.Open(segPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // segment not yet created → all in-progress
		}
		return fmt.Errorf("clog bufpool: open %q: %w", segPath, err)
	}
	defer f.Close()
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("clog bufpool: read %q@%d: %w", segPath, off, err)
	}
	return nil
}

// evictVictimLocked returns the index of the least-recently-used valid slot.
// Caller holds p.mu and has already confirmed every slot is occupied.
func (p *clogBufferPool) evictVictimLocked() int {
	victim := 0
	best := ^uint64(0)
	for i := range p.slots {
		if p.slots[i].valid && p.slots[i].lastUsed < best {
			best = p.slots[i].lastUsed
			victim = i
		}
	}
	return victim
}

// pinPageLocked returns the slot index holding pageNo, faulting it in (and
// evicting + writing back the LRU page if the pool is full) as needed. Caller
// holds p.mu. On a fault-in error the offending slot is left invalid.
func (p *clogBufferPool) pinPageLocked(pageNo int64) (int, error) {
	if idx, ok := p.pageMap[pageNo]; ok {
		p.clock++
		p.slots[idx].lastUsed = p.clock
		return idx, nil
	}

	var idx int
	if len(p.slots) < p.nslots {
		p.slots = append(p.slots, clogPageSlot{})
		idx = len(p.slots) - 1
	} else {
		idx = p.evictVictimLocked()
		if p.slots[idx].dirty {
			if err := p.writePageToDisk(p.slots[idx].pageNo, p.slots[idx].data); err != nil {
				return -1, err
			}
		}
		delete(p.pageMap, p.slots[idx].pageNo)
		p.slots[idx].valid = false
	}

	s := &p.slots[idx]
	if s.data == nil {
		s.data = make([]byte, storage.BlockSize)
	}
	if err := p.readPageFromDisk(pageNo, s.data); err != nil {
		s.valid = false
		return -1, err
	}
	s.pageNo = pageNo
	s.valid = true
	s.dirty = false
	p.clock++
	s.lastUsed = p.clock
	p.pageMap[pageNo] = idx
	return idx, nil
}

// writePageToDisk writes a single page back to its segment file (extending the
// file with a zero gap if the page sits past the current EOF) and fsyncs it.
// Used on eviction of a dirty page; flushDirty batches the fsync per segment
// instead.
func (p *clogBufferPool) writePageToDisk(pageNo int64, data []byte) error {
	segPath, off := p.segPathForPage(pageNo)
	f, err := os.OpenFile(segPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("clog bufpool: open %q: %w", segPath, err)
	}
	if _, err := f.WriteAt(data, off); err != nil {
		f.Close()
		return fmt.Errorf("clog bufpool: write %q@%d: %w", segPath, off, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("clog bufpool: sync %q: %w", segPath, err)
	}
	return f.Close()
}

// getStatus returns the 2-bit status recorded for xid, faulting its page in if
// not resident. xid < FirstNormalTransactionID is the caller's responsibility
// to short-circuit; here it just reads the (typically zero) lane.
func (p *clogBufferPool) getStatus(xid storage.TransactionID) (TxnStatus, error) {
	pageNo := int64(uint64(xid) / clogXactsPerPage)
	xidInPage := uint64(xid) % clogXactsPerPage
	byteInPage := xidInPage / clogXactsPerByte
	shift := uint((xidInPage % clogXactsPerByte) * clogBitsPerXact)

	p.mu.Lock()
	defer p.mu.Unlock()
	idx, err := p.pinPageLocked(pageNo)
	if err != nil {
		return TxnStatusUnknown, err
	}
	lane := (p.slots[idx].data[byteInPage] >> shift) & 0x3
	return statusFromLane(lane), nil
}

// setStatus records status for xid using PG's clear-then-set lane update
// (TransactionIdSetStatusBit: the 2-bit field is masked off and rewritten, so a
// lane can move between any two values, unlike the legacy OR-only mirror). The
// page is marked dirty for the next flushDirty/eviction. Returns whether the
// stored lane actually changed (false ⇒ idempotent no-op). status must be a
// terminal status (Committed/Aborted/SubCommitted).
func (p *clogBufferPool) setStatus(xid storage.TransactionID, status TxnStatus) (bool, error) {
	lane, ok := laneFromStatus(status)
	if !ok {
		return false, fmt.Errorf("clog bufpool: status %d has no storable lane", status)
	}
	pageNo := int64(uint64(xid) / clogXactsPerPage)
	xidInPage := uint64(xid) % clogXactsPerPage
	byteInPage := xidInPage / clogXactsPerByte
	shift := uint((xidInPage % clogXactsPerByte) * clogBitsPerXact)

	p.mu.Lock()
	defer p.mu.Unlock()
	idx, err := p.pinPageLocked(pageNo)
	if err != nil {
		return false, err
	}
	cur := (p.slots[idx].data[byteInPage] >> shift) & 0x3
	if cur == lane {
		return false, nil
	}
	p.slots[idx].data[byteInPage] = (p.slots[idx].data[byteInPage] &^ (0x3 << shift)) | (lane << shift)
	p.slots[idx].dirty = true
	return true, nil
}

// flushDirty writes every dirty resident page back to its segment file and
// fsyncs each touched segment exactly once (not once per page), matching the
// group-commit "one fsync per segment" goal (M0117-0005). Pages stay resident
// and are marked clean. The pool mutex is held throughout.
func (p *clogBufferPool) flushDirty() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	bySeg := make(map[int64][]int)
	for i := range p.slots {
		if p.slots[i].valid && p.slots[i].dirty {
			segNo := p.slots[i].pageNo / slruPagesPerSegment
			bySeg[segNo] = append(bySeg[segNo], i)
		}
	}
	for segNo, idxs := range bySeg {
		segPath := filepath.Join(p.dir, fmt.Sprintf("%04X", segNo))
		f, err := os.OpenFile(segPath, os.O_RDWR|os.O_CREATE, 0600)
		if err != nil {
			return fmt.Errorf("clog bufpool: open %q: %w", segPath, err)
		}
		for _, i := range idxs {
			off := (p.slots[i].pageNo % slruPagesPerSegment) * int64(storage.BlockSize)
			if _, err := f.WriteAt(p.slots[i].data, off); err != nil {
				f.Close()
				return fmt.Errorf("clog bufpool: write %q@%d: %w", segPath, off, err)
			}
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return fmt.Errorf("clog bufpool: sync %q: %w", segPath, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("clog bufpool: close %q: %w", segPath, err)
		}
		for _, i := range idxs {
			p.slots[i].dirty = false
		}
	}
	return nil
}

// residentPages returns the number of pages currently held in the pool. For
// tests asserting the LRU bound is honored.
func (p *clogBufferPool) residentPages() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pageMap)
}

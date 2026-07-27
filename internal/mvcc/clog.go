package mvcc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/goopg/goopg/internal/storage"
)

// TxnStatus is the commit-log status for a single transaction ID.
type TxnStatus byte

const (
	// TxnStatusUnknown means no COMMIT or ROLLBACK record was written for this
	// XID. After a crash, any XID with this status is treated as aborted.
	TxnStatusUnknown TxnStatus = 0
	// TxnStatusCommitted records a successful COMMIT.
	TxnStatusCommitted TxnStatus = 1
	// TxnStatusAborted records a ROLLBACK or crash-implied abort.
	TxnStatusAborted TxnStatus = 2
	// TxnStatusSubCommitted records a subtransaction that has committed while its
	// parent (top-level) transaction is still in progress. It is NOT a terminal
	// answer: a reader must resolve it by consulting the parent's status (see
	// DidCommit), mirroring PG's TRANSACTION_STATUS_SUB_COMMITTED
	// (postgres/src/include/access/clog.h:30 and transam.c TransactionIdDidCommit).
	// The raw byte value (3) is chosen to equal the on-disk SLRU 2-bit lane
	// (0x03) so the flat-file and SLRU encodings agree. M0117-0004.
	TxnStatusSubCommitted TxnStatus = 3
)

// CLog is the commit log: it maps transaction IDs to their terminal status,
// backed by the PG-canonical `pg_xact/` SLRU segment files under
// <DataDir>/pg_xact (2 bits/XID, paged in/out via clogBufferPool).
//
// Thread-safe: GetStatus/setStatus contend only on the pool's internal lock
// (clog_bufferpool.go). slruDirMu guards only the slruDir field itself.
type CLog struct {
	slruDirMu sync.RWMutex
	slruDir   string // PG-canonical pg_xact/ SLRU directory; set once by EnablePGSLRUMirror

	// oldestClogXid is the lowest XID whose status is still retained. Below it,
	// status has been truncated away and callers MUST treat the XID as
	// committed/frozen (it is older than every relfrozenxid). Mirrors PG's
	// TransamVariables->oldestClogXid. Guarded by oldestMu so it can be read
	// without taking slruDirMu. Zero means "no truncation has occurred".
	oldestMu      sync.RWMutex
	oldestClogXid storage.TransactionID

	// truncateLogger, when non-nil, is invoked by TruncateCLOG to emit a
	// CLOG_TRUNCATE WAL record (G9). Installed by initdb.Open after recovery
	// completes; nil during recovery so replay does not re-append. nil-safe.
	truncateLogger func(oldestXid storage.TransactionID) error

	// --- M0117-0006: SLRU buffer pool is the sole in-memory store (Part B/C) ---

	// pool is the bounded LRU page cache (clog_bufferpool.go) that IS the CLOG
	// store: GetStatus/setStatus and the bulk callers (InitializeAsCommitted /
	// MarkUnknownAsAborted / HighestKnownXID / TruncateCLOG / IsEmpty) all route
	// through it. Created by EnablePGSLRUMirror, which MUST run before any of
	// those methods are called (production always does this immediately after
	// OpenCLog; see initdb.Open / initdb.bootstrapCLog). M0117-0006 Part C
	// removed the legacy fully-resident per-XID "banks" store this pool used to
	// coexist with — there is exactly one live store now, so there is no
	// dual-store hazard (M0117-0004) to reconcile. atomic.Pointer so the
	// startup store and concurrent commit-path loads are race-free (the pointer
	// is set once, before the server accepts connections).
	pool atomic.Pointer[clogBufferPool]

	// fsyncDisabled mirrors `fsync = off` for mirrorToSLRUUnlocked (the
	// pool carries its own copy — see SetFsyncDisabled). Test harnesses
	// only.
	fsyncDisabled atomic.Bool

	// clogBuffers is the resident-page budget for pool (the transaction_buffers
	// GUC value; 0 ⇒ auto-tune via EffectiveCLOGBuffers at creation time). Set
	// via SetCLOGBuffers before EnablePGSLRUMirror; a no-op afterwards.
	clogBuffers int
}

// SetTruncateLogger installs the WAL-writer hook that TruncateCLOG calls to
// emit a CLOG_TRUNCATE record (G9). nil-safe: with no logger, TruncateCLOG
// performs the truncation but writes no WAL record (used during recovery
// replay, where the record already exists in the WAL).
func (c *CLog) SetTruncateLogger(fn func(oldestXid storage.TransactionID) error) {
	c.oldestMu.Lock()
	c.truncateLogger = fn
	c.oldestMu.Unlock()
}

// OldestClogXid returns the lowest XID whose CLOG status is still retained.
// Returns 0 when no truncation has occurred. Callers consulting status for an
// XID below this value MUST treat it as committed/frozen (its status bytes are
// gone). Mirrors TransamVariables->oldestClogXid.
func (c *CLog) OldestClogXid() storage.TransactionID {
	c.oldestMu.RLock()
	defer c.oldestMu.RUnlock()
	return c.oldestClogXid
}

// AdvanceOldestClogXid monotonically advances oldestClogXid toward xid. It
// never moves backward (wraparound-safe via txnPrecedes). Mirrors PG
// varsup.c:AdvanceOldestClogXid.
func (c *CLog) AdvanceOldestClogXid(xid storage.TransactionID) {
	c.oldestMu.Lock()
	if c.oldestClogXid == 0 || txnPrecedes(c.oldestClogXid, xid) {
		c.oldestClogXid = xid
	}
	c.oldestMu.Unlock()
}

// txnPrecedes reports whether a is logically before b in modulo-2^32 XID space
// (the half older than b). Mirrors PG's TransactionIdPrecedes. XID 0 (Invalid)
// is handled by callers; here it is treated as the smallest value. Delegates to
// storage.XIDPrecedes so the modular-comparison formula has a single source of
// truth shared with catalog.DatFrozenXID / the checkpointer TruncateCLOGFn
// (M0117-0001); the two must not drift.
func txnPrecedes(a, b storage.TransactionID) bool {
	return storage.XIDPrecedes(a, b)
}

// CLOGPagePrecedes reports whether SLRU page1 logically precedes page2 in the
// wraparound-aware XID page space. Mirrors PG clog.c:CLOGPagePrecedes: it
// compares the first AND last XID of page1 against page2's first XID so a page
// straddling the wraparound boundary is ordered correctly.
func CLOGPagePrecedes(page1, page2 int64) bool {
	xid1 := storage.TransactionID(uint64(page1)*uint64(clogXactsPerPage)) + FirstNormalTransactionID + 1
	xid2 := storage.TransactionID(uint64(page2)*uint64(clogXactsPerPage)) + FirstNormalTransactionID + 1
	return txnPrecedes(xid1, xid2) &&
		txnPrecedes(xid1, xid2+clogXactsPerPage-1)
}

// OpenCLog constructs a CLog. path is accepted for call-site compatibility
// with the pre-M0117-0006-Part-C signature but is no longer read: the legacy
// flat-file store is retired, and the SLRU buffer pool created by the
// mandatory follow-up EnablePGSLRUMirror call is now the only in-memory/
// durable store (see clog.go's CLog doc comment and the pg_xact/ SLRU
// directory it manages).
func OpenCLog(path string) (*CLog, error) {
	return &CLog{}, nil
}

// IsEmpty reports whether the clog has no committed/aborted entries yet on
// disk — used by initdb.Open to detect the pre-M0030-0007 upgrade case. This
// MUST be a disk-truth check, not a process-local flag: it is called once at
// startup, immediately after EnablePGSLRUMirror and before any in-process
// SetCommitted/SetAborted call, so a flag reset by every process restart would
// misreport "empty" on every restart of a populated cluster and misroute into
// the upgrade path (InitializeAsCommitted, which stamps every Unknown XID
// Committed) instead of the correct crash-recovery MarkUnknownAsAborted sweep
// — silently resurrecting crashed/in-progress transactions as committed.
// highestSLRUXID already scans the on-disk SLRU segments for the highest XID
// carrying a non-Unknown lane, returning 0 iff none exists anywhere on disk.
func (c *CLog) IsEmpty() bool {
	return c.highestSLRUXID() == 0
}

// GetStatus returns the recorded status for xid. Returns TxnStatusUnknown if
// xid has no entry (transaction never finished or XID is out of range).
func (c *CLog) GetStatus(xid storage.TransactionID) TxnStatus {
	// The buffer pool is the sole live store (M0117-0006 Part C); an unwritten
	// lane faults in as all-zero (= in-progress = Unknown). Callers still
	// short-circuit xid < OldestClogXid()/FirstNormalTransactionID upstream.
	st, err := c.pool.Load().getStatus(xid)
	if err != nil {
		return TxnStatusUnknown
	}
	return st
}

// SetCommitted marks xid as committed and persists the change to disk.
func (c *CLog) SetCommitted(xid storage.TransactionID) error {
	return c.setStatus(xid, TxnStatusCommitted)
}

// SetCommittedWithLSN marks xid as committed and associates it with the
// commit record's LSN, so the async-commit write barrier
// (flushWALBeforeWriteLocked) flushes the WAL up to at least lsn before
// this XID's CLOG page can reach disk (M0117-0007 Part B; PG's
// TransactionIdAsyncCommitTree). Used by the synchronous_commit=off path in
// place of SetCommitted, which associates no LSN (lsn=0 is a barrier no-op).
func (c *CLog) SetCommittedWithLSN(xid storage.TransactionID, lsn uint64) error {
	return c.setStatusWithLSN(xid, TxnStatusCommitted, lsn)
}

// SetAborted marks xid as aborted and persists the change to disk.
func (c *CLog) SetAborted(xid storage.TransactionID) error {
	return c.setStatus(xid, TxnStatusAborted)
}

// SetSubCommitted records xid as a sub-committed subtransaction (PG's
// TRANSACTION_STATUS_SUB_COMMITTED, 0x03) in the SLRU pool. Caller contract:
// xid is a subtransaction that has committed while
// its parent top-level XID is still in progress — the caller is responsible
// for checking that condition; this method only records the lane. Resolve a
// sub-committed XID's true commit fate with DidCommit (which consults the
// parent link). M0117-0004.
func (c *CLog) SetSubCommitted(xid storage.TransactionID) error {
	return c.setStatus(xid, TxnStatusSubCommitted)
}

// DidCommit resolves whether xid is committed, mirroring PostgreSQL's
// transam.c TransactionIdDidCommit:
//
//   - TxnStatusCommitted              → true;
//   - TxnStatusAborted / Unknown      → false;
//   - TxnStatusSubCommitted           → recurse on the parent top-level XID.
//
// parentOf maps a sub-XID to its immediate parent (the M0117-0003 SubxactMap
// supplies this; SubxactMap.TopLevelXid or a parents lookup). A nil parentOf,
// or a parent that resolves to 0/the XID itself, yields false — matching PG's
// "no pg_subtrans entry for subcommitted XID" branch (which WARNs and returns
// false). Recursion is bounded by a visited set so a corrupt self/cyclic
// parent link cannot loop. M0117-0004.
func (c *CLog) DidCommit(xid storage.TransactionID, parentOf func(storage.TransactionID) storage.TransactionID) bool {
	visited := make(map[storage.TransactionID]struct{})
	for {
		if xid < FirstNormalTransactionID {
			// Bootstrap/frozen/invalid XIDs are treated as committed by PG's
			// TransactionLogFetch short-circuit (transam.c).
			return xid == BootstrapTransactionID || xid == FrozenTransactionID
		}
		if _, seen := visited[xid]; seen {
			return false // cyclic/corrupt parent chain
		}
		visited[xid] = struct{}{}
		switch c.GetStatus(xid) {
		case TxnStatusCommitted:
			return true
		case TxnStatusSubCommitted:
			if parentOf == nil {
				return false
			}
			parent := parentOf(xid)
			if parent == 0 || parent == xid {
				return false
			}
			xid = parent
			continue
		default: // Aborted or Unknown
			return false
		}
	}
}

// InitializeAsCommitted marks every XID in the range [1, highXID) as
// TxnStatusCommitted, leaving entries that are already non-zero unchanged.
// Called by Open when the clog file was absent (upgrade from a pre-clog
// cluster): all XIDs assigned before the clog existed are assumed committed.
func (c *CLog) InitializeAsCommitted(highXID storage.TransactionID) error {
	if highXID == 0 {
		return nil
	}
	p := c.pool.Load()
	// Stamp [FirstNormalTransactionID, highXID) committed where currently
	// in-progress, through the pool (the sole live store). Bootstrap/frozen
	// lanes stay zero. One flushDirty at the end batches the fsync per touched
	// segment.
	dirty := false
	for i := int(FirstNormalTransactionID); i < int(highXID); i++ {
		xid := storage.TransactionID(i)
		st, err := p.getStatus(xid)
		if err != nil {
			return err
		}
		if st == TxnStatusUnknown {
			if _, err := p.setStatus(xid, TxnStatusCommitted); err != nil {
				return err
			}
			dirty = true
		}
	}
	if !dirty {
		return nil
	}
	return p.flushDirty()
}

// MarkUnknownAsAborted marks every XID in the range [1, highXID) whose current
// status is TxnStatusUnknown as TxnStatusAborted, leaving Committed/Aborted
// entries unchanged. Called by Open() after WAL replay finishes to implement
// crash-recovery's "any xid not explicitly Committed is treated as Aborted"
// semantics (M0106-0011): a transaction that wrote heap rows but crashed
// before its commit/abort marker reached disk leaves its xid as Unknown in
// the local clog, and downstream visibility filters need an explicit Aborted
// stamp to exclude its rows. Mirrors PostgreSQL's recovery-time treatment of
// TRANSACTION_STATUS_IN_PROGRESS CLOG slots.
//
// CAUTION for basebackup-attached clusters: upstream xids that pre-date the
// attach are not present in our local clog and would be incorrectly marked
// Aborted by this sweep. Such clusters MUST call InitializeAsCommitted with
// the upstream cluster's nextXid BEFORE this sweep runs so the upstream
// range is already Committed.
func (c *CLog) MarkUnknownAsAborted(highXID storage.TransactionID) error {
	if highXID == 0 {
		return nil
	}
	// Sweep through the pool (the sole live store). Floor at the lowest XID
	// still covered by an on-disk SLRU segment — after a prior TruncateCLOG,
	// segment files entirely below the truncation horizon were unlinked, so
	// XIDs below the lowest surviving segment have been frozen/truncated
	// (presumed committed); re-stamping them Aborted here would recreate the
	// deleted segments full of Aborted lanes (undoing truncation, breaking
	// basebackup byte-equality) and could make the direct GetStatus-based
	// heap-load filter reject legitimately retained tuples. Leaving them
	// Unknown is strictly safer: snapshot visibility treats them committed via
	// xid < Xmin, and the heap-load filter rejects only an explicit Aborted.
	// When no truncation has occurred the floor is FirstNormalTransactionID and
	// behavior is unchanged (never below it, so bootstrap/frozen lanes stay
	// zero). (Review fix M1.)
	p := c.pool.Load()
	low := int(FirstNormalTransactionID)
	if floor := c.firstRetainedSLRUXID(); int(floor) > low {
		low = int(floor)
	}
	dirty := false
	for i := low; i < int(highXID); i++ {
		xid := storage.TransactionID(i)
		st, err := p.getStatus(xid)
		if err != nil {
			return err
		}
		if st == TxnStatusUnknown {
			if _, err := p.setStatus(xid, TxnStatusAborted); err != nil {
				return err
			}
			dirty = true
		}
	}
	if !dirty {
		return nil
	}
	return p.flushDirty()
}

// firstRetainedSLRUXID returns the lowest XID still covered by an on-disk
// pg_xact/ SLRU segment file. It returns FirstNormalTransactionID when the
// mirror is disabled, no segment files exist, or segment 0 is present (i.e. no
// truncation has occurred) — preserving pre-truncation behavior. After a
// TruncateCLOG unlinks low segments, it returns (lowestSeg * clogXactsPerSegment)
// so callers can avoid resurrecting truncated XIDs. Used by MarkUnknownAsAborted
// (review fix M1).
func (c *CLog) firstRetainedSLRUXID() storage.TransactionID {
	c.slruDirMu.RLock()
	dir := c.slruDir
	c.slruDirMu.RUnlock()
	if dir == "" {
		return FirstNormalTransactionID
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return FirstNormalTransactionID
	}
	minSeg := int64(-1)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) != 4 {
			continue
		}
		var segNo int64
		valid := true
		for _, ch := range name {
			segNo <<= 4
			switch {
			case ch >= '0' && ch <= '9':
				segNo |= int64(ch - '0')
			case ch >= 'A' && ch <= 'F':
				segNo |= int64(ch - 'A' + 10)
			case ch >= 'a' && ch <= 'f':
				segNo |= int64(ch - 'a' + 10)
			default:
				valid = false
			}
		}
		if !valid {
			continue
		}
		if minSeg < 0 || segNo < minSeg {
			minSeg = segNo
		}
	}
	if minSeg <= 0 {
		return FirstNormalTransactionID
	}
	return storage.TransactionID(uint64(minSeg) * clogXactsPerSegment)
}

// highestSLRUXID scans the on-disk pg_xact/ segment files for the highest XID
// that carries a terminal (committed/aborted/sub-committed) 2-bit lane. It is
// the pool-path replacement for HighestKnownXID's bank scan (M0117-0006 Part B):
// once the buffer pool is the live store the banks are vestigial, so the
// authoritative high-water mark lives in the SLRU. Returns 0 when the mirror is
// disabled, unreadable, or holds no terminal lane. Segments are scanned in
// descending order and each from its tail, so the first terminal lane found is
// the maximum.
func (c *CLog) highestSLRUXID() storage.TransactionID {
	c.slruDirMu.RLock()
	dir := c.slruDir
	c.slruDirMu.RUnlock()
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	segs := make([]int64, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) != 4 {
			continue
		}
		segNo, ok := parseSLRUSegName(name)
		if !ok {
			continue
		}
		segs = append(segs, segNo)
	}
	slices.Sort(segs)
	for i := len(segs) - 1; i >= 0; i-- {
		segNo := segs[i]
		data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%04X", segNo)))
		if err != nil {
			continue
		}
		for bi := len(data) - 1; bi >= 0; bi-- {
			b := data[bi]
			if b == 0 {
				continue
			}
			for lane := clogXactsPerByte - 1; lane >= 0; lane-- {
				if (b>>(uint(lane)*clogBitsPerXact))&0x3 == 0 {
					continue
				}
				pageInSeg := uint64(bi) / uint64(storage.BlockSize)
				xidInPageBase := (uint64(bi) % uint64(storage.BlockSize)) * uint64(clogXactsPerByte)
				xid := uint64(segNo)*uint64(clogXactsPerSegment) +
					pageInSeg*uint64(clogXactsPerPage) + xidInPageBase + uint64(lane)
				return storage.TransactionID(xid)
			}
		}
	}
	return 0
}

// parseSLRUSegName parses a 4-hex-digit pg_xact/ segment file name (e.g.
// "0000", "001A") into its segment number, mirroring the inline parsers in
// loadFromSLRU / firstRetainedSLRUXID / truncateSLRUSegments.
func parseSLRUSegName(name string) (int64, bool) {
	if len(name) != 4 {
		return 0, false
	}
	var segNo int64
	for _, ch := range name {
		segNo <<= 4
		switch {
		case ch >= '0' && ch <= '9':
			segNo |= int64(ch - '0')
		case ch >= 'A' && ch <= 'F':
			segNo |= int64(ch - 'A' + 10)
		case ch >= 'a' && ch <= 'f':
			segNo |= int64(ch - 'a' + 10)
		default:
			return 0, false
		}
	}
	return segNo, true
}

// setStatus updates data[xid] = status and rewrites the file, with no
// async-commit LSN association. Equivalent to setStatusWithLSN(xid, status, 0).
func (c *CLog) setStatus(xid storage.TransactionID, status TxnStatus) error {
	return c.setStatusWithLSN(xid, status, 0)
}

// setStatusWithLSN updates data[xid] = status and rewrites the file. When a
// PG SLRU directory has been wired (see EnablePGSLRUMirror), the matching
// 2-bit lane of <slruDir>/<segno>:<page>:<byte> is also updated so a PG
// standby reading the basebackup-shipped pg_xact/ via
// SimpleLruReadPage_ReadOnly observes the correct status. M0106-0010
// batched-44. lsn != 0 additionally raises the XID's CLOG page's
// async-commit group LSN (M0117-0007 Part B) so the write barrier flushes
// the WAL before that page can reach disk; lsn == 0 (goopg's
// InvalidXLogRecPtr) is a no-op on the group array, matching PG's recovery
// branch.
func (c *CLog) setStatusWithLSN(xid storage.TransactionID, status TxnStatus, lsn uint64) error {
	p := c.pool.Load()
	// Bootstrap/frozen XIDs keep their pg_xact/0000 lanes zero (PG's
	// TransactionLogFetch never writes them) so basebackup byte-equality holds.
	if xid < FirstNormalTransactionID {
		return nil
	}
	// Write the 2-bit lane (clear-then-set, PG-faithful) into the resident
	// page. An idempotent lane (already at this terminal value) skips the
	// dirty-mark bookkeeping below entirely.
	changed, err := p.setStatusWithLSN(xid, status, lsn)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	// C2-S4: NO stamp performs an eager durable write-back any more —
	// p.setStatusWithLSN above marked the page dirty (and, for lsn != 0,
	// raised its group LSN so the SLRU write barrier flushes the WAL first),
	// which is all GetStatus needs. The page reaches disk at whichever
	// flush point comes first: LRU eviction (pinPageLocked barrier-guards a
	// dirty victim) or the checkpointer's FlushAll (registered as a
	// wal.DirtyPageFlusher, so residency is bounded by checkpoint_timeout).
	// Startup replayCLogFromWAL reconstructs anything lost in a crash from
	// the durable commit/abort records (C2-S3). This mirrors PG exactly:
	// TransactionIdSetPageStatus sets bits under the bank lock with zero
	// I/O. The M0117-0005 group-commit machinery that amortized the old
	// eager fsync was deleted with it (C2 design D4).
	return nil
}

// FlushAll writes every dirty resident CLOG page back to its segment file,
// fsyncing each touched segment. It implements wal.DirtyPageFlusher (Go
// interfaces are structurally satisfied, so this package need not import
// wal) and is registered with the checkpointer alongside the heap buffer
// pool (M0117-0007 Part B continuation) so an async commit's deferred
// write-back above is bounded by checkpoint_timeout — without this, an
// all-async workload could leave CLOG pages dirty in memory indefinitely,
// bounded only by the resident-page eviction budget. A nil pool (called
// before EnablePGSLRUMirror) is a no-op rather than a panic, mirroring
// SetFlushWALHook's out-of-order-call contract.
func (c *CLog) FlushAll() error {
	p := c.pool.Load()
	if p == nil {
		return nil
	}
	return p.flushDirty()
}

// PG SLRU CLOG layout constants. PG18 packs 2 bits per XID into bytes ordered
// as 4 lanes (lane = xid % 4, shift = lane * 2). 8192 bytes per page * 4 XIDs
// per byte = 32768 XIDs per page; 32 pages per segment file (named %04X of
// segno). See postgres/src/backend/access/transam/clog.c and slru.h.
const (
	clogBitsPerXact     = 2
	clogXactsPerByte    = 4
	clogXactsPerPage    = storage.BlockSize * clogXactsPerByte // 32768 with BLCKSZ=8192
	slruPagesPerSegment = 32
	clogXactsPerSegment = clogXactsPerPage * slruPagesPerSegment // 1048576

	// PG XidStatus constants, must match TRANSACTION_STATUS_* in
	// postgres/src/include/access/clog.h.
	pgClogStatusInProgress   = 0x00
	pgClogStatusCommitted    = 0x01
	pgClogStatusAborted      = 0x02
	pgClogStatusSubCommitted = 0x03
)

// EnablePGSLRUMirror wires this CLog to also write each
// SetCommitted/SetAborted into a PG-canonical pg_xact/ SLRU segment file under
// dir. Creates the directory and the initial segment file (zeroed BLCKSZ
// page, mirroring PG's BootStrapCLOG → SimpleLruZeroPage(0)) if they don't
// exist, so a fresh PG standby attaching via basebackup can read the SLRU
// without trying to extend a missing first page. Idempotent. M0106-0010
// batched-44.
func (c *CLog) EnablePGSLRUMirror(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("clog slru: mkdir %q: %w", dir, err)
	}
	seg0 := filepath.Join(dir, "0000")
	if _, err := os.Stat(seg0); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(seg0, make([]byte, storage.BlockSize), 0600); err != nil {
			return fmt.Errorf("clog slru: write zero page %q: %w", seg0, err)
		}
	} else if err != nil {
		return fmt.Errorf("clog slru: stat %q: %w", seg0, err)
	}

	// Set slruDir before creating the pool so mirrorToSLRUUnlocked (still used
	// by the sibling-path equivalence test) can see it.
	c.slruDirMu.Lock()
	c.slruDir = dir
	c.slruDirMu.Unlock()

	// M0117-0006 Part C: the pool IS the store from the start — no legacy
	// banks/flat-file staging step or backfill round-trip. It lazily faults
	// pages in directly from dir on first read (already exercised by
	// TestCLOGBufferPoolLRUEviction/RoundTripAllLanes), so there is no separate
	// load step to run before it exists. (The flushMu that used to bracket
	// this store guarded the M0117-0005 group-commit leader, deleted in
	// C2-S4; production calls EnablePGSLRUMirror exactly once at startup,
	// before the server accepts connections, and pool is an
	// atomic.Pointer.)
	pool := newCLOGBufferPool(dir, EffectiveCLOGBuffers(c.clogBuffers, 0))
	// Carry an already-set fsync=off flag into the new pool so the call
	// order of SetFsyncDisabled vs EnablePGSLRUMirror doesn't matter.
	pool.fsyncDisabled.Store(c.fsyncDisabled.Load())
	c.pool.Store(pool)
	return nil
}

// SetCLOGBuffers sets the resident-page budget for the M0117-0006 Part B SLRU
// buffer pool (the transaction_buffers GUC value; 0 ⇒ auto-tune via
// EffectiveCLOGBuffers at pool-creation time). Must be called before
// EnablePGSLRUMirror; it is a no-op once the pool has been created.
func (c *CLog) SetCLOGBuffers(n int) {
	c.clogBuffers = n
}

// SetFlushWALHook wires the CLOG buffer pool's async-commit write barrier
// (M0117-0007 Part A) to fn, invoked with a dirty page's max group LSN
// immediately before that page is written back to disk. Must be called
// after EnablePGSLRUMirror (which creates the pool); a nil pool (called out
// of order) is a no-op rather than a panic. fn is typically
// wal.Writer.FlushUpTo — injected as a plain closure so this package stays
// free of a wal import.
func (c *CLog) SetFlushWALHook(fn func(lsn uint64) error) {
	if p := c.pool.Load(); p != nil {
		p.SetFlushWALHook(fn)
	}
}

// SetFsyncDisabled mirrors `fsync = off` for the CLOG store: page write-backs
// (eviction, checkpoint FlushAll) and the SLRU mirror write still happen, but
// their per-segment fsync is skipped. Like SetFlushWALHook, call after
// EnablePGSLRUMirror; a nil pool is a no-op for the pool half. Test harnesses
// only; see ci/design/test-gate-speedups/02.
func (c *CLog) SetFsyncDisabled(disabled bool) {
	c.fsyncDisabled.Store(disabled)
	if p := c.pool.Load(); p != nil {
		p.fsyncDisabled.Store(disabled)
	}
}

// HighestKnownXID returns the highest XID that has a committed or aborted
// status in the clog. Returns 0 if no terminal status is recorded. Used at
// startup to advance txnMgr.NextXID past all previously committed XIDs so
// new snapshots have a high enough Xmax to see pre-crash rows. (M0106-0013)
func (c *CLog) HighestKnownXID() storage.TransactionID {
	return c.highestSLRUXID()
}

// SLRUDir returns the PG-canonical pg_xact/ directory, or "" if the mirror is
// disabled. Intended for tests.
func (c *CLog) SLRUDir() string {
	c.slruDirMu.RLock()
	defer c.slruDirMu.RUnlock()
	return c.slruDir
}

// mirrorToSLRUUnlocked writes the 2-bit lane for xid into the matching
// pg_xact/<segno> segment file. Does not require any CLog-level lock; the
// caller is responsible for ensuring slruDir is set before calling. No-op if
// the mirror is disabled or status is not a terminal committed/aborted code.
// Extends the segment file in BLCKSZ-page units so SimpleLruReadPage_ReadOnly
// sees a complete page.
func (c *CLog) mirrorToSLRUUnlocked(xid storage.TransactionID, status TxnStatus) error {
	c.slruDirMu.RLock()
	dir := c.slruDir
	c.slruDirMu.RUnlock()

	if dir == "" {
		return nil
	}
	// PG's TransactionLogFetch short-circuits BootstrapTransactionId (1) and
	// FrozenTransactionId (2) — and the unused InvalidTransactionId (0) — as
	// COMMITTED without consulting the SLRU (see
	// postgres/src/backend/access/transam/transam.c). PG's own initdb leaves
	// the corresponding lanes in pg_xact/0000 as zero; we mirror that
	// invariant so basebackup byte-equality holds.
	if xid < FirstNormalTransactionID {
		return nil
	}
	var bits byte
	switch status {
	case TxnStatusCommitted:
		bits = pgClogStatusCommitted
	case TxnStatusAborted:
		bits = pgClogStatusAborted
	case TxnStatusSubCommitted:
		bits = pgClogStatusSubCommitted
	default:
		return nil
	}
	segNo := uint64(xid) / clogXactsPerSegment
	xidInSeg := uint64(xid) % clogXactsPerSegment
	pageInSeg := xidInSeg / clogXactsPerPage
	xidInPage := xidInSeg % clogXactsPerPage
	byteOffset := int64(pageInSeg)*int64(storage.BlockSize) + int64(xidInPage/clogXactsPerByte)
	bShift := uint((xidInPage % clogXactsPerByte) * clogBitsPerXact)

	name := fmt.Sprintf("%04X", segNo)
	segPath := filepath.Join(dir, name)
	f, err := os.OpenFile(segPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("clog slru: open %q: %w", segPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("clog slru: stat %q: %w", segPath, err)
	}
	minSize := (int64(pageInSeg) + 1) * int64(storage.BlockSize)
	if fi.Size() < minSize {
		if err := f.Truncate(minSize); err != nil {
			return fmt.Errorf("clog slru: extend %q: %w", segPath, err)
		}
	}
	var bBuf [1]byte
	if _, err := f.ReadAt(bBuf[:], byteOffset); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("clog slru: read %q@%d: %w", segPath, byteOffset, err)
	}
	// Strict OR mirrors PG's TransactionIdSetStatusBit: lanes only advance
	// from in-progress to terminal. We never need to clear bits.
	bBuf[0] |= bits << bShift
	if _, err := f.WriteAt(bBuf[:], byteOffset); err != nil {
		return fmt.Errorf("clog slru: write %q@%d: %w", segPath, byteOffset, err)
	}
	if c.fsyncDisabled.Load() {
		return nil
	}
	return f.Sync()
}

// TruncateCLOG removes CLOG status for every XID strictly older than the SLRU
// page containing oldestXid: the pg_xact/ SLRU segment files (and any resident
// pool pages caching them) that lie entirely below the cutoff page are
// dropped. The partial page containing oldestXid and everything newer are
// retained. Idempotent and wraparound-aware (page comparison uses
// CLOGPagePrecedes). Safe to call repeatedly with the same or an older XID.
//
// Ordering mirrors PG clog.c:TruncateCLOG
// (postgres/src/backend/access/transam/clog.c:1000):
//  1. compute the cutoff page = page(oldestXid);
//  2. if nothing below the cutoff exists, return (nothing to remove);
//  3. AdvanceOldestClogXid(oldestXid) BEFORE removing anything, so concurrent
//     status lookups never read a truncated-away slot;
//  4. emit the CLOG_TRUNCATE WAL record (so the record is durable ahead of the
//     physical removal and a standby learns the new valid xid);
//  5. remove the segment files (and drop any resident pool pages they backed).
//
// Truncation base note: goopg keeps the SLRU XID-indexed (no rebasing).
// Removing a segment file entirely below the cutoff makes GetStatus fault-in
// TxnStatusUnknown for those XIDs; visibility code MUST short-circuit any XID
// below OldestClogXid() as committed/frozen before consulting GetStatus. We
// only drop segments ENTIRELY below the cutoff XID, never the segment that
// straddles it, so retained XIDs keep their exact bytes.
func (c *CLog) TruncateCLOG(oldestXid storage.TransactionID) error {
	if oldestXid < FirstNormalTransactionID {
		return nil // never truncate the bootstrap/frozen range
	}

	// (1) Cutoff page = the SLRU page holding oldestXid. Everything on a page
	// strictly preceding this page is removable.
	cutoffPage := int64(uint64(oldestXid) / uint64(clogXactsPerPage))

	// (2) "Nothing to remove" guard. If oldestClogXid is already at or beyond
	// oldestXid, this is a no-op replay. Also skip when no on-disk segment or
	// in-memory bank lies entirely below the cutoff page.
	c.oldestMu.RLock()
	cur := c.oldestClogXid
	c.oldestMu.RUnlock()
	if cur != 0 && !txnPrecedes(cur, oldestXid) {
		return nil // already truncated at or past oldestXid
	}

	// (3) Advance the horizon BEFORE removing anything.
	c.AdvanceOldestClogXid(oldestXid)

	// (4) Emit the CLOG_TRUNCATE WAL record (nil during recovery replay).
	c.oldestMu.RLock()
	logger := c.truncateLogger
	c.oldestMu.RUnlock()
	if logger != nil {
		if err := logger(oldestXid); err != nil {
			return fmt.Errorf("clog: log truncate(%d): %w", oldestXid, err)
		}
	}

	// (5a) Remove SLRU segment files whose entire page range precedes the
	// cutoff page. A segment holds slruPagesPerSegment pages; its last page is
	// removable iff CLOGPagePrecedes(lastPageOfSeg, cutoffPage).
	if err := c.truncateSLRUSegments(cutoffPage); err != nil {
		return fmt.Errorf("clog: truncate slru segments: %w", err)
	}

	// (5b) Drop any resident pool pages that the segment removal above just
	// unlinked, WITHOUT writing them back (their backing file is gone). A
	// re-read below the OldestClogXid floor cannot legitimately occur (callers
	// short-circuit), but a stale resident page would otherwise mask the
	// truncation.
	c.pool.Load().invalidateBelow(cutoffPage)
	return nil
}

// truncateSLRUSegments unlinks pg_xact/ segment files whose highest page
// strictly precedes cutoffPage (wraparound-aware via CLOGPagePrecedes). The
// segment containing cutoffPage (and all newer segments) is kept. No-op when
// the SLRU mirror is disabled. Mirrors the file-removal half of PG's
// SimpleLruTruncate / SlruScanDirCbDeleteCutoff.
func (c *CLog) truncateSLRUSegments(cutoffPage int64) error {
	c.slruDirMu.RLock()
	dir := c.slruDir
	c.slruDirMu.RUnlock()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("clog slru: readdir %q: %w", dir, err)
	}
	const pagesPerSeg = int64(slruPagesPerSegment)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) != 4 {
			continue
		}
		var segNo int64
		valid := true
		for _, ch := range name {
			segNo <<= 4
			switch {
			case ch >= '0' && ch <= '9':
				segNo |= int64(ch - '0')
			case ch >= 'A' && ch <= 'F':
				segNo |= int64(ch - 'A' + 10)
			case ch >= 'a' && ch <= 'f':
				segNo |= int64(ch - 'a' + 10)
			default:
				valid = false
			}
		}
		if !valid {
			continue
		}
		// The highest page held by this segment.
		lastPageOfSeg := segNo*pagesPerSeg + (pagesPerSeg - 1)
		// Removable iff the segment's last page strictly precedes the cutoff
		// page — i.e. the entire segment is older than oldestXid's page.
		if CLOGPagePrecedes(lastPageOfSeg, cutoffPage) {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("clog slru: remove %q: %w", name, err)
			}
		}
	}
	return nil
}

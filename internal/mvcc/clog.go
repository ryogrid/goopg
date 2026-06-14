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
)

// xidsPerBank is the number of XIDs managed by a single clogBank. 128K XIDs
// per bank gives fine-grained locking: concurrent commits on different banks
// never contend on the same mutex.
const xidsPerBank = 128 * 1024

// clogBank holds the per-bank lock and XID status bytes.
type clogBank struct {
	mu   sync.RWMutex
	data []byte // len == xidsPerBank slots; grown on first write
}

// CLog is a flat-file persistent commit log that maps transaction IDs to their
// terminal status. One byte per XID; stored at <DataDir>/global/pg_xact.
//
// Thread-safe: GetStatus and setStatus contend only on the relevant bank's
// mutex. Concurrent commits hitting different XID banks proceed in parallel.
// banksMu is held (write) only when the banks slice itself must grow.
type CLog struct {
	path    string
	banks   []*clogBank  // indexed by xid/xidsPerBank; grows on demand
	banksMu sync.RWMutex // protects banks slice growth only
	slruDir string       // optional: PG-canonical pg_xact/ SLRU directory; empty = mirror disabled

	// oldestClogXid is the lowest XID whose status is still retained. Below it,
	// status has been truncated away and callers MUST treat the XID as
	// committed/frozen (it is older than every relfrozenxid). Mirrors PG's
	// TransamVariables->oldestClogXid. Guarded by oldestMu so it can be read
	// without taking banksMu. Zero means "no truncation has occurred".
	oldestMu      sync.RWMutex
	oldestClogXid storage.TransactionID

	// truncateLogger, when non-nil, is invoked by TruncateCLOG to emit a
	// CLOG_TRUNCATE WAL record (G9). Installed by initdb.Open after recovery
	// completes; nil during recovery so replay does not re-append. nil-safe.
	truncateLogger func(oldestXid storage.TransactionID) error
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

// bankIdx returns the bank index for xid.
func bankIdx(xid storage.TransactionID) int {
	return int(xid) / xidsPerBank
}

// byteIdx returns the byte offset within its bank for xid.
func byteIdx(xid storage.TransactionID) int {
	return int(xid) % xidsPerBank
}

// getOrCreateBank returns the bank for the given index, creating it (and any
// intermediate banks) if needed. Acquires banksMu.Lock() only when the slice
// must grow.
func (c *CLog) getOrCreateBank(idx int) *clogBank {
	c.banksMu.RLock()
	if idx < len(c.banks) && c.banks[idx] != nil {
		b := c.banks[idx]
		c.banksMu.RUnlock()
		return b
	}
	c.banksMu.RUnlock()

	c.banksMu.Lock()
	defer c.banksMu.Unlock()
	// Grow slice to hold idx.
	for len(c.banks) <= idx {
		c.banks = append(c.banks, nil)
	}
	if c.banks[idx] == nil {
		c.banks[idx] = &clogBank{}
	}
	return c.banks[idx]
}

// getBank returns the bank for the given index if it exists, or nil. Does not
// create a new bank. Used for read-only lookups.
func (c *CLog) getBank(idx int) *clogBank {
	c.banksMu.RLock()
	defer c.banksMu.RUnlock()
	if idx < len(c.banks) {
		return c.banks[idx]
	}
	return nil
}

// OpenCLog opens (or creates) the commit log at path. If the file exists its
// contents are loaded; if not, an empty in-memory CLog is returned. The clog
// file is created on the first SetCommitted / SetAborted call.
func OpenCLog(path string) (*CLog, error) {
	c := &CLog{path: path}
	raw, err := os.ReadFile(path)
	if err == nil {
		// Distribute loaded bytes to banks by XID range.
		c.distributeToBanks(raw)
		return c, nil
	}
	if os.IsNotExist(err) {
		return c, nil // fresh clog — file created on first write
	}
	return nil, fmt.Errorf("clog: read %q: %w", path, err)
}

// distributeToBanks populates banks from a flat byte slice (as read from disk).
// Each byte at index i represents XID i.
func (c *CLog) distributeToBanks(raw []byte) {
	if len(raw) == 0 {
		return
	}
	// Calculate how many banks are needed.
	lastXID := len(raw) - 1
	maxBankIdx := lastXID / xidsPerBank

	c.banksMu.Lock()
	for len(c.banks) <= maxBankIdx {
		c.banks = append(c.banks, nil)
	}
	c.banksMu.Unlock()

	for bi := 0; bi <= maxBankIdx; bi++ {
		start := bi * xidsPerBank
		end := start + xidsPerBank
		if end > len(raw) {
			end = len(raw)
		}
		chunk := make([]byte, end-start)
		copy(chunk, raw[start:end])

		c.banksMu.Lock()
		if c.banks[bi] == nil {
			c.banks[bi] = &clogBank{}
		}
		c.banks[bi].data = chunk
		c.banksMu.Unlock()
	}
}

// IsEmpty reports whether the clog has no entries yet (file was absent or the
// on-disk data is zero-length). Used by Open to detect the upgrade case.
func (c *CLog) IsEmpty() bool {
	c.banksMu.RLock()
	nBanks := len(c.banks)
	c.banksMu.RUnlock()
	if nBanks == 0 {
		return true
	}
	// Check if any bank has data.
	for bi := 0; bi < nBanks; bi++ {
		b := c.getBank(bi)
		if b == nil {
			continue
		}
		b.mu.RLock()
		n := len(b.data)
		b.mu.RUnlock()
		if n > 0 {
			return false
		}
	}
	return true
}

// GetStatus returns the recorded status for xid. Returns TxnStatusUnknown if
// xid has no entry (transaction never finished or XID is out of range).
func (c *CLog) GetStatus(xid storage.TransactionID) TxnStatus {
	bi := bankIdx(xid)
	byt := byteIdx(xid)
	b := c.getBank(bi)
	if b == nil {
		return TxnStatusUnknown
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if byt >= len(b.data) {
		return TxnStatusUnknown
	}
	return TxnStatus(b.data[byt])
}

// SetCommitted marks xid as committed and persists the change to disk.
func (c *CLog) SetCommitted(xid storage.TransactionID) error {
	return c.setStatus(xid, TxnStatusCommitted)
}

// SetAborted marks xid as aborted and persists the change to disk.
func (c *CLog) SetAborted(xid storage.TransactionID) error {
	return c.setStatus(xid, TxnStatusAborted)
}

// InitializeAsCommitted marks every XID in the range [1, highXID) as
// TxnStatusCommitted, leaving entries that are already non-zero unchanged.
// Called by Open when the clog file was absent (upgrade from a pre-clog
// cluster): all XIDs assigned before the clog existed are assumed committed.
func (c *CLog) InitializeAsCommitted(highXID storage.TransactionID) error {
	if highXID == 0 {
		return nil
	}
	top := int(highXID)
	// Mark [1, top) as committed per bank, preserving any entries already set.
	for i := 1; i < top; i++ {
		xid := storage.TransactionID(i)
		bi := bankIdx(xid)
		byt := byteIdx(xid)
		b := c.getOrCreateBank(bi)
		b.mu.Lock()
		// Grow bank data if needed.
		for len(b.data) <= byt {
			b.data = append(b.data, byte(TxnStatusUnknown))
		}
		if b.data[byt] == byte(TxnStatusUnknown) {
			b.data[byt] = byte(TxnStatusCommitted)
		}
		b.mu.Unlock()
	}
	return c.flush()
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
	top := int(highXID)
	// Floor the sweep at the lowest XID still covered by an on-disk pg_xact/
	// SLRU segment. After a prior TruncateCLOG, segment files entirely below the
	// truncation horizon were unlinked, so XIDs below the lowest surviving
	// segment have been frozen/truncated (presumed committed) — re-stamping them
	// Aborted here would both recreate the deleted segments full of Aborted lanes
	// (undoing truncation, breaking basebackup byte-equality) and could make the
	// direct GetStatus-based heap-load filter reject legitimately retained
	// tuples. Leaving them Unknown is strictly safer: snapshot visibility treats
	// them committed via xid < Xmin, and the heap-load filter rejects only an
	// explicit Aborted. When no truncation has occurred (segment 0 present, or
	// the mirror is disabled) the floor is FirstNormalTransactionID and behavior
	// is unchanged. (Review fix M1.)
	low := 1
	if floor := c.firstRetainedSLRUXID(); floor > FirstNormalTransactionID {
		low = int(floor)
	}
	dirty := false
	for i := low; i < top; i++ {
		xid := storage.TransactionID(i)
		bi := bankIdx(xid)
		byt := byteIdx(xid)
		b := c.getOrCreateBank(bi)
		b.mu.Lock()
		// Grow bank data if needed; newly-appended slots default to Unknown
		// and will be stamped Aborted (crashed-in-progress xids).
		for len(b.data) <= byt {
			b.data = append(b.data, byte(TxnStatusUnknown))
		}
		if b.data[byt] == byte(TxnStatusUnknown) {
			b.data[byt] = byte(TxnStatusAborted)
			dirty = true
		}
		b.mu.Unlock()
	}
	if !dirty {
		return nil
	}
	// Project the stamped range into the SLRU mirror with ONE fsync per
	// segment file rather than one per XID. The naive per-XID path
	// (mirrorToSLRUUnlocked, which fsyncs on every call) made startup look
	// hung after pg_resetwal --next-transaction-id advances NextXID far past
	// the bootstrap pg_xact segment: the implicit-abort sweep then stamps up
	// to ~1M XIDs and would issue ~1M fsyncs. Batching collapses that to one
	// fsync per ~1M-XID segment. M0110-0004 (RW-002 (a)).
	if err := c.mirrorTerminalRangeBatchedUnlocked(storage.TransactionID(low), highXID); err != nil {
		return err
	}
	return c.flush()
}

// firstRetainedSLRUXID returns the lowest XID still covered by an on-disk
// pg_xact/ SLRU segment file. It returns FirstNormalTransactionID when the
// mirror is disabled, no segment files exist, or segment 0 is present (i.e. no
// truncation has occurred) — preserving pre-truncation behavior. After a
// TruncateCLOG unlinks low segments, it returns (lowestSeg * clogXactsPerSegment)
// so callers can avoid resurrecting truncated XIDs. Used by MarkUnknownAsAborted
// (review fix M1).
func (c *CLog) firstRetainedSLRUXID() storage.TransactionID {
	c.banksMu.RLock()
	dir := c.slruDir
	c.banksMu.RUnlock()
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

// mirrorTerminalRangeBatchedUnlocked writes the SLRU 2-bit lanes for every
// committed/aborted XID in [FirstNormalTransactionID, hi) into the pg_xact/
// segment files, performing a single fsync per segment file instead of one
// per XID (the cost that made MarkUnknownAsAborted pathologically slow after a
// pg_resetwal NextXID jump). OR semantics mirror PG's
// TransactionIdSetStatusBit; existing file content is read first so any
// committed bits already mirrored are preserved, and re-ORing an already-set
// lane is idempotent. No-op when the SLRU mirror is disabled. The caller must
// hold no bank locks (GetStatus reacquires them per slot).
func (c *CLog) mirrorTerminalRangeBatchedUnlocked(loXID, hi storage.TransactionID) error {
	c.banksMu.RLock()
	dir := c.slruDir
	c.banksMu.RUnlock()
	if dir == "" || hi <= FirstNormalTransactionID {
		return nil
	}
	floor := uint64(FirstNormalTransactionID)
	if uint64(loXID) > floor {
		floor = uint64(loXID)
	}
	// Start at the segment holding the floor so segments unlinked by a prior
	// TruncateCLOG are not recreated by the O_CREATE open below (review fix M1).
	firstSeg := floor / clogXactsPerSegment
	lastSeg := (uint64(hi) - 1) / clogXactsPerSegment
	for seg := firstSeg; seg <= lastSeg; seg++ {
		segStart := seg * clogXactsPerSegment
		segEnd := segStart + clogXactsPerSegment
		lo := segStart
		if lo < floor {
			lo = floor
		}
		hiX := segEnd
		if hiX > uint64(hi) {
			hiX = uint64(hi)
		}
		if lo >= hiX {
			continue
		}
		// Page span needed to hold the highest touched XID in this segment.
		topXidInSeg := (hiX - 1) % clogXactsPerSegment
		topPage := topXidInSeg / clogXactsPerPage
		needSize := int64(topPage+1) * int64(storage.BlockSize)

		segPath := filepath.Join(dir, fmt.Sprintf("%04X", seg))
		f, err := os.OpenFile(segPath, os.O_RDWR|os.O_CREATE, 0600)
		if err != nil {
			return fmt.Errorf("clog slru: open %q: %w", segPath, err)
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return fmt.Errorf("clog slru: stat %q: %w", segPath, err)
		}
		bufSize := needSize
		if fi.Size() > bufSize {
			bufSize = fi.Size()
		}
		buf := make([]byte, bufSize)
		if fi.Size() > 0 {
			if _, err := f.ReadAt(buf[:fi.Size()], 0); err != nil && !errors.Is(err, io.EOF) {
				f.Close()
				return fmt.Errorf("clog slru: read %q: %w", segPath, err)
			}
		}
		for xid := lo; xid < hiX; xid++ {
			var bits byte
			switch c.GetStatus(storage.TransactionID(xid)) {
			case TxnStatusCommitted:
				bits = pgClogStatusCommitted
			case TxnStatusAborted:
				bits = pgClogStatusAborted
			default:
				continue
			}
			xidInSeg := xid % clogXactsPerSegment
			pageInSeg := xidInSeg / clogXactsPerPage
			xidInPage := xidInSeg % clogXactsPerPage
			byteOffset := int(pageInSeg)*storage.BlockSize + int(xidInPage/clogXactsPerByte)
			bShift := uint((xidInPage % clogXactsPerByte) * clogBitsPerXact)
			buf[byteOffset] |= bits << bShift
		}
		if _, err := f.WriteAt(buf, 0); err != nil {
			f.Close()
			return fmt.Errorf("clog slru: write %q: %w", segPath, err)
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return fmt.Errorf("clog slru: sync %q: %w", segPath, err)
		}
		f.Close()
	}
	return nil
}

// setStatus updates data[xid] = status and rewrites the file. When a PG SLRU
// directory has been wired (see EnablePGSLRUMirror), the matching 2-bit lane
// of <slruDir>/<segno>:<page>:<byte> is also updated so a PG standby reading
// the basebackup-shipped pg_xact/ via SimpleLruReadPage_ReadOnly observes the
// correct status. M0106-0010 batched-44.
func (c *CLog) setStatus(xid storage.TransactionID, status TxnStatus) error {
	bi := bankIdx(xid)
	byt := byteIdx(xid)
	b := c.getOrCreateBank(bi)

	b.mu.Lock()
	// Grow buffer to accommodate xid.
	for len(b.data) <= byt {
		b.data = append(b.data, byte(TxnStatusUnknown))
	}
	if TxnStatus(b.data[byt]) == status {
		b.mu.Unlock()
		return nil // idempotent — no write needed
	}
	b.data[byt] = byte(status)
	b.mu.Unlock()

	if err := c.flush(); err != nil {
		return err
	}
	return c.mirrorToSLRUUnlocked(xid, status)
}

// flush collects all bank data into a single flat slice and writes it to the
// clog file. Acquires banksMu.RLock() and per-bank mu.RLock() to read data.
func (c *CLog) flush() error {
	c.banksMu.RLock()
	nBanks := len(c.banks)
	c.banksMu.RUnlock()

	if nBanks == 0 {
		return os.WriteFile(c.path, nil, 0600)
	}

	// Find the last bank that has data to determine total byte count.
	lastUsedBank := -1
	lastUsedLen := 0
	for bi := nBanks - 1; bi >= 0; bi-- {
		b := c.getBank(bi)
		if b == nil {
			continue
		}
		b.mu.RLock()
		n := len(b.data)
		b.mu.RUnlock()
		if n > 0 {
			lastUsedBank = bi
			lastUsedLen = n
			break
		}
	}
	if lastUsedBank < 0 {
		return os.WriteFile(c.path, nil, 0600)
	}

	totalLen := lastUsedBank*xidsPerBank + lastUsedLen
	out := make([]byte, totalLen)

	for bi := 0; bi <= lastUsedBank; bi++ {
		b := c.getBank(bi)
		if b == nil {
			// Absent bank → all-Unknown slots; already zero in out.
			continue
		}
		b.mu.RLock()
		start := bi * xidsPerBank
		// Cap copyLen to what fits in out: the last bank may have grown
		// between the scan above (which set lastUsedLen) and this copy, so
		// start+len(b.data) might exceed len(out). Limit defensively.
		copyLen := len(b.data)
		if start+copyLen > len(out) {
			copyLen = len(out) - start
		}
		copy(out[start:start+copyLen], b.data[:copyLen])
		b.mu.RUnlock()
	}

	return os.WriteFile(c.path, out, 0600)
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
	pgClogStatusInProgress = 0x00
	pgClogStatusCommitted  = 0x01
	pgClogStatusAborted    = 0x02
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

	// Set slruDir before loading so mirrorToSLRUUnlocked can see it.
	// Use banksMu as a convenient global lock for this field assignment.
	c.banksMu.Lock()
	c.slruDir = dir
	c.banksMu.Unlock()

	// M0106-0013: load committed/aborted entries from the on-disk SLRU
	// files into the banks BEFORE the backfill below. The SLRU is fsynced on
	// every commit (mirrorToSLRUUnlocked calls f.Sync()), making it the most
	// durable source of truth after a crash. The flat-file (loaded by
	// OpenCLog) is written without fsync and may be stale or truncated
	// (os.WriteFile uses O_TRUNC; a crash between truncate and write leaves
	// a zero-length file). By loading SLRU first, crash-recovery sees the
	// correct committed/aborted status even when the flat-file is missing.
	if err := c.loadFromSLRU(dir); err != nil {
		return err
	}

	// Backfill any in-memory committed/aborted entries so an opened clog
	// (which loaded data from the flat file but had no SLRU mirror) is
	// projected to the SLRU on first enable. mirrorToSLRUUnlocked uses OR so
	// it never clears bits already set by loadFromSLRU above.
	c.banksMu.RLock()
	nBanks := len(c.banks)
	c.banksMu.RUnlock()

	for bi := 0; bi < nBanks; bi++ {
		b := c.getBank(bi)
		if b == nil {
			continue
		}
		b.mu.RLock()
		data := make([]byte, len(b.data))
		copy(data, b.data)
		b.mu.RUnlock()

		for byt, v := range data {
			st := TxnStatus(v)
			if st == TxnStatusCommitted || st == TxnStatusAborted {
				xid := storage.TransactionID(bi*xidsPerBank + byt)
				if xid == 0 {
					continue
				}
				if err := c.mirrorToSLRUUnlocked(xid, st); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// loadFromSLRU reads committed/aborted entries from existing SLRU segment
// files under dir and merges them into the banks. Called by EnablePGSLRUMirror
// before the backfill pass so the on-disk SLRU state (fsynced at every commit)
// wins over a potentially stale flat-file. Best-effort: segment files that
// cannot be read are skipped rather than returning an error — a missing or
// corrupt segment means the affected XIDs remain as Unknown and are handled by
// MarkUnknownAsAborted.
func (c *CLog) loadFromSLRU(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("clog slru: readdir %q: %w", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) != 4 {
			continue
		}
		// Parse 4-hex-digit segment number (e.g. "0000", "0001").
		var segNo uint64
		valid := true
		for _, ch := range name {
			segNo <<= 4
			switch {
			case ch >= '0' && ch <= '9':
				segNo |= uint64(ch - '0')
			case ch >= 'A' && ch <= 'F':
				segNo |= uint64(ch - 'A' + 10)
			case ch >= 'a' && ch <= 'f':
				segNo |= uint64(ch - 'a' + 10)
			default:
				valid = false
			}
		}
		if !valid {
			continue
		}
		segData, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue // best-effort
		}
		// Decode each byte: 2 bits per XID, 4 XIDs per byte.
		// XID at (segNo, byteIdx, lane):
		//   pageInSeg  = byteIdx / BlockSize
		//   xidInPage  = (byteIdx % BlockSize) * clogXactsPerByte + lane
		//   XID        = segNo*clogXactsPerSegment + pageInSeg*clogXactsPerPage + xidInPage
		baseXID := segNo * uint64(clogXactsPerSegment)
		for i, b := range segData {
			if b == 0 {
				continue
			}
			pageInSeg := uint64(i) / uint64(storage.BlockSize)
			xidInPageBase := (uint64(i) % uint64(storage.BlockSize)) * uint64(clogXactsPerByte)
			for lane := uint64(0); lane < uint64(clogXactsPerByte); lane++ {
				rawBits := (b >> (lane * uint64(clogBitsPerXact))) & 0x3
				var status TxnStatus
				switch rawBits {
				case pgClogStatusCommitted:
					status = TxnStatusCommitted
				case pgClogStatusAborted:
					status = TxnStatusAborted
				case pgClogStatusInProgress:
					continue
				default:
					// 0x03 = both bits set. This is PG's SUB_COMMITTED state
					// (not used by goopg), but can appear as a corruption
					// artifact if MarkUnknownAsAborted previously ORed the
					// aborted bit onto a committed XID. Treat as committed:
					// the committed bit (0x01) was definitely set at some
					// point.
					status = TxnStatusCommitted
				}
				xid := baseXID + pageInSeg*uint64(clogXactsPerPage) + xidInPageBase + lane
				if xid == 0 || xid > uint64(^storage.TransactionID(0)) {
					continue
				}
				txid := storage.TransactionID(xid)
				bi := bankIdx(txid)
				byt := byteIdx(txid)
				bank := c.getOrCreateBank(bi)
				bank.mu.Lock()
				for len(bank.data) <= byt {
					bank.data = append(bank.data, byte(TxnStatusUnknown))
				}
				// SLRU is the authoritative source (fsynced); overwrite
				// whatever the flat-file had for this slot.
				bank.data[byt] = byte(status)
				bank.mu.Unlock()
			}
		}
	}
	return nil
}

// HighestKnownXID returns the highest XID that has a committed or aborted
// status in the clog. Returns 0 if no terminal status is recorded. Used at
// startup to advance txnMgr.NextXID past all previously committed XIDs so
// new snapshots have a high enough Xmax to see pre-crash rows. (M0106-0013)
func (c *CLog) HighestKnownXID() storage.TransactionID {
	c.banksMu.RLock()
	nBanks := len(c.banks)
	c.banksMu.RUnlock()

	for bi := nBanks - 1; bi >= 0; bi-- {
		b := c.getBank(bi)
		if b == nil {
			continue
		}
		b.mu.RLock()
		// For bank 0, XID 0 is invalid — stop at byt=1.
		// For all other banks, byt=0 maps to a valid non-zero XID.
		minByt := 0
		if bi == 0 {
			minByt = 1
		}
		for byt := len(b.data) - 1; byt >= minByt; byt-- {
			if TxnStatus(b.data[byt]) != TxnStatusUnknown {
				xid := storage.TransactionID(bi*xidsPerBank + byt)
				b.mu.RUnlock()
				return xid
			}
		}
		b.mu.RUnlock()
	}
	return 0
}

// SLRUDir returns the PG-canonical pg_xact/ directory, or "" if the mirror is
// disabled. Intended for tests.
func (c *CLog) SLRUDir() string {
	c.banksMu.RLock()
	defer c.banksMu.RUnlock()
	return c.slruDir
}

// mirrorToSLRUUnlocked writes the 2-bit lane for xid into the matching
// pg_xact/<segno> segment file. Does not require any CLog-level lock; the
// caller is responsible for ensuring slruDir is set before calling. No-op if
// the mirror is disabled or status is not a terminal committed/aborted code.
// Extends the segment file in BLCKSZ-page units so SimpleLruReadPage_ReadOnly
// sees a complete page.
func (c *CLog) mirrorToSLRUUnlocked(xid storage.TransactionID, status TxnStatus) error {
	c.banksMu.RLock()
	dir := c.slruDir
	c.banksMu.RUnlock()

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
	return f.Sync()
}

// TruncateCLOG removes CLOG status for every XID strictly older than the SLRU
// page containing oldestXid: the in-memory banks, the flat-file prefix, AND
// the pg_xact/ SLRU segment files that lie entirely below the cutoff page are
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
//  5. remove the segment files / banks / flat-file prefix.
//
// Truncation base note: goopg keeps the flat file and banks XID-indexed (no
// rebasing). Removing a whole bank (setting it to nil) makes GetStatus return
// TxnStatusUnknown for those XIDs; visibility code MUST short-circuit any XID
// below OldestClogXid() as committed/frozen before consulting GetStatus. We
// only drop banks that are ENTIRELY below the cutoff XID, never the bank that
// straddles it, so retained XIDs keep their exact bytes. flush() then writes a
// flat file whose leading (dropped-bank) region is all-zero — byte-compatible
// with the prior layout for every retained XID.
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

	// (5a) Drop in-memory banks entirely below the cutoff XID. The first XID of
	// the cutoff page is the lowest XID we must retain; any bank whose highest
	// XID is below it can be released. We keep the straddling bank intact.
	firstRetainedXid := storage.TransactionID(uint64(cutoffPage) * uint64(clogXactsPerPage))
	dropBelowBank := int(firstRetainedXid) / xidsPerBank // bank fully retained from here up
	c.banksMu.Lock()
	for bi := 0; bi < dropBelowBank && bi < len(c.banks); bi++ {
		c.banks[bi] = nil
	}
	c.banksMu.Unlock()

	// (5b) Rewrite the flat file (leading region for dropped banks becomes
	// all-zero, equivalent to TxnStatusUnknown — never consulted for those XIDs).
	if err := c.flush(); err != nil {
		return fmt.Errorf("clog: flush after truncate: %w", err)
	}

	// (5c) Remove SLRU segment files whose entire page range precedes the
	// cutoff page. A segment holds slruPagesPerSegment pages; its last page is
	// removable iff CLOGPagePrecedes(lastPageOfSeg, cutoffPage).
	if err := c.truncateSLRUSegments(cutoffPage); err != nil {
		return fmt.Errorf("clog: truncate slru segments: %w", err)
	}
	return nil
}

// truncateSLRUSegments unlinks pg_xact/ segment files whose highest page
// strictly precedes cutoffPage (wraparound-aware via CLOGPagePrecedes). The
// segment containing cutoffPage (and all newer segments) is kept. No-op when
// the SLRU mirror is disabled. Mirrors the file-removal half of PG's
// SimpleLruTruncate / SlruScanDirCbDeleteCutoff.
func (c *CLog) truncateSLRUSegments(cutoffPage int64) error {
	c.banksMu.RLock()
	dir := c.slruDir
	c.banksMu.RUnlock()
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

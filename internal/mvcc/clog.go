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
	dirty := false
	for i := 1; i < top; i++ {
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
			if err := c.mirrorToSLRUUnlocked(xid, TxnStatusAborted); err != nil {
				b.mu.Unlock()
				return err
			}
		}
		b.mu.Unlock()
	}
	if !dirty {
		return nil
	}
	return c.flush()
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
			xidInPageBase := (uint64(i)%uint64(storage.BlockSize))*uint64(clogXactsPerByte)
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

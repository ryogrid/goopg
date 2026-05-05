package mvcc

import (
	"fmt"
	"os"
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

// CLog is a flat-file persistent commit log that maps transaction IDs to their
// terminal status. One byte per XID; stored at <DataDir>/global/pg_xact.
//
// Thread-safe: all public methods hold mu for the duration of the operation.
type CLog struct {
	mu   sync.RWMutex
	path string
	data []byte // data[xid] = TxnStatus; grows on demand
}

// OpenCLog opens (or creates) the commit log at path. If the file exists its
// contents are loaded; if not, an empty in-memory CLog is returned. The clog
// file is created on the first SetCommitted / SetAborted call.
func OpenCLog(path string) (*CLog, error) {
	c := &CLog{path: path}
	raw, err := os.ReadFile(path)
	if err == nil {
		c.data = raw
		return c, nil
	}
	if os.IsNotExist(err) {
		return c, nil // fresh clog — file created on first write
	}
	return nil, fmt.Errorf("clog: read %q: %w", path, err)
}

// IsEmpty reports whether the clog has no entries yet (file was absent or the
// on-disk data is zero-length). Used by Open to detect the upgrade case.
func (c *CLog) IsEmpty() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.data) == 0
}

// GetStatus returns the recorded status for xid. Returns TxnStatusUnknown if
// xid has no entry (transaction never finished or XID is out of range).
func (c *CLog) GetStatus(xid storage.TransactionID) TxnStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	idx := int(xid)
	if idx >= len(c.data) {
		return TxnStatusUnknown
	}
	return TxnStatus(c.data[idx])
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
	c.mu.Lock()
	defer c.mu.Unlock()
	top := int(highXID)
	// Grow if needed.
	for len(c.data) < top {
		c.data = append(c.data, byte(TxnStatusUnknown))
	}
	// Mark [1, top) as committed, preserving any entries already set.
	for i := 1; i < top; i++ {
		if c.data[i] == byte(TxnStatusUnknown) {
			c.data[i] = byte(TxnStatusCommitted)
		}
	}
	return os.WriteFile(c.path, c.data, 0600)
}

// setStatus updates data[xid] = status and rewrites the file.
func (c *CLog) setStatus(xid storage.TransactionID, status TxnStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := int(xid)
	// Grow buffer to accommodate xid.
	for len(c.data) <= idx {
		c.data = append(c.data, byte(TxnStatusUnknown))
	}
	if TxnStatus(c.data[idx]) == status {
		return nil // idempotent — no write needed
	}
	c.data[idx] = byte(status)
	return os.WriteFile(c.path, c.data, 0600)
}

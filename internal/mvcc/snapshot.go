package mvcc

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/storage"
)

// IsolationLevel is the SQL transaction isolation level used for
// snapshot acquisition.
type IsolationLevel int

const (
	IsolationReadCommitted IsolationLevel = iota
	IsolationRepeatableRead
)

func (l IsolationLevel) String() string {
	switch l {
	case IsolationReadCommitted:
		return "read committed"
	case IsolationRepeatableRead:
		return "repeatable read"
	default:
		return fmt.Sprintf("unknown(%d)", int(l))
	}
}

// ParseIsolationLevel accepts PostgreSQL-style names. For v0 we map
// READ UNCOMMITTED to READ COMMITTED and SERIALIZABLE to the same
// snapshot behavior as REPEATABLE READ.
func ParseIsolationLevel(v string) (IsolationLevel, error) {
	key := strings.ToLower(strings.TrimSpace(v))
	switch key {
	case "read uncommitted", "read committed":
		return IsolationReadCommitted, nil
	case "repeatable read", "serializable":
		return IsolationRepeatableRead, nil
	default:
		return 0, fmt.Errorf("mvcc: unsupported isolation level %q", v)
	}
}

// Snapshot is the immutable visibility horizon for one statement.
//
// XIDs strictly below Xmin are treated as completed (committed for v0);
// XIDs >= Xmax are in the future; in-between XIDs are in-progress iff
// present in InProgress.
type Snapshot struct {
	Xmin       storage.TransactionID
	Xmax       storage.TransactionID
	InProgress []storage.TransactionID
}

// Clone deep-copies the snapshot so callers can hold it independently
// from manager internals.
func (s Snapshot) Clone() Snapshot {
	out := Snapshot{
		Xmin:       s.Xmin,
		Xmax:       s.Xmax,
		InProgress: make([]storage.TransactionID, len(s.InProgress)),
	}
	copy(out.InProgress, s.InProgress)
	return out
}

// HasInProgress returns true when xid is listed in the snapshot's
// in-progress array.
func (s Snapshot) HasInProgress(xid storage.TransactionID) bool {
	for _, in := range s.InProgress {
		if in == xid {
			return true
		}
	}
	return false
}

// SeesCommittedXID reports whether xid is visible as committed to this
// snapshot under the v0 model.
func (s Snapshot) SeesCommittedXID(xid storage.TransactionID) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	if xid < s.Xmin {
		return true
	}
	if xid >= s.Xmax {
		return false
	}
	return !s.HasInProgress(xid)
}

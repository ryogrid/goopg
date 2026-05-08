package mvcc

import (
	"fmt"
	"sort"
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

// snapshotLinearScanThreshold is the InProgress array length at or
// below which the per-tuple visibility check uses a straight-line
// scan over the slice. Above this, sort.Search's binary search wins.
// The crossover is empirical (M0069-0007 microbenchmark): at SF=1
// single-thread the typical InProgress is 1-3 entries, so the
// threshold keeps the hot path branch-prediction-friendly while
// guarding against pathological N (concurrent OLTP + analytical
// mix).
const snapshotLinearScanThreshold = 16

// HasInProgress returns true when xid is listed in the snapshot's
// in-progress array.
//
// The array is sorted ascending at snapshot construction time
// (manager.captureSnapshotLocked), so we can binary-search above
// snapshotLinearScanThreshold. For small N the linear scan stays
// because branch prediction + cache locality make the simple loop
// faster than sort.Search's call overhead.
func (s Snapshot) HasInProgress(xid storage.TransactionID) bool {
	n := len(s.InProgress)
	if n <= snapshotLinearScanThreshold {
		for _, in := range s.InProgress {
			if in == xid {
				return true
			}
		}
		return false
	}
	idx := sort.Search(n, func(i int) bool {
		return s.InProgress[i] >= xid
	})
	return idx < n && s.InProgress[idx] == xid
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

// Package multixact models PostgreSQL's MultiXactId abstraction: a single
// heap tuple's xmax can name a *set* of transactions — any number of row-lock
// holders (FOR KEY SHARE / FOR SHARE / FOR NO KEY UPDATE / FOR UPDATE) plus at
// most one updater — rather than a single transaction.
//
// goopg's tuple xmax is currently single-holder: it carries exactly one
// TransactionID, distinguished only by the HEAP_XMAX_LOCK_ONLY infomask bit
// (locker vs. updater) and HEAP_KEYS_UPDATED (key vs. no-key update). That
// model cannot represent "a FOR KEY SHARE locker AND a concurrent no-key
// updater on the same row", which is exactly the shape the upstream
// isolation specs in the MultiXact cluster exercise (lock-update-traversal,
// multixact-no-forget, nowait-2, skip-locked-2, propagate-lock-delete,
// tuplelock-conflict, aborted-keyrevoke; see docs/design/0118-0002).
//
// This package is the *engine-first* slice of that subsystem (mirroring the
// internal/amcheck build-out): the self-contained, fully-unit-testable core —
// the member representation, the lock-mode conflict matrix faithfully ported
// from upstream, the update-xid selection, and an in-memory member store —
// with NO wiring into the executor / storage hot paths. The risky hot-path
// integration (xmax encoding via HEAP_XMAX_IS_MULTI, stampLockInner member
// combination, isConcurrentlyUpdated multixact decode) lands in later loops on
// top of this verified primitive.
//
// All tables here are ports of:
//   - src/include/access/multixact.h          (MultiXactStatus, MultiXactMember, ISUPDATE)
//   - src/backend/access/heap/heapam.c        (tupleLockExtraInfo, MultiXactStatusLock)
//   - src/backend/storage/lmgr/lock.c         (LockConflicts)
//   - src/include/storage/lockdefs.h          (LOCKMODE values)
package multixact

import "github.com/goopg/goopg/internal/storage"

// MultiXactId identifies a set of transactions that hold or held a row-level
// lock (and/or performed the update) on a single heap tuple. Mirrors upstream
// MultiXactId (a uint32 in the same numbering space as TransactionId but
// distinct: a tuple's xmax is a MultiXactId when HEAP_XMAX_IS_MULTI is set in
// t_infomask).
type MultiXactId uint32

const (
	// InvalidMultiXactId is the zero / unset MultiXactId. Mirrors
	// InvalidMultiXactId in multixact.h. goopg already stamps this value as
	// nextMulti/oldestMulti in pg_control (see internal/initdb/pgcontrol.go).
	InvalidMultiXactId MultiXactId = 0
	// FirstMultiXactId is the first MultiXactId handed out by a fresh cluster.
	// Mirrors FirstMultiXactId in multixact.h.
	FirstMultiXactId MultiXactId = 1
)

// Status mirrors upstream MultiXactStatus (multixact.h:39-47). It records, for
// one member of a MultiXact, what that transaction did to the tuple: a pure
// row lock of a given strength, or an update. The numeric values are
// load-bearing — they are the on-disk member status encoding and the index
// into MultiXactStatusLock — so they are pinned to the upstream constants.
type Status uint8

const (
	// StatusForKeyShare is a SELECT ... FOR KEY SHARE row lock — the weakest;
	// it only blocks changes to the tuple's key columns (FK enforcement).
	StatusForKeyShare Status = 0x00
	// StatusForShare is a SELECT ... FOR SHARE row lock.
	StatusForShare Status = 0x01
	// StatusForNoKeyUpdate is a SELECT ... FOR NO KEY UPDATE row lock — the
	// lock an UPDATE that does not touch key columns takes before writing.
	StatusForNoKeyUpdate Status = 0x02
	// StatusForUpdate is a SELECT ... FOR UPDATE row lock — the strongest pure
	// lock.
	StatusForUpdate Status = 0x03
	// StatusNoKeyUpdate is an actual UPDATE that does not change key columns.
	StatusNoKeyUpdate Status = 0x04
	// StatusUpdate is an actual UPDATE that changes key columns, or a DELETE.
	StatusUpdate Status = 0x05

	// MaxStatus is the highest valid Status value (MaxMultiXactStatus).
	MaxStatus = StatusUpdate
)

// IsUpdate reports whether the status corresponds to a tuple update (an actual
// write to the row) as opposed to a pure row lock. Mirrors
// ISUPDATE_from_mxstatus (multixact.h:52-53): status > MultiXactStatusForUpdate,
// i.e. StatusNoKeyUpdate or StatusUpdate.
func (s Status) IsUpdate() bool { return s > StatusForUpdate }

// Valid reports whether s is one of the six defined MultiXactStatus values.
func (s Status) Valid() bool { return s <= MaxStatus }

// String renders the status with its upstream MultiXactStatus name, for test
// diagnostics and design-doc-friendly logging.
func (s Status) String() string {
	switch s {
	case StatusForKeyShare:
		return "ForKeyShare"
	case StatusForShare:
		return "ForShare"
	case StatusForNoKeyUpdate:
		return "ForNoKeyUpdate"
	case StatusForUpdate:
		return "ForUpdate"
	case StatusNoKeyUpdate:
		return "NoKeyUpdate"
	case StatusUpdate:
		return "Update"
	default:
		return "InvalidStatus"
	}
}

// Member mirrors upstream MultiXactMember (multixact.h:56-60): one transaction
// that is part of a MultiXact, together with what it did to the tuple.
type Member struct {
	Xid    storage.TransactionID
	Status Status
}

// --- Lock-mode conflict matrix (faithful port) -----------------------------
//
// Whether two MultiXact members conflict is NOT decided by the Status values
// directly. Upstream maps each Status to a heavyweight LOCKMODE and consults
// the standard lock-conflict table, so that — for example — a FOR KEY SHARE
// member (AccessShareLock) does NOT conflict with a concurrent no-key UPDATE
// (ExclusiveLock), which is precisely why goopg's branch (a) in stampLockInner
// can skip-without-waiting today. We port the three upstream tables verbatim
// rather than hand-collapsing the 6x6 result, so the mapping stays auditable
// against upstream.

// lockMode mirrors the LOCKMODE values in storage/lockdefs.h.
type lockMode int

const (
	noLock                   lockMode = 0
	accessShareLock          lockMode = 1
	rowShareLock             lockMode = 2
	rowExclusiveLock         lockMode = 3
	shareUpdateExclusiveLock lockMode = 4
	shareLock                lockMode = 5
	shareRowExclusiveLock    lockMode = 6
	exclusiveLock            lockMode = 7
	accessExclusiveLock      lockMode = 8
	maxLockMode              lockMode = 8
)

// lockBit mirrors LOCKBIT_ON: the single-bit mask for one lock mode.
func lockBit(m lockMode) uint16 { return 1 << uint(m) }

// lockConflicts ports lock.c's LockConflicts[]: entry i is the set of lock
// modes that conflict with mode i. Indexed by lockMode; NoLock conflicts with
// nothing.
var lockConflicts = [maxLockMode + 1]uint16{
	noLock: 0,

	accessShareLock: lockBit(accessExclusiveLock),

	rowShareLock: lockBit(exclusiveLock) | lockBit(accessExclusiveLock),

	rowExclusiveLock: lockBit(shareLock) | lockBit(shareRowExclusiveLock) |
		lockBit(exclusiveLock) | lockBit(accessExclusiveLock),

	shareUpdateExclusiveLock: lockBit(shareUpdateExclusiveLock) |
		lockBit(shareLock) | lockBit(shareRowExclusiveLock) |
		lockBit(exclusiveLock) | lockBit(accessExclusiveLock),

	shareLock: lockBit(rowExclusiveLock) | lockBit(shareUpdateExclusiveLock) |
		lockBit(shareRowExclusiveLock) |
		lockBit(exclusiveLock) | lockBit(accessExclusiveLock),

	shareRowExclusiveLock: lockBit(rowExclusiveLock) | lockBit(shareUpdateExclusiveLock) |
		lockBit(shareLock) | lockBit(shareRowExclusiveLock) |
		lockBit(exclusiveLock) | lockBit(accessExclusiveLock),

	exclusiveLock: lockBit(rowShareLock) |
		lockBit(rowExclusiveLock) | lockBit(shareUpdateExclusiveLock) |
		lockBit(shareLock) | lockBit(shareRowExclusiveLock) |
		lockBit(exclusiveLock) | lockBit(accessExclusiveLock),

	accessExclusiveLock: lockBit(accessShareLock) | lockBit(rowShareLock) |
		lockBit(rowExclusiveLock) | lockBit(shareUpdateExclusiveLock) |
		lockBit(shareLock) | lockBit(shareRowExclusiveLock) |
		lockBit(exclusiveLock) | lockBit(accessExclusiveLock),
}

// statusHWLock ports the composition tupleLockExtraInfo[MultiXactStatusLock[s]].hwlock
// from heapam.c: each MultiXactStatus maps (through its LockTupleMode) to the
// heavyweight lock mode used for conflict detection. Indexed by Status.
//
//	ForKeyShare    -> LockTupleKeyShare        -> AccessShareLock
//	ForShare       -> LockTupleShare           -> RowShareLock
//	ForNoKeyUpdate -> LockTupleNoKeyExclusive  -> ExclusiveLock
//	ForUpdate      -> LockTupleExclusive       -> AccessExclusiveLock
//	NoKeyUpdate    -> LockTupleNoKeyExclusive  -> ExclusiveLock
//	Update         -> LockTupleExclusive       -> AccessExclusiveLock
var statusHWLock = [MaxStatus + 1]lockMode{
	StatusForKeyShare:    accessShareLock,
	StatusForShare:       rowShareLock,
	StatusForNoKeyUpdate: exclusiveLock,
	StatusForUpdate:      accessExclusiveLock,
	StatusNoKeyUpdate:    exclusiveLock,
	StatusUpdate:         accessExclusiveLock,
}

// doLockModesConflict mirrors DoLockModesConflict: whether holding mode a
// blocks acquiring mode b. The conflict relation is symmetric.
func doLockModesConflict(a, b lockMode) bool {
	return lockConflicts[a]&lockBit(b) != 0
}

// StatusesConflict reports whether a member holding lock status `held` blocks a
// new request of status `req` on the same tuple. This is the row-lock
// compatibility relation: e.g. ForKeyShare vs NoKeyUpdate is false (a FK key
// lock does not block a no-key update), while ForShare vs NoKeyUpdate is true.
//
// Conflict is symmetric: StatusesConflict(a, b) == StatusesConflict(b, a).
func StatusesConflict(held, req Status) bool {
	return doLockModesConflict(statusHWLock[held], statusHWLock[req])
}

// MembersConflict reports whether a new row-lock/update request of status `req`
// by transaction `reqXid` conflicts with any member of an existing MultiXact.
// It is the pure (liveness-free) core of heapam.c's DoesMultiXactIdConflict.
//
// A member is ignored when its Xid equals reqXid (a transaction never
// conflicts with itself — it may upgrade its own lock) or when isCurrent
// reports it no longer running (a finished locker's lock is gone; the caller
// supplies transaction liveness, which is a runtime concern this leaf package
// deliberately does not own). Pass a nil isCurrent to treat every member as
// still relevant (the conservative, liveness-agnostic answer used in tests and
// by callers that pre-filter their member lists).
func MembersConflict(members []Member, reqXid storage.TransactionID, req Status, isCurrent func(storage.TransactionID) bool) bool {
	for _, m := range members {
		if m.Xid == reqXid {
			continue
		}
		if isCurrent != nil && !isCurrent(m.Xid) {
			continue
		}
		if StatusesConflict(m.Status, req) {
			return true
		}
	}
	return false
}

// GetUpdateXid returns the transaction that performed the update recorded in a
// MultiXact's member set, if any. A well-formed MultiXact has at most one
// update member (ISUPDATE_from_mxstatus). Mirrors the member-selection core of
// MultiXactIdGetUpdateXid (the SLRU/liveness lookup is the wiring layer's job).
//
// The bool result is false when the member set is pure locks (no updater).
func GetUpdateXid(members []Member) (storage.TransactionID, bool) {
	for _, m := range members {
		if m.Status.IsUpdate() {
			return m.Xid, true
		}
	}
	return storage.InvalidTransactionID, false
}

// HasLockers reports whether the member set contains at least one pure
// row-lock member (a non-update member). Used by the wiring layer to decide
// whether a tuple is still locked after its updater finishes.
func HasLockers(members []Member) bool {
	for _, m := range members {
		if !m.Status.IsUpdate() {
			return true
		}
	}
	return false
}

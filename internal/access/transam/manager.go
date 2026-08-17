package transam

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/storage/lmgr/lockwait"
	"github.com/goopg/goopg/internal/storage"
)

const (
	BootstrapTransactionID   storage.TransactionID = 1
	FrozenTransactionID      storage.TransactionID = 2
	FirstNormalTransactionID storage.TransactionID = 3
)

var (
	ErrUnknownTransaction = errors.New("mvcc: unknown transaction")
	ErrXIDWraparound      = errors.New("mvcc: xid wraparound is not implemented")
	// ErrUnsupportedDetach is returned by DetachToDedicatedSlot for a
	// SERIALIZABLE transaction, whose SSI bookkeeping is keyed by the
	// transaction Handle and cannot be relocated without re-keying. M0118-0009.
	ErrUnsupportedDetach = errors.New("mvcc: cannot detach a SERIALIZABLE transaction to a dedicated slot")
)

// TxnHandle is an opaque, monotonically-allocated identifier for an
// in-progress transaction that exists independently of the XID. It
// is the active-set key so that read-only transactions (which never
// receive an XID under the lazy-allocation model — M0093, PG parity)
// don't collide on the InvalidTransactionID sentinel.
//
// Handles are not durable; they exist only within a single Manager
// lifetime. The first handle is 1; 0 is reserved as the zero value.
type TxnHandle uint64

// Transaction is one open transaction handle.
type Transaction struct {
	// Handle is the manager-internal identity of this transaction,
	// always non-zero for a tx returned by Begin. Used as the
	// active-set key. Survives lazy XID assignment.
	Handle TxnHandle

	// XID is the assigned transaction ID. Zero
	// (storage.InvalidTransactionID) until the first write-path
	// operation calls Manager.AssignXID — this is the M0093 lazy
	// allocation mirroring PostgreSQL's RecordTransactionCommit
	// fast-path for read-only transactions.
	XID storage.TransactionID

	Isolation IsolationLevel
}

// Manager tracks active transactions and creates statement snapshots.
// It is safe for concurrent use.
type Manager struct {
	xidgen    XidGen
	procArray ProcArray

	abortedMu   sync.RWMutex
	abortedXIDs []storage.TransactionID

	// releasedWaiterXIDs holds top-level XIDs whose transaction block is still
	// open in its proc-array slot but whose statement-level abort (a deadlock
	// victim, mirroring PostgreSQL's AbortTransaction) has already released the
	// XID for the purpose of WaitForXID / IsXIDActive. A peer blocked on this
	// XID's catalog-tuple xmax (the intra-grant-inplace pg_class in-place wait)
	// then unblocks at the abort rather than at the victim's later explicit
	// ROLLBACK/COMMIT, while the slot itself stays active so that explicit
	// COMMIT/ROLLBACK still finalises the (write-less) transaction normally —
	// the victim never recorded any heap write to make visible. Snapshot
	// visibility is intentionally NOT consulted against this set (it reads the
	// slot/CLOG as before). Cleared when the slot is finished. Design 0118-0115
	// (intra-grant-inplace perm 8).
	releasedWaiterMu   sync.RWMutex
	releasedWaiterXIDs map[storage.TransactionID]struct{}

	// clog, when non-nil, is the durable commit log attached to every snapshot
	// this Manager captures (M0117-0002). It lets Snapshot.SeesCommittedXID fall
	// back to the persistent CLOG for in-window XIDs the in-memory arrays cannot
	// classify (recovered aborts the rebuilt-empty abortedXIDs list has
	// forgotten). nil (the default) keeps the pure in-memory v0 behaviour;
	// installed by the server/initdb.Open recovery wiring via SetCLog. Read under
	// abortedMu (cheap, already taken in captureSnapshot's neighbourhood).
	clog *CLog

	xactMarkerMu sync.RWMutex
	// The bool parameter is waitLocalFlush: true (every existing caller —
	// Commit/Rollback) requires the hook to durably flush the commit WAL
	// record before returning; false (CommitAsync, synchronous_commit=off)
	// lets the hook return without waiting for that flush, relying on the
	// CLOG async-commit write barrier (M0117-0007) instead. M0117-0007 Part B.
	xactMarker func(storage.TransactionID, XactMarker, bool) error

	// onTxnEnd is called after every transaction commit or abort, once the
	// XID slot has been cleared and commitCond has been broadcast. Use case:
	// cleaning up cross-session registries (e.g. spec insert registry for
	// pg_locks). The callback must be non-blocking and short.
	onTxnEndMu sync.RWMutex
	onTxnEnd   func(xid storage.TransactionID)

	// relcacheInvalPending is set by DDL that writes to nailed catalog
	// relations (pg_class, pg_attribute, pg_proc, pg_type) so the
	// xact-marker hook emits the commit record with the HAS_INVALS chunk
	// (EncodeXactCommitPG(xid, true)) and unlinks both pg_internal.init
	// files at commit time.
	relcacheInvalPending atomic.Bool

	// catalogXminSource, when installed, returns the smallest catalog_xmin
	// pinned by any active logical replication slot (0 = nothing pinned).
	// OldestXmin folds it into the global pruning/truncation horizon so the
	// heap-prune, VACUUM and CLOG-truncation paths never reclaim catalog (or,
	// conservatively in v0, any permanent-relation) tuple versions a logical
	// decoder may still need. Installed once during startup wiring
	// (initdb.Open) via SetCatalogXminSource; nil keeps the pure in-memory v0
	// behaviour. Read lock-free via atomic.Pointer on the hot prune path.
	catalogXminSource atomic.Pointer[func() uint64]

	// WaitForXID uses waitMu + commitCond only.
	waitMu     sync.Mutex
	commitCond *sync.Cond

	// autoProcNum is an atomic counter for callers (mostly tests) that do
	// not supply an explicit procNum. Auto-assigned slots are never recycled
	// within a Manager lifetime, so the counter wraps modulo procArray size.
	autoProcNum atomic.Int32

	// connSlotCursor rotates AcquireConnSlot's scan start (see its doc).
	connSlotCursor atomic.Int32

	// Cold path: SSI + predicate locks share ssiMu.
	ssiMu          sync.Mutex
	ssiState       ssiState
	predicateLocks predicateLocksRegistry
	// ssiCond signals when a SERIALIZABLE xact finishes (commit or abort),
	// so a READ ONLY DEFERRABLE xact blocked in waitForSafeSnapshot can re-check
	// whether its safe-snapshot condition is now met. Bound to ssiMu; created
	// lazily under ssiMu (ssiCondLocked) so Managers built as a bare struct
	// literal in tests work without NewManager. M0118-0001.
	ssiCond *sync.Cond

	// subxact tracking (M0050-0002): maps subxact XIDs to their parent
	// XIDs, and records individually-aborted subxact XIDs. Lazily
	// initialised on first use. Protected by subxactMu.
	subxactFields
}

// NewManager returns a fresh manager whose first assigned xid is 3,
// mirroring PostgreSQL's first normal xid.
func NewManager() *Manager {
	m := &Manager{}
	// init xidgen: next stores the next XID to assign.
	// Allocate() returns next and advances to next+1; Peek() returns next.
	// FirstNormalTransactionID = 3 mirrors PostgreSQL's first normal XID.
	m.xidgen.next.Store(uint64(FirstNormalTransactionID))
	m.procArray = newProcArray(defaultProcArraySize)
	m.commitCond = sync.NewCond(&m.waitMu)
	m.ssiCond = sync.NewCond(&m.ssiMu)
	return m
}

// SetNextXID advances nextXID forward without allocating it. Used on
// startup to skip over xids already present in persisted heap tuples
// from previous sessions, so the new session's snapshots see those
// tuples as committed (xmin < snap.Xmin) instead of in-progress.
//
// The change is monotonic: a smaller value is ignored.
func (m *Manager) SetNextXID(x storage.TransactionID) {
	m.xidgen.SetNext(x)
}

// ReplayXactCommit is called by the standby's StreamReplayer when it applies
// a RecordKindXactCommit from the primary. It advances nextXID past xid so
// that snapshots taken on the standby see the replayed tuples as committed
// (xmin < snap.Xmax). Without this call, the snapshot's Xmax equals the
// committed XID, causing "xid >= Xmax → invisible" for every replayed tuple.
func (m *Manager) ReplayXactCommit(xid storage.TransactionID) {
	m.xidgen.SetNext(xid + 1)
}

// ReplayXactAbort is called by the standby's StreamReplayer when it applies a
// RecordKindXactAbort from the primary. It advances nextXID past xid (so the
// aborted transaction is not treated as "future") and records it in abortedXIDs
// (so its heap tuples remain invisible to queries on the standby).
func (m *Manager) ReplayXactAbort(xid storage.TransactionID) {
	m.xidgen.SetNext(xid + 1)
	m.abortedMu.Lock()
	m.abortedXIDs = insertSortedXID(m.abortedXIDs, xid)
	m.abortedMu.Unlock()
}

// NextXID returns the value the next Begin will allocate. Used by
// the bootstrap path to snapshot transaction state across restarts.
func (m *Manager) NextXID() storage.TransactionID {
	return m.xidgen.Peek()
}

// SetCLog installs the durable commit log consulted as the visibility fallback
// for in-window XIDs (M0117-0002). After this call every snapshot captured by
// this Manager carries c, so Snapshot.SeesCommittedXID consults the persistent
// CLOG for XIDs the in-memory InProgress/Aborted arrays cannot classify (e.g. a
// recovered abort the rebuilt-empty abortedXIDs list has forgotten). Passing nil
// disables the fallback (restores the pure in-memory v0 behaviour). Intended to
// be called once during startup/recovery wiring (initdb.Open).
func (m *Manager) SetCLog(c *CLog) {
	m.abortedMu.Lock()
	m.clog = c
	m.abortedMu.Unlock()
}

// HasCLog reports whether a durable commit log is installed. Exists so callers
// outside this package can assert the startup wiring: M0131-S30.7 found that
// SetCLog had no production caller at all for the whole of M0117..M0131, which
// made the entire durable-abort visibility fallback dead code on every live
// server (docs/design/0131-0027).
func (m *Manager) HasCLog() bool {
	m.abortedMu.RLock()
	defer m.abortedMu.RUnlock()
	return m.clog != nil
}

// SetCatalogXminSource installs (or, with nil, clears) the hook OldestXmin
// consults to hold the global pruning/truncation horizon back to the oldest
// catalog_xmin pinned by any active logical replication slot. The server wires
// this to wal.Slots.MinCatalogXmin during startup so heap pruning, VACUUM and
// CLOG truncation never reclaim catalog tuple versions a logical decoder can
// still need. fn must be non-blocking (it runs on the prune hot path); a nil fn
// or a fn returning 0 leaves the horizon unchanged.
func (m *Manager) SetCatalogXminSource(fn func() uint64) {
	if fn == nil {
		m.catalogXminSource.Store(nil)
		return
	}
	m.catalogXminSource.Store(&fn)
}

// xidWarnAge is how many XIDs before uint32 overflow to emit a warning.
// Mirrors upstream's GetNewTransactionId warning threshold (~40M before max).
const xidWarnAge = storage.TransactionID(40_000_000)

// xidStopAge is how many XIDs before uint32 overflow to refuse new txns.
const xidStopAge = storage.TransactionID(3_000_000)

// xidMaxSafe is the last "safe" XID before the hard stop threshold.
// uint32 max = 4,294,967,295; reserved XIDs 0-2 put the usable ceiling here.
const xidMaxSafe = ^storage.TransactionID(0) - xidStopAge

// Begin allocates a slot and tracks the transaction as in-progress.
// M0093: NO XID is allocated; the first write path calls AssignXID
// to materialise one. Returns a Transaction with Handle != 0 and
// XID == storage.InvalidTransactionID.
//
// procNum identifies the backend's slot in the ProcArray (0-based).
// If omitted (variadic), a slot is auto-assigned from an internal
// counter — suitable for tests and callers that have not yet threaded
// an explicit procNum. Production server code should always supply one.
func (m *Manager) Begin(iso IsolationLevel, procNums ...int32) (Transaction, error) {
	var procNum int32
	if len(procNums) > 0 {
		procNum = procNums[0]
	} else {
		// Auto-assign: scan for a free slot (inTxn==0) starting from slot 1.
		// Slot 0 is reserved for connections that supply explicit procNum=0.
		// CAS-based scan avoids colliding with slots held by active connections
		// that pass an explicit procNum — the old counter-based approach falsely
		// allocated slot 1 on the very first call, clobbering a live connection.
		found := false
		// Scan only the connection/internal region; the high region is reserved
		// for detached prepared transactions (DetachToDedicatedSlot). M0118-0009.
		sz := min(ConnSlotCount, len(m.procArray.slots))
		for i := 1; i < sz; i++ {
			if m.procArray.slots[i].inTxn.CompareAndSwap(0, 1) {
				procNum = int32(i)
				found = true
				break
			}
		}
		if !found {
			return Transaction{}, fmt.Errorf("mvcc: no free process slots for internal transaction")
		}
		// Initialise the slot fields before returning — skip the later block
		// that does the same for explicit-procNum callers.
		s := &m.procArray.slots[procNum]
		s.xid.Store(0)
		s.xmin.Store(^uint64(0))
		s.firstSnap = nil
		s.pinnedSnap.Store(false)
		s.snapshotXmin = ^uint32(0)
		s.isolation = int32(iso)
		// inTxn already set to 1 by CAS above.
		handle := TxnHandle(procNum + 1)
		if iso == IsolationSerializable {
			m.ssiMu.Lock()
			m.registerSerializableLocked(handle)
			m.ssiMu.Unlock()
		}
		return Transaction{Handle: handle, XID: storage.InvalidTransactionID, Isolation: iso}, nil
	}
	switch iso {
	case IsolationReadCommitted, IsolationRepeatableRead, IsolationSerializable:
	default:
		return Transaction{}, fmt.Errorf("mvcc: unsupported isolation level %v", iso)
	}
	if procNum < 0 || int(procNum) >= len(m.procArray.slots) {
		return Transaction{}, fmt.Errorf("mvcc: procNum %d out of range [0, %d)", procNum, len(m.procArray.slots))
	}

	s := &m.procArray.slots[procNum]
	s.xid.Store(0)
	s.xmin.Store(^uint64(0))
	s.firstSnap = nil
	s.pinnedSnap.Store(false)
	s.snapshotXmin = ^uint32(0)
	s.isolation = int32(iso)
	s.inTxn.Store(1)

	// M0104-0002: allocate per-txn SSI bookkeeping for SERIALIZABLE
	// transactions. RC/RR never register; the registry stays nil
	// for those workloads. Subsequent slices (predicate locks,
	// rw-conflict tracking, pre-commit failure) attach to the
	// SerializableXact returned here.
	// Handle = procNum+1 so that Handle is always ≥1 (Handle=0 remains the
	// "invalid/unset" sentinel, since procNum=0 is a valid slot index).
	handle := TxnHandle(procNum + 1)
	if iso == IsolationSerializable {
		m.ssiMu.Lock()
		m.registerSerializableLocked(handle)
		m.ssiMu.Unlock()
	}
	return Transaction{Handle: handle, XID: storage.InvalidTransactionID, Isolation: iso}, nil
}

// AssignXID lazily allocates a real XID for tx (M0093, PG-parity).
// Idempotent: returns the existing XID on subsequent calls.
//
// CRITICAL: callers MUST refresh their Transaction.XID with the
// returned value BEFORE any code that consults tx.XID as a real
// XID — in particular before isConcurrentlyUpdated (M0090),
// NewHeapTuple xmin, PageSetHeapTupleXmax, PageSetHeapTupleLockOnly.
// Use executor.Context.MaterializeWriterXID() which does this
// atomically for the common executor call sites.
func (m *Manager) AssignXID(tx Transaction) (storage.TransactionID, error) {
	procNum := int32(tx.Handle) - 1
	if procNum < 0 || int(procNum) >= len(m.procArray.slots) {
		return 0, ErrUnknownTransaction
	}
	s := &m.procArray.slots[procNum]
	if s.inTxn.Load() == 0 {
		return 0, ErrUnknownTransaction
	}

	if existing := s.xid.Load(); existing != 0 {
		return storage.TransactionID(existing), nil
	}

	newXID := m.xidgen.Allocate()
	if newXID == ^storage.TransactionID(0) {
		return 0, ErrXIDWraparound
	}
	// Anti-wraparound guard (M0046-0005): refuse new transactions
	// when too close to uint32 overflow.
	if newXID > xidMaxSafe {
		return 0, fmt.Errorf(
			"mvcc: database must be vacuumed within %d transactions to prevent XID wraparound",
			^storage.TransactionID(0)-newXID)
	}
	s.xid.Store(uint64(newXID))

	// M0104-0002: stamp the new top-level XID onto the SSI
	// bookkeeping object so future slices that key conflict
	// records by XID can find the SerializableXact. Read-only
	// SERIALIZABLE transactions never reach this branch and keep
	// SerializableXact.XID == InvalidTransactionID.
	if tx.Isolation == IsolationSerializable {
		m.ssiMu.Lock()
		if m.ssiState.xacts != nil {
			if sx, ok := m.ssiState.xacts[tx.Handle]; ok {
				sx.XID = newXID
			}
		}
		m.ssiMu.Unlock()
	}
	return newXID, nil
}

// FreshSnapshot captures a brand-new "latest" MVCC snapshot reflecting every
// transaction committed up to this instant, independent of any transaction's
// isolation level or pinned statement snapshot.
//
// This mirrors PostgreSQL's deferred referential-integrity machinery: a
// constraint trigger queued INITIALLY DEFERRED fires at COMMIT under a freshly
// pushed snapshot (RI_FKey_check / ri_PerformCheck push GetLatestSnapshot before
// running the check SPI query), NOT the firing statement's snapshot. Without
// this a REPEATABLE READ transaction's pinned snapshot would fail to see a
// concurrently-committed parent row that satisfies the constraint, raising a
// spurious 23503 at COMMIT (see the fk-snapshot isolation spec, where s2's RR
// snapshot cannot see s1's just-committed fk_parted_pk row but the deferred
// check must still pass). Used only by the deferred FK check at COMMIT; the
// snapshot still classifies the committing transaction's own XID as in-progress,
// so own-write visibility continues to flow through the currentXID self-check in
// TupleVisibleSubxact. 0119-0004 (deferred-ri-fresh-snapshot).
func (m *Manager) FreshSnapshot() Snapshot {
	return m.captureSnapshot()
}

// SnapshotFor returns the statement snapshot for tx.
//
// READ COMMITTED gets a fresh snapshot on every call.
// REPEATABLE READ pins the first snapshot for the whole transaction.
//
// M0093: lookup is by Handle (not XID), and the per-state
// snapshotXmin is updated to the min observed Xmin so OldestXmin
// can pin VACUUM correctly even for read-only RR transactions.
func (m *Manager) SnapshotFor(tx Transaction) (Snapshot, error) {
	procNum := int32(tx.Handle) - 1
	if procNum < 0 || int(procNum) >= len(m.procArray.slots) {
		return Snapshot{}, ErrUnknownTransaction
	}
	s := &m.procArray.slots[procNum]
	if s.inTxn.Load() == 0 {
		return Snapshot{}, ErrUnknownTransaction
	}
	if IsolationLevel(s.isolation) != tx.Isolation {
		return Snapshot{}, fmt.Errorf("mvcc: transaction isolation mismatch handle=%d", tx.Handle)
	}

	switch tx.Isolation {
	case IsolationReadCommitted:
		snap := m.captureSnapshot()
		// Update slot xmin to track the lowest seen Xmin for OldestXmin.
		for {
			cur := s.xmin.Load()
			want := uint64(snap.Xmin)
			if want >= cur {
				break
			}
			if s.xmin.CompareAndSwap(cur, want) {
				break
			}
		}
		return snap, nil

	case IsolationRepeatableRead, IsolationSerializable:
		// SERIALIZABLE shares snapshot acquisition with RR pending the
		// predicate-lock substrate (M0104-0003); conflict detection
		// will overlay on top of the pinned snapshot, not replace it.
		if s.firstSnap == nil {
			if tx.Isolation == IsolationSerializable {
				// GetSafeSnapshot deferral: a READ ONLY DEFERRABLE xact blocks
				// here until concurrent writers drain, so the snapshot it then
				// captures is safe. No-op for every other SERIALIZABLE xact.
				// M0118-0001 (read-only-anomaly-3).
				m.waitForSafeSnapshot(tx.Handle)
			}
			snap := m.captureSnapshot()
			s.firstSnap = &snap
			s.xmin.Store(uint64(snap.Xmin))
			// Mark the slot as holding a snapshot pinned for the whole txn: DETACH
			// PARTITION CONCURRENTLY must wait for such a session to finish (its
			// snapshot keeps seeing the partition attached). RC sessions never set
			// this — their per-statement snapshot is released, so a txn-scoped
			// relation lock is the proxy instead. Design 0118-0060.
			s.pinnedSnap.Store(true)
			if tx.Isolation == IsolationSerializable {
				// Capture lastCommitBeforeSnapshot for the de-facto READ ONLY
				// SSI optimisation (predicate.c). M0118-0001 (receipt-report).
				m.stampSerializableSnapshotSeqNo(tx.Handle)
			}
		}
		return s.firstSnap.Clone(), nil

	default:
		return Snapshot{}, fmt.Errorf("mvcc: unsupported isolation level %v", tx.Isolation)
	}
}

// AcquireConnSlot claims a free connection proc slot for the lifetime of
// one client connection and returns its procNum; ReleaseConnSlot frees it
// at disconnect. Connection-level ownership (connHeld) is DISTINCT from
// per-transaction occupancy (inTxn): an idle connection holds its slot
// between transactions.
//
// This replaces the historical `(pid-1) % ConnSlotCount` assignment in the
// server, which WRAPPED once cumulative connections exceeded the slot
// count and handed a live long-running session's slot to a brand-new
// connection — the new session's Begin then clobbered the victim's
// in-flight transaction ("mvcc: unknown transaction" storms ~180 s into
// any run with ~5 conn/s churn, found by the C3-S5 soak's wait-event
// sampler). Slot 0 stays reserved for explicit-procNum callers.
func (m *Manager) AcquireConnSlot() (int32, error) {
	sz := min(ConnSlotCount, len(m.procArray.slots))
	// Round-robin from a rotating cursor rather than lowest-free: always
	// reusing the just-freed slot puts a brand-new connection on a
	// procNum whose previous owner's peripheral state (activity registry,
	// per-backend undo bookkeeping) may still be draining — the temporal
	// spacing the old modulo scheme provided implicitly. The cursor keeps
	// that spacing while still never handing out a HELD slot.
	start := m.connSlotCursor.Add(1)
	for off := int32(0); off < int32(sz-1); off++ {
		i := 1 + (start+off)%int32(sz-1)
		if m.procArray.slots[i].connHeld.CompareAndSwap(0, 1) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("mvcc: no free connection slots (max %d concurrent connections)", sz-1)
}

// ReleaseConnSlot returns a connection's slot to the free pool. The caller
// must have finished/rolled back any open transaction first (connection
// teardown does).
func (m *Manager) ReleaseConnSlot(procNum int32) {
	if procNum <= 0 || int(procNum) >= len(m.procArray.slots) {
		return
	}
	m.procArray.slots[procNum].connHeld.Store(0)
}

// Commit marks tx committed and removes it from the active set.
// The XactMarkerLogger hook (when installed) is invoked with
// kind=XactCommit before the active-set removal so a hook failure
// surfaces as a Commit error and the transaction stays in-progress
// for the caller to retry.
//
// M0093: the hook fires ONLY when the transaction was assigned a
// real XID. Read-only commits skip the hook entirely (no WAL
// XactCommit record, no fsync, no clog write) — mirroring PG's
// RecordTransactionCommit fast-path for txns with no XID.
func (m *Manager) Commit(tx Transaction) error {
	return m.finish(tx, XactCommit, true)
}

// CommitAsync commits tx without waiting for the local WAL flush to
// complete (PostgreSQL's synchronous_commit=off). Durability is still
// guaranteed: the commit-record LSN is recorded against the XID's CLOG page
// (xactMarker's hook calls CLog.SetCommittedWithLSN instead of SetCommitted)
// and the async-commit write barrier flushes the WAL up to that LSN the
// moment the page is written back to disk, so a claimed commit can never
// reach disk without its WAL backing it — the transaction is simply not
// held up waiting for that flush to happen now. M0117-0007 Part B.
func (m *Manager) CommitAsync(tx Transaction) error {
	return m.finish(tx, XactCommit, false)
}

// Rollback marks tx aborted and removes it from the active set.
// The XactMarkerLogger hook is invoked the same way as Commit
// but with kind=XactAbort. A hook failure on rollback also keeps
// the transaction in-progress; the caller is expected to retry
// or escalate.
func (m *Manager) Rollback(tx Transaction) error {
	return m.finish(tx, XactAbort, true)
}

// DetachToDedicatedSlot relocates tx off its originating backend's proc slot
// onto a fresh dedicated slot, returning the moved Transaction (a new Handle,
// the same XID / isolation / snapshot). It is the goopg analogue of PostgreSQL
// handing a PREPAREd transaction to a dummy PGPROC: the originating backend's
// slot is freed so the backend can begin further transactions, while the
// prepared transaction's state (active XID, snapshot xmin) stays live in the
// proc array — visible as in-progress to other sessions and committable /
// abortable later from ANY backend via Commit/Rollback on the returned
// Transaction.
//
// Restricted to READ COMMITTED / REPEATABLE READ (ErrUnsupportedDetach for
// SERIALIZABLE): SSI predicate locks and rw-conflict records are keyed by the
// transaction Handle and would need re-keying. The SERIALIZABLE 2PC paths keep
// the transaction on its original slot (same-backend finalisation) instead.
// M0118-0009 (stats — cross-backend two-phase commit).
func (m *Manager) DetachToDedicatedSlot(tx Transaction) (Transaction, error) {
	if tx.Isolation == IsolationSerializable {
		return Transaction{}, ErrUnsupportedDetach
	}
	oldProc := int32(tx.Handle) - 1
	if oldProc < 0 || int(oldProc) >= len(m.procArray.slots) {
		return Transaction{}, ErrUnknownTransaction
	}
	old := &m.procArray.slots[oldProc]
	if old.inTxn.Load() == 0 {
		return Transaction{}, ErrUnknownTransaction
	}
	// Claim a fresh dedicated slot from the reserved high region so no backend
	// reusing a connection/internal procNum can later clobber it.
	newProc := int32(-1)
	for i := ConnSlotCount; i < len(m.procArray.slots); i++ {
		if m.procArray.slots[i].inTxn.CompareAndSwap(0, 1) {
			newProc = int32(i)
			break
		}
	}
	if newProc < 0 {
		return Transaction{}, fmt.Errorf("mvcc: no free process slots to detach prepared transaction")
	}
	xid := old.xid.Load()
	ns := &m.procArray.slots[newProc]
	// Populate the dedicated slot fully BEFORE clearing the old one so a
	// concurrent OldestXmin/snapshot reader always observes the XID as active in
	// at least one slot (it may briefly appear in both — harmless).
	ns.xid.Store(xid)
	ns.xmin.Store(old.xmin.Load())
	ns.firstSnap = old.firstSnap
	ns.snapshotXmin = old.snapshotXmin
	ns.isolation = old.isolation
	ns.pinnedSnap.Store(old.pinnedSnap.Load())
	// ns.inTxn already 1 from the CAS above.
	// Release the originating backend's slot so it can begin new transactions.
	old.xid.Store(0)
	old.firstSnap = nil
	old.pinnedSnap.Store(false)
	old.xmin.Store(^uint64(0))
	old.snapshotXmin = ^uint32(0)
	old.inTxn.Store(0)
	return Transaction{Handle: TxnHandle(newProc + 1), XID: storage.TransactionID(xid), Isolation: tx.Isolation}, nil
}

// AllocateSubXid allocates a fresh XID for a subtransaction and registers
// it as a child of parentXid. The sub-XID is not tracked in the proc-array
// (subxact XIDs are not independent top-level transactions); visibility is
// handled entirely by SeesCommittedXIDWithSubxacts via the subxact map.
//
// M0093 note: parentXid must already be a real (non-Invalid) XID. The
// caller — executor's SAVEPOINT path — calls Context.MaterializeWriterXID
// first so the parent has a real XID before sub-XIDs are allocated under
// it.
func (m *Manager) AllocateSubXid(parentXid storage.TransactionID) (storage.TransactionID, error) {
	subXid := m.xidgen.Allocate()
	if subXid == ^storage.TransactionID(0) {
		return 0, ErrXIDWraparound
	}
	if subXid > xidMaxSafe {
		return 0, fmt.Errorf(
			"mvcc: database must be vacuumed within %d transactions to prevent XID wraparound",
			^storage.TransactionID(0)-subXid)
	}
	// Register through RegisterSubXid (not addSubxactEntry directly) so the
	// subxid→parent link lands in the persistent SubxactMap when one is attached
	// (initdb.Open's SetSubxactMap, the real-server path). TopLevelXid / IsAborted
	// / MarkSubxactAborted all read the attached map first; writing only the
	// in-memory subxactParents fallback here would leave TopLevelXid(subxid)
	// unresolved, so xidActiveWithSubxact would report a savepoint-scoped row lock
	// as dead and conflicting waiters would never block on it. M0118-0004.
	m.RegisterSubXid(subXid, parentXid)
	return subXid, nil
}

// ActiveCount returns the number of in-progress transactions.
func (m *Manager) ActiveCount() int {
	count := 0
	for i := range m.procArray.slots {
		if m.procArray.slots[i].inTxn.Load() == 1 {
			count++
		}
	}
	return count
}

// OldestXmin is the lowest xid still potentially observable by any
// in-progress or future snapshot. Returns nextXID when no transaction
// is active. VACUUM uses this as the horizon below which xmax-tagged
// tuples can be reclaimed.
//
// M0093: folds in both (a) assigned XIDs of active transactions AND
// (b) the xmin of any active txn that has taken a snapshot
// but not yet been assigned an XID.
func (m *Manager) OldestXmin() storage.TransactionID {
	result := storage.TransactionID(m.xidgen.Peek())
	for i := range m.procArray.slots {
		s := &m.procArray.slots[i]
		// Skip idle slots — their zero xmin value would falsely pin VACUUM at 0.
		if s.inTxn.Load() == 0 {
			continue
		}
		if xid := s.xid.Load(); xid != 0 && storage.TransactionID(xid) < result {
			result = storage.TransactionID(xid)
		}
		if xmin := s.xmin.Load(); xmin != ^uint64(0) && storage.TransactionID(xmin) < result {
			result = storage.TransactionID(xmin)
		}
	}
	// Hold the horizon back to the oldest catalog_xmin pinned by any active
	// logical replication slot (0 = nothing pinned). A logical decoder rebuilds
	// historic catalog snapshots from tuple versions at/after this xid; letting
	// the prune/VACUUM/CLOG-truncation horizon advance past it would reclaim
	// catalog rows out from under an in-flight decode. Upstream tracks a separate
	// data vs catalog horizon (only catalog + user_catalog relations are held by
	// catalog_xmin); v0 conservatively floors the single global horizon, which
	// over-retains dead tuples on ordinary permanent tables while a slot lags but
	// is never unsafe. See docs/design/0008-0001-logical-decoding-pipeline.md.
	if src := m.catalogXminSource.Load(); src != nil {
		if cx := (*src)(); cx != 0 && storage.TransactionID(cx) < result {
			result = storage.TransactionID(cx)
		}
	}
	return result
}

// OldestXminForProc is the session-local pruning horizon: the lowest xid still
// observable by ONLY the transaction occupying proc slot procNum, ignoring all
// other backends. PostgreSQL applies this narrower horizon (GlobalVisTempRels)
// to TEMPORARY relations — a temp table is private to its owning backend, so a
// concurrent session holding an older snapshot cannot see (and therefore cannot
// be harmed by reclaiming) the temp table's deleted rows. Used by VACUUM and the
// index-only-scan prune-on-read for temp relations (horizons.spec, M0118-0009).
//
// It still respects the owning backend's OWN in-progress transaction: the slot's
// assigned xid (a row the backend itself just deleted but has not committed) and
// its snapshot xmin both floor the result, so an uncommitted delete is never
// reclaimable. Falls back to the global OldestXmin when procNum is out of range
// or the slot is idle (conservative — never reclaims more than the global path).
func (m *Manager) OldestXminForProc(procNum int32) storage.TransactionID {
	if procNum < 0 || int(procNum) >= len(m.procArray.slots) {
		return m.OldestXmin()
	}
	s := &m.procArray.slots[procNum]
	if s.inTxn.Load() == 0 {
		return m.OldestXmin()
	}
	result := storage.TransactionID(m.xidgen.Peek())
	if xid := s.xid.Load(); xid != 0 && storage.TransactionID(xid) < result {
		result = storage.TransactionID(xid)
	}
	if xmin := s.xmin.Load(); xmin != ^uint64(0) && storage.TransactionID(xmin) < result {
		result = storage.TransactionID(xmin)
	}
	return result
}

func (m *Manager) finish(tx Transaction, kind XactMarker, waitLocalFlush bool) error {
	procNum := int32(tx.Handle) - 1
	if procNum < 0 || int(procNum) >= len(m.procArray.slots) {
		return ErrUnknownTransaction
	}
	s := &m.procArray.slots[procNum]
	if s.inTxn.Load() == 0 {
		return ErrUnknownTransaction
	}
	if IsolationLevel(s.isolation) != tx.Isolation {
		return fmt.Errorf("mvcc: transaction isolation mismatch handle=%d", tx.Handle)
	}

	// Save the XID before clearing the slot.
	xid := storage.TransactionID(s.xid.Load())

	// M0104-0006: run the pre-commit dangerous-structure scan before
	// any side effects. On detection, return a typed
	// *SerializationFailureError so the executor can surface
	// SQLSTATE 40001 and call Rollback to perform the actual
	// cleanup. The transaction remains in the slot until the caller
	// rolls back, mirroring upstream's `ereport(ERROR, ...)` flow
	// out of `PreCommit_CheckForSerializationFailure`.
	if tx.Isolation == IsolationSerializable && kind == XactCommit {
		m.ssiMu.Lock()
		err := m.preCommitCheckForSerializationFailureLocked(tx.Handle)
		m.ssiMu.Unlock()
		if err != nil {
			return err
		}
	}

	// M0093: invoke the xactMarker hook only when the transaction
	// was assigned a real XID. Read-only commits skip the hook
	// entirely — no WAL XactCommit record, no fsync, no clog
	// write. Mirrors PG's RecordTransactionCommit fast-path.
	if xid != storage.InvalidTransactionID {
		m.xactMarkerMu.RLock()
		hook := m.xactMarker
		m.xactMarkerMu.RUnlock()
		if hook != nil {
			if err := hook(xid, kind, waitLocalFlush); err != nil {
				return fmt.Errorf("mvcc: xact-marker hook (xid=%d, kind=%v): %w", xid, kind, err)
			}
		}
	}

	// Clear the slot.
	s.xid.Store(0)
	s.firstSnap = nil
	s.pinnedSnap.Store(false)
	s.xmin.Store(^uint64(0))
	s.inTxn.Store(0)

	// M0100-0002: track rolled-back XIDs so their rows stay invisible
	// even after the XID falls below future snapshots' Xmin.
	if kind == XactAbort && xid != storage.InvalidTransactionID {
		m.abortedMu.Lock()
		m.abortedXIDs = insertSortedXID(m.abortedXIDs, xid)
		m.abortedMu.Unlock()
	}

	// Drop any deadlock-victim waiter-release marker now that the slot is
	// genuinely finished. Design 0118-0115 (intra-grant-inplace perm 8).
	if xid != storage.InvalidTransactionID {
		m.releasedWaiterMu.Lock()
		if m.releasedWaiterXIDs != nil {
			delete(m.releasedWaiterXIDs, xid)
		}
		m.releasedWaiterMu.Unlock()
	}

	// M0104-0002: release SSI bookkeeping for SERIALIZABLE
	// transactions (commit or abort). Stamps FinishedAt with the
	// next CommitSeqNo and deletes the entry from the registry.
	// RC/RR transactions skip this branch.
	if tx.Isolation == IsolationSerializable {
		m.ssiMu.Lock()
		m.releaseSerializableLocked(tx.Handle, kind == XactCommit)
		m.ssiMu.Unlock()
	}

	// Broadcast to unblock any goroutine waiting in WaitForXID.
	m.waitMu.Lock()
	m.commitCond.Broadcast()
	m.waitMu.Unlock()

	if xid != storage.InvalidTransactionID {
		m.onTxnEndMu.RLock()
		hook := m.onTxnEnd
		m.onTxnEndMu.RUnlock()
		if hook != nil {
			hook(xid)
		}
	}

	return nil
}

// SetOnTxnEnd registers a callback fired after every transaction commit or
// abort (once the slot is cleared and WaitForXID waiters are unblocked).
// Only one callback is supported; a second call replaces the first.
func (m *Manager) SetOnTxnEnd(fn func(xid storage.TransactionID)) {
	m.onTxnEndMu.Lock()
	m.onTxnEnd = fn
	m.onTxnEndMu.Unlock()
}

// WaitForXID blocks until the transaction identified by xid is no longer
// in-progress (committed or aborted) or ctx is cancelled.  Returns nil when
// the transaction has finished, or ctx.Err() on cancellation.
//
// This is used by the ON CONFLICT executor to implement the "speculative
// insert" row-wait: when an upsert detects that a conflicting row belongs
// to an in-progress transaction, it blocks here until that transaction
// commits or aborts before re-evaluating the conflict.
func (m *Manager) WaitForXID(ctx context.Context, xid storage.TransactionID) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Wake up the Cond.Wait below so it can check ctx.
			m.commitCond.Broadcast()
		case <-done:
		}
	}()
	defer close(done)

	// Arm the session's lock_timeout, if set. The deadline is taken from the
	// start of THIS wait (mirroring upstream's per-ProcSleep arming); when it
	// elapses the wait returns ErrLockTimeout so the executor emits
	// "canceling statement due to lock timeout", distinct from a
	// statement_timeout that arrives via ctx cancellation. M0118-0009.
	lockTimeout, hasLockTimeout := lockwait.Timeout(ctx)
	var lockDeadline time.Time
	if hasLockTimeout {
		lockDeadline = time.Now().Add(lockTimeout)
		lt := time.AfterFunc(lockTimeout, func() { m.commitCond.Broadcast() })
		defer lt.Stop()
	}

	m.waitMu.Lock()
	defer m.waitMu.Unlock()
	for m.xidActiveWithSubxact(xid) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if hasLockTimeout && !time.Now().Before(lockDeadline) {
			return lockwait.ErrLockTimeout
		}
		m.commitCond.Wait()
	}
	return ctx.Err()
}

// WaitForOlderSlotsToCommit waits until every backend slot that was in an
// active transaction (inTxn==1) at the time of the call has finished its
// transaction (inTxn back to 0). The caller's own slot (selfHandle) is
// excluded to prevent the caller from waiting for itself.
//
// Used by DROP INDEX CONCURRENTLY to drain all snapshots that were open
// before the drop: once this returns, no transaction that started before
// the DROP is still running, so the index may be safely removed.
// M0100-0009.
func (m *Manager) WaitForOlderSlotsToCommit(ctx context.Context, selfHandle TxnHandle) error {
	return m.WaitForSlotsToCommit(ctx, m.SnapshotActiveOtherSlots(selfHandle))
}

// WaitForPinnedSnapshotsToCommit waits until every OTHER backend that holds a
// transaction-pinned MVCC snapshot (REPEATABLE READ / SERIALIZABLE) has
// finished its transaction. Used by DETACH PARTITION CONCURRENTLY together with
// the relation-locker wait: an RR/SSI session keeps a snapshot that still sees
// the partition attached even when it holds no table lock (e.g. it only
// PREPAREd a statement), so the detacher must wait for it. READ COMMITTED
// sessions are deliberately excluded — their per-statement snapshot is released
// between steps, so a durable txn-scoped relation lock (waitForRelationLockers)
// is the correct proxy for "still using the partition", NOT mere
// in-transaction status. This is what lets a READ COMMITTED session that only
// issued BEGIN (no table access) avoid blocking the detacher. Design 0118-0060
// (M0118-0008 detach-partition-concurrently-2 permutation 5).
func (m *Manager) WaitForPinnedSnapshotsToCommit(ctx context.Context, selfHandle TxnHandle) error {
	return m.WaitForPinnedSnapshotsReleased(ctx, m.SnapshotActiveOtherPinnedSlots(selfHandle))
}

// WaitForPinnedSnapshotsReleased blocks until every slot in active has released
// its transaction-pinned (RR/SSI) snapshot, or ctx is cancelled. A slot's
// snapshot is released either when its transaction ends (commit/rollback clears
// pinnedSnap in End) OR when a statement errors at the top level and the
// transaction enters the aborted state (ReleasePinnedSnapshot clears pinnedSnap
// while inTxn stays 1 until the eventual ROLLBACK). The latter mirrors
// PostgreSQL's AbortTransaction, which drops the transaction snapshot the moment
// a top-level statement errors — so a concurrent DETACH PARTITION CONCURRENTLY
// that was waiting for an RR session's snapshot unblocks as soon as that session
// hits an error, BEFORE its explicit ROLLBACK/COMMIT (detach-partition-
// concurrently-4 permutation `s1brr s1s s2detach s1insert s1c`, where s1insert's
// FK error must let s2detach complete ahead of s1c). Design 0118-0063.
func (m *Manager) WaitForPinnedSnapshotsReleased(ctx context.Context, active []int) error {
	if len(active) == 0 {
		return nil
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			m.commitCond.Broadcast()
		case <-done:
		}
	}()
	defer close(done)

	m.waitMu.Lock()
	defer m.waitMu.Unlock()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		allDone := true
		for _, i := range active {
			s := &m.procArray.slots[i]
			if s.inTxn.Load() == 1 && s.pinnedSnap.Load() {
				allDone = false
				break
			}
		}
		if allDone {
			return nil
		}
		m.commitCond.Wait()
	}
}

// ReleasePinnedSnapshot clears the transaction-pinned snapshot marker on the
// slot identified by handle without ending the transaction (inTxn stays 1). It
// is called when a statement errors at the top level of an explicit transaction
// (no open savepoint), mirroring PostgreSQL's AbortTransaction releasing the
// transaction snapshot at abort: the aborted transaction can only ROLLBACK from
// here, so its RR/SSI snapshot will never be used again and a concurrent waiter
// (WaitForPinnedSnapshotsReleased, e.g. DETACH PARTITION CONCURRENTLY) must stop
// waiting for it. The broadcast wakes any such waiter to re-check. A no-op when
// the slot held no pinned snapshot (RC sessions, or a slot already released).
// Design 0118-0063 (M0118-0008 detach-partition-concurrently-4).
func (m *Manager) ReleasePinnedSnapshot(handle TxnHandle) {
	idx := int(handle) - 1
	if idx < 0 || idx >= len(m.procArray.slots) {
		return
	}
	s := &m.procArray.slots[idx]
	if s.pinnedSnap.Swap(false) {
		m.waitMu.Lock()
		m.commitCond.Broadcast()
		m.waitMu.Unlock()
	}
}

// SnapshotActiveOtherPinnedSlots is SnapshotActiveOtherSlots filtered to slots
// that hold a transaction-pinned (RR/SSI) snapshot. See
// WaitForPinnedSnapshotsToCommit.
func (m *Manager) SnapshotActiveOtherPinnedSlots(selfHandle TxnHandle) []int {
	selfIdx := int(selfHandle) - 1 // Handle = procNum+1
	active := make([]int, 0, 4)
	for i := range m.procArray.slots {
		if i == selfIdx {
			continue
		}
		s := &m.procArray.slots[i]
		if s.inTxn.Load() == 1 && s.pinnedSnap.Load() {
			active = append(active, i)
		}
	}
	return active
}

// SnapshotActiveOtherSlots returns the indices of every backend slot that is
// currently in an active transaction (inTxn==1), excluding the caller's own
// slot (selfHandle). Capturing the active set is split from the wait so a
// caller can snapshot at one point (e.g. the start of CREATE INDEX
// CONCURRENTLY, before it might itself block) and drain that fixed set later.
// Waiting on a start-time snapshot — rather than re-scanning at wait time —
// prevents two CONCURRENTLY builds that begin nearly simultaneously from each
// waiting on the other (a mutual wait): each only ever waits for transactions
// that were already running when it started.
func (m *Manager) SnapshotActiveOtherSlots(selfHandle TxnHandle) []int {
	selfIdx := int(selfHandle) - 1 // Handle = procNum+1
	active := make([]int, 0, 4)
	for i := range m.procArray.slots {
		if i == selfIdx {
			continue
		}
		if m.procArray.slots[i].inTxn.Load() == 1 {
			active = append(active, i)
		}
	}
	return active
}

// WaitForSlotsToCommit blocks until every slot in active has finished its
// transaction (inTxn back to 0), or ctx is cancelled, or the session's
// lock_timeout budget carried on ctx (lockwait.Timeout) elapses. The active set
// is the caller's responsibility to capture (see SnapshotActiveOtherSlots). A
// nil/empty set returns immediately.
//
// The lock_timeout arm mirrors the heavyweight lock manager (lockmgr.ProcSleep):
// a CREATE INDEX CONCURRENTLY that parks here waiting on a still-running
// transaction — including a prepared transaction whose slot stays active until
// COMMIT/ROLLBACK PREPARED — is cancelled with lockwait.ErrLockTimeout once the
// budget elapses, which the executor maps to "canceling statement due to lock
// timeout", independent of statement_timeout (prepared-transactions-cic
// isolation spec, M0118-0009).
func (m *Manager) WaitForSlotsToCommit(ctx context.Context, active []int) error {
	if len(active) == 0 {
		return nil
	}

	// Arm the session's lock_timeout, if one is carried on ctx. The timer is
	// re-armed from the moment this wait begins, matching upstream's
	// enable_timeout_after(LOCK_TIMEOUT) at the top of ProcSleep.
	var lockTimeoutCh <-chan time.Time
	if d, ok := lockwait.Timeout(ctx); ok {
		lt := time.NewTimer(d)
		defer lt.Stop()
		lockTimeoutCh = lt.C
	}

	timedOut := make(chan struct{})
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			m.commitCond.Broadcast()
		case <-lockTimeoutCh:
			close(timedOut)
			m.commitCond.Broadcast()
		case <-done:
		}
	}()
	defer close(done)

	m.waitMu.Lock()
	defer m.waitMu.Unlock()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-timedOut:
			return lockwait.ErrLockTimeout
		default:
		}
		allDone := true
		for _, i := range active {
			if m.procArray.slots[i].inTxn.Load() == 1 {
				allDone = false
				break
			}
		}
		if allDone {
			return nil
		}
		m.commitCond.Wait()
	}
}

// xidInProgress reports whether xid is assigned to any currently active
// transaction. Lock-free: uses atomic loads on procArray slots.
func (m *Manager) xidInProgress(xid storage.TransactionID) bool {
	for i := range m.procArray.slots {
		if m.procArray.slots[i].xid.Load() == uint64(xid) {
			return true
		}
	}
	return false
}

// IsXIDActive reports whether xid belongs to a currently-running
// transaction. Safe to call from any goroutine; uses atomic loads
// internally. Used by upsertOp.findInProgressConflict to detect
// heap tuples whose xmin was materialised after the caller's snapshot
// was captured (M0100-0002).
func (m *Manager) IsXIDActive(xid storage.TransactionID) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	return m.xidActiveWithSubxact(xid)
}

// xidActiveWithSubxact reports whether xid belongs to a currently-running
// transaction, treating a subtransaction xid as active iff its top-level parent
// is still running AND the subxid has not been individually rolled back
// (ROLLBACK TO SAVEPOINT). Subxact xids are deliberately not held in the
// proc-array (see AllocateSubXid), so xidInProgress alone reports every subxid
// as dead; row-lock liveness (executor activeLockHolders / WaitForXID) needs the
// resolved view so a lock acquired inside a savepoint keeps blocking conflicting
// waiters until that savepoint is released or rolled back. The top-level fast
// path returns first, so behaviour for ordinary (non-subxact) xids is byte-for-
// byte unchanged. execRollbackTo marks every discarded savepoint level aborted,
// so a per-subxid IsAborted check is sufficient (no ancestry walk needed).
// M0118-0004 (docs/design/0118-0012).
func (m *Manager) xidActiveWithSubxact(xid storage.TransactionID) bool {
	if m.xidWaitersReleased(xid) {
		return false
	}
	if m.xidInProgress(xid) {
		return true
	}
	top := m.TopLevelXid(xid)
	if top == xid {
		// Not a subxid (or unresolved) — already shown not in progress above.
		return false
	}
	if m.xidWaitersReleased(top) {
		return false
	}
	return m.xidInProgress(top) && !m.IsAborted(xid)
}

// xidWaitersReleased reports whether xid was released for WaitForXID/IsXIDActive
// purposes by a deadlock-victim statement abort while its slot stays open
// (ReleaseXIDWaiters). Design 0118-0115 (intra-grant-inplace perm 8).
func (m *Manager) xidWaitersReleased(xid storage.TransactionID) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	m.releasedWaiterMu.RLock()
	defer m.releasedWaiterMu.RUnlock()
	if m.releasedWaiterXIDs == nil {
		return false
	}
	_, ok := m.releasedWaiterXIDs[xid]
	return ok
}

// ReleaseXIDWaiters marks xid as no longer active for the purpose of WaitForXID /
// IsXIDActive and wakes every blocked waiter, WITHOUT clearing xid's proc-array
// slot. It is the in-place half of PostgreSQL's AbortTransaction for a deadlock
// victim: the victim's transaction block stays open (so its eventual explicit
// COMMIT/ROLLBACK still finalises the slot through the canonical path) but a peer
// blocked on the victim's catalog-tuple xmax — the intra-grant-inplace pg_class
// in-place wait — unblocks at the abort rather than at the victim's later
// ROLLBACK. The victim is write-less in every spec that uses this (its statement
// errored before writing), so leaving snapshot visibility to read the still-open
// slot/CLOG unchanged is correct. The marker is cleared when the slot is finished.
// Design 0118-0115 (intra-grant-inplace perm 8).
func (m *Manager) ReleaseXIDWaiters(xid storage.TransactionID) {
	if xid == storage.InvalidTransactionID {
		return
	}
	m.releasedWaiterMu.Lock()
	if m.releasedWaiterXIDs == nil {
		m.releasedWaiterXIDs = make(map[storage.TransactionID]struct{})
	}
	m.releasedWaiterXIDs[xid] = struct{}{}
	m.releasedWaiterMu.Unlock()

	m.waitMu.Lock()
	m.commitCond.Broadcast()
	m.waitMu.Unlock()
}

// HasAbortedXID reports whether xid is recorded in the manager's aborted
// set (transactions that ran Rollback). Lives next to IsXIDActive because
// callers typically pair the two: WaitForXID returns once the xact is
// settled, and the caller then asks "did it commit or abort?" — committed
// is "not active AND not aborted"; aborted is HasAbortedXID. Used by the
// FK-on-delete wait path to translate "in-flight child INSERT settled" into
// the correct post-wait action: commit → raise 40001 in RR/Serializable,
// abort → no FK conflict.
func (m *Manager) HasAbortedXID(xid storage.TransactionID) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	m.abortedMu.RLock()
	defer m.abortedMu.RUnlock()
	n := len(m.abortedXIDs)
	if n == 0 {
		return false
	}
	idx := sort.Search(n, func(i int) bool { return m.abortedXIDs[i] >= xid })
	return idx < n && m.abortedXIDs[idx] == xid
}

// XidVisibilityStatus is the coarse, snapshot-independent commit classification
// of a transaction id. It mirrors the in-range cases of upstream's
// get_xid_status (verify_heapam.c) and exists for callers — chiefly amcheck's
// verify_heapam HOT-chain checks — that need to know whether a past xid
// committed, aborted, or is still running, rather than whether it is visible
// under one snapshot. The current-backend's-own-xid case (upstream
// XID_IS_CURRENT_XID) is the caller's to detect, because the Manager classifies
// it as in-progress (it is in the proc array) and only the caller knows its own
// XID.
type XidVisibilityStatus int

const (
	// XidVisUnknown means the xid is out of the cluster's valid range
	// (invalid, or not yet assigned) and so undeterminable, matching upstream's
	// xmin_commit_status_ok == false.
	XidVisUnknown XidVisibilityStatus = iota
	// XidVisInProgress means a currently-running transaction owns xid.
	XidVisInProgress
	// XidVisCommitted means xid is settled and committed.
	XidVisCommitted
	// XidVisAborted means xid is settled and aborted (rolled back / crashed).
	XidVisAborted
)

// ClassifyXID resolves xid's commit status from the proc array (in-progress),
// the in-memory aborted set, and the durable CLOG, without reference to any
// snapshot. It is the seam amcheck's verify_heapam SRF wires to: committed /
// aborted feed the clog-dependent HOT-chain checks, in-progress gates the
// in-progress branch, and an out-of-range xid disables those checks for the
// tuple (mirroring upstream get_xid_status returning xmin_commit_status_ok ==
// false). A normal xid below NextXID that is neither active nor recorded as
// aborted is treated as committed, matching snapshot visibility's "settled and
// not aborted ⇒ committed" assumption (and TransactionIdDidCommit, which yields
// committed for any in-range xid without an aborted CLOG entry).
func (m *Manager) ClassifyXID(xid storage.TransactionID) XidVisibilityStatus {
	if xid == storage.InvalidTransactionID {
		return XidVisUnknown
	}
	// Not yet assigned ⇒ out of the cluster's valid range (upstream's
	// "xid >= next_xid" bound in get_xid_status).
	if xid >= m.NextXID() {
		return XidVisUnknown
	}
	if m.xidInProgress(xid) {
		return XidVisInProgress
	}
	// A rolled-back xact may have no durable CLOG entry yet; the in-memory
	// aborted list is authoritative for this server's lifetime.
	if m.HasAbortedXID(xid) {
		return XidVisAborted
	}
	m.abortedMu.RLock()
	clog := m.clog
	m.abortedMu.RUnlock()
	if clog != nil {
		switch clog.GetStatus(xid) {
		case TxnStatusCommitted:
			return XidVisCommitted
		case TxnStatusAborted:
			return XidVisAborted
		}
	}
	return XidVisCommitted
}

// XactMarker discriminates the two transaction-end markers fed to
// the M0008 logical decoder via SetXactMarkerLogger. Mirrors the
// upstream xact-end records.
type XactMarker int

const (
	// XactCommit is the kind passed to the hook from Commit.
	XactCommit XactMarker = iota
	// XactAbort is the kind passed to the hook from Rollback.
	XactAbort
)

func (k XactMarker) String() string {
	switch k {
	case XactCommit:
		return "commit"
	case XactAbort:
		return "abort"
	}
	return "unknown"
}

// SetXactMarkerLogger installs a callback the manager invokes on
// every Commit / Rollback before removing the xact from the
// active set. The hook is the seam the M0008 logical decoder
// uses to learn about transaction boundaries — production wires
// it to wal.Writer.Append(EncodeXactCommit/Abort(xid)) so the
// classifier sees commit/abort markers in the WAL stream.
//
// Pass nil to clear a previously installed hook. Tests that care
// only about MVCC mechanics typically leave it unset. The third
// parameter (waitLocalFlush) is true for Commit/Rollback and false for
// CommitAsync (M0117-0007 Part B); see the xactMarker field doc.
func (m *Manager) SetXactMarkerLogger(fn func(storage.TransactionID, XactMarker, bool) error) {
	m.xactMarkerMu.Lock()
	defer m.xactMarkerMu.Unlock()
	m.xactMarker = fn
}

// SetRelcacheInvalPending marks the current transaction as having written to
// a nailed catalog relation (pg_class, pg_attribute, pg_proc, or pg_type).
// The xact-marker hook reads this flag at commit time to choose between
// EncodeXactCommit and EncodeXactCommitInval and to unlink both
// pg_internal.init files so the next backend reloads fresh descriptors.
func (m *Manager) SetRelcacheInvalPending() {
	m.relcacheInvalPending.Store(true)
}

// TakeRelcacheInvalPending atomically reads and clears the relcache-inval
// pending flag. Returns true if the flag was set, meaning the committing
// transaction touched a nailed catalog relation.
func (m *Manager) TakeRelcacheInvalPending() bool {
	return m.relcacheInvalPending.Swap(false)
}

// captureSnapshot builds a consistent snapshot with a lock-free walk of
// the proc-array slots.
func (m *Manager) captureSnapshot() Snapshot {
	xmax := m.xidgen.Peek()
	inProgress := make([]storage.TransactionID, 0, 16)
	xmin := xmax
	for i := range m.procArray.slots {
		xid := storage.TransactionID(m.procArray.slots[i].xid.Load())
		if xid == 0 || xid >= xmax {
			continue
		}
		inProgress = append(inProgress, xid)
		if xid < xmin {
			xmin = xid
		}
	}
	if len(inProgress) == 0 {
		xmin = xmax
	}
	sort.Slice(inProgress, func(i, j int) bool { return inProgress[i] < inProgress[j] })

	// M0100-0002: include ALL aborted XIDs in the snapshot so rolled-back
	// rows remain invisible even when their xmin falls below Xmin.
	m.abortedMu.RLock()
	var aborted []storage.TransactionID
	if len(m.abortedXIDs) > 0 {
		aborted = make([]storage.TransactionID, len(m.abortedXIDs))
		copy(aborted, m.abortedXIDs)
	}
	// M0117-0002: attach the durable commit log (nil unless SetCLog was called)
	// so the snapshot can fall back to the CLOG for in-window XIDs the in-memory
	// arrays cannot classify.
	clog := m.clog
	m.abortedMu.RUnlock()

	return Snapshot{
		Xmin:                 xmin,
		Xmax:                 xmax,
		InProgress:           inProgress,
		Aborted:              aborted,
		clog:                 clog,
		PartitionDetachEpoch: CurrentPartitionDetachEpoch(),
	}
}

// insertSortedXID inserts xid into a sorted slice of XIDs (ascending).
func insertSortedXID(s []storage.TransactionID, xid storage.TransactionID) []storage.TransactionID {
	idx := sort.Search(len(s), func(i int) bool { return s[i] >= xid })
	if idx < len(s) && s[idx] == xid {
		return s // already present
	}
	s = append(s, 0)
	copy(s[idx+1:], s[idx:])
	s[idx] = xid
	return s
}

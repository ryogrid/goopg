package mvcc

import (
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// CommitSeqNo is a monotonically increasing identifier for the
// order in which serializable transactions finished. It mirrors
// PostgreSQL's `SerCommitSeqNo` (predicate_internals.h); the order
// is used by the rw-conflict / dangerous-structure check that lands
// in M0104-0006 to decide which conflict orientation is dangerous.
//
// Values are dense and start at 1; 0 (`InvalidCommitSeqNo`) means
// "not finished yet" and is the zero value for an in-flight
// SerializableXact.
type CommitSeqNo uint64

const (
	// InvalidCommitSeqNo is the zero value, meaning the SSI xact
	// has not yet completed (commit or abort).
	InvalidCommitSeqNo CommitSeqNo = 0
)

// SerializableXact is the per-transaction SSI bookkeeping object,
// the goopg analogue of PostgreSQL's `SERIALIZABLEXACT` struct in
// `src/include/storage/predicate_internals.h`.
//
// M0104-0002 introduces the lifecycle plumbing: an instance is
// created when a SERIALIZABLE transaction Begins and destroyed
// when it finishes (commit or rollback). Conflict edges and
// predicate locks are NOT populated in this slice — that wiring
// is staged for M0104-0003..0005. The fields are declared now so
// later slices can populate them without changing the lifecycle
// API or callers that already register/observe SerializableXact.
//
// SerializableXact must be accessed through Manager.SerializableXact
// (or under m.mu when called from inside Manager). Direct field
// mutation from outside the mvcc package is not supported.
type SerializableXact struct {
	// Handle is the owning transaction's manager-internal handle.
	// Stable for the SerializableXact's lifetime.
	Handle TxnHandle

	// XID is the assigned top-level XID, or InvalidTransactionID
	// while the SERIALIZABLE transaction is still read-only. Mirrors
	// PostgreSQL's `SERIALIZABLEXACT.topXid`; updated atomically by
	// Manager.AssignXID when the transaction first writes.
	XID storage.TransactionID

	// FinishedAt records the commit sequence number assigned when
	// the SERIALIZABLE transaction finishes. Zero (InvalidCommitSeqNo)
	// while in-flight. M0104-0006 uses the dense ordering to evaluate
	// the dangerous-structure test on the rw-conflict graph.
	FinishedAt CommitSeqNo

	// Doomed is set when M0104-0004/0005 conflict tracking detects a
	// rw-edge configuration that mandates rollback. The pre-commit
	// check (M0104-0006) consults this flag and aborts with SQLSTATE
	// 40001. M0104-0002 only allocates the field; production callers
	// never set it yet.
	Doomed bool

	// inConflicts / outConflicts are the rw-edge slices that
	// M0104-0004 / M0104-0005 will populate. The lifecycle slice
	// leaves them nil — the SSI graph is empty until conflict-in /
	// conflict-out hooks land.
	//
	// Stored as []*SerializableXact (not handles or XIDs) to match
	// PostgreSQL's SHM list layout, where each edge directly
	// addresses the peer SerializableXact. Cleanup on finish() must
	// rip the dying xact out of its peers' slices to prevent
	// dangling pointers once predicate.c-style sharing lands.
	inConflicts  []*SerializableXact
	outConflicts []*SerializableXact

	// predicateLocks is the list of SIREAD predicate-lock targets
	// owned by this transaction (M0104-0003 will populate). The
	// lifecycle slice declares the slot but never appends; cleanup
	// on finish() releases it.
	predicateLocks []predicateLockRef
}

// predicateLockRef is an opaque forward declaration for the
// predicate-lock target type that M0104-0003 will define. We expose
// it as an unexported type with no fields so the SerializableXact
// struct shape is stable across slices: M0104-0003 can fill in the
// concrete fields without changing SerializableXact's public surface.
type predicateLockRef struct{}

// IsActive reports whether the SerializableXact is still in-flight.
// Returns true while the owning transaction is in Manager.active,
// and false once finish() has run.
func (s *SerializableXact) IsActive() bool {
	return s != nil && s.FinishedAt == InvalidCommitSeqNo
}

// ssiState is the Manager-owned SSI registry. Embedded into Manager
// alongside subxactFields so the SSI plumbing is testable in
// isolation but shares Manager.mu for ordering with snapshot
// acquisition, AssignXID, and finish.
type ssiState struct {
	// xacts maps the SERIALIZABLE transaction's handle to its
	// SerializableXact bookkeeping object. Read-only/Read-committed
	// /Repeatable-read transactions never appear here.
	xacts map[TxnHandle]*SerializableXact

	// nextCommitSeqNo is the next CommitSeqNo that finish() will
	// stamp. Starts at 1; 0 is reserved as InvalidCommitSeqNo.
	nextCommitSeqNo CommitSeqNo

	// initOnce guards lazy allocation of the xacts map. Avoids
	// charging the map cost to RC/RR-only workloads.
	initOnce sync.Once
}

func (s *ssiState) ensureInit() {
	s.initOnce.Do(func() {
		s.xacts = map[TxnHandle]*SerializableXact{}
		s.nextCommitSeqNo = 1
	})
}

// registerSerializableLocked allocates and registers the
// SerializableXact for handle. Called from Manager.Begin under
// m.mu when iso == IsolationSerializable.
func (m *Manager) registerSerializableLocked(handle TxnHandle) *SerializableXact {
	m.ssiState.ensureInit()
	sx := &SerializableXact{
		Handle: handle,
		XID:    storage.InvalidTransactionID,
	}
	m.ssiState.xacts[handle] = sx
	return sx
}

// releaseSerializableLocked stamps the FinishedAt commit sequence
// number, removes the SerializableXact from the registry, and
// returns it for callers that want to observe the cleared state
// (tests, future logging). Called from Manager.finish under m.mu
// for every SERIALIZABLE transaction that reached the active set,
// regardless of commit vs abort outcome.
//
// Returns nil if the handle was never registered (e.g. RC/RR
// transactions).
func (m *Manager) releaseSerializableLocked(handle TxnHandle) *SerializableXact {
	if m.ssiState.xacts == nil {
		return nil
	}
	sx, ok := m.ssiState.xacts[handle]
	if !ok {
		return nil
	}
	sx.FinishedAt = m.ssiState.nextCommitSeqNo
	m.ssiState.nextCommitSeqNo++
	// M0104-0003+ will null out edge slices here when peers point
	// back; for now they are always nil, so an unconditional clear
	// is fine.
	sx.inConflicts = nil
	sx.outConflicts = nil
	sx.predicateLocks = nil
	delete(m.ssiState.xacts, handle)
	return sx
}

// SerializableXact returns the live SSI bookkeeping object for the
// given handle, or nil if none is registered. Safe for concurrent
// use; takes m.mu internally.
//
// The returned pointer is stable for the SerializableXact's
// lifetime (until finish releases it). Callers MUST NOT mutate
// fields directly — future slices (M0104-0004..0006) will expose
// dedicated mutator methods that handle locking and edge fixup.
func (m *Manager) SerializableXact(handle TxnHandle) *SerializableXact {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ssiState.xacts == nil {
		return nil
	}
	return m.ssiState.xacts[handle]
}

// SerializableXactCount returns the number of registered (in-flight)
// SerializableXact bookkeeping objects. Diagnostic and test helper;
// production callers should not depend on this number.
func (m *Manager) SerializableXactCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ssiState.xacts == nil {
		return 0
	}
	return len(m.ssiState.xacts)
}

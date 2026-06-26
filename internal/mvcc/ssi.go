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
// (or under m.ssiMu when called from inside Manager). Direct field
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

	// BeginAt is the dense sequence stamp assigned when the
	// SerializableXact registers (Begin). It mirrors the "snapshot
	// point" upstream tracks via SxactGlobalXmin: a committed xact C is
	// retained (M0118-0001) only while some still-active xact A began
	// before C finished (A.BeginAt < C.FinishedAt), because only such an
	// A can still form a dangerous rw-conflict with C. BeginAt and
	// FinishedAt share one monotonic counter so the two are directly
	// comparable for the overlap test in purgeFinishedSerializableLocked.
	BeginAt CommitSeqNo

	// SnapshotSeqNo is the dense counter watermark captured when this
	// SERIALIZABLE xact takes its FIRST statement snapshot (deferred to the
	// first non-BEGIN statement, NOT at Begin — see manager.SnapshotFor). It
	// mirrors PostgreSQL's SERIALIZABLEXACT.SeqNo.lastCommitBeforeSnapshot
	// (predicate.c): a peer P committed strictly before this xact's snapshot
	// iff P.FinishedAt < SnapshotSeqNo, because FinishedAt and SnapshotSeqNo
	// draw from the same monotonic nextCommitSeqNo (see committedBeforeSnapshot).
	// Used ONLY by the READ ONLY de-facto optimisation in onConflictCheckLocked
	// (a declared READ ONLY reader cannot close a dangerous structure to a T2
	// that committed after its snapshot). InvalidCommitSeqNo until the snapshot
	// is taken. M0118-0001 (receipt-report).
	SnapshotSeqNo CommitSeqNo

	// ConflictOut mirrors PostgreSQL's SXACT_FLAG_CONFLICT_OUT
	// (predicate.c). It is set at commit when this (now committed) xact
	// holds an rw-conflict OUT to an already-committed peer — i.e. it is
	// the W in `R -> W -> T2` with both W and T2 committed. A later
	// reader R that reads W's data then trips Case 1 of
	// onConflictCheckLocked. Only meaningful once FinishedAt is stamped.
	ConflictOut bool

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

	// predicateLocks is the set of SIREAD predicate-lock targets
	// owned by this transaction. M0104-0003 populates it via
	// Manager.AcquirePredicateLock; entries are pruned in place when
	// coarsening promotes finer locks. Stored as a set (not a slice)
	// so coverage checks and de-duplication on acquire are O(1) per
	// owned tag, and so detachPredicateLockLocked can remove a tag in
	// place during release/coarsening without slice reshuffling. The
	// map is nil for read-only-on-this-target xacts so RC/RR /
	// non-SERIALIZABLE workloads pay no allocation; first acquire
	// initialises it.
	predicateLocks map[PredicateLockTag]struct{}

	// ReadOnly / Deferrable record the declared transaction modes (BEGIN
	// ... READ ONLY DEFERRABLE), set by Manager.MarkSerializableModes right
	// after Begin. ReadOnly mirrors PostgreSQL's SXACT_FLAG_READ_ONLY: it marks
	// the xact as a non-writer for SSI purposes regardless of whether it has
	// assigned an XID yet, so a concurrent READ ONLY DEFERRABLE peer does not
	// wait on it. Deferrable requests the GetSafeSnapshot deferral in
	// SnapshotFor — the read-only xact blocks until every concurrent
	// declared-read-write SERIALIZABLE xact has finished, then takes a safe
	// snapshot that can never form a dangerous structure (so it is never
	// aborted with 40001). M0118-0001 (read-only-anomaly-3).
	ReadOnly   bool
	Deferrable bool

	// Prepared mirrors PostgreSQL's SXACT_FLAG_PREPARED. It is set when a
	// SERIALIZABLE transaction passes its pre-commit dangerous-structure check
	// at PREPARE TRANSACTION time (same-backend 2PC, M0118-0009) and stays open
	// awaiting COMMIT/ROLLBACK PREPARED. A still-in-flight pivot that is already
	// Prepared cannot be doomed by a later committer (the pivot is durably
	// committed-to-disk in upstream and may yet COMMIT PREPARED), so that
	// committer must abort itself instead — see
	// preCommitCheckForSerializationFailureLocked (predicate.c line ~4756,
	// "Canceled on commit attempt with conflict in from prepared pivot").
	// Only set on the PREPARE path; a normal COMMIT finishes immediately and is
	// gated out by FinishedAt, so it never needs this flag.
	Prepared bool

	// PrepareSeqNo is the dense sequence stamp assigned when Prepared is set,
	// drawn from the same nextCommitSeqNo counter as FinishedAt/BeginAt/
	// SnapshotSeqNo. It mirrors PostgreSQL's SERIALIZABLEXACT.prepareSeqNo
	// (predicate.c). For a *committed* xact upstream tracks prepareSeqNo and
	// commitSeqNo separately; goopg collapses both into FinishedAt for the
	// already-passing specs, so prepareSeqNo(sx) returns FinishedAt once the xact
	// commits and PrepareSeqNo only while it is prepared-but-not-committed.
	// InvalidCommitSeqNo until PREPARE TRANSACTION. M0118-0009.
	PrepareSeqNo CommitSeqNo
}

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
	// SerializableXact bookkeeping object. It holds only ACTIVE
	// (in-flight) xacts — committed xacts are moved to `finished` and
	// aborted xacts are dropped, both at release. Read-only/Read-committed
	// /Repeatable-read transactions never appear here.
	xacts map[TxnHandle]*SerializableXact

	// finished retains COMMITTED SerializableXacts (M0118-0001) past
	// their commit so the dangerous-structure check can still reach them
	// while a concurrent xact might form a new rw-conflict against them
	// (the `R -> W -> T2` / `T0 -> R -> W` pivots where a partner has
	// already committed). Mirrors upstream's deferred cleanup of finished
	// SERIALIZABLEXACTs (predicate.c ClearOldPredicateLocks /
	// SxactGlobalXmin). Stored as a pointer slice — NOT keyed by handle —
	// because proc-slot handles are reused by later Begins while a
	// committed xact is still retained; rw-edges already address peers by
	// *SerializableXact, so the pointer slice has no handle-aliasing
	// hazard. purgeFinishedSerializableLocked drops entries once no active
	// xact overlaps them. Aborted xacts are never retained here.
	finished []*SerializableXact

	// nextCommitSeqNo is the next dense sequence number stamped onto
	// BeginAt (at register) and FinishedAt (at finish). Starts at 1; 0 is
	// reserved as InvalidCommitSeqNo. A single shared counter keeps
	// BeginAt and FinishedAt comparable for the retention overlap test.
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
// m.ssiMu when iso == IsolationSerializable.
func (m *Manager) registerSerializableLocked(handle TxnHandle) *SerializableXact {
	m.ssiState.ensureInit()
	// Drop any retained committed xacts whose overlap window has closed
	// before reusing this proc-slot handle: the new xact begins strictly
	// after every already-finished xact, so a stale committed xact at the
	// same handle (or any non-overlapping one) is no longer reachable for
	// a dangerous structure and must be scrubbed from its peers.
	m.purgeFinishedSerializableLocked()
	sx := &SerializableXact{
		Handle:  handle,
		XID:     storage.InvalidTransactionID,
		BeginAt: m.ssiState.nextCommitSeqNo,
	}
	m.ssiState.nextCommitSeqNo++
	m.ssiState.xacts[handle] = sx
	return sx
}

// releaseSerializableLocked stamps the FinishedAt commit sequence
// number, removes the SerializableXact from the registry, and
// returns it for callers that want to observe the cleared state
// (tests, future logging). Called from Manager.finish under m.ssiMu
// for every SERIALIZABLE transaction that reached the active set,
// regardless of commit vs abort outcome.
//
// Returns nil if the handle was never registered (e.g. RC/RR
// transactions).
func (m *Manager) releaseSerializableLocked(handle TxnHandle, committed bool) *SerializableXact {
	if m.ssiState.xacts == nil {
		return nil
	}
	sx, ok := m.ssiState.xacts[handle]
	if !ok {
		return nil
	}
	// M0104-0003 / M0118-0001: clear the GLOBAL predicate-lock holder sets of
	// this xact's handle before stamping FinishedAt, while the SerializableXact
	// is still addressable through the registry. The handle-keyed holder sets
	// must never name a retained committed xact (a later Begin may reuse the
	// proc-slot handle and alias the lock).
	//
	// The two finish kinds differ in what survives:
	//   - COMMIT: detach from the global holders ONLY and KEEP sx.predicateLocks
	//     so checkForSerializableConflictInLocked's retained-xact (finished) walk
	//     can still discover this committed reader when a later writer touches a
	//     target it predicate-locked — the `R -> W` edge in the project-manager /
	//     classroom-scheduling phantom structures, where R commits before W
	//     writes. Mirrors PostgreSQL retaining a committed SERIALIZABLEXACT's
	//     predicate locks until cleanup.
	//   - ABORT: fully release (detach + null sx.predicateLocks); an aborted
	//     reader cannot participate in any committed dangerous structure.
	if committed {
		m.detachPredicateLocksFromGlobalLocked(handle, sx)
	} else {
		m.releasePredicateLocksLocked(handle)
	}

	sx.FinishedAt = m.ssiState.nextCommitSeqNo
	m.ssiState.nextCommitSeqNo++
	delete(m.ssiState.xacts, handle)

	// Wake any READ ONLY DEFERRABLE xact blocked in waitForSafeSnapshot: this
	// SERIALIZABLE xact just finished, which may have completed a waiter's
	// drain set. Broadcasting under m.ssiMu is safe (the waiter re-checks its
	// condition on wake). M0118-0001.
	m.ssiCondLocked().Broadcast()

	if committed {
		// M0118-0001: retain committed xacts so a later concurrent xact
		// can still discover the `R -> W -> T2` / `T0 -> R -> W` pivots in
		// which this xact is a committed partner. Keep the rw-edges intact;
		// purge defers the peer scrub until no active xact overlaps.
		//
		// Flag SXACT_FLAG_CONFLICT_OUT (predicate.c) if this xact holds an
		// out-conflict to an already-committed peer, so Case 1 of
		// onConflictCheckLocked can fire for a future reader of our data.
		for _, peer := range sx.outConflicts {
			if peer != nil && peer.FinishedAt != InvalidCommitSeqNo {
				sx.ConflictOut = true
				break
			}
		}
		m.ssiState.finished = append(m.ssiState.finished, sx)
		m.purgeFinishedSerializableLocked()
		return sx
	}

	// Aborted xacts cannot participate in a committed dangerous structure,
	// so scrub their edges from every peer immediately (mirrors the
	// rollback arm of upstream ReleaseOneSerializableXact) and drop them.
	removeSerializableXactFromPeersLocked(sx)
	sx.inConflicts = nil
	sx.outConflicts = nil
	m.purgeFinishedSerializableLocked()
	return sx
}

// purgeFinishedSerializableLocked drops every retained committed xact whose
// overlap window has closed — i.e. no still-active xact began before it
// finished, so no active xact can form a new rw-conflict against it. The peer
// scrub then removes its dangling references from any surviving (active or
// still-retained) xact. This is goopg's analogue of the SxactGlobalXmin-driven
// cleanup in predicate.c (ClearOldPredicateLocks / ReleaseOneSerializableXact):
// a committed xact is only useful for dangerous-structure detection while a
// concurrent xact that overlapped it is still live.
//
// Caller must hold m.ssiMu.
func (m *Manager) purgeFinishedSerializableLocked() {
	fin := m.ssiState.finished
	if len(fin) == 0 {
		return
	}
	var minActiveBegin CommitSeqNo
	hasActive := false
	for _, a := range m.ssiState.xacts {
		if a == nil {
			continue
		}
		if !hasActive || a.BeginAt < minActiveBegin {
			minActiveBegin = a.BeginAt
			hasActive = true
		}
	}
	keep := fin[:0]
	for _, c := range fin {
		if c == nil {
			continue
		}
		// Purgeable when every active xact began at or after c finished
		// (no overlap), or when nothing is active at all.
		if !hasActive || c.FinishedAt <= minActiveBegin {
			removeSerializableXactFromPeersLocked(c)
			c.inConflicts = nil
			c.outConflicts = nil
			continue
		}
		keep = append(keep, c)
	}
	for i := len(keep); i < len(fin); i++ {
		fin[i] = nil
	}
	m.ssiState.finished = keep
}

// SerializableXact returns the live SSI bookkeeping object for the
// given handle, or nil if none is registered. Safe for concurrent
// use; takes m.ssiMu internally.
//
// The returned pointer is stable for the SerializableXact's
// lifetime (until finish releases it). Callers MUST NOT mutate
// fields directly — future slices (M0104-0004..0006) will expose
// dedicated mutator methods that handle locking and edge fixup.
func (m *Manager) SerializableXact(handle TxnHandle) *SerializableXact {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return nil
	}
	return m.ssiState.xacts[handle]
}

// SerializableXactCount returns the number of registered (in-flight)
// SerializableXact bookkeeping objects. Diagnostic and test helper;
// production callers should not depend on this number.
func (m *Manager) SerializableXactCount() int {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return 0
	}
	return len(m.ssiState.xacts)
}

// ssiCondLocked returns the SSI finish-event condition variable, creating it on
// first use. Caller must hold m.ssiMu. The lazy init lets a Manager built as a
// bare struct literal (tests) participate in the safe-snapshot wait without
// going through NewManager.
func (m *Manager) ssiCondLocked() *sync.Cond {
	if m.ssiCond == nil {
		m.ssiCond = sync.NewCond(&m.ssiMu)
	}
	return m.ssiCond
}

// MarkSerializableModes records the declared READ ONLY / DEFERRABLE transaction
// modes on the SERIALIZABLE xact registered for handle. Called by the executor
// immediately after Begin when the BEGIN statement carried those modes. A no-op
// for RC/RR transactions (no SerializableXact) and for unregistered handles.
// Mirrors PostgreSQL setting SXACT_FLAG_READ_ONLY / SXACT_FLAG_DEFERRABLE in
// RegisterSerializableTransactionInt. M0118-0001.
func (m *Manager) MarkSerializableModes(handle TxnHandle, readOnly, deferrable bool) {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return
	}
	if sx, ok := m.ssiState.xacts[handle]; ok {
		sx.ReadOnly = readOnly
		sx.Deferrable = deferrable
	}
}

// stampSerializableSnapshotSeqNo records the current nextCommitSeqNo watermark
// as the xact's SnapshotSeqNo the first time it takes a statement snapshot,
// mirroring PostgreSQL capturing SeqNo.lastCommitBeforeSnapshot at snapshot
// acquisition (predicate.c GetSerializableTransactionSnapshotInt). It does NOT
// consume a counter value — the watermark is only a read so that a peer P that
// committed before this snapshot satisfies P.FinishedAt < SnapshotSeqNo. Called
// from SnapshotFor right after the first SERIALIZABLE snapshot is captured. A
// no-op for unregistered handles and once already stamped (the snapshot is
// pinned for the whole transaction). M0118-0001 (receipt-report).
func (m *Manager) stampSerializableSnapshotSeqNo(handle TxnHandle) {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return
	}
	if sx, ok := m.ssiState.xacts[handle]; ok && sx.SnapshotSeqNo == InvalidCommitSeqNo {
		sx.SnapshotSeqNo = m.ssiState.nextCommitSeqNo
	}
}

// waitForSafeSnapshot implements PostgreSQL's GetSafeSnapshot (predicate.c) for
// a SERIALIZABLE READ ONLY DEFERRABLE transaction: it blocks until every
// concurrent declared-read-write SERIALIZABLE transaction that was active when
// the wait began has finished (committed or aborted). Only then is it safe for
// the read-only transaction to take its snapshot — a snapshot taken after all
// concurrent writers have committed cannot place this transaction on the
// rw-conflict "out" side of a dangerous structure, so it never has to be rolled
// back with SQLSTATE 40001. This trades a one-time wait for the absence of
// false-positive serialization failures (the read-only-anomaly-3 scenario from
// the Fekete et al. paper).
//
// goopg only ever resolves the read-only xact to ROSafe (it drains the
// concurrent writers and proceeds); it does not implement the ROUnsafe
// early-abort/retry refinement, which no ported spec exercises (the sole
// DEFERRABLE spec resolves to a safe snapshot). The divergence is documented in
// docs/design/0118-0001.
//
// Caller must NOT hold m.ssiMu and must call this BEFORE capturing the
// snapshot. Returns immediately unless the xact for handle is both ReadOnly and
// Deferrable. Writers that start after the wait begins do not extend it —
// matching upstream, which only enrolls writers live at registration time.
func (m *Manager) waitForSafeSnapshot(handle TxnHandle) {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return
	}
	self, ok := m.ssiState.xacts[handle]
	if !ok || !self.ReadOnly || !self.Deferrable {
		return
	}
	// Enroll every concurrent declared-read-write SERIALIZABLE xact active now.
	// FinishedAt is stamped (and never cleared) on commit or abort, so holding
	// the pointers is safe even after the peer is deleted from the registry.
	var waitOn []*SerializableXact
	for h, sx := range m.ssiState.xacts {
		if h == handle || sx == nil || sx.ReadOnly {
			continue
		}
		waitOn = append(waitOn, sx)
	}
	cond := m.ssiCondLocked()
	for {
		pending := false
		for _, sx := range waitOn {
			if sx.FinishedAt == InvalidCommitSeqNo {
				pending = true
				break
			}
		}
		if !pending {
			return
		}
		cond.Wait()
	}
}

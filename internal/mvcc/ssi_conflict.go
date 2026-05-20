package mvcc

import (
	"github.com/goopg/goopg/internal/storage"
)

// CheckForSerializableConflictOut is the read-path SSI hook: when a
// SERIALIZABLE reader observes that a tuple version it just read was
// inserted (or last modified) by another transaction, this records an
// rw-conflict edge R → W between the reader R and the writer W.
//
// The edge is the goopg analogue of PostgreSQL's
// `CheckForSerializableConflictOut` in
// `postgres/src/backend/storage/lmgr/predicate.c`. An rw-conflict (also
// called an anti-dependency) of R → W means R read a row before W
// wrote it, so any serial execution must order R before W. The edge is
// installed in both directions: R's `outConflicts` gains W, and W's
// `inConflicts` gains R. Storing both directions matches upstream's
// `RWConflict` linkage and lets M0104-0006's dangerous-structure check
// walk in either direction in O(deg(node)).
//
// The call is a no-op (returns false) when:
//
//   - readerHandle is not a SERIALIZABLE in-flight xact (RC/RR or
//     already finished — RC/RR cannot participate in SSI cycles, and
//     finished xacts are scrubbed of their edges at release);
//   - writerXID is `InvalidTransactionID`, `BootstrapTransactionID`,
//     or `FrozenTransactionID` (system writers cannot participate in
//     SSI cycles);
//   - writerXID matches the reader's own assigned XID (self-modify is
//     not a conflict);
//   - the writer is not in the SSI registry — either it is a non-
//     SERIALIZABLE writer (RC/RR) which by definition cannot
//     participate in SSI cycles, or it has already finished. The
//     first-slice substrate releases predicate locks and SSI
//     bookkeeping at finish, so finished-but-still-conflict-relevant
//     retention is M0104-0006's concern; for now the call is silently
//     a no-op against finished writers.
//
// Returns true iff a new edge was installed; existing edges
// (idempotent calls) return false. The duplicate check is O(out-degree
// of R), which is bounded by the small number of concurrent
// SERIALIZABLE writers in practice.
func (m *Manager) CheckForSerializableConflictOut(readerHandle TxnHandle, writerXID storage.TransactionID) bool {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	return m.checkForSerializableConflictOutLocked(readerHandle, writerXID)
}

func (m *Manager) checkForSerializableConflictOutLocked(readerHandle TxnHandle, writerXID storage.TransactionID) bool {
	if writerXID == storage.InvalidTransactionID ||
		writerXID == BootstrapTransactionID ||
		writerXID == FrozenTransactionID {
		return false
	}
	if m.ssiState.xacts == nil {
		return false
	}
	reader, ok := m.ssiState.xacts[readerHandle]
	if !ok || reader == nil {
		return false
	}
	if reader.XID == writerXID {
		// Self-modify: the reader is observing a tuple it wrote
		// itself. No conflict — same xact.
		return false
	}
	writer := m.serializableXactByXIDLocked(writerXID)
	if writer == nil {
		// Either non-SERIALIZABLE writer (RC/RR), or the writer has
		// already finished and its bookkeeping has been released.
		// M0104-0006 will revisit retention so finished writers
		// remain conflict-relevant past their FinishedAt; for now
		// drop the edge silently.
		return false
	}
	if writer == reader {
		// Belt-and-suspenders: serializableXactByXIDLocked already
		// finds writer by XID, but a read-only SERIALIZABLE xact has
		// XID == Invalid and would never match here. Keeping the
		// pointer-equality check defensive against future API
		// changes.
		return false
	}
	return registerRWConflictLocked(reader, writer)
}

// serializableXactByXIDLocked returns the in-flight SerializableXact
// whose top-level XID equals xid, or nil if no such xact exists. The
// scan is O(active SERIALIZABLE xacts); upstream uses an OID-keyed
// hash table, but goopg's in-flight set is small enough (bounded by
// max_connections) that a linear scan is well under the cost of an
// extra map.
//
// Callers must hold m.ssiMu.
func (m *Manager) serializableXactByXIDLocked(xid storage.TransactionID) *SerializableXact {
	if xid == storage.InvalidTransactionID || m.ssiState.xacts == nil {
		return nil
	}
	for _, sx := range m.ssiState.xacts {
		if sx != nil && sx.XID == xid {
			return sx
		}
	}
	return nil
}

// registerRWConflictLocked installs an rw-conflict edge from → to in
// both directions: from.outConflicts gains to, and to.inConflicts
// gains from. Returns true iff the edge was new; an existing edge is
// a no-op (idempotent).
//
// Caller must hold m.ssiMu (the slices are not goroutine-safe — they're
// embedded in SerializableXact, which is only mutated under m.ssiMu).
func registerRWConflictLocked(from, to *SerializableXact) bool {
	for _, peer := range from.outConflicts {
		if peer == to {
			return false
		}
	}
	from.outConflicts = append(from.outConflicts, to)
	to.inConflicts = append(to.inConflicts, from)
	return true
}

// removeSerializableXactFromPeersLocked scrubs every reference to
// dying from the in/out-conflict slices of every peer it points at.
// Called from releaseSerializableLocked so the surviving peers do
// not retain dangling pointers once dying is removed from
// ssiState.xacts.
//
// Caller must hold m.ssiMu.
func removeSerializableXactFromPeersLocked(dying *SerializableXact) {
	for _, peer := range dying.outConflicts {
		peer.inConflicts = removeSerializableXactFromSlice(peer.inConflicts, dying)
	}
	for _, peer := range dying.inConflicts {
		peer.outConflicts = removeSerializableXactFromSlice(peer.outConflicts, dying)
	}
}

// removeSerializableXactFromSlice returns slice with all entries
// equal to target removed in place (preserving order is not required;
// callers use these slices as sets). Returns the same backing array
// to avoid an allocation; the unused tail is zeroed so the GC can
// reclaim the released SerializableXact when the dying xact is the
// last reference.
func removeSerializableXactFromSlice(slice []*SerializableXact, target *SerializableXact) []*SerializableXact {
	out := slice[:0]
	for _, sx := range slice {
		if sx == target {
			continue
		}
		out = append(out, sx)
	}
	for i := len(out); i < len(slice); i++ {
		slice[i] = nil
	}
	return out
}

// OutConflictCount returns the number of rw-conflict out-edges held
// by handle. Diagnostic helper for tests and future tooling; the
// pre-commit dangerous-structure check (M0104-0006) walks the slices
// directly. Returns 0 for non-SERIALIZABLE or unknown handles.
func (m *Manager) OutConflictCount(handle TxnHandle) int {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return 0
	}
	sx, ok := m.ssiState.xacts[handle]
	if !ok || sx == nil {
		return 0
	}
	return len(sx.outConflicts)
}

// InConflictCount returns the number of rw-conflict in-edges held by
// handle. Mirror of OutConflictCount on the receiving side. Returns 0
// for non-SERIALIZABLE or unknown handles.
func (m *Manager) InConflictCount(handle TxnHandle) int {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return 0
	}
	sx, ok := m.ssiState.xacts[handle]
	if !ok || sx == nil {
		return 0
	}
	return len(sx.inConflicts)
}

// CheckForSerializableConflictIn is the write-path SSI hook: when a
// SERIALIZABLE writer modifies a target identified by tag, this records
// an rw-conflict edge R → W for every SERIALIZABLE reader that holds a
// SIREAD predicate-lock covering the target. The edge orientation
// matches the read-path hook (`reader.outConflicts += writer`,
// `writer.inConflicts += reader`); only the discovery polarity differs
// — the write-path walks the predicate-lock holder set instead of
// looking up the writer by xmin/xmax.
//
// The call is the goopg analogue of PostgreSQL's
// `CheckForSerializableConflictIn` in
// `postgres/src/backend/storage/lmgr/predicate.c`. Coverage discovery
// matches upstream's "walk upward" pattern: holders on the exact tag,
// plus holders on any ancestor tag that covers it (page covers tuple,
// relation covers page and tuple). The substrate's covering map
// (`PredicateLockTag.Covers`) defines the hierarchy; the hook does not
// re-derive coverage rules.
//
// The call is a no-op (returns false) when:
//
//   - tag is invalid (Rel == 0);
//   - writerHandle is not a SERIALIZABLE in-flight xact (RC/RR or
//     already finished — RC/RR cannot participate in SSI cycles, and
//     finished writers are scrubbed of their edges at release);
//   - no holder is found on the exact or any covering tag (a frequent
//     hot-path case — most writes touch tags no concurrent reader
//     covers).
//
// Returns true iff at least one new edge was installed; existing edges
// (idempotent calls and self-references) do not count toward the
// return value. Per-holder duplicate checks are O(out-degree of the
// reader), bounded by the small number of concurrent SERIALIZABLE
// writers in practice.
func (m *Manager) CheckForSerializableConflictIn(writerHandle TxnHandle, tag PredicateLockTag) bool {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	return m.checkForSerializableConflictInLocked(writerHandle, tag)
}

func (m *Manager) checkForSerializableConflictInLocked(writerHandle TxnHandle, tag PredicateLockTag) bool {
	if tag.Granularity() == InvalidPredicateGranularity {
		return false
	}
	if m.ssiState.xacts == nil {
		return false
	}
	writer, ok := m.ssiState.xacts[writerHandle]
	if !ok || writer == nil {
		return false
	}
	if m.predicateLocks.targets == nil {
		return false
	}
	installed := false
	for _, ancestor := range coveringPredicateLockTags(tag) {
		tgt, ok := m.predicateLocks.targets[ancestor]
		if !ok {
			continue
		}
		for holder := range tgt.holders {
			if holder == writerHandle {
				// A SERIALIZABLE xact may legitimately hold a SIREAD on
				// a target it then writes; that is not a conflict with
				// itself.
				continue
			}
			reader, ok := m.ssiState.xacts[holder]
			if !ok || reader == nil {
				// Defensive: the holder slot should never outlive the
				// SerializableXact in ssiState.xacts because
				// releaseSerializableLocked drops predicate locks
				// before clearing the registry entry.
				continue
			}
			if registerRWConflictLocked(reader, writer) {
				installed = true
			}
		}
	}
	return installed
}

// coveringPredicateLockTags returns tag itself plus every coarser
// ancestor that, by the substrate's coverage hierarchy, would also
// imply SIREAD on tag. The list is the upward walk
// `tuple → page → relation`; finer descendants are deliberately not
// included because a writer touching a coarser target than the reader
// owns is conceptually rare in goopg's heap workload (writes are
// tuple-level), and PostgreSQL's `CheckForSerializableConflictIn`
// follows the same upward-only pattern via `GetParentPredicateLockTag`.
//
// Output ordering is finest-first; callers iterate the slice
// linearly so the iteration order is observable in tests but never
// load-bearing for correctness — `registerRWConflictLocked` is
// idempotent.
func coveringPredicateLockTags(tag PredicateLockTag) []PredicateLockTag {
	switch tag.Granularity() {
	case TupleGranularity:
		return []PredicateLockTag{
			tag,
			PageLockTag(tag.DB, tag.Rel, tag.Page),
			RelationLockTag(tag.DB, tag.Rel),
		}
	case PageGranularity:
		return []PredicateLockTag{
			tag,
			RelationLockTag(tag.DB, tag.Rel),
		}
	case RelationGranularity:
		return []PredicateLockTag{tag}
	default:
		return nil
	}
}

// HasRWConflict reports whether an rw-conflict edge from → to has
// been recorded. Diagnostic helper used by tests; production callers
// should not consult this number directly (use the conflict-graph
// walker that lands with M0104-0006).
func (m *Manager) HasRWConflict(from, to TxnHandle) bool {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return false
	}
	fromSX, ok := m.ssiState.xacts[from]
	if !ok || fromSX == nil {
		return false
	}
	toSX, ok := m.ssiState.xacts[to]
	if !ok || toSX == nil {
		return false
	}
	for _, peer := range fromSX.outConflicts {
		if peer == toSX {
			return true
		}
	}
	return false
}

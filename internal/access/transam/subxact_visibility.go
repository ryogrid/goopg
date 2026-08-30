package transam

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/goopg/goopg/internal/access/transam/multixact"
	"github.com/goopg/goopg/internal/storage"
)

// SubxactMap is an in-memory map from subxact XID to its parent XID
// and status. It mirrors upstream's pg_subtrans SLRU.
//
// By default the map lives entirely in memory (slru == nil) and parentage
// is lost on restart — the original v0 behaviour, preserved for every
// existing caller. When persistence is enabled via EnablePersistence (gap
// G5), each parent link is ALSO written through to an on-disk pg_subtrans
// SLRU mirror so it survives a restart, matching upstream subtrans.c.
//
// Abort status is intentionally NOT persisted here: PG's pg_subtrans only
// stores parent TransactionIds, never abort flags — abort status stays in
// CLOG / in-memory. See MarkAborted below.
//
// All operations are safe for concurrent use.
type SubxactMap struct {
	mu      sync.RWMutex
	parents map[storage.TransactionID]storage.TransactionID // subxid → parent xid
	aborted map[storage.TransactionID]bool                  // aborted subxids

	// nParents mirrors len(parents) so IsSubxact can answer the overwhelmingly
	// common "no subtransactions at all" case without taking mu.
	//
	// IsSubxact is called ONCE PER TUPLE on the visibility path
	// (TupleVisibleSubxact -> isCurrentTxXID). Under a 4-worker parallel scan
	// that RLock/RUnlock pair was 50 % of all atomic RMW traffic and ~5 % of
	// total CPU on TPC-H Q14, to probe a map that an OLAP query never populates.
	// (docs/design/not_ralph/perf-optimize-take6/README.md candidate B.)
	//
	// Kept in step with parents under mu; read without it. A zero count means
	// the map is empty, so the guarded lookup could only have returned false.
	nParents atomic.Int64

	// slru, when non-nil, is the persistent pg_subtrans mirror that parent
	// links are written through to. nil = persistence disabled (default).
	slru *SubtransSLRU
}

// NewSubxactMap constructs an empty map.
func NewSubxactMap() *SubxactMap {
	return &SubxactMap{
		parents: make(map[storage.TransactionID]storage.TransactionID),
		aborted: make(map[storage.TransactionID]bool),
	}
}

// EnablePersistence turns on the on-disk pg_subtrans SLRU mirror for this
// map (gap G5). After this call, every Register also writes the parent link
// through to dir so it survives a restart. Opt-in: NewSubxactMap leaves slru
// nil, so existing callers keep the pure in-memory behaviour unchanged.
// Idempotent enough to call once at startup; returns an error only if the
// SLRU directory cannot be opened/created.
//
// Only parent links are persisted — abort status stays in CLOG / in-memory
// (PG's pg_subtrans stores parent TransactionIds, not abort flags).
func (m *SubxactMap) EnablePersistence(dir string) error {
	slru, err := OpenSubtransSLRU(dir)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.slru = slru
	m.mu.Unlock()
	return nil
}

// SetFsyncDisabled mirrors `fsync = off` for the pg_subtrans mirror: parent
// links are still written through, but SetParent's fsync is skipped. Call
// after EnablePersistence; a nil SLRU (persistence disabled) is a no-op.
// Test harnesses only; see ci/design/test-gate-speedups/02.
func (m *SubxactMap) SetFsyncDisabled(disabled bool) {
	m.mu.RLock()
	slru := m.slru
	m.mu.RUnlock()
	if slru != nil {
		slru.fsyncDisabled.Store(disabled)
	}
}

// RestoreFromSLRU reloads every persisted parent link from the on-disk
// pg_subtrans SLRU into the in-memory map (gap G5 read path / restore-on-restart).
// It must be called after EnablePersistence; a nil SLRU returns an error so a
// mis-wired caller fails loudly rather than silently restoring nothing. Returns
// the number of links restored.
//
// Restored links are merged into the existing parents map (in-memory entries
// registered before the restore are preserved; a persisted link overwrites an
// in-memory entry for the same subxid, which cannot differ in a correct cluster).
// Abort status is NOT restored — PG's pg_subtrans stores only parent links, and
// abort/commit status lives in CLOG / in-memory (see Register / MarkAborted).
//
// Note: PostgreSQL zeroes pg_subtrans on startup (subtrans.c StartupSUBTRANS);
// goopg deliberately restores it so subxact parentage survives a restart for the
// G5 consumers (durable standby resolution, 2PC, post-backend-exit readers). This
// only redirects a sub-XID to its top-level ancestor; the ancestor's own
// commit/abort decision is unchanged, so it never makes a tuple more visible.
func (m *SubxactMap) RestoreFromSLRU() (int, error) {
	m.mu.RLock()
	slru := m.slru
	m.mu.RUnlock()
	if slru == nil {
		return 0, fmt.Errorf("subxact map: RestoreFromSLRU called before EnablePersistence")
	}
	links, err := slru.ScanParents()
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	for subxid, parent := range links {
		m.parents[subxid] = parent
		m.nParents.Store(int64(len(m.parents)))
	}
	m.mu.Unlock()
	return len(links), nil
}

// Truncate discards in-memory parent/abort entries for every subxid strictly
// older than oldestXact and, when persistence is enabled, truncates the
// on-disk pg_subtrans SLRU segments to match (M0122-0009). Without this, both
// the in-memory maps and the on-disk SLRU segment set grow without bound for
// the lifetime of a cluster.
//
// oldestXact should be the same conservative horizon TruncateCLOG is called
// with (e.g. min(datfrozenxid, OldestXmin)) — safe because any subxid below
// that horizon belongs to a top-level transaction that has long since
// committed or aborted, so its terminal status is already resolvable
// directly from CLOG without ever consulting the parent chain again. Mirrors
// PG subtrans.c:TruncateSUBTRANS, called with
// GetOldestTransactionIdConsideredRunning() at every checkpoint; using the
// CLOG horizon here instead is at least as conservative (never truncates
// more than upstream would).
//
// Wraparound-safe: entry ages are compared via storage.XIDPrecedes, not a
// plain `<`. Idempotent and safe to call repeatedly with the same or an
// older XID.
func (m *SubxactMap) Truncate(oldestXact storage.TransactionID) error {
	m.mu.Lock()
	for subxid := range m.parents {
		if storage.XIDPrecedes(subxid, oldestXact) {
			delete(m.parents, subxid)
			m.nParents.Store(int64(len(m.parents)))
		}
	}
	for subxid := range m.aborted {
		if storage.XIDPrecedes(subxid, oldestXact) {
			delete(m.aborted, subxid)
		}
	}
	slru := m.slru
	m.mu.Unlock()
	if slru == nil {
		return nil
	}
	return slru.TruncateBefore(oldestXact)
}

// Register records that subxid is a child of parentXid. Must be called
// when a subtransaction first writes (lazy XID allocation). When persistence
// is enabled (EnablePersistence), the parent link is also written through to
// the on-disk pg_subtrans SLRU mirror so it survives a restart (gap G5).
func (m *SubxactMap) Register(subxid, parentXid storage.TransactionID) {
	m.mu.Lock()
	m.parents[subxid] = parentXid
	m.nParents.Store(int64(len(m.parents)))
	slru := m.slru
	m.mu.Unlock()
	// Best-effort durable mirror: a failed SLRU write must not break the
	// in-memory registration (which is the source of truth at runtime) and
	// must never panic. The error is dropped, consistent with this file's
	// non-erroring method signatures; the parent link remains correct in
	// memory for the lifetime of the process.
	if slru != nil {
		_ = slru.SetParent(subxid, parentXid)
	}
}

// MarkAborted records that subxid was rolled back (ROLLBACK TO
// SAVEPOINT). Rows inserted under subxid remain invisible even after
// the top-level parent commits.
//
// Abort status is NOT mirrored to the pg_subtrans SLRU: upstream's
// pg_subtrans only stores parent links, not abort flags — abort/commit
// status lives in CLOG (and in-memory here). Only parent links are
// persisted (see Register / EnablePersistence).
//
// TODO(G5, deferred): the SUB_COMMITTED CLOG-mirror encoding that PG uses
// to record a subxact's commit status (TRANSACTION_STATUS_SUB_COMMITTED,
// the 0x03 lane in pg_xact) is intentionally NOT implemented here: it would
// require editing internal/mvcc/clog.go (owned by Foundation), so it is left
// for a follow-up that touches the CLOG mirror.
func (m *SubxactMap) MarkAborted(subxid storage.TransactionID) {
	m.mu.Lock()
	m.aborted[subxid] = true
	m.mu.Unlock()
}

// IsAborted reports whether subxid was individually rolled back.
func (m *SubxactMap) IsAborted(subxid storage.TransactionID) bool {
	m.mu.RLock()
	v := m.aborted[subxid]
	m.mu.RUnlock()
	return v
}

// Parent returns the parent XID of subxid, or 0 when subxid is not
// a registered subxact (i.e., it is already a top-level XID).
func (m *SubxactMap) Parent(subxid storage.TransactionID) storage.TransactionID {
	m.mu.RLock()
	p := m.parents[subxid]
	m.mu.RUnlock()
	return p
}

// TopLevelXid resolves xid to its top-level ancestor by walking the
// parent chain. Returns xid itself when xid is not in the subxact map.
// Detects cycles defensively (max 64 hops).
func (m *SubxactMap) TopLevelXid(xid storage.TransactionID) storage.TransactionID {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := 0; i < 64; i++ {
		parent, ok := m.parents[xid]
		if !ok {
			return xid
		}
		xid = parent
	}
	return xid // cycle guard
}

// IsSubxact reports whether xid is a registered subxact XID.
func (m *SubxactMap) IsSubxact(xid storage.TransactionID) bool {
	// Empty map: the locked lookup below could only return false, so skip it.
	// This is the whole point of nParents — see its comment.
	if m.nParents.Load() == 0 {
		return false
	}
	m.mu.RLock()
	_, ok := m.parents[xid]
	m.mu.RUnlock()
	return ok
}

// SubxactResolver is the interface TupleVisibleSubxact accepts for
// the subxact-to-parent resolution. The Manager satisfies it via its
// embedded *SubxactMap.
type SubxactResolver interface {
	// TopLevelXid walks the parent chain from xid to its top-level ancestor.
	TopLevelXid(xid storage.TransactionID) storage.TransactionID
	// IsAborted reports whether xid was individually rolled back.
	IsAborted(xid storage.TransactionID) bool
	// IsSubxact reports whether xid is a subtransaction XID.
	IsSubxact(xid storage.TransactionID) bool
}

// SeesCommittedXIDWithSubxacts is an extension of Snapshot.SeesCommittedXID
// that resolves subxact XIDs through the parent chain. It is called by
// TupleVisibleSubxact instead of SeesCommittedXID when a SubxactResolver
// is available.
//
// Visibility rules for subxacts:
//  1. If xid is individually aborted (ROLLBACK TO SAVEPOINT): invisible.
//  2. Resolve xid to its top-level ancestor via TopLevelXid.
//  3. Apply the normal snapshot check against the top-level XID.
//
// This matches upstream's HeapTupleSatisfiesMVCC subxact branch:
// the tuple is visible iff its creating xid's top-level transaction
// was committed AND the subxact itself was not rolled back.
func SeesCommittedXIDWithSubxacts(snap Snapshot, xid storage.TransactionID, r SubxactResolver) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	if r != nil && r.IsSubxact(xid) {
		// Rule 1: individually rolled-back subxact rows are always invisible.
		if r.IsAborted(xid) {
			return false
		}
		// Rule 2: resolve to top-level and apply normal snapshot check.
		topXid := r.TopLevelXid(xid)
		return snap.SeesCommittedXID(topXid)
	}
	// No subxact resolution needed.
	return snap.SeesCommittedXID(xid)
}

// TupleVisibleSubxact is TupleVisible extended with subxact-aware
// visibility resolution. When resolver is nil it degrades to the
// standard TupleVisible behaviour, so all existing callers continue
// to work.
func TupleVisibleSubxact(h storage.HeapTupleHeader, snap Snapshot, currentXID storage.TransactionID, r SubxactResolver, curcid storage.CommandId, combo *ComboCIDStore, mxs *multixact.Store) bool {
	if h.Xmin == storage.InvalidTransactionID {
		return false
	}
	xmaxIsLockOnly := h.Xmax != storage.InvalidTransactionID && storage.IsHeapTupleLockOnly(h.Infomask)

	// A tuple is self-visible when its xmin was written by the current
	// transaction. Inside a subtransaction the top-level XID (and any
	// intermediate ancestor XID) also qualifies as "self": tuples
	// inserted before the savepoint remain visible inside it.
	//
	// M0129-S8.3e: cmin/cmax comparison mirroring PG's
	// HeapTupleSatisfiesMVCC HEAPTUPLE_INSERT_IN_PROGRESS arm.
	// Must agree with TupleVisible (pattern_sibling_paths_must_agree).
	if isCurrentTxXID(h.Xmin, currentXID, r) {
		if xmaxIsLockOnly {
			return true
		}
		cmin := GetCmin(h, combo)
		if cmin >= curcid {
			return false
		}
		if isCurrentTxXID(h.Xmax, currentXID, r) {
			cmax := GetCmax(h, combo)
			if cmax >= curcid {
				return true // deleted by later command — pre-image visible
			}
			return false // deleted by earlier command — gone
		}
		return true
	}

	// M0115-0001: FrozenTransactionID fast path (same as TupleVisible).
	if h.Xmin != storage.FrozenTransactionID {
		// M0115-0002: hint-bit read for xmin.
		if h.Infomask&storage.HeapXminInvalid != 0 {
			return false
		}
		if h.Infomask&storage.HeapXminCommitted == 0 {
			if !SeesCommittedXIDWithSubxacts(snap, h.Xmin, r) {
				return false
			}
		} else if !snap.SeesCommittedXID(h.Xmin) {
			// HeapXminCommitted cached that xmin committed, but this snapshot
			// may have been taken before xmin committed (xmin was in-progress).
			return false
		}
	}

	if h.Xmax == storage.InvalidTransactionID {
		return true
	}
	if xmaxIsLockOnly {
		return true
	}

	// effXmax is the transaction id that actually updated/deleted the tuple.
	// We reach here only with xmax set and NOT lock-only. If the IS_MULTI bit
	// is set, h.Xmax is a MultiXactId (a row updated under a shared lock: an
	// updater plus one or more lockers), not a transaction id — resolve the
	// updater member so the self/snapshot checks below reason about the real
	// xid. This is the subtransaction-aware twin of mvcc.TupleVisible's
	// HEAP_XMAX_IS_MULTI handling and must agree with it
	// (pattern_sibling_paths_must_agree). The conservative directions follow the
	// visibility contract: an all-locker multi (no updater) -> visible (a lock is
	// not a delete); an unresolvable multi (nil store / missing record, both
	// unreachable while no producer emits non-lock-only multis) -> invisible
	// (never expose a version whose committed successor we cannot rule out).
	// M0118-0003.
	effXmax := h.Xmax
	if storage.IsHeapTupleXmaxMulti(h.Infomask) {
		if mxs == nil {
			return false
		}
		members, ok := mxs.Members(multixact.MultiXactId(h.Xmax))
		if !ok {
			return false
		}
		upd, has := multixact.GetUpdateXid(members)
		if !has {
			// Only lockers in the multi (no updater): not a delete. An
			// all-locker multi should carry LOCK_ONLY and return above; treat
			// this inconsistent case as a pure row lock — tuple visible.
			return true
		}
		effXmax = upd
	}

	if isCurrentTxXID(effXmax, currentXID, r) {
		cmax := GetCmax(h, combo)
		if cmax >= curcid {
			return true // deleted by later command — pre-image visible
		}
		return false // deleted by earlier command — gone
	}
	// M0115-0002: hint-bit read for xmax.
	if h.Infomask&storage.HeapXmaxInvalid != 0 {
		return true
	}
	if h.Infomask&storage.HeapXmaxCommitted != 0 {
		// HeapXmaxCommitted cached that xmax committed, but this snapshot
		// may have been taken before xmax committed (xmax was in-progress
		// when the snapshot was taken, e.g. origSnap in EPQ re-evaluation).
		return !snap.SeesCommittedXID(effXmax)
	}
	return !SeesCommittedXIDWithSubxacts(snap, effXmax, r)
}

// IsSelfXID reports whether xid belongs to the current transaction — either
// the exact currentXID or, when currentXID is a sub-xact, its top-level
// ancestor. Used by callers that need to skip hint-bit writes for self-visible
// tuples (which are visible without a snapshot check and may still be rolled back).
func IsSelfXID(xid, currentXID storage.TransactionID, r SubxactResolver) bool {
	return isCurrentTxXID(xid, currentXID, r)
}

// isCurrentTxXID reports whether xid was written by the current transaction.
// It returns true when xid equals currentXID exactly, OR when currentXID is
// a subtransaction and xid is its top-level ancestor (a parent XID inserted
// before the savepoint). The v0 subxact model records all sub-XIDs as
// children of the top-level XID, so TopLevelXid resolves in one hop.
func isCurrentTxXID(xid, currentXID storage.TransactionID, r SubxactResolver) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	if xid == currentXID {
		return true
	}
	if r == nil {
		return false
	}
	// Ancestor direction: currentXID is a subtransaction and xid is an XID at an
	// outer level (the top-level xact, resolved in one hop). Tuples written
	// before the savepoint remain self-visible inside it.
	if r.IsSubxact(currentXID) && xid == r.TopLevelXid(currentXID) {
		return true
	}
	// Descendant direction: xid is a still-open subtransaction of our own
	// transaction tree (a savepoint we opened, writing under its sub-XID). A
	// tuple it inserted is self-visible; a tuple it deleted is self-invisible —
	// exactly as if the top-level xact had written it. An *individually
	// rolled-back* subxact (ROLLBACK TO SAVEPOINT) is excluded, so its writes
	// fall through to the snapshot/abort check and become invisible
	// (aborted-keyrevoke: a rolled-back UPDATE's NEW tuple must disappear, while
	// its still-self-visible-before-rollback semantics are preserved here). The
	// pre-existing code resolved only the ancestor hop; the live sub-XID writer
	// is the reverse direction. M0118-0009.
	if r.IsSubxact(xid) && !r.IsAborted(xid) {
		top := r.TopLevelXid(xid)
		if top == currentXID {
			return true
		}
		if r.IsSubxact(currentXID) && top == r.TopLevelXid(currentXID) {
			return true
		}
	}
	return false
}

// SubxactMapForManager attaches a SubxactMap to the Manager's subxact
// tracking and exposes the SubxactResolver interface. The Manager calls
// this at transaction begin to initialise subxact tracking.
//
// For simplicity in v0, the SubxactMap is stored on the Manager itself
// via the AddSubxactMap / SubxactMapFor helpers below. A production
// implementation would store it in the per-txn state.
func (m *Manager) addSubxactEntry(subxid, parentXid storage.TransactionID) {
	m.subxactMu.Lock()
	if m.subxactParents == nil {
		m.subxactParents = make(map[storage.TransactionID]storage.TransactionID)
		m.subxactAborted = make(map[storage.TransactionID]bool)
	}
	m.subxactParents[subxid] = parentXid
	m.subxactMu.Unlock()
}

func (m *Manager) markSubxactAborted(subxid storage.TransactionID) {
	m.subxactMu.Lock()
	if m.subxactAborted == nil {
		m.subxactAborted = make(map[storage.TransactionID]bool)
	}
	m.subxactAborted[subxid] = true
	m.subxactMu.Unlock()
}

// RegisterSubXid records that subxid is a child of parentXid.
// Called when a subtransaction first writes (lazy XID allocation).
// When a persistent SubxactMap is attached (SetSubxactMap), the link is written
// through to it — and thus to the on-disk pg_subtrans SLRU (M0117-0003).
func (m *Manager) RegisterSubXid(subxid, parentXid storage.TransactionID) {
	if sm := m.attachedSubxactMap(); sm != nil {
		sm.Register(subxid, parentXid)
		return
	}
	m.addSubxactEntry(subxid, parentXid)
}

// MarkSubxactAborted records that subxid was individually rolled back, then
// wakes any WaitForXID sleeper: a blocked row-lock waiter that conflicts with a
// lock acquired inside this savepoint must re-evaluate now that the savepoint's
// lock is released (xidActiveWithSubxact will report the subxid dead). The
// broadcast happens under waitMu after the abort state is recorded, mirroring
// the top-level abort path in markXactEnd — the state change (under subxactMu /
// the persistent map) and the broadcast (under waitMu) are disjoint, never
// nested, so there is no lock-ordering hazard with WaitForXID (waitMu → subxactMu).
// M0118-0004 (docs/design/0118-0012).
func (m *Manager) MarkSubxactAborted(subxid storage.TransactionID) {
	if sm := m.attachedSubxactMap(); sm != nil {
		sm.MarkAborted(subxid)
	} else {
		m.markSubxactAborted(subxid)
	}
	m.waitMu.Lock()
	m.commitCond.Broadcast()
	m.waitMu.Unlock()
}

// attachedSubxactMap returns the persistent SubxactMap installed via
// SetSubxactMap, or nil when none is attached (the default v0 path).
func (m *Manager) attachedSubxactMap() *SubxactMap {
	return m.subxactMap.Load()
}

// TopLevelXid resolves xid to its top-level ancestor.
func (m *Manager) TopLevelXid(xid storage.TransactionID) storage.TransactionID {
	if sm := m.attachedSubxactMap(); sm != nil {
		return sm.TopLevelXid(xid)
	}
	m.subxactMu.RLock()
	defer m.subxactMu.RUnlock()
	if m.subxactParents == nil {
		return xid
	}
	for i := 0; i < 64; i++ {
		parent, ok := m.subxactParents[xid]
		if !ok {
			return xid
		}
		xid = parent
	}
	return xid
}

// IsAborted reports whether xid was individually rolled back.
func (m *Manager) IsAborted(xid storage.TransactionID) bool {
	if sm := m.attachedSubxactMap(); sm != nil {
		return sm.IsAborted(xid)
	}
	m.subxactMu.RLock()
	v := m.subxactAborted[xid]
	m.subxactMu.RUnlock()
	return v
}

// IsSubxact reports whether xid is a registered subxact XID.
func (m *Manager) IsSubxact(xid storage.TransactionID) bool {
	if sm := m.attachedSubxactMap(); sm != nil {
		return sm.IsSubxact(xid)
	}
	m.subxactMu.RLock()
	_, ok := m.subxactParents[xid]
	m.subxactMu.RUnlock()
	return ok
}

// subxactFields hold the per-manager subxact state. They are embedded
// directly rather than via a pointer so the zero Manager value stays
// valid (maps are lazily initialised).
type subxactFields struct {
	subxactMu      sync.RWMutex
	subxactParents map[storage.TransactionID]storage.TransactionID
	subxactAborted map[storage.TransactionID]bool

	// subxactMap, when non-nil, is the persistent pg_subtrans-backed store
	// installed by initdb.Open via SetSubxactMap (M0117-0003). When attached it
	// is the authoritative subxact store: RegisterSubXid/MarkSubxactAborted write
	// through to it (and thus to the on-disk SLRU), and TopLevelXid/IsAborted/
	// IsSubxact resolve from it. nil (the default, and every test) keeps the pure
	// in-memory subxactParents/subxactAborted behaviour byte-identical to v0.
	//
	// Stored in an atomic pointer (not under subxactMu) because TupleVisibleSubxact
	// consults the resolver on the per-tuple visibility hot path: a plain
	// load-the-attachment must be lock-free so the nil (default) case costs one
	// atomic load, not an extra RWMutex acquisition per tuple. Set once at startup
	// before any query runs (initdb.Open), so a relaxed atomic load is sufficient.
	subxactMap atomic.Pointer[SubxactMap]
}

// SetSubxactMap installs the persistent pg_subtrans-backed subxact store
// (M0117-0003), mirroring SetCLog. After this call the Manager routes all
// subxact registration and resolution through m (so parent links are persisted
// to and restored from the on-disk pg_subtrans SLRU). Passing nil restores the
// pure in-memory v0 behaviour. Intended to be called once during startup/recovery
// wiring (initdb.Open), after the map has been restored via RestoreFromSLRU.
func (m *Manager) SetSubxactMap(sm *SubxactMap) {
	m.subxactMap.Store(sm)
}

package transam

import (
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// PredicateLockGranularity classifies a PredicateLockTag's scope.
// Mirrors the implicit hierarchy in PostgreSQL's PREDICATELOCKTARGETTAG
// (`src/include/storage/predicate_internals.h`): a relation-level lock
// implies every page-level and tuple-level lock on that relation, and
// a page-level lock implies every tuple-level lock on that page.
type PredicateLockGranularity uint8

const (
	// InvalidPredicateGranularity is returned for an unconstructed
	// or degenerate PredicateLockTag (Rel == 0).
	InvalidPredicateGranularity PredicateLockGranularity = iota
	// RelationGranularity covers an entire relation.
	RelationGranularity
	// PageGranularity covers a single heap page.
	PageGranularity
	// TupleGranularity covers a single tuple slot.
	TupleGranularity
)

// String returns the lowercase name of the granularity for log lines
// and assertion failures.
func (g PredicateLockGranularity) String() string {
	switch g {
	case RelationGranularity:
		return "relation"
	case PageGranularity:
		return "page"
	case TupleGranularity:
		return "tuple"
	default:
		return "invalid"
	}
}

// PredicateLockTag identifies a single SIREAD predicate-lock target.
// Encoding mirrors PostgreSQL's PREDICATELOCKTARGETTAG: granularity is
// derived from sentinel fields, so a single struct shape can represent
// every level of the hierarchy without a tagged-union split:
//
//   - Rel == 0                               → invalid
//   - Page == InvalidBlockNumber             → relation-level
//   - Page != InvalidBlockNumber, Offset==0  → page-level
//   - Page != InvalidBlockNumber, Offset!=0  → tuple-level
//
// DB is the database OID; 0 is reserved (no shared catalogs in v0). Use
// the RelationLockTag / PageLockTag / TupleLockTag constructors rather
// than building this struct directly so the granularity sentinels stay
// consistent.
type PredicateLockTag struct {
	DB     uint32
	Rel    uint32
	Page   storage.BlockNumber
	Offset uint16
}

// RelationLockTag builds a relation-level SIREAD target.
func RelationLockTag(db, rel uint32) PredicateLockTag {
	return PredicateLockTag{
		DB:     db,
		Rel:    rel,
		Page:   storage.InvalidBlockNumber,
		Offset: 0,
	}
}

// PageLockTag builds a page-level SIREAD target. Panics if page is
// InvalidBlockNumber — callers must use RelationLockTag for that case.
func PageLockTag(db, rel uint32, page storage.BlockNumber) PredicateLockTag {
	if page == storage.InvalidBlockNumber {
		panic("transam.PageLockTag: page == InvalidBlockNumber; use RelationLockTag instead")
	}
	return PredicateLockTag{
		DB:     db,
		Rel:    rel,
		Page:   page,
		Offset: 0,
	}
}

// TupleLockTag builds a tuple-level SIREAD target. Panics if page is
// InvalidBlockNumber or offset is 0 — both are reserved sentinels.
func TupleLockTag(db, rel uint32, page storage.BlockNumber, offset uint16) PredicateLockTag {
	if page == storage.InvalidBlockNumber {
		panic("transam.TupleLockTag: page == InvalidBlockNumber")
	}
	if offset == 0 {
		panic("transam.TupleLockTag: offset == 0")
	}
	return PredicateLockTag{
		DB:     db,
		Rel:    rel,
		Page:   page,
		Offset: offset,
	}
}

// Granularity returns the level encoded by the tag's sentinel fields.
func (t PredicateLockTag) Granularity() PredicateLockGranularity {
	if t.Rel == 0 {
		return InvalidPredicateGranularity
	}
	if t.Page == storage.InvalidBlockNumber {
		return RelationGranularity
	}
	if t.Offset == 0 {
		return PageGranularity
	}
	return TupleGranularity
}

// Covers reports whether holding the receiver implies an SIREAD on
// other. Used to elide redundant acquisitions (an xact that already
// holds a relation-level lock does not need to acquire a tuple-level
// lock on the same relation), and to prune finer locks when coarsening
// promotes them.
//
// (db, rel) must match exactly. A relation-level lock covers every
// page and tuple in that relation; a page-level lock covers every
// tuple on that page; a tuple-level lock only covers itself.
func (t PredicateLockTag) Covers(other PredicateLockTag) bool {
	if t.DB != other.DB || t.Rel != other.Rel {
		return false
	}
	switch t.Granularity() {
	case RelationGranularity:
		return other.Granularity() != InvalidPredicateGranularity
	case PageGranularity:
		og := other.Granularity()
		if og == RelationGranularity || og == InvalidPredicateGranularity {
			return false
		}
		return other.Page == t.Page
	case TupleGranularity:
		return other == t
	default:
		return false
	}
}

// PredicateLockLimits configures coarsening thresholds for the
// SIREAD substrate. Mirrors the PostgreSQL GUCs
// `max_predicate_locks_per_xact`, `max_predicate_locks_per_relation`,
// `max_predicate_locks_per_page`. The substrate uses simple positive
// values; the GUC layer translates upstream's negative shorthand into
// these positives before calling SetPredicateLockLimits.
//
// Defaults are conservative (small) so coarsening behaviour is
// exercised early under tests; production deployments configure
// realistic limits via the GUCs.
type PredicateLockLimits struct {
	// PerXact bounds the number of predicate-lock targets a single
	// SerializableXact may hold before the substrate coarsens its
	// finest-granularity locks. Mirrors max_predicate_locks_per_xact.
	PerXact int
	// PerRelation bounds the number of page-level locks one xact may
	// hold on a single relation before promotion to a relation-level
	// lock. Mirrors max_predicate_locks_per_relation.
	PerRelation int
	// PerPage bounds the number of tuple-level locks one xact may
	// hold on a single page before promotion to a page-level lock.
	// Mirrors max_predicate_locks_per_page.
	PerPage int
}

// DefaultPredicateLockLimits returns goopg's substrate defaults.
// These mirror PostgreSQL's coarsening intent (page promotes at 2
// tuples, relation promotes at a fraction of per-xact). The per-xact
// ceiling is intentionally smaller than upstream's 64 so the
// substrate's escalation path is reachable in regression tests
// without synthesising 64+ targets per case.
func DefaultPredicateLockLimits() PredicateLockLimits {
	return PredicateLockLimits{
		PerXact:     64,
		PerRelation: 32,
		PerPage:     2,
	}
}

// predicateLockTarget is the substrate-internal record for a single
// SIREAD target. Mirrors PostgreSQL's PREDICATELOCKTARGET: the holder
// set is the inverted index of "which SerializableXacts have SIREAD
// coverage on this target", used by M0104-0005's conflict-out hook.
//
// Cost note: empty targets are evicted on the last release so the
// global map size tracks live (target, holder) pairs.
type predicateLockTarget struct {
	tag     PredicateLockTag
	holders map[TxnHandle]struct{}
}

// predicateLocksRegistry is the Manager-owned global SIREAD registry.
// Embedded into Manager alongside ssiState so the predicate-lock
// plumbing is testable in isolation but shares Manager.mu for
// ordering with snapshot acquisition and SerializableXact lifecycle.
//
// Lazy-init: RC/RR / non-SERIALIZABLE workloads pay no map-allocation
// cost — only AcquirePredicateLock can flip initOnce.
type predicateLocksRegistry struct {
	initOnce sync.Once
	targets  map[PredicateLockTag]*predicateLockTarget
	limits   PredicateLockLimits
}

func (r *predicateLocksRegistry) ensureInit() {
	r.initOnce.Do(func() {
		r.targets = map[PredicateLockTag]*predicateLockTarget{}
		if r.limits == (PredicateLockLimits{}) {
			r.limits = DefaultPredicateLockLimits()
		}
	})
}

// SetPredicateLockLimits installs new coarsening thresholds. Must be
// called before any SerializableXact acquires its first predicate
// lock; the GUC bridge (M0104-0004+) reads
// max_predicate_locks_per_{xact,relation,page} at session start and
// forwards the resolved positives here. Negative or zero values are
// ignored field-by-field so callers can override only the dimensions
// they care about (tests).
func (m *Manager) SetPredicateLockLimits(limits PredicateLockLimits) {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	m.predicateLocks.ensureInit()
	if limits.PerXact > 0 {
		m.predicateLocks.limits.PerXact = limits.PerXact
	}
	if limits.PerRelation > 0 {
		m.predicateLocks.limits.PerRelation = limits.PerRelation
	}
	if limits.PerPage > 0 {
		m.predicateLocks.limits.PerPage = limits.PerPage
	}
}

// PredicateLockLimits returns the active coarsening thresholds.
func (m *Manager) PredicateLockLimits() PredicateLockLimits {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	m.predicateLocks.ensureInit()
	return m.predicateLocks.limits
}

// AcquirePredicateLock records SIREAD coverage on tag for the
// SERIALIZABLE transaction identified by handle. Returns true if the
// lock was acquired (or was already held via an equal-or-coarser
// lock), false if the call was a no-op (non-SERIALIZABLE xact, or
// invalid tag).
//
// Coverage semantics mirror PostgreSQL's predicate.c:
//
//   - If the xact already holds an equal-or-coarser lock, the call is
//     a no-op (the new tag is implied).
//   - Otherwise the substrate first prunes every finer lock the xact
//     holds that tag now covers (e.g. acquiring a relation-level lock
//     after holding several tuple-level locks on that relation
//     removes those tuples).
//   - Then the new tag is installed and the coarsening policy runs.
//
// Cleanup is automatic on Manager.finish via releaseSerializableLocked,
// so callers do not need to release locks explicitly.
func (m *Manager) AcquirePredicateLock(handle TxnHandle, tag PredicateLockTag) bool {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	return m.acquirePredicateLockLocked(handle, tag)
}

func (m *Manager) acquirePredicateLockLocked(handle TxnHandle, tag PredicateLockTag) bool {
	if tag.Granularity() == InvalidPredicateGranularity {
		return false
	}
	if m.ssiState.xacts == nil {
		return false
	}
	sx, ok := m.ssiState.xacts[handle]
	if !ok || sx == nil {
		return false
	}
	m.predicateLocks.ensureInit()

	if sx.predicateLocks == nil {
		sx.predicateLocks = map[PredicateLockTag]struct{}{}
	}

	for owned := range sx.predicateLocks {
		if owned.Covers(tag) {
			return true
		}
	}

	for owned := range sx.predicateLocks {
		if owned != tag && tag.Covers(owned) {
			m.detachPredicateLockLocked(handle, owned)
		}
	}

	m.attachPredicateLockLocked(handle, sx, tag)
	m.coarsenAfterAcquireLocked(handle, sx, tag)
	return true
}

// HoldsPredicateLock reports whether handle currently has SIREAD
// coverage on tag, either by holding it directly or by holding a
// coarser lock that covers it.
func (m *Manager) HoldsPredicateLock(handle TxnHandle, tag PredicateLockTag) bool {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return false
	}
	sx, ok := m.ssiState.xacts[handle]
	if !ok || sx == nil {
		return false
	}
	for owned := range sx.predicateLocks {
		if owned.Covers(tag) {
			return true
		}
	}
	return false
}

// PredicateLockCount returns the number of distinct predicate-lock
// targets held by handle. Diagnostic helper for tests; production
// callers should not depend on this number directly.
func (m *Manager) PredicateLockCount(handle TxnHandle) int {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return 0
	}
	sx, ok := m.ssiState.xacts[handle]
	if !ok || sx == nil {
		return 0
	}
	return len(sx.predicateLocks)
}

// PredicateLockTargetHolderCount returns the number of SerializableXacts
// holding SIREAD coverage on tag, ignoring coarser locks (the
// substrate stores each tag in its own target slot). Diagnostic
// helper used by tests and the M0104-0005 conflict-out hook.
func (m *Manager) PredicateLockTargetHolderCount(tag PredicateLockTag) int {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.predicateLocks.targets == nil {
		return 0
	}
	tgt, ok := m.predicateLocks.targets[tag]
	if !ok {
		return 0
	}
	return len(tgt.holders)
}

// attachPredicateLockLocked records that handle (via sx) now holds an
// SIREAD on tag. Adds the tag to the xact's owned set and registers
// the xact in the global target's holders.
func (m *Manager) attachPredicateLockLocked(handle TxnHandle, sx *SerializableXact, tag PredicateLockTag) {
	sx.predicateLocks[tag] = struct{}{}
	tgt, ok := m.predicateLocks.targets[tag]
	if !ok {
		tgt = &predicateLockTarget{
			tag:     tag,
			holders: map[TxnHandle]struct{}{},
		}
		m.predicateLocks.targets[tag] = tgt
	}
	tgt.holders[handle] = struct{}{}
}

// detachPredicateLockLocked removes a single (handle, tag) pair from
// both the xact-owned set and the global target. Evicts the global
// target on its last holder.
func (m *Manager) detachPredicateLockLocked(handle TxnHandle, tag PredicateLockTag) {
	sx, ok := m.ssiState.xacts[handle]
	if ok && sx != nil && sx.predicateLocks != nil {
		delete(sx.predicateLocks, tag)
	}
	if m.predicateLocks.targets == nil {
		return
	}
	tgt, ok := m.predicateLocks.targets[tag]
	if !ok {
		return
	}
	delete(tgt.holders, handle)
	if len(tgt.holders) == 0 {
		delete(m.predicateLocks.targets, tag)
	}
}

// releasePredicateLocksLocked detaches every predicate lock owned by
// handle. Called from releaseSerializableLocked on commit and abort
// so SIREAD coverage cannot leak past transaction lifetime in the
// first-slice substrate. M0104-0006 will revisit retention for
// post-commit conflict-graph checks.
func (m *Manager) releasePredicateLocksLocked(handle TxnHandle) {
	sx, ok := m.ssiState.xacts[handle]
	if !ok || sx == nil || len(sx.predicateLocks) == 0 {
		return
	}
	for tag := range sx.predicateLocks {
		if m.predicateLocks.targets == nil {
			break
		}
		tgt, ok := m.predicateLocks.targets[tag]
		if !ok {
			continue
		}
		delete(tgt.holders, handle)
		if len(tgt.holders) == 0 {
			delete(m.predicateLocks.targets, tag)
		}
	}
	sx.predicateLocks = nil
}

// detachPredicateLocksFromGlobalLocked removes a SERIALIZABLE xact (by handle)
// from every GLOBAL predicate-lock holder set but LEAVES sx.predicateLocks
// intact. M0118-0001 calls this when a committed reader is retained past commit:
// the handle-keyed global holder sets must not keep naming the committed xact
// (its proc-slot handle may be reused by a later Begin, which would alias the
// retained lock onto the new xact), yet the committed xact's own owned-tag set
// must survive so checkForSerializableConflictInLocked can still discover it via
// the retained-xact (finished) walk. This mirrors PostgreSQL keeping a committed
// SERIALIZABLEXACT's predicate locks alive until no concurrent transaction can
// still form a dangerous rw-conflict against it (predicate.c
// ClearOldPredicateLocks / SxactGlobalXmin); goopg's purgeFinishedSerializableLocked
// is the analogous deferred cleanup that finally drops the retained xact.
func (m *Manager) detachPredicateLocksFromGlobalLocked(handle TxnHandle, sx *SerializableXact) {
	if sx == nil || len(sx.predicateLocks) == 0 || m.predicateLocks.targets == nil {
		return
	}
	for tag := range sx.predicateLocks {
		tgt, ok := m.predicateLocks.targets[tag]
		if !ok {
			continue
		}
		delete(tgt.holders, handle)
		if len(tgt.holders) == 0 {
			delete(m.predicateLocks.targets, tag)
		}
	}
}

// coarsenAfterAcquireLocked enforces the active coarsening policy
// after a new lock has been installed. Three stages, finest first:
//
//  1. If the new lock is tuple-level and the xact now holds more
//     than PerPage tuple-level locks on that page, promote them to a
//     single page-level lock.
//  2. If after step 1 the xact holds more than PerRelation
//     page-level locks on that relation, promote them to a single
//     relation-level lock.
//  3. If the xact's total predicate-lock count still exceeds
//     PerXact, promote the largest relation footprint to a
//     relation-level lock.
//
// Each promotion installs the coarser tag and detaches every finer
// tag the new tag covers. Promotion preserves the invariant that an
// xact never holds two predicate-lock targets where one covers the
// other.
func (m *Manager) coarsenAfterAcquireLocked(handle TxnHandle, sx *SerializableXact, newTag PredicateLockTag) {
	limits := m.predicateLocks.limits

	if newTag.Granularity() == TupleGranularity && limits.PerPage > 0 {
		tuples := 0
		for owned := range sx.predicateLocks {
			if owned.Granularity() != TupleGranularity {
				continue
			}
			if owned.DB == newTag.DB && owned.Rel == newTag.Rel && owned.Page == newTag.Page {
				tuples++
			}
		}
		if tuples > limits.PerPage {
			m.promoteToPageLocked(handle, sx, newTag.DB, newTag.Rel, newTag.Page)
		}
	}

	if limits.PerRelation > 0 {
		pages := 0
		for owned := range sx.predicateLocks {
			if owned.Granularity() != PageGranularity {
				continue
			}
			if owned.DB == newTag.DB && owned.Rel == newTag.Rel {
				pages++
			}
		}
		if pages > limits.PerRelation {
			m.promoteToRelationLocked(handle, sx, newTag.DB, newTag.Rel)
		}
	}

	if limits.PerXact > 0 && len(sx.predicateLocks) > limits.PerXact {
		db, rel, ok := m.largestRelationFootprintLocked(sx)
		if ok {
			m.promoteToRelationLocked(handle, sx, db, rel)
		}
	}
}

// promoteToPageLocked replaces every tuple-level lock the xact holds
// on (db, rel, page) with a single page-level lock.
func (m *Manager) promoteToPageLocked(handle TxnHandle, sx *SerializableXact, db, rel uint32, page storage.BlockNumber) {
	pageTag := PageLockTag(db, rel, page)
	if _, already := sx.predicateLocks[pageTag]; already {
		return
	}
	doomed := make([]PredicateLockTag, 0, 4)
	for owned := range sx.predicateLocks {
		if owned.Granularity() != TupleGranularity {
			continue
		}
		if owned.DB == db && owned.Rel == rel && owned.Page == page {
			doomed = append(doomed, owned)
		}
	}
	for _, t := range doomed {
		m.detachPredicateLockLocked(handle, t)
	}
	m.attachPredicateLockLocked(handle, sx, pageTag)
}

// promoteToRelationLocked replaces every page-level and tuple-level
// lock the xact holds on (db, rel) with a single relation-level lock.
func (m *Manager) promoteToRelationLocked(handle TxnHandle, sx *SerializableXact, db, rel uint32) {
	relTag := RelationLockTag(db, rel)
	if _, already := sx.predicateLocks[relTag]; already {
		return
	}
	doomed := make([]PredicateLockTag, 0, 8)
	for owned := range sx.predicateLocks {
		if owned == relTag {
			continue
		}
		if owned.DB == db && owned.Rel == rel {
			doomed = append(doomed, owned)
		}
	}
	for _, t := range doomed {
		m.detachPredicateLockLocked(handle, t)
	}
	m.attachPredicateLockLocked(handle, sx, relTag)
}

// largestRelationFootprintLocked returns the (db, rel) on which the
// xact holds the most fine-grained locks. Used as the
// PerXact-overflow promotion target: relation-level coarsening on
// the busiest relation is the single move that frees the most
// per-xact slots. Returns ok=false if no relation-scoped fine-grained
// lock exists (already all relation-level).
func (m *Manager) largestRelationFootprintLocked(sx *SerializableXact) (uint32, uint32, bool) {
	type key struct {
		db, rel uint32
	}
	counts := map[key]int{}
	for owned := range sx.predicateLocks {
		if owned.Granularity() == RelationGranularity {
			continue
		}
		counts[key{owned.DB, owned.Rel}]++
	}
	var (
		best   key
		bestN  int
		hasAny bool
	)
	for k, n := range counts {
		if !hasAny || n > bestN || (n == bestN && (k.db < best.db || (k.db == best.db && k.rel < best.rel))) {
			best = k
			bestN = n
			hasAny = true
		}
	}
	return best.db, best.rel, hasAny
}

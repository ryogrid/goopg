package transam

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPredicateLockTag_Granularity pins the sentinel-encoded
// granularity hierarchy. The substrate (and every future SSI hook)
// derives behaviour from Granularity(), so a regression that mis-
// classifies a tag would silently break coverage / coarsening.
func TestPredicateLockTag_Granularity(t *testing.T) {
	cases := []struct {
		name string
		tag  PredicateLockTag
		want PredicateLockGranularity
	}{
		{"invalid-rel-zero", PredicateLockTag{DB: 1, Rel: 0}, InvalidPredicateGranularity},
		{"relation", RelationLockTag(1, 100), RelationGranularity},
		{"page", PageLockTag(1, 100, 7), PageGranularity},
		{"tuple", TupleLockTag(1, 100, 7, 3), TupleGranularity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tag.Granularity(); got != tc.want {
				t.Fatalf("Granularity() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPredicateLockTag_Covers pins the implicit-coverage relation
// that drives idempotent acquire and coarsening promotion. A wrong
// answer here would let two redundant tags coexist (rel + tuple)
// and break the dangerous-structure check planned for M0104-0006.
func TestPredicateLockTag_Covers(t *testing.T) {
	rel := RelationLockTag(1, 100)
	page := PageLockTag(1, 100, 7)
	otherPage := PageLockTag(1, 100, 8)
	tup := TupleLockTag(1, 100, 7, 3)
	otherTup := TupleLockTag(1, 100, 7, 4)
	otherRelTup := TupleLockTag(1, 200, 7, 3)

	// relation covers everything in the same (db, rel)
	if !rel.Covers(page) {
		t.Fatal("rel.Covers(page) = false")
	}
	if !rel.Covers(tup) {
		t.Fatal("rel.Covers(tup) = false")
	}
	if rel.Covers(otherRelTup) {
		t.Fatal("rel.Covers(other-rel tup) = true")
	}
	// page covers tuples on the same page, not other pages, not rel
	if !page.Covers(tup) {
		t.Fatal("page.Covers(tup) = false")
	}
	if page.Covers(otherPage) {
		t.Fatal("page.Covers(other page) = true")
	}
	if page.Covers(rel) {
		t.Fatal("page.Covers(rel) = true")
	}
	if page.Covers(TupleLockTag(1, 100, 8, 3)) {
		t.Fatal("page.Covers(tup on different page) = true")
	}
	// tuple covers only itself
	if !tup.Covers(tup) {
		t.Fatal("tup.Covers(self) = false")
	}
	if tup.Covers(otherTup) {
		t.Fatal("tup.Covers(other tup on same page) = true")
	}
	if tup.Covers(page) {
		t.Fatal("tup.Covers(page) = true")
	}
}

// TestPredicateLock_AcquireOnlyForSerializable pins that
// AcquirePredicateLock is a no-op for RC/RR transactions and that
// the registry footprint stays zero for those workloads. Mirrors
// the ssi-registry cost contract — SSI overhead must not leak into
// the common case.
func TestPredicateLock_AcquireOnlyForSerializable(t *testing.T) {
	for _, iso := range []IsolationLevel{IsolationReadCommitted, IsolationRepeatableRead} {
		m := NewManager()
		tx, err := m.Begin(iso)
		if err != nil {
			t.Fatalf("Begin(%v): %v", iso, err)
		}
		ok := m.AcquirePredicateLock(tx.Handle, RelationLockTag(1, 100))
		if ok {
			t.Fatalf("AcquirePredicateLock for %v returned true, want false (no-op)", iso)
		}
		if got := m.PredicateLockCount(tx.Handle); got != 0 {
			t.Fatalf("PredicateLockCount for %v = %d, want 0", iso, got)
		}
		if err := m.Commit(tx); err != nil {
			t.Fatalf("Commit(%v): %v", iso, err)
		}
	}
}

// TestPredicateLock_AcquireAndReleaseOnCommit pins the basic
// lifecycle: SERIALIZABLE acquires record the tag in both the per-
// xact set and the global target map, and Commit (and Rollback)
// retire every entry — no SIREAD leak past the xact's finish.
func TestPredicateLock_AcquireAndReleaseOnCommit(t *testing.T) {
	for _, finishKind := range []string{"commit", "rollback"} {
		t.Run(finishKind, func(t *testing.T) {
			m := NewManager()
			tx, err := m.Begin(IsolationSerializable)
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			tag := PageLockTag(1, 100, 5)
			if ok := m.AcquirePredicateLock(tx.Handle, tag); !ok {
				t.Fatal("AcquirePredicateLock returned false")
			}
			if got := m.PredicateLockCount(tx.Handle); got != 1 {
				t.Fatalf("PredicateLockCount = %d, want 1", got)
			}
			if !m.HoldsPredicateLock(tx.Handle, tag) {
				t.Fatal("HoldsPredicateLock = false after acquire")
			}
			if got := m.PredicateLockTargetHolderCount(tag); got != 1 {
				t.Fatalf("global holders = %d, want 1", got)
			}
			if finishKind == "commit" {
				if err := m.Commit(tx); err != nil {
					t.Fatalf("Commit: %v", err)
				}
			} else {
				if err := m.Rollback(tx); err != nil {
					t.Fatalf("Rollback: %v", err)
				}
			}
			if got := m.PredicateLockTargetHolderCount(tag); got != 0 {
				t.Fatalf("global holders after %s = %d, want 0", finishKind, got)
			}
		})
	}
}

// TestPredicateLock_IdempotentUnderCoarserOwnership pins the
// "coarser lock subsumes finer acquire" invariant: holding a rel-
// level lock makes a follow-up tuple-level acquire a no-op, so the
// substrate never duplicates coverage. Without this, the xact-owned
// set would grow without bound under bursty access patterns.
func TestPredicateLock_IdempotentUnderCoarserOwnership(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = m.Commit(tx) }()

	rel := RelationLockTag(1, 100)
	if !m.AcquirePredicateLock(tx.Handle, rel) {
		t.Fatal("acquire rel: false")
	}
	if !m.AcquirePredicateLock(tx.Handle, TupleLockTag(1, 100, 7, 3)) {
		t.Fatal("acquire tup under rel: false")
	}
	if got := m.PredicateLockCount(tx.Handle); got != 1 {
		t.Fatalf("count after redundant tup acquire = %d, want 1", got)
	}
	if !m.HoldsPredicateLock(tx.Handle, TupleLockTag(1, 100, 7, 3)) {
		t.Fatal("HoldsPredicateLock(tup) = false despite rel coverage")
	}
}

// TestPredicateLock_AcquireCoarserPrunesFiner pins the inverse: a
// later relation-level acquire on a relation where the xact already
// owns finer locks prunes those finer locks, leaving exactly one
// (relation) entry. Mirrors PostgreSQL's RemoveScratchTarget logic.
func TestPredicateLock_AcquireCoarserPrunesFiner(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = m.Commit(tx) }()

	if !m.AcquirePredicateLock(tx.Handle, TupleLockTag(1, 100, 7, 3)) {
		t.Fatal("acquire tup A: false")
	}
	if !m.AcquirePredicateLock(tx.Handle, PageLockTag(1, 100, 9)) {
		t.Fatal("acquire page B: false")
	}
	if got := m.PredicateLockCount(tx.Handle); got != 2 {
		t.Fatalf("count before coarsen = %d, want 2", got)
	}
	if !m.AcquirePredicateLock(tx.Handle, RelationLockTag(1, 100)) {
		t.Fatal("acquire rel: false")
	}
	if got := m.PredicateLockCount(tx.Handle); got != 1 {
		t.Fatalf("count after rel acquire = %d, want 1", got)
	}
	if got := m.PredicateLockTargetHolderCount(PageLockTag(1, 100, 9)); got != 0 {
		t.Fatalf("page target still has holders after rel coarsen: %d", got)
	}
	if got := m.PredicateLockTargetHolderCount(TupleLockTag(1, 100, 7, 3)); got != 0 {
		t.Fatalf("tuple target still has holders after rel coarsen: %d", got)
	}
}

// TestPredicateLock_PerPageCoarsening pins the page-level promotion:
// when the xact holds more than PerPage tuple-level locks on the same
// page, the substrate replaces them with a single page-level lock.
// Floor at PerPage=2 (PG default): three tuples on the same page must
// collapse to one page entry.
func TestPredicateLock_PerPageCoarsening(t *testing.T) {
	m := NewManager()
	m.SetPredicateLockLimits(PredicateLockLimits{PerPage: 2, PerRelation: 999, PerXact: 999})

	tx, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = m.Commit(tx) }()

	for off := uint16(1); off <= 3; off++ {
		if !m.AcquirePredicateLock(tx.Handle, TupleLockTag(1, 100, 7, off)) {
			t.Fatalf("acquire tup off=%d: false", off)
		}
	}
	if got := m.PredicateLockCount(tx.Handle); got != 1 {
		t.Fatalf("count after 3 tuple acquires (PerPage=2) = %d, want 1", got)
	}
	if !m.HoldsPredicateLock(tx.Handle, PageLockTag(1, 100, 7)) {
		t.Fatal("page coverage missing after coarsening")
	}
	// Coverage check: every tuple on the page is now implied.
	if !m.HoldsPredicateLock(tx.Handle, TupleLockTag(1, 100, 7, 99)) {
		t.Fatal("tuple coverage missing under page lock")
	}
	// Different page is not covered.
	if m.HoldsPredicateLock(tx.Handle, TupleLockTag(1, 100, 8, 1)) {
		t.Fatal("tuple on different page wrongly covered")
	}
}

// TestPredicateLock_PerRelationCoarsening pins the relation-level
// promotion: when the xact holds more than PerRelation page-level
// locks on the same relation, the substrate replaces them with one
// relation-level lock. Combined with PerPage=2, three tuples on
// three distinct pages first promote to three page locks, then
// (PerRelation=2) promote those to one relation lock.
func TestPredicateLock_PerRelationCoarsening(t *testing.T) {
	m := NewManager()
	m.SetPredicateLockLimits(PredicateLockLimits{PerPage: 2, PerRelation: 2, PerXact: 999})

	tx, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = m.Commit(tx) }()

	// Three pages, each with three tuples → each page coarsens to a
	// page-level lock (3 pages held) → page count exceeds PerRelation,
	// promotes to a single relation-level lock.
	for page := storage.BlockNumber(1); page <= 3; page++ {
		for off := uint16(1); off <= 3; off++ {
			if !m.AcquirePredicateLock(tx.Handle, TupleLockTag(1, 100, page, off)) {
				t.Fatalf("acquire page=%d off=%d: false", page, off)
			}
		}
	}
	if got := m.PredicateLockCount(tx.Handle); got != 1 {
		t.Fatalf("count after 3 pages × 3 tuples = %d, want 1 (rel-level)", got)
	}
	if !m.HoldsPredicateLock(tx.Handle, RelationLockTag(1, 100)) {
		t.Fatal("relation coverage missing after coarsening")
	}
}

// TestPredicateLock_PerXactOverflowCoarsens pins the per-xact ceiling
// path: when the total predicate-lock count exceeds PerXact, the
// substrate promotes the busiest relation footprint to a relation-
// level lock. A regression here would let a long-running serializable
// transaction accumulate unbounded predicate-lock memory.
func TestPredicateLock_PerXactOverflowCoarsens(t *testing.T) {
	m := NewManager()
	m.SetPredicateLockLimits(PredicateLockLimits{PerPage: 999, PerRelation: 999, PerXact: 3})

	tx, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = m.Commit(tx) }()

	// Three tuples on rel=100 spread across pages, one tuple on rel=200.
	for page := storage.BlockNumber(1); page <= 3; page++ {
		if !m.AcquirePredicateLock(tx.Handle, TupleLockTag(1, 100, page, 1)) {
			t.Fatalf("acquire rel=100 page=%d: false", page)
		}
	}
	if got := m.PredicateLockCount(tx.Handle); got != 3 {
		t.Fatalf("count before overflow = %d, want 3", got)
	}
	// Fourth distinct tag — total now 4 > PerXact=3, busiest relation
	// (rel=100, three tuples) coarsens to a relation lock. Final
	// state: 1 rel lock on rel=100 + 1 tuple on rel=200 = 2 entries.
	if !m.AcquirePredicateLock(tx.Handle, TupleLockTag(1, 200, 1, 1)) {
		t.Fatal("acquire rel=200: false")
	}
	if got := m.PredicateLockCount(tx.Handle); got != 2 {
		t.Fatalf("count after overflow = %d, want 2 (rel=100 collapsed)", got)
	}
	if !m.HoldsPredicateLock(tx.Handle, RelationLockTag(1, 100)) {
		t.Fatal("rel=100 coverage missing after PerXact overflow coarsen")
	}
	if !m.HoldsPredicateLock(tx.Handle, TupleLockTag(1, 200, 1, 1)) {
		t.Fatal("rel=200 tuple lock lost during PerXact overflow coarsen")
	}
}

// TestPredicateLock_GlobalTargetHoldersTrackMultipleXacts pins the
// inverted index that M0104-0005's conflict-out hook will consult:
// when two SERIALIZABLE transactions independently lock the same
// target, the holder set grows; release of one xact leaves the
// target alive for the other.
func TestPredicateLock_GlobalTargetHoldersTrackMultipleXacts(t *testing.T) {
	m := NewManager()
	tA, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin A: %v", err)
	}
	tB, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin B: %v", err)
	}
	tag := PageLockTag(1, 100, 5)
	if !m.AcquirePredicateLock(tA.Handle, tag) {
		t.Fatal("A acquire: false")
	}
	if !m.AcquirePredicateLock(tB.Handle, tag) {
		t.Fatal("B acquire: false")
	}
	if got := m.PredicateLockTargetHolderCount(tag); got != 2 {
		t.Fatalf("holders = %d, want 2", got)
	}
	if err := m.Commit(tA); err != nil {
		t.Fatalf("Commit A: %v", err)
	}
	if got := m.PredicateLockTargetHolderCount(tag); got != 1 {
		t.Fatalf("holders after A commit = %d, want 1", got)
	}
	if err := m.Commit(tB); err != nil {
		t.Fatalf("Commit B: %v", err)
	}
	if got := m.PredicateLockTargetHolderCount(tag); got != 0 {
		t.Fatalf("holders after B commit = %d, want 0", got)
	}
}

// TestPredicateLock_InvalidTagRejected pins that an unconstructed tag
// (Rel == 0) returns false from Acquire — no entry in the per-xact
// set or in the global target map. Defensive: a buggy caller passing
// the zero PredicateLockTag must not poison the registry with a
// granularity-less placeholder.
func TestPredicateLock_InvalidTagRejected(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = m.Commit(tx) }()

	if m.AcquirePredicateLock(tx.Handle, PredicateLockTag{}) {
		t.Fatal("AcquirePredicateLock(zero tag) returned true")
	}
	if got := m.PredicateLockCount(tx.Handle); got != 0 {
		t.Fatalf("count after invalid acquire = %d, want 0", got)
	}
}

// TestPredicateLock_LimitsRoundTrip pins the GUC bridge contract:
// SetPredicateLockLimits with positive values updates the active
// limits; non-positive values are ignored so callers can adjust one
// dimension without resetting the others.
func TestPredicateLock_LimitsRoundTrip(t *testing.T) {
	m := NewManager()
	defaults := m.PredicateLockLimits()
	if defaults != DefaultPredicateLockLimits() {
		t.Fatalf("default limits = %+v, want %+v", defaults, DefaultPredicateLockLimits())
	}
	m.SetPredicateLockLimits(PredicateLockLimits{PerPage: 7})
	got := m.PredicateLockLimits()
	if got.PerPage != 7 {
		t.Fatalf("PerPage = %d, want 7", got.PerPage)
	}
	if got.PerXact != defaults.PerXact || got.PerRelation != defaults.PerRelation {
		t.Fatalf("partial set clobbered other fields: %+v", got)
	}
}

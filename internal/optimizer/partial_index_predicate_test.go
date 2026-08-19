package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Partial indexes must never be chosen on the plain-scan path.
//
// PG only builds index paths for a partial index whose predicate it has PROVEN
// from the query's restriction clauses: `check_index_predicates()` sets
// `index->predOK`, and `create_index_paths()` skips an index where it is false.
// goopg has no predicate-implication prover, so any index whose predicate is
// not trivially true must be declined — using it silently drops every row the
// predicate excludes.
//
// This was not hypothetical. `internal/planner/pathindexordered.go`
// (`addOneOrderedIndexPath`) already declined partial indexes for exactly this
// reason, but the guard was never mirrored onto the main scan path, so
// `findBTreeIndexForColumn` happily returned a partial index for an unrelated
// qual. Measured on a live server before the fix:
//
//	CREATE INDEX onek2_u1_prtl ON onek2 (unique1) WHERE unique1 < 20 OR unique1 > 980;
//	SELECT count(*) FROM onek2 WHERE unique1 = 50;   -- 0, must be 1
//	                        -> Index Only Scan using onek2_u1_prtl
//
// That is the mechanism behind the two regress divergences that survived the
// 20260806-232940 nightly (`portals_p2` FETCH from a cursor over
// `onek2 WHERE unique1 = 50`, and `select`'s `onek2` partial-index block).
// Both pass standalone and fail in the full suite because the partial indexes
// only exist once `create_index` has run — the order dependence was the
// symptom, the missing guard was the cause.
func TestPartialIndexNotChosenForUnprovenQual(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "onek2"}, []catalog.Column{
		{Name: "unique1", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "unique2", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "onek2_u1_prtl"}, tbl, []string{"unique1"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	// Mark it partial, the way CREATE INDEX ... WHERE does
	// (internal/executor/operators_ddl.go sets HasPredicate alongside
	// Predicate/PredicateString).
	idx.HasPredicate = true
	idx.PredicateString = "((unique1 < 20) OR (unique1 > 980))"

	if got := findBTreeIndexForColumn(cat, tbl, "unique1", nil); got != nil {
		t.Fatalf("findBTreeIndexForColumn returned partial index %q; a partial index "+
			"whose predicate is not proven from the quals must be declined "+
			"(it silently drops the rows the predicate excludes)", got.Name)
	}

	// The parameterized inner-side twin picks candidates through a separate
	// loop, so it needs the guard independently — a unit test on one path
	// proves nothing about the other.
	innerToOuter := map[string]Expr{"unique1": &ColumnRef{Name: "x"}}
	if got, _ := pickIndexCoveringAllLeadingColumns(cat, tbl, innerToOuter); got != nil {
		t.Fatalf("pickIndexCoveringAllLeadingColumns returned partial index %q; "+
			"same rule applies on the parameterized path", got.Name)
	}
}

// A non-partial index on the same shape must still be selected — the guard
// keys on HasPredicate, not on the index merely existing. Without this, the
// fix above could "pass" by disabling index scans wholesale.
func TestNonPartialIndexStillChosen(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "onek2"}, []catalog.Column{
		{Name: "unique1", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "onek2_u1"}, tbl, []string{"unique1"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	got := findBTreeIndexForColumn(cat, tbl, "unique1", nil)
	if got == nil {
		t.Fatal("findBTreeIndexForColumn declined a plain (non-partial) index")
	}
	if got.Name != "onek2_u1" {
		t.Fatalf("chose %q, want onek2_u1", got.Name)
	}

	innerToOuter := map[string]Expr{"unique1": &ColumnRef{Name: "x"}}
	if idx, _ := pickIndexCoveringAllLeadingColumns(cat, tbl, innerToOuter); idx == nil {
		t.Fatal("pickIndexCoveringAllLeadingColumns declined a plain (non-partial) index")
	}
}

// When BOTH a partial and a plain index cover the column, the plain one must
// win rather than the search stopping at the partial one. This pins the
// `continue` (skip this candidate) semantics against a `return nil` (abandon
// the search) regression.
func TestPlainIndexPreferredOverPartial(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "onek2"}, []catalog.Column{
		{Name: "unique1", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Partial registered FIRST so a naive implementation returns it.
	partial, err := cat.CreateIndex(parser.ObjectName{Name: "onek2_u1_prtl"}, tbl, []string{"unique1"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	partial.HasPredicate = true
	partial.PredicateString = "((unique1 < 20) OR (unique1 > 980))"
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "onek2_u1"}, tbl, []string{"unique1"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	got := findBTreeIndexForColumn(cat, tbl, "unique1", nil)
	if got == nil {
		t.Fatal("no index chosen; the plain index onek2_u1 is usable")
	}
	if got.Name != "onek2_u1" {
		t.Fatalf("chose %q, want the plain index onek2_u1", got.Name)
	}
}

// TestPartialIndexChosenWhenPredicateEqualsQual is the M0134-0017b headline
// case: the index predicate is LITERALLY the query's own equality qual
// (`seqno = 9999`, matching `hash_i4_partial_index` in
// postgres/src/test/regress/sql/hash_index.sql:56). provePartialIndexPredicate
// must accept it, and findBTreeIndexForColumn must return that partial index
// BY NAME — not merely "an index scan happened", which could hide a wrong
// index or a coincidental SeqScan-free plan.
func TestPartialIndexChosenWhenPredicateEqualsQual(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "hash_i4_heap"}, []catalog.Column{
		{Name: "seqno", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "random", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "hash_i4_partial_index"}, tbl, []string{"seqno"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	idx.HasPredicate = true
	idx.Predicate = &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Column: "seqno"},
		Right: &parser.IntegerConst{Value: 9999},
	}
	idx.PredicateString = "(seqno = 9999)"

	// Select a non-indexed column too (mirroring the design doc's `SELECT *`
	// repro) so the plan needs a heap fetch and picks *IndexScan rather than
	// *IndexOnlyScan — both prove the same predicate-implication path, but
	// this keeps the assertion shape simple and matches the acceptance case.
	stmt := parseOne(t, "SELECT seqno, random FROM hash_i4_heap WHERE seqno = 9999")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	is, ok := proj.Child.(*IndexScan)
	if !ok {
		t.Fatalf("child=%T want *IndexScan (proven partial index not selected)", proj.Child)
	}
	if is.Index == nil || is.Index.Name != "hash_i4_partial_index" {
		got := "<nil>"
		if is.Index != nil {
			got = is.Index.Name
		}
		t.Fatalf("chose index %q, want hash_i4_partial_index", got)
	}
}

// TestPartialIndexNotChosenForNonImpliedQual is the load-bearing
// non-regression case: a partial index whose predicate is an OR of two
// ranges (`unique1 < 20 OR unique1 > 980`, matching `onek2_u1_prtl` from the
// wrong-answer bug pinned by TestPartialIndexNotChosenForUnprovenQual above)
// must NOT be chosen for `unique1 = 50` — 50 is not implied by that OR, and
// this is the exact shape that produced 0 rows where 1 exists. See the
// deferral ledger row dated 2026-08-07 (`M0127 S7 gate /
// AI-20260806-232940-001,-002`) and docs/design/
// m0134-0017-partial-index-predicate-implication.md. Asserted both directly
// against the helper (provePartialIndexPredicate must refuse an OR-shaped
// predicate) and through the full plan (no IndexScan on the partial index).
func TestPartialIndexNotChosenForNonImpliedQual(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "onek2"}, []catalog.Column{
		{Name: "unique1", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "unique2", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "onek2_u1_prtl"}, tbl, []string{"unique1"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	idx.HasPredicate = true
	// OR(unique1 < 20, unique1 > 980) — not a single Var-op-Const clause, so
	// provePartialIndexPredicate's asColOpConst recognizer must refuse it
	// outright (asColOpConst only unwraps a *BinaryOp whose non-Const operand
	// is a *ColumnRef; here the top-level BinaryOp's operands are themselves
	// BinaryOps, neither of which is a ColumnRef or a Const).
	idx.Predicate = &parser.BinaryOp{
		Op:    parser.OpOr,
		Left:  &parser.BinaryOp{Op: parser.OpLt, Left: &parser.ColumnRef{Column: "unique1"}, Right: &parser.IntegerConst{Value: 20}},
		Right: &parser.BinaryOp{Op: parser.OpGt, Left: &parser.ColumnRef{Column: "unique1"}, Right: &parser.IntegerConst{Value: 980}},
	}
	idx.PredicateString = "((unique1 < 20) OR (unique1 > 980))"

	// Direct helper assertion: the resolved predicate proves nothing against
	// `unique1 = 50`.
	resolvedPred, err := ResolveIndexPredicate(idx.Predicate, tbl)
	if err != nil {
		t.Fatalf("ResolveIndexPredicate: %v", err)
	}
	queryClause := &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Name: "unique1"}, Right: &IntegerConst{Value: 50}}
	if provePartialIndexPredicate(resolvedPred, queryClause) {
		t.Fatal("provePartialIndexPredicate proved an OR predicate implied by unique1 = 50 — unsound, reopens the 2026-08-07 wrong-answer bug")
	}

	// Plan-level assertion: findBTreeIndexForColumn must decline the index,
	// so the query still falls through to a SeqScan/Filter (today's
	// behavior), never an IndexScan on onek2_u1_prtl.
	got := findBTreeIndexForColumn(cat, tbl, "unique1", queryClause)
	if got != nil {
		t.Fatalf("findBTreeIndexForColumn returned partial index %q for unique1 = 50 against predicate %q — would silently drop rows",
			got.Name, idx.PredicateString)
	}
}

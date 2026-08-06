package planner

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

	if got := findBTreeIndexForColumn(cat, tbl, "unique1"); got != nil {
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

	got := findBTreeIndexForColumn(cat, tbl, "unique1")
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

	got := findBTreeIndexForColumn(cat, tbl, "unique1")
	if got == nil {
		t.Fatal("no index chosen; the plain index onek2_u1 is usable")
	}
	if got.Name != "onek2_u1" {
		t.Fatalf("chose %q, want the plain index onek2_u1", got.Name)
	}
}

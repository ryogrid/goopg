package planner

// M0127-P5.4c-ii-a — `build_index_pathkeys` (pathkeysindex.go).
//
// These tests are the falsifiable half of the slice. The paths are LIVE since
// M0127-P5.9 (2026-08-06) — `GOOPG_PGSHAPED_DP` defaults ON and `planSelect`
// calls the search — so the repository CAN now observe a wrong recorded
// ordering, just far more expensively than here; what is pinned
// here is each of PG's loop rules separately — INCLUDE columns excluded,
// per-column ASC/DESC and NULLS placement, backward inversion of BOTH, the STOP
// (not skip) on an unusable column, non-orderable access methods, and the
// redundant-repeat drop — plus the one end-to-end fact that the parameterised
// index path now carries the order.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// bipIndex builds a bare `catalog.Index` over the given key columns. Direction
// metadata is left at PG's default (ASC NULLS LAST) and set per-test where the
// test is about direction.
func bipIndex(cols ...string) *catalog.Index {
	return &catalog.Index{Name: "idx", Columns: cols, Method: "btree"}
}

// bipExprs maps each name to a distinct `*ColumnRef` with a distinct `Index`, so
// that two different columns can never compare equal under `exprEqual`.
func bipExprs(names ...string) map[string]Expr {
	m := make(map[string]Expr, len(names))
	for i, n := range names {
		m[n] = &ColumnRef{Name: n, Index: i, Type: catalog.Type{Name: "int4"}}
	}
	return m
}

// wantKeys asserts the pathkey list names exactly `names`, ascending/nulls-last
// unless overridden by the caller's own checks.
func wantKeyNames(t *testing.T, got []PathKey, names ...string) {
	t.Helper()
	if len(got) != len(names) {
		t.Fatalf("got %d pathkeys, want %d (%v)", len(got), len(names), names)
	}
	for i, n := range names {
		cr, ok := got[i].Expr.(*ColumnRef)
		if !ok || cr.Name != n {
			t.Fatalf("pathkey %d = %#v, want column %q", i, got[i].Expr, n)
		}
	}
}

// TestBuildIndexPathkeysMultiColumnInIndexOrder: the base case. A scan of
// `(a, b)` delivers rows ordered by a then b, ascending with nulls last —
// PG's default `indoption` for every column of a plain `CREATE INDEX`.
func TestBuildIndexPathkeysMultiColumnInIndexOrder(t *testing.T) {
	keys := buildIndexPathkeys(bipIndex("a", "b"), bipExprs("a", "b", "c"), false)
	wantKeyNames(t, keys, "a", "b")
	for i, k := range keys {
		if !k.SortAsc || k.NullsFirst {
			t.Fatalf("key %d = %+v; want ASC NULLS LAST", i, k)
		}
	}
}

// TestBuildIndexPathkeysExpressionsAreTheCallersOwn: the reason `colExprs` is a
// parameter rather than something the builder synthesises. goopg's pathkeys are
// syntactic (design 04 §2.1), so the pathkey must BE the expression the join
// clauses carry — a same-named ColumnRef with a different `Index` is a different
// column to `exprEqual`, and a merge join would then never match its own key.
func TestBuildIndexPathkeysExpressionsAreTheCallersOwn(t *testing.T) {
	exprs := bipExprs("a")
	keys := buildIndexPathkeys(bipIndex("a"), exprs, false)
	if len(keys) != 1 || keys[0].Expr != exprs["a"] {
		t.Fatalf("pathkey expression %#v is not the caller's own %#v", keys, exprs["a"])
	}
	// The negative half: a freshly minted, same-named ColumnRef does NOT match.
	if exprEqual(keys[0].Expr, &ColumnRef{Name: "a", Index: 99, Type: catalog.Type{Name: "int4"}}) {
		t.Fatal("a re-synthesised ColumnRef compared equal; the pathkey would match the wrong column")
	}
}

// TestBuildIndexPathkeysHonoursDescAndNullsFirst: `index->reverse_sort[i]` /
// `index->nulls_first[i]` (pathkeys.c:775-776), which goopg mirrors from
// pg_index.indoption. A `(a DESC NULLS FIRST, b)` index delivers exactly that,
// per column — the placement is not a property of the index as a whole.
func TestBuildIndexPathkeysHonoursDescAndNullsFirst(t *testing.T) {
	idx := bipIndex("a", "b")
	idx.ColDescending = []bool{true, false}
	idx.ColNullsFirst = []bool{true, false}
	keys := buildIndexPathkeys(idx, bipExprs("a", "b"), false)
	wantKeyNames(t, keys, "a", "b")
	if keys[0].SortAsc || !keys[0].NullsFirst {
		t.Fatalf("key a = %+v; want DESC NULLS FIRST", keys[0])
	}
	if !keys[1].SortAsc || keys[1].NullsFirst {
		t.Fatalf("key b = %+v; want ASC NULLS LAST", keys[1])
	}
}

// TestBuildIndexPathkeysBackwardInvertsBoth: `ScanDirectionIsBackward`
// (pathkeys.c:770-774) flips the direction AND the null placement. Flipping only
// the direction would claim an order the scan does not deliver — a backward scan
// of an ASC NULLS LAST index emits nulls FIRST.
func TestBuildIndexPathkeysBackwardInvertsBoth(t *testing.T) {
	keys := buildIndexPathkeys(bipIndex("a"), bipExprs("a"), true)
	wantKeyNames(t, keys, "a")
	if keys[0].SortAsc || !keys[0].NullsFirst {
		t.Fatalf("backward scan of ASC NULLS LAST gave %+v; want DESC NULLS FIRST", keys[0])
	}
}

// TestBuildIndexPathkeysStopsAtUnusableColumn: PG breaks out of the loop rather
// than skipping (:815-822). An index on (a, b, c) whose `b` is not usable
// delivers (a) — emitting (a, c) would be WRONG, since c is ordered only within
// equal b, and a merge join trusting it would drop matching rows.
func TestBuildIndexPathkeysStopsAtUnusableColumn(t *testing.T) {
	keys := buildIndexPathkeys(bipIndex("a", "b", "c"), bipExprs("a", "c"), false)
	wantKeyNames(t, keys, "a")
}

// TestBuildIndexPathkeysSkipsExpressionColumn: an expression index column is
// recorded as an empty name with the AST in `ColExprs`. Translating that into
// the planner expression the query's clauses carry needs a binding context this
// seam does not have, so the list stops there — the same conservative direction
// as an unusable column. Ledgered as a deferral, not a design choice.
func TestBuildIndexPathkeysSkipsExpressionColumn(t *testing.T) {
	idx := bipIndex("a", "", "c")
	keys := buildIndexPathkeys(idx, bipExprs("a", "c"), false)
	wantKeyNames(t, keys, "a")
}

// TestBuildIndexPathkeysNonOrderableAccessMethods: PG's `sortopfamily == NULL`
// (:748). A GiST index returns nothing in key order. A `USING hash` index is
// built on goopg's B-tree substrate — `Method` stays "btree" — and so WOULD come
// back ordered, but PG's hash AM is not orderable, and claiming an order PG never
// claims is how goopg would plan a merge join PG would not.
func TestBuildIndexPathkeysNonOrderableAccessMethods(t *testing.T) {
	gist := bipIndex("a")
	gist.Method = "gist"
	if keys := buildIndexPathkeys(gist, bipExprs("a"), false); keys != nil {
		t.Fatalf("gist index claimed ordering %v", keys)
	}
	hash := bipIndex("a")
	hash.DeclaredHash = true
	if keys := buildIndexPathkeys(hash, bipExprs("a"), false); keys != nil {
		t.Fatalf("USING hash index claimed ordering %v", keys)
	}
}

// TestBuildIndexPathkeysDropsRedundantRepeat: `pathkey_is_redundant` (:800). A
// second key over an expression already sorted on can never reorder anything, so
// it is dropped and the loop CONTINUES — a following distinct column is still
// usable, unlike the break above.
func TestBuildIndexPathkeysDropsRedundantRepeat(t *testing.T) {
	keys := buildIndexPathkeys(bipIndex("a", "a", "b"), bipExprs("a", "b"), false)
	wantKeyNames(t, keys, "a", "b")
}

// TestBuildIndexPathkeysExcludesIncludeColumns: PG breaks at
// `i >= index->nkeycolumns` (:763-764) because INCLUDE columns are stored
// unordered. goopg keeps them in a separate field, so the exclusion is structural
// — this pins that the field is genuinely separate and not folded into Columns.
func TestBuildIndexPathkeysExcludesIncludeColumns(t *testing.T) {
	idx := bipIndex("a")
	idx.IncludeColumns = []string{"b"}
	keys := buildIndexPathkeys(idx, bipExprs("a", "b"), false)
	wantKeyNames(t, keys, "a")
}

// TestParameterizedIndexPathCarriesIndexOrdering is the end-to-end fact: the one
// index-path constructor that exists today records the ordering, so
// `Path.Pathkeys` is non-empty for the first time from a real index and
// `addPath`'s pathkey dimension stops being a constant `dimEqual`.
//
// PG passes the same `useful_pathkeys` to the parameterised path as to the plain
// one (`build_index_paths`, indxpath.c:750-800), so this is not a special case
// for parameterisation — it is simply the only index path goopg builds.
func TestParameterizedIndexPathCarriesIndexOrdering(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	outer, inner := relsetOf(0), relsetOf(1)
	// `lineitem.l_orderkey = orders.o_orderkey` — the PK probe, which binds
	// `orders_pkey` entirely.
	s := ppiCtx(t, orders, 1500, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))
	s.addParameterizedIndexPaths(cat)

	rel := s.relMap[inner]
	var idxPath *Path
	for _, p := range rel.Pathlist {
		if p.Kind == PathIndexScan {
			idxPath = p
			break
		}
	}
	if idxPath == nil {
		t.Fatal("no parameterised index path was generated")
	}
	if len(idxPath.Pathkeys) != 1 {
		t.Fatalf("index path pathkeys = %v; want the one-column PK ordering", idxPath.Pathkeys)
	}
	cr, ok := idxPath.Pathkeys[0].Expr.(*ColumnRef)
	if !ok || cr.Name != "o_orderkey" {
		t.Fatalf("pathkey expression %#v; want o_orderkey", idxPath.Pathkeys[0].Expr)
	}
	if !idxPath.Pathkeys[0].SortAsc || idxPath.Pathkeys[0].NullsFirst {
		t.Fatalf("pathkey %+v; want ASC NULLS LAST", idxPath.Pathkeys[0])
	}
	// The pathkey must be the clause's OWN operand expression, not a copy: that
	// is what lets a merge clause one level up recognise the order.
	if !exprEqual(cr, s.clauses.all[0].rightKey) {
		t.Fatal("pathkey expression does not compare equal to the clause's inner operand")
	}
}

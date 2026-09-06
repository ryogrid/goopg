package optimizer

// C-07 / P3-06 — `standard_qp_callback` + the completed `has_useful_pathkeys`
// (querypathkeys.go).
//
// Three groups, in decreasing distance from the plan:
//
//   - the PRECEDENCE rule on its own, which is the half of upstream most
//     likely to be mis-remembered ("ORDER BY wins" is wrong three times over);
//   - the DERIVATION against real parsed statements, where every
//     goopg-specific decline lives;
//   - the DECISION `hasUsefulPathkeys` gates, including the one this item
//     deliberately does NOT flip. That last test is a red-then-green marker
//     for C-11/C-12: it pins today's decline and names what has to exist
//     before it may become an offer.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// qpkKeys renders a pathkey list as `name/ASC|DESC/NF|NL` triples, which is
// the whole of what a PathKey claims and therefore the whole of what these
// tests compare.
func qpkKeys(keys []PathKey) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		name := "?"
		if col, ok := k.Expr.(*ColumnRef); ok {
			name = col.Name
		}
		dir := "DESC"
		if k.SortAsc {
			dir = "ASC"
		}
		nulls := "NL"
		if k.NullsFirst {
			nulls = "NF"
		}
		out = append(out, name+"/"+dir+"/"+nulls)
	}
	return out
}

func qpkEqual(got []PathKey, want []string) bool {
	g := qpkKeys(got)
	if len(g) != len(want) {
		return false
	}
	for i := range g {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

// qpkCtx is the FROM-level resolve context `planSelect` hands the derivation:
// one relation `t` with columns a, b, c at binding offsets 0..2.
func qpkCtx(t *testing.T) *resolveContext {
	t.Helper()
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
		{Name: "c", Type: catalog.Type{Name: "int4"}, Ordinal: 2},
	}
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	schema := tableSchemaWithSource(tbl, 0)
	bindings := []rangeBinding{{table: tbl, alias: "t", offset: 0, sourceIdx: 0}}
	ctx := newResolveContext(bindings, schema, DefaultPlannerSettings())
	ctx.cat = c
	return ctx
}

func qpkParse(t *testing.T, sql string) *parser.SelectStmt {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("parse %q: %d statements, want 1", sql, len(stmts))
	}
	sel, ok := stmts[0].(*parser.SelectStmt)
	if !ok {
		t.Fatalf("parse %q: %T, want *parser.SelectStmt", sql, stmts[0])
	}
	return sel
}

// TestChooseQueryPathkeysPrecedence pins the tail of `standard_qp_callback`
// (planner.c:3600) exactly, including the two steps that are counter-intuitive:
// GROUP BY beats a LONGER ORDER BY (upstream refuses to trade an exploitable
// grouping order for a more rigorous output order), and DISTINCT beats ORDER BY
// only when STRICTLY longer — an equal-length DISTINCT loses, because ORDER BY
// then carries the directions the query actually asked for.
func TestChooseQueryPathkeysPrecedence(t *testing.T) {
	k := func(names ...string) []PathKey {
		out := make([]PathKey, len(names))
		for i, n := range names {
			out[i] = PathKey{Expr: &ColumnRef{Name: n}, SortAsc: true}
		}
		return out
	}
	cases := []struct {
		name string
		sets queryPathkeySets
		want []string
	}{
		{"nothing at all", queryPathkeySets{}, nil},
		{"group beats everything", queryPathkeySets{
			group: k("a"), window: k("b"), distinct: k("c", "d"), sort: k("e"), setop: k("f"),
		}, []string{"a/ASC/NL"}},
		{"window beats distinct and sort", queryPathkeySets{
			window: k("b"), distinct: k("c", "d"), sort: k("e"), setop: k("f"),
		}, []string{"b/ASC/NL"}},
		{"strictly longer distinct beats sort", queryPathkeySets{
			distinct: k("c", "d"), sort: k("e"),
		}, []string{"c/ASC/NL", "d/ASC/NL"}},
		{"equal-length distinct loses to sort", queryPathkeySets{
			distinct: k("c"), sort: k("e"),
		}, []string{"e/ASC/NL"}},
		{"shorter distinct loses to sort", queryPathkeySets{
			distinct: k("c"), sort: k("e", "f"),
		}, []string{"e/ASC/NL", "f/ASC/NL"}},
		{"sort beats setop", queryPathkeySets{
			sort: k("e"), setop: k("f"),
		}, []string{"e/ASC/NL"}},
		{"setop is the last resort", queryPathkeySets{
			setop: k("f"),
		}, []string{"f/ASC/NL"}},
		{"distinct alone wins over an absent sort", queryPathkeySets{
			distinct: k("c"), setop: k("f"),
		}, []string{"c/ASC/NL"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chooseQueryPathkeys(tc.sets); !qpkEqual(got, tc.want) {
				t.Fatalf("query_pathkeys = %v; want %v", qpkKeys(got), tc.want)
			}
		})
	}
}

// TestDeriveQueryPathkeys walks the derivation against real statements. Each
// case names the upstream rule it stands for.
func TestDeriveQueryPathkeys(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{
			// make_pathkeys_for_sortclauses over parse->sortClause; ASC
			// defaults to NULLS LAST and DESC to NULLS FIRST.
			name: "plain ORDER BY",
			sql:  "SELECT a, b FROM t ORDER BY a, b DESC",
			want: []string{"a/ASC/NL", "b/DESC/NF"},
		},
		{
			// An explicit NULLS placement overrides the direction default.
			name: "ORDER BY with explicit nulls placement",
			sql:  "SELECT a FROM t ORDER BY a DESC NULLS LAST",
			want: []string{"a/DESC/NL"},
		},
		{
			// GROUP BY wins over ORDER BY even though ORDER BY is longer.
			name: "GROUP BY beats a longer ORDER BY",
			sql:  "SELECT a, count(*) FROM t GROUP BY a ORDER BY count(*), a",
			want: []string{"a/ASC/NL"},
		},
		{
			// transformGroupClause reuses the matching sortClause entry, so
			// the grouping order inherits ORDER BY's DESC rather than the
			// default ASC of a bare grouping item.
			name: "GROUP BY inherits the ORDER BY direction",
			sql:  "SELECT a FROM t GROUP BY a ORDER BY a DESC",
			want: []string{"a/DESC/NF"},
		},
		{
			// ...and its ORDER. `GROUP BY a, b ORDER BY b` groups by (b, a).
			name: "GROUP BY takes the ORDER BY prefix order",
			sql:  "SELECT a, b FROM t GROUP BY a, b ORDER BY b",
			want: []string{"b/ASC/NL", "a/ASC/NL"},
		},
		{
			// The shared prefix ends at the first ORDER BY item that is not a
			// grouping item; the rest of the group list follows with default
			// ordering.
			name: "GROUP BY prefix reuse stops at a non-grouping sort item",
			sql:  "SELECT a, b, count(*) FROM t GROUP BY a, b ORDER BY count(*), b DESC",
			want: []string{"a/ASC/NL", "b/ASC/NL"},
		},
		{
			// A pathkey list is a PREFIX contract: an unexpressible key
			// truncates it rather than letting a later key move up.
			name: "ORDER BY truncates at the first unexpressible key",
			sql:  "SELECT a, b, count(*) FROM t GROUP BY a, b ORDER BY a, count(*), b",
			want: []string{"a/ASC/NL", "b/ASC/NL"},
		},
		{
			name: "ORDER BY on an aggregate alone yields nothing",
			sql:  "SELECT count(*) FROM t ORDER BY count(*)",
			want: nil,
		},
		{
			// Positional and alias references reach the column underneath.
			name: "ORDER BY positional",
			sql:  "SELECT b, a FROM t ORDER BY 2 DESC, 1",
			want: []string{"a/DESC/NF", "b/ASC/NL"},
		},
		{
			name: "ORDER BY alias",
			sql:  "SELECT a AS x FROM t ORDER BY x",
			want: []string{"a/ASC/NL"},
		},
		{
			// transformDistinctClause: sortClause entries first, then the
			// remaining target-list entries. Two keys beats ORDER BY's one.
			name: "DISTINCT is more rigorous than ORDER BY",
			sql:  "SELECT DISTINCT a, b FROM t ORDER BY b",
			want: []string{"b/ASC/NL", "a/ASC/NL"},
		},
		{
			// Equal length: ORDER BY wins, carrying its own direction.
			name: "DISTINCT no more rigorous than ORDER BY",
			sql:  "SELECT DISTINCT a FROM t ORDER BY a DESC",
			want: []string{"a/DESC/NF"},
		},
		{
			name: "DISTINCT with no ORDER BY",
			sql:  "SELECT DISTINCT b, a FROM t",
			want: []string{"b/ASC/NL", "a/ASC/NL"},
		},
		{
			// DISTINCT ON is the written list, with the validated ORDER BY
			// prefix supplying directions.
			name: "DISTINCT ON",
			sql:  "SELECT DISTINCT ON (a, b) a, b, c FROM t ORDER BY a DESC, b",
			want: []string{"a/DESC/NF", "b/ASC/NL"},
		},
		{
			// make_pathkeys_for_window over the FIRST (bottom) window:
			// PARTITION BY keys with default ordering, then ORDER BY keys.
			name: "window beats ORDER BY",
			sql:  "SELECT rank() OVER (PARTITION BY a ORDER BY b DESC) FROM t ORDER BY c",
			want: []string{"a/ASC/NL", "b/DESC/NF"},
		},
		{
			// GROUP BY still outranks a window.
			name: "GROUP BY beats a window",
			sql:  "SELECT a, rank() OVER (PARTITION BY b) FROM t GROUP BY a, b",
			want: []string{"a/ASC/NL", "b/ASC/NL"},
		},
		{
			// Grouping sets are declined whole: goopg has no rollup list to
			// read a "first rollup's groupClause" from, and the flattened
			// union over-states what any one set delivers.
			name: "grouping sets decline",
			sql:  "SELECT a, b, count(*) FROM t GROUP BY ROLLUP(a, b)",
			want: nil,
		},
		{
			// A key naming no column of the searched relations ends the list.
			name: "ORDER BY an expression yields nothing",
			sql:  "SELECT a FROM t ORDER BY a + 1",
			want: nil,
		},
		{
			name: "no ordering clause at all",
			sql:  "SELECT a FROM t WHERE a > 1",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := qpkCtx(t)
			got := deriveQueryPathkeys(qpkParse(t, tc.sql), ctx)
			if !qpkEqual(got, tc.want) {
				t.Fatalf("query_pathkeys = %v; want %v", qpkKeys(got), tc.want)
			}
		})
	}
}

// TestDeriveQueryPathkeysBindsTheSearchesOwnColumns: goopg's pathkeys are
// SYNTACTIC (pathkeys.go), so a pathkey is only useful if it carries the very
// column identity the search's clauses and `buildIndexPathkeys` compare with
// `exprEqual`. A key resolved against some other schema would compare unequal
// to every index column and the whole derivation would be inert for a silent
// reason rather than a stated one.
func TestDeriveQueryPathkeysBindsTheSearchesOwnColumns(t *testing.T) {
	ctx := qpkCtx(t)
	keys := deriveQueryPathkeys(qpkParse(t, "SELECT a, c FROM t ORDER BY c, a"), ctx)
	if len(keys) != 2 {
		t.Fatalf("got %v; want two keys", qpkKeys(keys))
	}
	col, ok := keys[0].Expr.(*ColumnRef)
	if !ok {
		t.Fatalf("first pathkey expr is %T; want *ColumnRef", keys[0].Expr)
	}
	if col.Index != 2 {
		t.Fatalf("pathkey on `c` has binding index %d; want 2 (the FROM-level coordinate the "+
			"search's clause operands are written in)", col.Index)
	}
	if col.SourceTableIdx != 0 {
		t.Fatalf("pathkey on `c` has SourceTableIdx %d; want 0", col.SourceTableIdx)
	}
}

// TestDeriveQueryPathkeysDeclinesAnOuterReference: a correlated ORDER BY names
// a column of an ENCLOSING query level, which no path of THIS search orders by.
// `resolveColumnRef` answers such a reference with an `*OuterColumnRef`, and
// claiming it as a pathkey would let a consumer skip a sort on the strength of
// an ordering the search never delivers.
func TestDeriveQueryPathkeysDeclinesAnOuterReference(t *testing.T) {
	outer := qpkCtx(t)
	inner := qpkCtx(t)
	inner.bindings = nil
	inner.schema = nil
	inner.parent = outer
	if got := deriveQueryPathkeys(qpkParse(t, "SELECT 1 FROM t ORDER BY a"), inner); len(got) != 0 {
		t.Fatalf("query_pathkeys = %v; want none (the key resolves to an outer level)", qpkKeys(got))
	}
}

// TestHasUsefulPathkeysArms is `has_useful_pathkeys` (pathkeys.c:2319). Both
// live arms, and the case that must stay false: no join clause mentions the
// rel AND the query asked for no ordering, so an ordered path over it could
// serve nobody.
func TestHasUsefulPathkeysArms(t *testing.T) {
	newCtx := func(t *testing.T, clauses []*restrictInfo, qpk []PathKey) (*searchCtx, *RelOptInfo) {
		t.Helper()
		s, err := newSearchCtx(2, defaultCostParams(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			rel := newRelOptInfo(RelSet(1)<<uint(i), 100, 32)
			if err := s.addRel(rel); err != nil {
				t.Fatal(err)
			}
		}
		s.clauses = &restrictInfoList{all: clauses}
		s.queryPathkeys = qpk
		return s, s.findRel(relsetOf(1))
	}
	order := []PathKey{{Expr: &ColumnRef{Name: "a"}, SortAsc: true}}
	clause := ppiEquiClause(relsetOf(0), "x", relsetOf(1), "y")
	otherClause := ppiEquiClause(relsetOf(0), "x", relsetOf(0), "z")

	if s, rel := newCtx(t, nil, nil); s.hasUsefulPathkeys(rel) {
		t.Fatal("no join clause and no query ordering: has_useful_pathkeys must be false")
	}
	if s, rel := newCtx(t, []*restrictInfo{clause}, nil); !s.hasUsefulPathkeys(rel) {
		t.Fatal("a join clause mentioning the rel must make pathkeys useful (merging arm)")
	}
	if s, rel := newCtx(t, nil, order); !s.hasUsefulPathkeys(rel) {
		t.Fatal("query_pathkeys must make pathkeys useful (ordering arm)")
	}
	// The merging arm is PER-REL, as upstream's `rel->joininfo` is: a clause
	// that mentions only the other relation does not open this one.
	if s, rel := newCtx(t, []*restrictInfo{otherClause}, nil); s.hasUsefulPathkeys(rel) {
		t.Fatal("a clause naming only rel 0 must not make rel 1's pathkeys useful")
	}
	if s, _ := newCtx(t, nil, order); s.hasUsefulPathkeys(nil) {
		t.Fatal("a nil rel must not be useful")
	}
}

// TestAddOrderedIndexPathsGateIsCompleteButGenerationStaysShut is C-07's
// recorded scope line, as a test rather than a comment — RE-ADJUDICATED
// 2026-09-07, after C-11 and C-12 both landed.
//
// The rel below has NO join clause and an index whose leading column is
// exactly what the query's ORDER BY asks for. `has_useful_pathkeys` says yes
// — the ordering arm is live — and the producer still emits nothing, because
// the useful-COLUMN set is `pathkeys_useful_for_merging` alone
// (`mergeableColumnExprsFor`); `pathkeys_useful_for_ordering` has no
// counterpart.
//
// The PREVIOUS revision of this test named C-11 (`ORDERED` upper rel) and
// C-12 (a real upper-rel `PathSort`) as the item that would flip it
// red-to-green. Both landed (2026-09-06) and NEITHER flipped it. That is the
// new fact this test records, and it was measured, not reasoned:
//
//   - the widening itself works. Unioning the query-pathkey columns into
//     `colExprs` makes `btg`'s map go `[w]` -> `[w x]` for
//     `… WHERE btg.w = oth.k ORDER BY btg.x` and `addOneOrderedIndexPath`
//     then adds a real `index.ordered` path on `btg_x_y_idx`;
//   - and the plan does not move — not on cost, and not with
//     `enable_seqscan = off`. The chosen path comes back through
//     `finalPath` with `pathkeys=0`, `planJoinlistSearch` publishes
//     `r.node`, and `createOrderedPaths` stacks its Sort on a
//     `newPrebuiltPath` that carries no ordering at all
//     (`TestCreateOrderedPathsInputArmIsUnreachableFromANode`).
//
// So the widening still buys an ordering-only full index scan that can only
// lose on total cost, or win `CheapestStartup` under a LIMIT while the
// redundant Sort above it still runs; and, on a GROUP BY, silently disable
// the GROUP_AGG producer's index-driven sorted-input variant, which matches
// on the child being a bare `*SeqScan` (`indexOrderedAggInput`,
// groupingpaths.go). Unmotivated plan churn, so it is still not forced.
//
// One blocker found in the re-adjudication is new and independent of the
// seam: `addOrderedIndexPaths` runs only INSIDE the PG-shaped join search,
// and `tryPGShapedJoinSearch` declines at `nrels < 2` (joinsearchseam.go).
// A single-table `SELECT … FROM t ORDER BY t.pk` — the canonical shape the
// widening is meant to serve — never reaches the producer at all, whatever
// the useful-column set says.
//
// This test is therefore still a MARKER, not a preference. Its unblocker is
// now named precisely: a search boundary that publishes the chosen PATH (or
// at least its `Pathkeys`) instead of a bare Node, so
// `createOrderedPaths`' `upper.ordered.input` arm becomes reachable. On that
// day, flip both this test and its consumer-side twin.
func TestAddOrderedIndexPathsGateIsCompleteButGenerationStaysShut(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	inner := relsetOf(1)
	// No join clauses at all — the merging arm is shut.
	s := orderedCtx(t, orders, 1_500_000)
	// ...and the query wants exactly `orders_pkey`'s ordering.
	s.queryPathkeys = []PathKey{{Expr: &ColumnRef{Name: "o_orderkey"}, SortAsc: true}}

	rel := s.findRel(inner)
	if !s.hasUsefulPathkeys(rel) {
		t.Fatal("has_useful_pathkeys must pass on the ordering arm; the C-07 gate is complete")
	}

	s.addOrderedIndexPaths(cat)

	if got := orderedPathsOf(rel); len(got) != 0 {
		t.Fatalf("got %d ordered index paths on the ordering arm alone; want 0 until the search "+
			"boundary carries Pathkeys across the seam (C-11 and C-12 landed and did not: "+
			"see TestCreateOrderedPathsInputArmIsUnreachableFromANode). Flipping this test is "+
			"that item's red-then-green marker", len(got))
	}
}

// TestAddOrderedIndexPathsGateIsANoOpForTodaysProducer: replacing the old
// blanket `len(s.clauses.all) == 0` early return with a per-rel
// `has_useful_pathkeys` must not have narrowed generation. It cannot: the gate
// tests whether ANY clause mentions the rel, while `mergeableColumnExprsFor`
// needs a clause with the rel on exactly one SIDE — strictly stronger. This
// pins the direction of that implication on the shape that would break first,
// a rel whose only clause is an unmergeable inequality.
func TestAddOrderedIndexPathsGateIsANoOpForTodaysProducer(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	outer, inner := relsetOf(0), relsetOf(1)
	// A join clause that is NOT an equijoin: `joininfo` holds it (so the gate
	// opens) but it is no mergeclause (so no column becomes useful).
	ineq := &restrictInfo{relids: outer | inner, ecID: noEquivClass}
	s := orderedCtx(t, orders, 1_500_000, ineq)

	rel := s.findRel(inner)
	if !s.hasUsefulPathkeys(rel) {
		t.Fatal("a join clause mentioning the rel must open the gate, mergeable or not")
	}
	s.addOrderedIndexPaths(cat)
	if got := orderedPathsOf(rel); len(got) != 0 {
		t.Fatalf("got %d ordered index paths for a non-mergeable join clause; want 0", len(got))
	}
}

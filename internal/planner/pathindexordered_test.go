package planner

// M0127-P5.4c-ii-b — unparameterised ordered index paths (pathindexordered.go).
//
// The property the slice exists for is the first test below: a path with
// pathkeys AND an empty `RequiredOuter`. Everything P5.4c-i built for the
// merge arm — the sort-skip branch, `build_join_pathkeys` — is unreachable
// without one, because `addMergeJoinPath` refuses a parameterised path
// outright. The rest pin the generation gate, which is the half most likely to
// be wrong in the quiet direction: producing a full-index scan for every index
// on every relation would flood `addPath` with paths no join can use.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// orderedCtx is `ppiCtx` with the base-row counts `cost_index` needs. They are
// absent from `ppiCtx` because the parameterised paths read `rel.Rows` and the
// statistics, never `baseRelInfo.baseRows`; the ordered path reads the
// PRE-restriction count, which is what `baserel->tuples` means.
func orderedCtx(t *testing.T, inner *catalog.Table, innerRows int64, clauses ...*restrictInfo) *searchCtx {
	t.Helper()
	s := ppiCtx(t, inner, float64(innerRows), clauses...)
	s.relInfos = []baseRelInfo{{baseRows: 100}, {table: inner, baseRows: innerRows}}
	return s
}

// orderedPathsOf returns the unparameterised index paths on the rel.
func orderedPathsOf(rel *RelOptInfo) []*Path {
	var out []*Path
	for _, p := range rel.Pathlist {
		if p.Kind == PathIndexScan && p.RequiredOuter == 0 {
			out = append(out, p)
		}
	}
	return out
}

// TestAddOrderedIndexPathsProducesAnUnparameterisedOrderedPath is the slice's
// whole reason for existing: an index path that carries the index's ordering
// and requires NOTHING from outside the relation, which is the only shape
// `try_mergejoin_path` (joinpath.c:1073-1081) will accept as a merge input.
func TestAddOrderedIndexPathsProducesAnUnparameterisedOrderedPath(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	outer, inner := relsetOf(0), relsetOf(1)
	s := orderedCtx(t, orders, 1_500_000, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))

	s.addOrderedIndexPaths(cat)

	paths := orderedPathsOf(s.findRel(inner))
	if len(paths) != 1 {
		t.Fatalf("got %d unparameterised index paths; want exactly one (orders_pkey)", len(paths))
	}
	p := paths[0]
	if len(p.Pathkeys) != 1 {
		t.Fatalf("pathkeys = %v; want one key on o_orderkey", p.Pathkeys)
	}
	col, ok := p.Pathkeys[0].Expr.(*ColumnRef)
	if !ok || col.Name != "o_orderkey" {
		t.Fatalf("pathkey expr = %#v; want the o_orderkey ColumnRef the clause carries", p.Pathkeys[0].Expr)
	}
	if !p.Pathkeys[0].SortAsc || p.Pathkeys[0].NullsFirst {
		t.Fatalf("pathkey = %+v; want ASC NULLS LAST (the index's default ordering)", p.Pathkeys[0])
	}
	if p.Rows != s.findRel(inner).Rows {
		t.Fatalf("path rows = %v; want the rel's post-restriction %v", p.Rows, s.findRel(inner).Rows)
	}
	if p.Cost.Startup <= 0 {
		t.Fatalf("startup = %v; want the B-tree descent charge", p.Cost.Startup)
	}
}

// TestAddOrderedIndexPathsSurvivesACheaperSeqScan: the ordered path is
// SUPPOSED to lose on cost — it reads the whole relation through the index at
// random-page prices, and goopg has no correlation statistic to soften that.
// It must survive in the pathlist anyway, on the strength of its ordering
// alone, because the join above is where the sort it saves gets paid for.
// This is the `addPath` pathkey dimension P5.4c-ii-a made live, exercised end
// to end for the first time.
func TestAddOrderedIndexPathsSurvivesACheaperSeqScan(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	outer, inner := relsetOf(0), relsetOf(1)
	s := orderedCtx(t, orders, 1_500_000, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))

	s.addOrderedIndexPaths(cat)

	rel := s.findRel(inner)
	paths := orderedPathsOf(rel)
	if len(paths) != 1 {
		t.Fatalf("got %d ordered index paths; want one", len(paths))
	}
	if rel.CheapestTotal == nil {
		t.Fatal("rel has no cheapest-total path")
	}
	if rel.CheapestTotal == paths[0] {
		t.Fatalf("the ordered index path became cheapest-total (%v); a full index scan of 1.5M rows "+
			"must lose to the sequential scan on cost", paths[0].Cost)
	}
	if paths[0].Cost.Total <= rel.CheapestTotal.Cost.Total {
		t.Fatalf("ordered path total %v is not above the cheapest %v",
			paths[0].Cost.Total, rel.CheapestTotal.Cost.Total)
	}
}

// TestAddOrderedIndexPathsTruncatesAtTheFirstUnmergeableColumn is
// `truncate_useless_pathkeys` / `pathkeys_useful_for_merging`'s prefix rule: a
// merge join can only exploit a PREFIX of an ordering, so sort-key positions
// past the first column no clause can merge on are useless and must not be
// claimed. Claiming them would be the dangerous direction — the merge arm
// would then believe its input is sorted on a column nothing sorted it by.
func TestAddOrderedIndexPathsTruncatesAtTheFirstUnmergeableColumn(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}},
		{Name: "p_brand", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "part_two_col"},
		tbl, []string{"p_partkey", "p_brand"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	outer, inner := relsetOf(0), relsetOf(1)
	// A clause on the LEADING column only.
	s := orderedCtx(t, tbl, 200_000, ppiEquiClause(outer, "l_partkey", inner, "p_partkey"))

	s.addOrderedIndexPaths(c)

	paths := orderedPathsOf(s.findRel(inner))
	if len(paths) != 1 {
		t.Fatalf("got %d ordered index paths; want one", len(paths))
	}
	if len(paths[0].Pathkeys) != 1 {
		t.Fatalf("pathkeys = %v; want the ordering truncated to the one mergeable prefix column",
			paths[0].Pathkeys)
	}
}

// TestAddOrderedIndexPathsDeclinesWhenTheLEADINGColumnIsUnmergeable: the
// truncation is a prefix rule, so a clause on the index's SECOND column buys
// nothing at all — PG's loop breaks at position 0 and returns NIL.
func TestAddOrderedIndexPathsDeclinesWhenTheLEADINGColumnIsUnmergeable(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}},
		{Name: "p_brand", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "part_two_col"},
		tbl, []string{"p_partkey", "p_brand"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	outer, inner := relsetOf(0), relsetOf(1)
	s := orderedCtx(t, tbl, 200_000, ppiEquiClause(outer, "l_brand", inner, "p_brand"))

	s.addOrderedIndexPaths(c)

	if got := orderedPathsOf(s.findRel(inner)); len(got) != 0 {
		t.Fatalf("got %d ordered index paths; want none — no clause can merge on the leading column", len(got))
	}
}

// TestAddOrderedIndexPathsRejectsNonMergeableClauseKinds: `has_useful_pathkeys`
// asks whether the rel has a join clause that could become a MERGECLAUSE. An
// inequality join qual is a join clause and no ordering helps it, so a rel
// whose only clause is one produces nothing.
func TestAddOrderedIndexPathsRejectsNonMergeableClauseKinds(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	outer, inner := relsetOf(0), relsetOf(1)
	s := orderedCtx(t, orders, 1_500_000, plainClause(outer|inner))

	s.addOrderedIndexPaths(cat)

	if got := orderedPathsOf(s.findRel(inner)); len(got) != 0 {
		t.Fatalf("got %d ordered index paths off an inequality join qual; want none", len(got))
	}
}

// TestAddOrderedIndexPathsRejectsUnorderedAndPartialIndexes covers the two
// declines that are about the INDEX rather than the clauses: a `USING hash`
// index rides goopg's B-tree substrate but is not an ordered AM in PG
// (`index->sortopfamily == NULL`), and a partial index whose predicate goopg
// cannot prove would silently drop rows.
func TestAddOrderedIndexPathsRejectsUnorderedAndPartialIndexes(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "k", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "t_k_idx"}, tbl, []string{"k"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	outer, inner := relsetOf(0), relsetOf(1)
	clause := ppiEquiClause(outer, "x", inner, "k")

	idx.DeclaredHash = true
	s := orderedCtx(t, tbl, 1000, clause)
	s.addOrderedIndexPaths(c)
	if got := orderedPathsOf(s.findRel(inner)); len(got) != 0 {
		t.Fatalf("a USING hash index produced %d ordered paths; want none", len(got))
	}

	idx.DeclaredHash = false
	idx.HasPredicate = true
	s = orderedCtx(t, tbl, 1000, clause)
	s.addOrderedIndexPaths(c)
	if got := orderedPathsOf(s.findRel(inner)); len(got) != 0 {
		t.Fatalf("an unproven partial index produced %d ordered paths; want none", len(got))
	}

	// With both flags cleared the same fixture DOES produce one, so the two
	// checks above are testing the flags and not an unrelated decline.
	idx.HasPredicate = false
	s = orderedCtx(t, tbl, 1000, clause)
	s.addOrderedIndexPaths(c)
	if got := orderedPathsOf(s.findRel(inner)); len(got) != 1 {
		t.Fatalf("the control case produced %d ordered paths; want one", len(got))
	}
}

// TestAddOrderedIndexPathsHonoursDescendingKeys: the ordering a path claims is
// the index's DECLARED ordering, not a default. A DESC NULLS FIRST key must
// reach the pathkey, or the merge arm would sort its other input the wrong
// way round.
func TestAddOrderedIndexPathsHonoursDescendingKeys(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "k", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "t_k_desc"}, tbl, []string{"k"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	idx.ColDescending = []bool{true}
	idx.ColNullsFirst = []bool{true}

	outer, inner := relsetOf(0), relsetOf(1)
	s := orderedCtx(t, tbl, 1000, ppiEquiClause(outer, "x", inner, "k"))
	s.addOrderedIndexPaths(c)

	paths := orderedPathsOf(s.findRel(inner))
	if len(paths) != 1 {
		t.Fatalf("got %d ordered index paths; want one", len(paths))
	}
	if k := paths[0].Pathkeys[0]; k.SortAsc || !k.NullsFirst {
		t.Fatalf("pathkey = %+v; want DESC NULLS FIRST", k)
	}
}

// TestMergeableColumnExprsForKeepsTheClauseOperand pins the syntactic-pathkey
// requirement the P5.4c-ii-a loop learned: the expression handed to
// `buildIndexPathkeys` must be the very `*ColumnRef` the clause carries. A
// re-synthesised one with the same name has a different `Index` /
// `SourceTableIdx` and `exprEqual` reads it as a different column, which would
// make the merge arm silently fail to match its own input's ordering.
func TestMergeableColumnExprsForKeepsTheClauseOperand(t *testing.T) {
	outer, inner := relsetOf(0), relsetOf(1)
	ri := ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey")
	want := ri.rightKey

	got := mergeableColumnExprsFor(inner, []*restrictInfo{ri})
	if len(got) != 1 {
		t.Fatalf("got %d mergeable columns; want one", len(got))
	}
	if got["o_orderkey"] != want {
		t.Fatalf("mergeable expr is %#v; want the clause's own operand %#v", got["o_orderkey"], want)
	}
	// The OUTER rel sees its own operand off the same clause.
	if outerGot := mergeableColumnExprsFor(outer, []*restrictInfo{ri}); outerGot["l_orderkey"] != ri.leftKey {
		t.Fatalf("outer rel's mergeable expr is %#v; want %#v", outerGot["l_orderkey"], ri.leftKey)
	}
}

// TestAddBaseRelIndexPathsRunsBothHalves: `create_index_paths` generates the
// join half and the plain half together, and the combined entry point exists
// so a caller cannot wire up one and forget the other — the two compete in the
// same pathlist, so half a field is a silently wrong comparison.
func TestAddBaseRelIndexPathsRunsBothHalves(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	outer, inner := relsetOf(0), relsetOf(1)
	s := orderedCtx(t, orders, 1_500_000, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))

	s.addBaseRelIndexPaths(cat)

	var parameterised, plain int
	for _, p := range s.findRel(inner).Pathlist {
		if p.Kind != PathIndexScan {
			continue
		}
		if p.RequiredOuter != 0 {
			parameterised++
		} else {
			plain++
		}
	}
	if parameterised == 0 || plain == 0 {
		t.Fatalf("addBaseRelIndexPaths produced %d parameterised and %d plain index paths; want both halves",
			parameterised, plain)
	}
}

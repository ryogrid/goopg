package optimizer

// M0146-0005v — btree skip-scan planner admission and num_sa_scans costing.
//
// The producer arm is `addOneParameterizedSkipPath` (pathparamindex.go): an
// equality clause binding a NON-LEADING btree column makes the index usable
// the way PG18's `_bt_skiparray` uses it — the skipped prefix's distinct
// values are generated at run time and each drives a bounded descent. What
// these tests pin:
//
//   - `pickIndexSkipRun` eligibility: first bound column at position >= 1,
//     contiguous run, btree + plain columns + NOT NULL on every unbound
//     position (a NULL there is a row the index does not contain — the
//     probe would silently lose matches);
//   - `indexSkipClauses` ordering: IndexClauses[i].indexCol == skip+i, the
//     positional contract `IndexScan.SkipPrefix` documents;
//   - `skipScanDescents`: PG's num_sa_scans arithmetic —
//     get_variable_numdistinct + 1 per skipped column, with both reverts
//     (default estimate, estimate past index->pages) and the bound-quals
//     consequence the second return carries;
//   - `createIndexScanPlan` emission: `IndexScan{SkipPrefix, Keys}` — and
//     the fail-closed panics for shapes the executor cannot represent.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// skipCatalog builds orders(o_a, o_b, o_c) — every column NOT NULL — with a
// three-column btree (o_a, o_b, o_c), the shape TPC-DS Q82's
// inventory_pkey(inv_date_sk, inv_item_sk, inv_warehouse_sk) presents.
func skipCatalog(t *testing.T) (catalog.Catalog, *catalog.Table, *catalog.Index) {
	t.Helper()
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "orders"}, []catalog.Column{
		{Name: "o_a", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "o_b", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "o_c", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "orders_abc"}, tbl,
		[]string{"o_a", "o_b", "o_c"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	return c, tbl, idx
}

func TestPickIndexSkipRunEligible(t *testing.T) {
	c, tbl, idx := skipCatalog(t)

	bound := map[string]Expr{"o_b": &ColumnRef{Name: "x_b"}}
	gotIdx, skip, run := pickIndexSkipRun(c, tbl, bound)
	if gotIdx != idx || skip != 1 || run != 1 {
		t.Fatalf("bind {o_b} → (%v, %d, %d), want (orders_abc, 1, 1)", gotIdx, skip, run)
	}

	// A longer contiguous run binds more columns into the probe.
	bound = map[string]Expr{"o_b": &ColumnRef{Name: "x_b"}, "o_c": &ColumnRef{Name: "x_c"}}
	gotIdx, skip, run = pickIndexSkipRun(c, tbl, bound)
	if gotIdx != idx || skip != 1 || run != 2 {
		t.Fatalf("bind {o_b,o_c} → (%v, %d, %d), want (orders_abc, 1, 2)", gotIdx, skip, run)
	}

	// Binding a LATER column skips more of the prefix.
	bound = map[string]Expr{"o_c": &ColumnRef{Name: "x_c"}}
	gotIdx, skip, run = pickIndexSkipRun(c, tbl, bound)
	if gotIdx != idx || skip != 2 || run != 1 {
		t.Fatalf("bind {o_c} → (%v, %d, %d), want (orders_abc, 2, 1)", gotIdx, skip, run)
	}
}

func TestPickIndexSkipRunDeclines(t *testing.T) {
	bound := func() map[string]Expr { return map[string]Expr{"o_b": &ColumnRef{Name: "x_b"}} }

	// The leading column is the prefix picker's domain — a bound column 0
	// is not a skip.
	c, tbl, _ := skipCatalog(t)
	if got, _, _ := pickIndexSkipRun(c, tbl, map[string]Expr{"o_a": &ColumnRef{Name: "x_a"}}); got != nil {
		t.Fatalf("bind {o_a} picked %v; column 0 is a prefix probe, not a skip", got.Name)
	}
	// No bound column at all — nothing to probe.
	if got, _, _ := pickIndexSkipRun(c, tbl, nil); got != nil {
		t.Fatalf("no bound columns picked %v", got.Name)
	}

	// mutate exercises the picker's per-index gates by editing the stored
	// index in place (CreateIndex returns the catalog's own pointer).
	mutate := func(edit func(*catalog.Index, *catalog.Table)) (got *catalog.Index) {
		c2, tbl2, idx2 := skipCatalog(t)
		edit(idx2, tbl2)
		got, _, _ = pickIndexSkipRun(c2, tbl2, bound())
		return got
	}
	// Partial index — same gate the prefix picker applies: an unproven
	// predicate must not lose rows.
	if got := mutate(func(i *catalog.Index, _ *catalog.Table) { i.HasPredicate = true }); got != nil {
		t.Fatalf("partial index picked %v", got.Name)
	}
	// Expression key ("" column) inside the skipped prefix.
	if got := mutate(func(i *catalog.Index, _ *catalog.Table) { i.Columns[0] = "" }); got != nil {
		t.Fatalf("expression skipped column picked %v", got.Name)
	}
	// A DESC column inside the skipped prefix (descending group
	// enumeration is unbuilt).
	if got := mutate(func(i *catalog.Index, _ *catalog.Table) { i.ColDescending = []bool{true, false, false} }); got != nil {
		t.Fatalf("DESC skipped column picked %v", got.Name)
	}
	// Non-btree — only btree carries the ordering a skip enumerates.
	if got := mutate(func(i *catalog.Index, _ *catalog.Table) { i.Method = "hash" }); got != nil {
		t.Fatalf("hash index picked %v", got.Name)
	}
	// A NULLABLE skipped column: NULL-keyed rows are absent from the
	// index, so the probe would silently lose matches. Same for a
	// nullable tail column — the unbound positions are every position
	// outside the run.
	if got := mutate(func(_ *catalog.Index, t2 *catalog.Table) { t2.Columns[0].NotNull = false }); got != nil {
		t.Fatalf("nullable skipped column picked %v", got.Name)
	}
	if got := mutate(func(_ *catalog.Index, t2 *catalog.Table) { t2.Columns[2].NotNull = false }); got != nil {
		t.Fatalf("nullable tail column picked %v", got.Name)
	}
}

func TestIndexSkipClausesPositions(t *testing.T) {
	_, _, idx := picComposite(t)
	// picComposite's index is (ps_partkey, ps_suppkey): bind ONLY the
	// second column — a skip of one — and confirm the clause carries the
	// true position, not slot 0.
	outer, inner := relsetOf(0), relsetOf(1)
	suppRI := ppiEquiClause(outer, "l_suppkey", inner, "ps_suppkey")
	bound := []paramIndexClause{
		{ri: suppRI, innerCol: "ps_suppkey", innerKey: suppRI.rightKey, outerKey: suppRI.leftKey, outerRels: outer},
	}
	got := indexSkipClauses(idx, bound, 1, 1)
	if len(got) != 1 || got[0].indexCol != 1 {
		t.Fatalf("indexSkipClauses → %+v, want one clause at indexCol 1", got)
	}
	if cr, ok := got[0].key.(*ColumnRef); !ok || cr.Name != "l_suppkey" {
		t.Fatalf("clause key = %#v, want the outer l_suppkey ref", got[0].key)
	}
}

func TestSkipScanDescents(t *testing.T) {
	_, tbl, idx := skipCatalog(t)
	// Analyzed stats: o_a has 3 distinct values → numSA = 3+1 = 4
	// (PG counts the initial probe as a value when no skip quals
	// constrain the column, selfuncs.c:7539).
	tbl.Stats = &catalog.TableStats{
		Analyzed: true,
		Columns:  []catalog.ColumnStats{{NDistinct: 3}, {}, {}},
	}
	numSA, counted := skipScanDescents(tbl, idx, 1, 10000, 100)
	if numSA != 4 || !counted {
		t.Fatalf("ndistinct=3 → (%v, %v), want (4, true)", numSA, counted)
	}
	// Pages-exceeded revert: 4 descents against a 2-page index reverts to
	// the pre-column product (1) AND drops the run's quals from
	// indexBoundQuals — the second return is what makes the caller price
	// the index side over the whole index (selfuncs.c:7569-7573).
	numSA, counted = skipScanDescents(tbl, idx, 1, 10000, 2)
	if numSA != 1 || counted {
		t.Fatalf("pages-exceeded → (%v, %v), want (1, false)", numSA, counted)
	}
	// No statistics on a large relation: defaultNumDistinct is a guess
	// (isdefault), which reverts identically — PG's "conservatively assume
	// no skipping" branch, not a path decline.
	tbl.Stats = nil
	numSA, counted = skipScanDescents(tbl, idx, 1, 100000, 1000)
	if numSA != 1 || counted {
		t.Fatalf("stats-less → (%v, %v), want (1, false)", numSA, counted)
	}
	// …but a SMALL relation is not a guess: PG's small-rel arm takes
	// tuples as the distinct count, so the multiply stands.
	numSA, counted = skipScanDescents(tbl, idx, 1, 100, 1000)
	if numSA != 101 || !counted {
		t.Fatalf("small-rel → (%v, %v), want (101, true)", numSA, counted)
	}
}

// TestParameterizedSkipPathWorkBounds pins the two executor-capability
// declines in `addOneParameterizedSkipPath` — `maxSkipProbeRows` (a fat
// probe is a bulk scan in disguise, and it can be reached through ANY
// outer rel) and `maxSkipProbeLifetimeRows` (a thin probe under a huge
// modelled rescan count). Both exist because goopg's per-descent and
// per-heap-tuple executor costs are far higher than PG's; the witness
// is the TPC-DS Q72 SF1 fire-set timeout.
func TestParameterizedSkipPathWorkBounds(t *testing.T) {
	build := func(t *testing.T, factRows, outerRows float64, stats *catalog.TableStats) *searchCtx {
		t.Helper()
		c := catalog.NewInMemory()
		lineitem, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
			{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
			{Name: "l_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.CreateIndex(parser.ObjectName{Name: "lineitem_ps_idx"},
			lineitem, []string{"l_partkey", "l_suppkey"}, false, "btree", false); err != nil {
			t.Fatal(err)
		}
		lineitem.Stats = stats

		part, supplier, fact := relsetOf(0), relsetOf(1), relsetOf(2)
		s, err := newSearchCtx(3, defaultCostParams(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for i, rows := range []float64{200, outerRows, factRows} {
			rel := newRelOptInfo(RelSet(1)<<uint(i), rows, 32)
			if err := s.addRel(rel); err != nil {
				t.Fatal(err)
			}
			generateScanPaths(rel, s.cp, estScanPages(rows, 32), 0, 0, true)
			setCheapest(rel)
		}
		s.relInfos = []baseRelInfo{{}, {}, {table: lineitem}}
		s.levelRels(1)[2].baseLeaf = &SeqScan{Table: lineitem}
		s.clauses = &restrictInfoList{all: []*restrictInfo{
			ppiEquiClause(part, "p_partkey", fact, "l_partkey"),
			ppiEquiClause(supplier, "s_suppkey", fact, "l_suppkey"),
		}}
		s.addParameterizedIndexPaths(c)
		return s
	}
	skipEmitted := func(s *searchCtx) bool {
		for _, p := range s.levelRels(1)[2].Pathlist {
			if p.IndexSkipPrefix > 0 {
				return true
			}
		}
		return false
	}

	// Thin probe, small outer: 600_000 × (1/50_000) = 12 rows per probe —
	// admitted (the {supplier} election in the sibling test).
	thin := &catalog.TableStats{
		Analyzed: true,
		Columns:  []catalog.ColumnStats{{NDistinct: 50_000}, {NDistinct: 50_000}},
	}
	if !skipEmitted(build(t, 600_000, 100, thin)) {
		t.Fatal("thin probe under a small outer was declined")
	}
	// Fat probe: stats-less sel = 600_000/200 = 3000 rows per probe —
	// declined by maxSkipProbeRows under EVERY required outer.
	if skipEmitted(build(t, 600_000, 100, nil)) {
		t.Fatal("fat (3000-row) skip probe was emitted")
	}
	// Thin probe, huge modelled outer: 12 × 100M > 5e8 — declined by
	// maxSkipProbeLifetimeRows.
	if skipEmitted(build(t, 600_000, 100_000_000, thin)) {
		t.Fatal("thin probe under a 100M-row modelled outer was emitted")
	}
}

func TestCreateIndexScanPlanSkipEmission(t *testing.T) {
	idx := &catalog.Index{Name: "sk_idx", Columns: []string{"a", "b", "c"}, Method: "btree"}
	leaf := &SeqScan{Table: &catalog.Table{Name: "t"}}
	p := cpiPath(leaf, idx, []indexPathClause{
		{ri: nil, indexCol: 1, key: &ColumnRef{Name: "x_b"}},
	}, relsetOf(1))
	p.IndexSkipPrefix = 1
	node := createIndexScanPlan(p)
	is, ok := node.(*IndexScan)
	if !ok {
		t.Fatalf("emitted %T, want *IndexScan", node)
	}
	if is.SkipPrefix != 1 || len(is.Keys) != 1 || is.Key != nil {
		t.Fatalf("IndexScan{SkipPrefix:%d Key:%v Keys:%d}, want {1 nil 1}",
			is.SkipPrefix, is.Key, len(is.Keys))
	}
	// A clause claiming column 0 under SkipPrefix 1 is the positional
	// contract break — it must panic, not probe the wrong column.
	bad := cpiPath(leaf, idx, []indexPathClause{
		{ri: nil, indexCol: 0, key: &ColumnRef{Name: "x_a"}},
	}, relsetOf(1))
	bad.IndexSkipPrefix = 1
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("mis-positioned skip clause did not panic")
		} else if msg, ok := r.(string); !ok || !strings.Contains(msg, "index-column order") {
			t.Fatalf("panic = %v, want the index-column-order assertion", r)
		}
	}()
	createIndexScanPlan(bad)
}

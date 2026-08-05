package planner

// M0127-P5.6-f-ii — the superkey/FK proof in the join-GRAPH coordinate space.
//
// These pin `graphJoinKeyDivisor` through its real caller, `estimateJoinCost`'s
// PRODUCTION branch (the integer DP, `costDrivenJoinOrder` off), because the
// number that branch returns is what the join-order SEARCH selects on. P5.6-f
// made `estimateJoin` — the estimator that prices the FINISHED plan — exact on
// Q9 and the plan did not move a single node, precisely because the search had
// its own second implementation here. Asserting the divisor in isolation would
// have missed that, so every case below goes through the caller.
//
// Their `*Join`-space twins are in joinkeyproof_test.go; the two provers are
// the same algorithm arm for arm and must move together (hard-won rule #2).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// perTableIndexCatalog answers IndexesOnTable per table name, which the shared
// `stubIndexCatalog` (joinkeyproof_test.go) cannot do — the graph prover walks
// several relations in one call and a catalog that hands the same index to all
// of them would prove keys that do not exist.
type perTableIndexCatalog struct {
	catalog.Catalog
	byTable map[string][]*catalog.Index
}

func (c *perTableIndexCatalog) IndexesOnTable(tbl *catalog.Table, _ ...uint32) []*catalog.Index {
	if tbl == nil {
		return nil
	}
	return c.byTable[tbl.Name]
}

// graphCol is one column's name and ANALYZE distinct count.
type graphCol struct {
	name string
	nd   int64
}

func graphTable(name string, rows int64, cols []graphCol) *catalog.Table {
	tbl := &catalog.Table{
		Name:  name,
		Stats: &catalog.TableStats{RowCount: rows},
	}
	for _, c := range cols {
		tbl.Columns = append(tbl.Columns, catalog.Column{Name: c.name})
		tbl.Stats.Columns = append(tbl.Stats.Columns, catalog.ColumnStats{NDistinct: c.nd})
	}
	return tbl
}

// q9Graph is TPC-H Q9's `lineitem ⋈ partsupp`: two equality conjuncts over
// partsupp's two-column primary key, the shape whose joinrel P5.6-f fixed in
// the finished plan and whose PLAN this task is about.
func q9Graph() (*joinGraph, []*joinEdge) {
	lineitem := graphTable("lineitem", 6001215, []graphCol{
		{"l_orderkey", 1500000}, {"l_partkey", 200000}, {"l_suppkey", 10000},
	})
	partsupp := graphTable("partsupp", 800000, []graphCol{
		{"ps_partkey", 200000}, {"ps_suppkey", 10000},
	})
	g := &joinGraph{nodes: 2, tables: []*catalog.Table{lineitem, partsupp}}
	g.edges = []joinEdge{
		{leftTable: 0, rightTable: 1, leftKey: &ColumnRef{Index: 1, Name: "l_partkey"}, rightKey: &ColumnRef{Index: 0, Name: "ps_partkey"}},
		{leftTable: 0, rightTable: 1, leftKey: &ColumnRef{Index: 2, Name: "l_suppkey"}, rightKey: &ColumnRef{Index: 1, Name: "ps_suppkey"}},
	}
	return g, []*joinEdge{&g.edges[0], &g.edges[1]}
}

func partsuppPKCatalog(cols ...string) *perTableIndexCatalog {
	return &perTableIndexCatalog{byTable: map[string][]*catalog.Index{
		"partsupp": {{Name: "partsupp_pk", Unique: true, Columns: cols}},
	}}
}

// TestGraphProofCompositeUniqueIndexReachesTheSearch is the task's headline
// case, and it is asserted through `estimateJoinCost` because reaching the
// SEARCH is the whole point.
//
// Without the key, the §4 multi-clause divisor multiplies the two edges'
// per-column NDVs — 200 000 · 10 000 — and reads 2 400 against an actual
// 5 997 241, which is the error the joinkeyproof.go header describes upstream
// avoiding. With the composite key proven, partsupp cannot fan out, the two
// pairs are consumed together, and the single divisor is partsupp's RAW
// 800 000 — which returns lineitem's own cardinality.
func TestGraphProofCompositeUniqueIndexReachesTheSearch(t *testing.T) {
	g, edges := q9Graph()
	a, b := uint16(1), uint16(2)

	blind, _ := estimateJoinCost(6001215, 800000, edges[0], a, b, g, nil)
	if want := int64(2400); blind != want {
		t.Fatalf("no-catalog estimate = %d, want %d (the §4 product 200000·10000)", blind, want)
	}

	proven, _ := estimateJoinCost(6001215, 800000, edges[0], a, b, g, partsuppPKCatalog("ps_partkey", "ps_suppkey"))
	if want := int64(6001215); proven != want {
		t.Fatalf("proven estimate = %d, want %d (divide by partsupp's raw 800000)", proven, want)
	}
}

// TestJoinKeyDivisorIsOneImplementationForBothSearchModes pins the merge
// itself. `costDrivenJoinOrder` used to select between two different divisor
// computations; a search that prices the same candidate differently depending
// on a flag is the shape that let P5.6-f's exact joinrel leave Q9's plan
// unchanged. Both modes must now agree, edge for edge.
func TestJoinKeyDivisorIsOneImplementationForBothSearchModes(t *testing.T) {
	g, edges := q9Graph()
	cat := partsuppPKCatalog("ps_partkey", "ps_suppkey")

	defer SetCostDrivenJoinOrder(SetCostDrivenJoinOrder(false))
	integer, _ := estimateJoinCost(6001215, 800000, edges[0], 1, 2, g, cat)
	SetCostDrivenJoinOrder(true)
	driven, _ := estimateJoinCost(6001215, 800000, edges[0], 1, 2, g, cat)

	if integer != driven {
		t.Fatalf("integer DP estimate %d != cost-driven estimate %d; the divisor must not depend on the search mode", integer, driven)
	}
}

// TestGraphProofSpansEveryEdgeNotJustTheSelectedOne pins the enumeration half.
// `findEdgeBetweenIdx` hands `estimateJoinCost` ONE edge; a two-column key is
// only ever a superkey of the two-edge set. Passing the single `ps_partkey`
// edge as the caller's selection must still prove the key, because the prover
// enumerates `crossEdgesBetween` itself.
func TestGraphProofSpansEveryEdgeNotJustTheSelectedOne(t *testing.T) {
	g, edges := q9Graph()
	cat := partsuppPKCatalog("ps_partkey", "ps_suppkey")
	first, _ := estimateJoinCost(6001215, 800000, edges[0], 1, 2, g, cat)
	second, _ := estimateJoinCost(6001215, 800000, edges[1], 1, 2, g, cat)
	if first != second {
		t.Fatalf("estimate depends on which of the two edges was selected: %d vs %d", first, second)
	}
	if want := int64(6001215); first != want {
		t.Fatalf("estimate = %d, want %d", first, want)
	}
}

// TestGraphProofChickensOutOnAPartialCover is costsize.c:5760 — "if we failed to
// remove all the matching clauses we expected to find, chicken out". A join that
// equates only `ps_partkey` does NOT make partsupp unique (200 000 parts, four
// suppliers each), and claiming it would divide four real rows down to one.
func TestGraphProofChickensOutOnAPartialCover(t *testing.T) {
	g, _ := q9Graph()
	g.edges = g.edges[:1] // drop the ps_suppkey conjunct

	cat := partsuppPKCatalog("ps_partkey", "ps_suppkey")
	if div, ok := graphJoinKeyDivisor(crossEdgesOrSelf(&g.edges[0], 1, 2, g), g, cat); ok {
		t.Fatalf("proved a two-column key from one equated column (divisor %d)", div)
	}
	// The §4 per-clause divisor answers instead: ps_partkey's 200 000.
	got, _ := estimateJoinCost(6001215, 800000, &g.edges[0], 1, 2, g, cat)
	if want := int64(24004860); got != want {
		t.Fatalf("partial-cover estimate = %d, want the unproven fallback %d", got, want)
	}
}

// TestGraphProofConsumesEachEdgeOnce pins the ⊆-test's companion rule: two keys
// of the same relation can both be superkeys of the equated set, but an edge
// charged for one must not be charged again for the other. Largest divisor
// first, then the remaining edges — never 800 000².
func TestGraphProofConsumesEachEdgeOnce(t *testing.T) {
	g, edges := q9Graph()
	cat := &perTableIndexCatalog{byTable: map[string][]*catalog.Index{
		"partsupp": {
			{Name: "partsupp_pk", Unique: true, Columns: []string{"ps_partkey", "ps_suppkey"}},
			{Name: "partsupp_ps_partkey_key", Unique: true, Columns: []string{"ps_partkey"}},
		},
	}}
	got, _ := estimateJoinCost(6001215, 800000, edges[0], 1, 2, g, cat)
	if want := int64(6001215); got != want {
		t.Fatalf("estimate = %d, want %d (each edge consumed once, divisor 800000 not 800000^2)", got, want)
	}
}

// TestGraphProofDividesByTheParentRawCountForADeclaredFK is the defect the
// deleted `uniqueNoFanoutRawCount` carried: an FK declared on the CHILD makes
// each child row match exactly one PARENT row, so the divisor is the PARENT's
// raw count (`1.0 / ref_tuples`, costsize.c:5847). Dividing by the child's
// 1 500 000 instead would return 150 000 — the fact table's own cardinality
// divided out of the join.
func TestGraphProofDividesByTheParentRawCountForADeclaredFK(t *testing.T) {
	orders := graphTable("orders", 1500000, []graphCol{
		{"o_orderkey", 1500000}, {"o_custkey", 150000},
	})
	orders.ForeignKeys = []catalog.ForeignKey{{
		Columns: []string{"o_custkey"}, RefTable: "customer", RefColumns: []string{"c_custkey"},
	}}
	customer := graphTable("customer", 150000, []graphCol{{"c_custkey", 150000}})
	g := &joinGraph{nodes: 2, tables: []*catalog.Table{orders, customer}}
	g.edges = []joinEdge{{
		leftTable: 0, rightTable: 1,
		leftKey:  &ColumnRef{Index: 1, Name: "o_custkey"},
		rightKey: &ColumnRef{Index: 0, Name: "c_custkey"},
	}}
	// No unique index anywhere: the FK arm is the only evidence. Asserted on
	// the DIVISOR, not on the resulting estimate — `o_custkey` and `c_custkey`
	// both have 150 000 distinct values, so the §4 fallback happens to produce
	// the same row count and could not tell the two directions apart.
	cat := &perTableIndexCatalog{byTable: map[string][]*catalog.Index{}}
	div, ok := graphJoinKeyDivisor(crossEdgesOrSelf(&g.edges[0], 1, 2, g), g, cat)
	if !ok {
		t.Fatal("a valid, enforced FK proved nothing")
	}
	if want := int64(150000); div != want {
		t.Fatalf("FK divisor = %d, want the PARENT's raw %d (dividing by the child's 1500000 divides the fact table out of the join)", div, want)
	}
	got, _ := estimateJoinCost(1500000, 150000, &g.edges[0], 1, 2, g, cat)
	if want := int64(1500000); got != want {
		t.Fatalf("FK estimate = %d, want %d", got, want)
	}
}

// TestGraphProofRejectsAnFKToAnAbsentParent keeps the proof tied to the join
// being priced. `orders` declares an FK to `customer`, but this join equates
// `o_custkey` to a table of another name — the child rows are not
// fully-contained in THAT relation and no proof may be made.
func TestGraphProofRejectsAnFKToAnAbsentParent(t *testing.T) {
	orders := graphTable("orders", 1500000, []graphCol{
		{"o_orderkey", 1500000}, {"o_custkey", 150000},
	})
	orders.ForeignKeys = []catalog.ForeignKey{{
		Columns: []string{"o_custkey"}, RefTable: "customer", RefColumns: []string{"c_custkey"},
	}}
	other := graphTable("promo_target", 150000, []graphCol{{"pt_custkey", 150000}})
	g := &joinGraph{nodes: 2, tables: []*catalog.Table{orders, other}}
	g.edges = []joinEdge{{
		leftTable: 0, rightTable: 1,
		leftKey:  &ColumnRef{Index: 1, Name: "o_custkey"},
		rightKey: &ColumnRef{Index: 0, Name: "pt_custkey"},
	}}
	cat := &perTableIndexCatalog{byTable: map[string][]*catalog.Index{}}
	if div, ok := graphJoinKeyDivisor(crossEdgesOrSelf(&g.edges[0], 1, 2, g), g, cat); ok {
		t.Fatalf("proved an FK against a relation the join does not reference (divisor %d)", div)
	}
}

// TestGraphProofDoesNotMergeTwoArmsOfASelfJoin. `nation n1, nation n2` puts the
// SAME *catalog.Table behind two FROM-list positions. The `*Join`-space prover
// needs leaf-scan pointer identity to keep them apart; the graph's table INDEX
// does it by construction, and this pins that it stays that way. `n1.n_nationkey
// = x AND n2.n_regionkey = y` equates ONE column of each instance, so a
// two-column unique key is a superkey of NEITHER.
func TestGraphProofDoesNotMergeTwoArmsOfASelfJoin(t *testing.T) {
	nation := graphTable("nation", 25, []graphCol{{"n_nationkey", 25}, {"n_regionkey", 5}})
	probe := graphTable("probe", 1000, []graphCol{{"p_a", 1000}, {"p_b", 1000}})
	// tables[0] and tables[1] are two instances of the SAME table.
	g := &joinGraph{nodes: 3, tables: []*catalog.Table{nation, nation, probe}}
	g.edges = []joinEdge{
		{leftTable: 0, rightTable: 2, leftKey: &ColumnRef{Index: 0, Name: "n_nationkey"}, rightKey: &ColumnRef{Index: 0, Name: "p_a"}},
		{leftTable: 1, rightTable: 2, leftKey: &ColumnRef{Index: 1, Name: "n_regionkey"}, rightKey: &ColumnRef{Index: 1, Name: "p_b"}},
	}
	cat := &perTableIndexCatalog{byTable: map[string][]*catalog.Index{
		"nation": {{Name: "nation_ck", Unique: true, Columns: []string{"n_nationkey", "n_regionkey"}}},
	}}
	// Subsets {n1,n2} vs {probe}: both edges span them.
	if _, ok := graphJoinKeyDivisor(crossEdgesOrSelf(&g.edges[0], 0b011, 0b100, g), g, cat); ok {
		t.Fatal("proved a composite key from one column of each of two self-join instances")
	}
}

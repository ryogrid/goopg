package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPGShapedSearchPicksNLIOnCost is the searched-arm counterpart to the NLI
// rule tests that M0127-P5.9 pinned to `useLegacyEnumerator`, and it is what
// makes that pinning honest: the rewrite rule is gone from the searched path,
// so the question worth asking is whether the OPERATOR is still reachable
// there. It is — by cost, which is how upstream reaches it too
// (`match_unsorted_outer` + a parameterised inner path, joinpath.c).
//
// The fixture is the one difference that matters. The legacy tests plan
// against tables with no `TableStats` at all, where the search correctly sees
// zero rows on both sides and a bare nested loop really is cheapest. Give it
// the shape the rule was a proxy for — a small outer, a large indexed inner —
// and the search picks the NLI without being told to.
//
// This is deliberately a COST assertion, not a structural one. If a later cost
// change makes the search stop reaching NLI on a 50-row-vs-200k-row join with
// a unique index on the join key, that is a defect worth a failing test, and
// the failure should be loud here rather than surfacing as a TPC-H timing
// regression three gates later.
func TestPGShapedSearchPicksNLIOnCost(t *testing.T) {
	if !pgShapedDPEnabled() {
		t.Skip("kill-switch set; this test is about the searched arm")
	}
	cat := catalog.NewInMemory()
	part, err := cat.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "p_name", Type: catalog.Type{Name: "varchar", Args: []int64{55}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "part_pk"}, part,
		[]string{"p_partkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	lineitem, err := cat.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_quantity", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Analyzed: true is load-bearing — it is goopg's stand-in for PG's
	// `reltuples >= 0`, i.e. "these numbers were measured". Without it the
	// relation-size fallback treats the row counts as absent.
	cat.SetTableStats(part, &catalog.TableStats{RowCount: 200_000, Pages: 3_000, Analyzed: true})
	cat.SetTableStats(lineitem, &catalog.TableStats{RowCount: 50, Pages: 1, Analyzed: true})

	stmt := parseOne(t, `SELECT p_name, l_quantity FROM lineitem, part WHERE l_partkey = p_partkey`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !findNLI(node) {
		t.Fatalf("the searched arm did not reach a NestedLoopIndexJoin on a 50 x 200k indexed join; tree: %s",
			describePlanTree(node))
	}
}

// TestPGShapedSearchPicksHashJoinOnCost is the same argument for the other
// operator the legacy rules used to guarantee. Two large relations with no
// usable index on the join key: the searched arm must reach a hash join, and
// must key it on the equi-pair rather than leaving a bare cross product with a
// filter above it.
func TestPGShapedSearchPicksHashJoinOnCost(t *testing.T) {
	if !pgShapedDPEnabled() {
		t.Skip("kill-switch set; this test is about the searched arm")
	}
	cat := catalog.NewInMemory()
	a, err := cat.CreateTable(parser.ObjectName{Name: "a"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "av", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := cat.CreateTable(parser.ObjectName{Name: "b"}, []catalog.Column{
		{Name: "y", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "bv", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cat.SetTableStats(a, &catalog.TableStats{RowCount: 500_000, Pages: 5_000, Analyzed: true})
	cat.SetTableStats(b, &catalog.TableStats{RowCount: 400_000, Pages: 4_000, Analyzed: true})

	stmt := parseOne(t, `SELECT av, bv FROM a, b WHERE x = y`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	j := findFirstJoin(node)
	if j == nil {
		t.Fatalf("no Join in the searched plan; tree: %s", describePlanTree(node))
	}
	if j.Algo != JoinAlgoHash {
		t.Errorf("searched arm chose Algo=%v on a 500k x 400k unindexed equi-join, want JoinAlgoHash; tree: %s",
			j.Algo, describePlanTree(node))
	}
	if j.LeftKey == nil || j.RightKey == nil {
		t.Errorf("hash join has no keys — the equi-pair stayed a residual; tree: %s", describePlanTree(node))
	}
}

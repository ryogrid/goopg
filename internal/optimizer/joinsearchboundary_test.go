package optimizer

// M0127-P5.9-c — the search boundary survives the legacy remap family.
//
// P5.5-f-i's `createplanroot_test.go` proves the boundary is BUILT correctly.
// This file proves it is still correct at the END of `Plan()`, which is a
// different claim and the one P5.9 run 1 falsified: the map was built right and
// then rewritten by `remapTopProjection` (bushy.go), the one legacy descent that
// steps over a `*Project` unconditionally and so walked straight through the
// search root into the searched join below it. The posMap it derived there was
// the search's own binding→plan-position permutation, and applying it to the
// boundary's target list composed two permutations: every column's value came
// back one relation-block from its name.
//
// The fixtures run through the real `Plan()` with the flag flipped in-process,
// because that is the only way to observe the interaction — every individual
// piece (the search, the boundary, the remap) is correct on its own.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// jsbReproducerSQL is P5.9 run 1's minimal reproducer, reduced from
// `analysis/leftdeep-joins/2026-08-05-p59-s5-acceptance.txt` §3.1.
//
// The FROM order is `customer, orders` while the cost-chosen order is
// `orders ⋈ customer` (the `o_orderkey = 1` restriction makes `orders` the
// cheap side), so the search root is NOT in binding order and the boundary must
// emit its reordering Project. That is exactly the discriminator the acceptance
// sweep saw: a query whose winner is already left-deep in binding order takes
// `boundaryMapIsIdentity`'s early return and was never affected.
const jsbReproducerSQL = `select * from customer, orders ` +
	`where o_custkey = c_custkey and o_orderkey = 1`

// jsbCatalog builds the two-relation catalog the reproducer needs, with row
// counts far enough apart that the search's choice is not a coin flip.
func jsbCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	mk := func(name string, rows int64, cols ...string) {
		t.Helper()
		cs := make([]catalog.Column, len(cols))
		for i, c := range cols {
			cs[i] = catalog.Column{Name: c, Type: catalog.Type{Name: "int4"}}
		}
		tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, cs)
		if err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows, Pages: int(rows / 100), Analyzed: true}
	}
	mk("customer", 150, "c_custkey", "c_name", "c_acct")
	mk("orders", 1500000, "o_orderkey", "o_custkey", "o_status", "o_total")
	return cat
}

// jsbPlan plans one statement with `GOOPG_PGSHAPED_DP` forced to `on`.
func jsbPlan(t *testing.T, sql string, on bool) Node {
	t.Helper()
	saved := pgShapedDP
	pgShapedDP = on
	defer func() { pgShapedDP = saved }()

	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	n, err := Plan(stmts[0], jsbCatalog(t))
	if err != nil {
		t.Fatalf("plan (pgshaped=%v): %v", on, err)
	}
	return n
}

// jsbResolveTopTargets walks the top `*Project`'s targets down to the column
// each one actually reads, and returns those columns' NAMES.
//
// Following the reference to the column it lands on — rather than reading the
// Project's schema, which is a copy of the names the resolver assigned — is the
// whole point. A permuted map leaves the schema saying `c_custkey` while the
// target beneath it reads `o_custkey`, and only the second of those two is what
// the executor returns.
func jsbResolveTopTargets(t *testing.T, root Node) []string {
	t.Helper()
	p, ok := root.(*Project)
	if !ok {
		t.Fatalf("plan root = %T, want the top *Project", root)
	}
	out := make([]string, len(p.Targets))
	for i, tg := range p.Targets {
		cr, isCol := tg.(*ColumnRef)
		if !isCol {
			t.Fatalf("top target %d = %T, want a pass-through *ColumnRef for `select *`", i, tg)
		}
		out[i] = jsbResolveThrough(t, p.Child, cr.Index)
	}
	return out
}

// jsbResolveThrough returns the name of the column that output column `col` of
// `n` actually carries, following pass-through `*Project`s (the boundary is
// one) down to the node that names it.
func jsbResolveThrough(t *testing.T, n Node, col int) string {
	t.Helper()
	for {
		schema := n.Output()
		if col < 0 || col >= len(schema) {
			t.Fatalf("column %d of a %d-column %T", col, len(schema), n)
		}
		p, isProj := n.(*Project)
		if !isProj {
			return schema[col].Name
		}
		cr, isCol := p.Targets[col].(*ColumnRef)
		if !isCol {
			return schema[col].Name
		}
		n, col = p.Child, cr.Index
	}
}

// TestSearchBoundarySurvivesTopProjectionRemap is the regression test.
//
// It asserts on the column a reference RESOLVES TO, not on indices: an
// index-level expectation would have to encode the search's chosen order, and
// then it would fail for a cost-model change rather than for a coordinate bug.
// Both arms must answer with the FROM-order concatenation, because that is what
// `select *` means and the join order is not allowed to be visible in it.
func TestSearchBoundarySurvivesTopProjectionRemap(t *testing.T) {
	want := []string{"c_custkey", "c_name", "c_acct", "o_orderkey", "o_custkey", "o_status", "o_total"}

	for _, on := range []bool{false, true} {
		got := jsbResolveTopTargets(t, jsbPlan(t, jsbReproducerSQL, on))
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("pgshaped=%v: `select *` resolves to %v, want the FROM-order concatenation %v\n"+
				"a rotation here is P5.9 run 1's defect: the boundary map was rewritten by a pass above it",
				on, got, want)
		}
	}
}

// TestSearchBoundaryIsAProjectForANonBindingOrderWinner keeps the test above
// honest.
//
// If a cost-model change ever made the search pick `customer` as the outer, the
// root would be in binding order already, the boundary would elide its Project
// (`boundaryMapIsIdentity`), and the assertion above would pass without ever
// exercising the map — the "green for the wrong reason" failure mode that made
// `searchedtree_test.go` supply its own named-clause helper. So the fixture's
// premise is asserted separately: this is the shape that HAS a map.
func TestSearchBoundaryIsAProjectForANonBindingOrderWinner(t *testing.T) {
	root := jsbPlan(t, jsbReproducerSQL, true)
	p, ok := root.(*Project)
	if !ok {
		t.Fatalf("plan root = %T, want the top *Project", root)
	}
	boundary, isProj := p.Child.(*Project)
	if !isProj {
		t.Fatalf("below the top Project = %T, want the boundary *Project; the fixture no longer "+
			"produces a winner that reordered, so it proves nothing about the boundary map", p.Child)
	}
	if !isSearchedTree(boundary) {
		t.Fatalf("the *Project below the top one does not carry the searched-subtree tag")
	}
}

// TestAssertBoundaryProjectionIntactCatchesARotation proves the tripwire is not
// vacuous, by doing to a real boundary node exactly what `remapTopProjection`
// did: permute the target INDICES and leave the names where they were.
//
// It uses `createplanroot_test.go`'s fixtures so the node under test is one the
// production arm actually built, not a hand-assembled lookalike.
func TestAssertBoundaryProjectionIntactCatchesARotation(t *testing.T) {
	a, b := cpjTwoRel()
	n := createPlanAtSearchRoot(cprHashRoot(b, a), 5)
	p, ok := n.(*Project)
	if !ok {
		t.Fatalf("createPlanAtSearchRoot = %T, want the boundary *Project", n)
	}

	// Intact: the check must be silent on the node as built.
	assertBoundaryProjectionIntact(p)

	// Rotate the indices by one, names untouched — the exact shape the
	// double-remap produced.
	width := len(p.Child.Output())
	for _, tg := range p.Targets {
		cr := tg.(*ColumnRef)
		cr.Index = (cr.Index + 1) % width
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("assertBoundaryProjectionIntact accepted a rotated coordinate map; " +
				"the tripwire cannot catch P5.9 run 1's defect")
		}
		if msg, isStr := r.(string); !isStr || !strings.Contains(msg, "search-boundary projection target") {
			t.Fatalf("panic = %v, want the boundary-map diagnostic", r)
		}
	}()
	assertBoundaryProjectionIntact(p)
}

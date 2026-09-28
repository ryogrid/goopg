package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0146-0028 slice 1 — pull_up_simple_subquery for FROM-clause subqueries
// with a bare-column target list (derivedpullup.go).

func pullupPlanFrom(t *testing.T, q string) (*parser.SelectStmt, Node, *resolveContext) {
	t.Helper()
	stmts, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := stmts[0].(*parser.SelectStmt)
	node, rctx, err := planFromClause(s, scopeTestCatalog(t), DefaultPlannerSettings(), newRtableScope())
	if err != nil {
		t.Fatalf("planFromClause: %v", err)
	}
	return s, node, rctx
}

func pullupResolve(t *testing.T, ctx *resolveContext, table, col string) Expr {
	t.Helper()
	e, err := resolveColumnRef(&parser.ColumnRef{Table: table, Column: col}, ctx)
	if err != nil {
		t.Fatalf("resolve %s.%s: %v", table, col, err)
	}
	return e
}

// The body's relation becomes a leaf of the parent's FROM walk, hidden from
// parent-level names; the derived alias resolves to the body column it
// renames, and the body WHERE is carried up as a qual.
func TestDerivedPullupSplicesBodyLeaves(t *testing.T) {
	_, node, rctx := pullupPlanFrom(t, "SELECT * FROM (SELECT ax AS k, ay FROM a WHERE ay > 1) y, b WHERE y.k = b.bx")
	if len(rctx.pulledDerived) != 1 || len(rctx.pulledQuals) != 1 {
		t.Fatalf("pulledDerived=%d pulledQuals=%d, want 1 and 1", len(rctx.pulledDerived), len(rctx.pulledQuals))
	}
	if len(rctx.bindings) != 2 || !rctx.bindings[0].pulledHidden || rctx.bindings[1].pulledHidden {
		t.Fatalf("bindings: want [a(hidden), b], got %d", len(rctx.bindings))
	}
	if len(node.Output()) != 4 {
		t.Fatalf("FROM tree width %d, want a's 2 + b's 2 columns", len(node.Output()))
	}
	k, ok := pullupResolve(t, rctx, "y", "k").(*ColumnRef)
	if !ok || k.Index != rctx.bindings[0].offset || k.Name != "ax" {
		t.Fatalf("y.k resolved to %#v, want a.ax at slot %d", k, rctx.bindings[0].offset)
	}
	if u, ok := pullupResolve(t, rctx, "", "ay").(*ColumnRef); !ok || u.Index != rctx.bindings[0].offset+1 {
		t.Fatalf("unqualified ay resolved to %#v", u)
	}
	// The body's relation is not nameable from the parent (PG: missing
	// FROM-clause entry for table "a").
	if _, err := resolveColumnRef(&parser.ColumnRef{Table: "a", Column: "ax"}, rctx); err == nil {
		t.Fatal("a.ax resolved at the parent level; a pulled-up RTE is not nameable there")
	}
	// A column the derived table does not output is not visible either.
	if _, err := resolveColumnRef(&parser.ColumnRef{Table: "y", Column: "ax"}, rctx); err == nil {
		t.Fatal("y.ax resolved, but y outputs only k and ay")
	}
}

// `SELECT *` emits the derived table's outputs, under their derived names,
// where the derived item stood; ORDER BY <n> counts those outputs.
func TestDerivedPullupStarAndOrdinal(t *testing.T) {
	s, _, rctx := pullupPlanFrom(t, "SELECT * FROM b, (SELECT ay AS v FROM a) y ORDER BY 3")
	exprs, schema, err := expandStarTarget(s.Targets[0].Expr.(*parser.StarExpr), rctx)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(schema) != 3 || schema[0].Name != "bx" || schema[1].Name != "by" || schema[2].Name != "v" {
		t.Fatalf("star schema %v, want [bx by v]", schema)
	}
	ord, ok := ordinalThroughStarTargets(3, s.Targets, rctx)
	if !ok {
		t.Fatal("ordinal 3 not resolved through the star")
	}
	cr, _ := ord.(*ColumnRef)
	want := exprs[2].(*ColumnRef)
	if cr == nil || cr.Index != want.Index || cr.Name != "ay" {
		t.Fatalf("ORDER BY 3 -> %#v, want the body column a.ay at slot %d", ord, want.Index)
	}
	// The input schema's third column is a.ax — the slot the old
	// input-positional rule would have sorted by.
	if cr.Index == 2 && rctx.bindings[1].offset != 2 {
		t.Fatalf("ordinal landed on the input-schema position")
	}
}

// Shapes slice 1 leaves as ordinary derived leaves.
func TestDerivedPullupDeclines(t *testing.T) {
	for _, q := range []string{
		"SELECT * FROM (SELECT ax FROM a) y",                                   // lone FROM item
		"SELECT * FROM (SELECT ax + 1 AS k FROM a) y, b",                       // expression target
		"SELECT * FROM (SELECT ax, count(*) FROM a GROUP BY ax) y, b",          // grouping
		"SELECT * FROM (SELECT DISTINCT ax FROM a) y, b",                       // DISTINCT
		"SELECT * FROM (SELECT ax FROM a LIMIT 1) y, b",                        // LIMIT
		"SELECT * FROM (SELECT ax FROM a) y (z), b",                            // column alias list
		"SELECT * FROM (SELECT ax FROM a JOIN c ON a.ay = c.cy) y, b",          // JOIN in body
		"SELECT * FROM (SELECT ax FROM a WHERE ay IN (SELECT dx FROM d)) y, b", // sublink in body
		"SELECT * FROM (SELECT ax, ax FROM a) y, b",                            // duplicate output name
		"SELECT * FROM b, LATERAL (SELECT ax FROM a WHERE ay = b.by) y",        // LATERAL
	} {
		t.Run(q, func(t *testing.T) {
			stmts, err := parser.Parse(q)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			_, rctx, err := planFromClause(stmts[0].(*parser.SelectStmt), scopeTestCatalog(t), DefaultPlannerSettings(), newRtableScope())
			if err != nil {
				return // a shape the FROM walk itself rejects is also not pulled up
			}
			if len(rctx.pulledDerived) != 0 {
				t.Fatalf("pulled up a shape slice 1 declines")
			}
		})
	}
}

// Two pulled bodies over the same relation (TPC-DS Q59's shape): the
// unqualified name they share is ambiguous exactly as the two derived leaves
// were, and each alias reaches its own body.
func TestDerivedPullupTwoBodiesSameRelation(t *testing.T) {
	_, _, rctx := pullupPlanFrom(t, "SELECT 1 FROM (SELECT ax FROM a WHERE ay < 5) y, (SELECT ax FROM a WHERE ay >= 5) x WHERE y.ax = x.ax")
	if len(rctx.pulledDerived) != 2 || len(rctx.pulledQuals) != 2 {
		t.Fatalf("pulledDerived=%d pulledQuals=%d, want 2 and 2", len(rctx.pulledDerived), len(rctx.pulledQuals))
	}
	y := pullupResolve(t, rctx, "y", "ax").(*ColumnRef)
	x := pullupResolve(t, rctx, "x", "ax").(*ColumnRef)
	if y.Index == x.Index || y.SourceTableIdx == x.SourceTableIdx {
		t.Fatalf("y.ax and x.ax resolved to the same leaf: %#v %#v", y, x)
	}
	if _, err := resolveColumnRef(&parser.ColumnRef{Column: "ax"}, rctx); err == nil {
		t.Fatal("unqualified ax resolved; it is ambiguous between y and x")
	}
}

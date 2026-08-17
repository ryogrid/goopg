package nodes

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestRebuildViewQuery is the sub-slice-2-part-b fixed-point gate: resolving a
// view, rebuilding its AST from the IR, and re-resolving that AST must reproduce
// the exact same pg_rewrite.ev_action bytes PostgreSQL 18.3 stored. This proves
// RebuildViewQuery is the faithful inverse of ResolveViewQuery — the reload path
// re-derives a semantically identical view without a live catalog lookup.
func TestRebuildViewQuery(t *testing.T) {
	for _, tc := range []struct {
		name, sql, golden string
	}{
		{"v_with_where", "SELECT client, src FROM bench_log WHERE client > 0", goldenViewV},
		{"v2_funcexpr_no_where", "SELECT upper(src) AS us FROM bench_log", goldenViewV2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := ResolveViewQuery(parseSelect(t, tc.sql), benchLogResolver{})
			if err != nil {
				t.Fatalf("ResolveViewQuery: %v", err)
			}
			sel, err := RebuildViewQuery(q)
			if err != nil {
				t.Fatalf("RebuildViewQuery: %v", err)
			}
			q2, err := ResolveViewQuery(sel, benchLogResolver{})
			if err != nil {
				t.Fatalf("re-ResolveViewQuery: %v", err)
			}
			if got := OutRuleAction([]Node{q2}); got != tc.golden {
				t.Fatalf("rebuild round-trip ev_action mismatch:\n got=%s\nwant=%s", got, tc.golden)
			}
		})
	}
}

// TestRebuildViewQueryStructure inspects the rebuilt AST directly so a
// round-tripping-but-wrong reconstruction can't slip through: the FROM item, the
// no-redundant-alias rule for a plain column target, the WHERE operator shape,
// and the explicit alias kept for a computed target whose resname differs from
// the auto-derived function name.
func TestRebuildViewQueryStructure(t *testing.T) {
	q, err := ResolveViewQuery(parseSelect(t, "SELECT client, src FROM bench_log WHERE client > 0"), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery: %v", err)
	}
	sel, err := RebuildViewQuery(q)
	if err != nil {
		t.Fatalf("RebuildViewQuery: %v", err)
	}
	if len(sel.From) != 1 || sel.From[0].Name != "bench_log" {
		t.Fatalf("From = %+v, want one RangeVar bench_log", sel.From)
	}
	if len(sel.Targets) != 2 {
		t.Fatalf("Targets = %d, want 2", len(sel.Targets))
	}
	// A plain column target keeps its column name -> no explicit alias.
	for i, want := range []string{"client", "src"} {
		if sel.Targets[i].Alias != "" {
			t.Errorf("target %d alias = %q, want empty (plain column)", i, sel.Targets[i].Alias)
		}
		cr, ok := sel.Targets[i].Expr.(*parser.ColumnRef)
		if !ok || cr.Column != want {
			t.Errorf("target %d expr = %#v, want ColumnRef %q", i, sel.Targets[i].Expr, want)
		}
	}
	bo, ok := sel.Where.(*parser.BinaryOp)
	if !ok {
		t.Fatalf("Where = %T, want *parser.BinaryOp", sel.Where)
	}
	if l, ok := bo.Left.(*parser.ColumnRef); !ok || l.Column != "client" {
		t.Errorf("Where left = %#v, want ColumnRef client", bo.Left)
	}

	// A computed target whose resname was aliased keeps the explicit alias.
	q2, err := ResolveViewQuery(parseSelect(t, "SELECT upper(src) AS us FROM bench_log"), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery v2: %v", err)
	}
	sel2, err := RebuildViewQuery(q2)
	if err != nil {
		t.Fatalf("RebuildViewQuery v2: %v", err)
	}
	if sel2.Targets[0].Alias != "us" {
		t.Errorf("computed target alias = %q, want us", sel2.Targets[0].Alias)
	}
	if _, ok := sel2.Targets[0].Expr.(*parser.FuncCall); !ok {
		t.Errorf("computed target expr = %T, want *parser.FuncCall", sel2.Targets[0].Expr)
	}
}

// TestRebuildViewQueryRejects confirms RebuildViewQuery reports a
// producer/reader mismatch (rather than emitting a malformed AST) for an IR
// Query outside the modeled single-relation SELECT subset.
func TestRebuildViewQueryRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    *Query
	}{
		{"not_select", &Query{CommandType: 2}},
		{"no_rtable", &Query{CommandType: 1}},
		{"empty_targets", &Query{
			CommandType: 1,
			Rtable:      []Node{&RangeTblEntry{Eref: &Alias{Aliasname: "t", Colnames: []string{"a"}}}},
		}},
		{"var_attno_out_of_range", &Query{
			CommandType: 1,
			Rtable:      []Node{&RangeTblEntry{Eref: &Alias{Aliasname: "t", Colnames: []string{"a"}}}},
			TargetList: []Node{&TargetEntry{
				Expr:    &Var{Varno: 1, Varattno: 5},
				Resno:   1,
				Resname: "x",
			}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RebuildViewQuery(tc.q); err == nil {
				t.Errorf("RebuildViewQuery(%s) = nil error, want mismatch error", tc.name)
			}
		})
	}
}

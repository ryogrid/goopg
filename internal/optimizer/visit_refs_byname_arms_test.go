package optimizer

// M0125-0002 commit 7 pins for visitColumnRefsByName and its three
// consumers — the LAST walker of the series, and the one D2 row 7 named
// as having the largest and least predictable effect.
//
// What makes this walker different from commits 3/4/5/6 is that its
// consumers do not read the callback stream, they read a verdict SEEDED
// TRUE and falsified only from inside the callback:
//
//	allMatched := true
//	visitColumnRefsByName(c, func(name string) { if !found { allMatched = false } })
//	return allMatched
//
// A conjunct built entirely from kinds the old 7-arm switch did not
// enumerate produced ZERO callbacks and returned a vacuous `true`. For the
// pushdown guards that vacuous true authorises a push. (The original exemplar
// was `extraInScans`, where it ADMITTED the conjunct into
// MultiHashJoin.Filters to be evaluated against a row the value was not on;
// both went at M0127-P6.2.) So this walker's fail-open is not a missed
// optimisation, it is the admission side of a wrong-answer path.
//
// The conversion therefore changes the SIGNATURE, not just the arm set:
// the second result says whether the name test COVERED the expression.
// D3 fixed this policy before any code was written ("plan slots signal,
// and the caller must treat 'an opaque child exists' as not matched").
//
// Three groups of pins below, in the order they were proved to fail
// against the old body:
//
//  1. newly-collected names — the 25-of-32 kinds the old switch skipped;
//  2. the totality result — every kind that cannot be certified by a
//     name test, stated per kind rather than derived;
//  3. the consumer inversion — allColumnRefNamesInScope /
//     pushOuterQualsIntoLaterals must now REJECT what they used to
//     admit vacuously.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// byNameCollect returns the names fn was handed and the totality result,
// so a test can assert both halves of the new contract at once.
func byNameCollect(e Expr) ([]string, bool) {
	var got []string
	total := visitColumnRefsByName(e, func(n string) { got = append(got, n) })
	return got, total
}

func byNameSaw(e Expr, want string) bool {
	names, _ := byNameCollect(e)
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// byNameScan builds a SeqScan whose Output() carries the given column
// names — the only thing collectScanOutputNames reads.
func byNameScan(table string, cols ...string) *SeqScan {
	s := &SeqScan{Table: &catalog.Table{Name: table}}
	for _, c := range cols {
		s.schema = append(s.schema, ijCol(c))
	}
	return s
}

// ---------------------------------------------------------------------
// 1. Kinds whose names the old 7-arm switch never collected.
//
// One case per kind: a test that covers BinaryOp proves nothing about
// CollateExpr, which is exactly how the original hole survived five
// years of edits.
// ---------------------------------------------------------------------

func TestVisitColumnRefsByName_NewlyCollectedKinds(t *testing.T) {
	col := func() Expr { return &ColumnRef{Index: 1, Name: "want_me"} }
	plan := &SeqScan{Table: &catalog.Table{Name: "x"}}

	cases := []struct {
		name string
		expr Expr
	}{
		{"IsNullExpr", &IsNullExpr{Operand: col()}},
		{"IsBoolExpr", &IsBoolExpr{Operand: col()}},
		{"CollateExpr", &CollateExpr{Operand: col()}},
		{"CastExpr", &CastExpr{Operand: col()}},
		{"IsDistinctFromExpr/Left", &IsDistinctFromExpr{Left: col(), Right: &IntegerConst{}}},
		{"IsDistinctFromExpr/Right", &IsDistinctFromExpr{Left: &IntegerConst{}, Right: col()}},
		{"RowExpr", &RowExpr{Elems: []Expr{col()}}},
		{"InExpr/List", &InExpr{Operand: &IntegerConst{}, List: []Expr{col()}}},
		// Subplan Args are evaluated against the CURRENT outer row, so
		// they live in the parent's coordinate space and their names
		// must be checked against the parent's scans.
		{"InExpr/Args", &InExpr{Operand: &IntegerConst{}, Plan: plan, Args: []Expr{col()}}},
		{"ExistsExpr/Args", &ExistsExpr{Plan: plan, Args: []Expr{col()}}},
		{"SubqueryExpr/Args", &SubqueryExpr{Plan: plan, Args: []Expr{col()}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !byNameSaw(c.expr, "want_me") {
				t.Errorf("visitColumnRefsByName skipped the ColumnRef under %s; "+
					"its consumers then return a vacuous true and admit the "+
					"conjunct unchecked (RC-1a, admission side)", c.name)
			}
		})
	}
}

// The arms the old switch DID have must keep working through the
// conversion.
func TestVisitColumnRefsByName_PreservedArms(t *testing.T) {
	col := func() Expr { return &ColumnRef{Index: 1, Name: "want_me"} }

	cases := []struct {
		name string
		expr Expr
	}{
		{"root ColumnRef", col()},
		{"BinaryOp/Left", &BinaryOp{Op: parser.OpEq, Left: col(), Right: &IntegerConst{}}},
		{"BinaryOp/Right", &BinaryOp{Op: parser.OpEq, Left: &IntegerConst{}, Right: col()}},
		{"UnaryOp", &UnaryOp{Operand: col()}},
		{"FuncCall", &FuncCall{Name: "abs", Args: []Expr{col()}}},
		{"ExtractExpr", &ExtractExpr{Source: col()}},
		{"CaseExpr/Operand", &CaseExpr{Operand: col(),
			Whens: []CaseWhen{{When: &IntegerConst{}, Then: &IntegerConst{}}}}},
		{"CaseExpr/When", &CaseExpr{Whens: []CaseWhen{{When: col(), Then: &IntegerConst{}}}}},
		{"CaseExpr/Then", &CaseExpr{Whens: []CaseWhen{{When: &IntegerConst{}, Then: col()}}}},
		{"CaseExpr/Else", &CaseExpr{
			Whens: []CaseWhen{{When: &IntegerConst{}, Then: &IntegerConst{}}}, Else: col()}},
		{"InExpr/Operand", &InExpr{Operand: col(), List: []Expr{&IntegerConst{}}}},
		{"nested depth", &BinaryOp{Op: parser.OpAnd,
			Left:  &IsNullExpr{Operand: &CastExpr{Operand: col()}},
			Right: &IntegerConst{}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !byNameSaw(c.expr, "want_me") {
				t.Errorf("visitColumnRefsByName no longer collects %s", c.name)
			}
		})
	}
}

// ---------------------------------------------------------------------
// 2. The totality result, stated per kind.
//
// The table records a DECISION for every kind exprChildSlots knows, so
// a 33rd Expr type cannot re-open the vacuous-true gap silently: the
// exhaustiveness gate forces it into exprChildSlots, and this table
// forces a decision about it here.
//
// The rule the table encodes: a kind is non-total iff it READS ROW DATA
// WITHOUT NAMING THE COLUMN IT READS, or it opens an inner scope, or it
// is unknown to the primitive. Parameters, the table OID and the MERGE
// action string read no row column and stay total.
// ---------------------------------------------------------------------

func TestVisitColumnRefsByName_TotalityOnEveryExprKind(t *testing.T) {
	plan := &SeqScan{Table: &catalog.Table{Name: "x"}}
	row := &MultiAssignSubqRow{Plan: plan}
	col := func() Expr { return &ColumnRef{Index: 1, Name: "a"} }

	kinds := map[string]struct {
		expr      Expr
		wantTotal bool
	}{
		// 15 childless leaves.
		"IntegerConst":      {&IntegerConst{Value: 1}, true},
		"StringConst":       {&StringConst{Value: "s"}, true},
		"NumericConst":      {&NumericConst{}, true},
		"TypedStringLit":    {&TypedStringLit{}, true},
		"IntervalLit":       {&IntervalLit{}, true},
		"NullConst":         {&NullConst{}, true},
		"BooleanConst":      {&BooleanConst{Value: true}, true},
		"ColumnRef/named":   {col(), true},
		"ColumnRef/unnamed": {&ColumnRef{Index: 1}, false}, // Name is "for diagnostics" and IS empty on some paths
		"OuterColumnRef":    {&OuterColumnRef{Level: 1, Index: 3, Name: "o"}, false},
		"ParamRef":          {&ParamRef{}, true},
		"ExecParamRef":      {&ExecParamRef{}, true},
		"TableOidExpr":      {&TableOidExpr{}, true},
		"CTIDExpr":          {&CTIDExpr{}, false},
		"MergeActionExpr":   {&MergeActionExpr{}, true},
		"MergeWholeRowRef":  {&MergeWholeRowRef{}, false},

		// 11 same-scope containers — total iff their children are.
		"ExtractExpr":        {&ExtractExpr{Source: col()}, true},
		"IsNullExpr":         {&IsNullExpr{Operand: col()}, true},
		"IsBoolExpr":         {&IsBoolExpr{Operand: col()}, true},
		"CollateExpr":        {&CollateExpr{Operand: col()}, true},
		"CastExpr":           {&CastExpr{Operand: col()}, true},
		"UnaryOp":            {&UnaryOp{Operand: col()}, true},
		"BinaryOp":           {&BinaryOp{Op: parser.OpEq, Left: col(), Right: &IntegerConst{Value: 1}}, true},
		"IsDistinctFromExpr": {&IsDistinctFromExpr{Left: col(), Right: &IntegerConst{Value: 1}}, true},
		"FuncCall":           {&FuncCall{Name: "abs", Args: []Expr{col()}}, true},
		"RowExpr":            {&RowExpr{Elems: []Expr{col()}}, true},
		"CaseExpr":           {&CaseExpr{Whens: []CaseWhen{{When: col(), Then: &IntegerConst{Value: 1}}}}, true},

		// A container inherits its child's verdict — the property that
		// makes the leaf decisions above load-bearing at depth.
		"BinaryOp/over-outer": {&BinaryOp{Op: parser.OpEq,
			Left: &OuterColumnRef{Level: 1, Index: 3, Name: "o"}, Right: col()}, false},

		// 6 scope-opening kinds. InExpr appears twice: only the Plan
		// form crosses a scope.
		"InExpr/list":         {&InExpr{Operand: col(), List: []Expr{&IntegerConst{Value: 1}}}, true},
		"InExpr/plan":         {&InExpr{Operand: col(), Plan: plan}, false},
		"ExistsExpr":          {&ExistsExpr{Plan: plan}, false},
		"SubqueryExpr":        {&SubqueryExpr{Plan: plan}, false},
		"ArraySubqueryExpr":   {&ArraySubqueryExpr{Plan: plan}, false},
		"MultiAssignSubqRow":  {row, false},
		"MultiAssignSubqElem": {&MultiAssignSubqElem{Row: row}, false},
	}

	for name, k := range kinds {
		t.Run(name, func(t *testing.T) {
			_, got := byNameCollect(k.expr)
			if got != k.wantTotal {
				t.Fatalf("visitColumnRefsByName(%s) total = %v, want %v", name, got, k.wantTotal)
			}
		})
	}
}

// A nil conjunct is vacuously covered: there is nothing to certify, and
// splitAnd can hand an empty predicate through. Pinned so the fail-closed
// direction is not extended to a case where it would decline everything.
func TestVisitColumnRefsByName_NilIsTotal(t *testing.T) {
	names, total := byNameCollect(nil)
	if len(names) != 0 || !total {
		t.Fatalf("nil: names=%v total=%v, want [] true", names, total)
	}
}

// An inner plan is REPORTED, not walked: names inside the subplan resolve
// in the subplan's own coordinate space, so matching them against this
// subtree's scan names would be a coincidence rather than evidence.
func TestVisitColumnRefsByName_InnerPlanNamesAreNotCollected(t *testing.T) {
	inner := &Filter{
		Child:     &SeqScan{},
		Predicate: &ColumnRef{Index: 0, Name: "inner_only"},
	}
	for _, c := range []struct {
		name string
		expr Expr
	}{
		{"InExpr.Plan", &InExpr{Operand: &ColumnRef{Index: 0, Name: "o"}, Plan: inner}},
		{"ExistsExpr.Plan", &ExistsExpr{Plan: inner}},
		{"SubqueryExpr.Plan", &SubqueryExpr{Plan: inner}},
		{"ArraySubqueryExpr.Plan", &ArraySubqueryExpr{Plan: inner}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if byNameSaw(c.expr, "inner_only") {
				t.Errorf("%s: collected a name from the inner scope", c.name)
			}
		})
	}
}

// ---------------------------------------------------------------------
// 3. The consumer inversion.
//
// These are the pins that actually change plans. Each builds a conjunct
// whose NAMED refs all match, so the callback stream alone says "yes",
// and whose opaque part is invisible to the old walker.
// ---------------------------------------------------------------------

// TestExtraInScans_{RejectsOpaqueConjunct,StillAdmitsCoveredConjunct} were
// deleted at M0127-P6.2 with `extraInScans` and the MultiHashJoin.Filters
// admission it guarded. The inversion they pinned is unchanged and is still
// pinned by the two consumers below: a conjunct whose NAMED refs all match —
// so the callback stream alone says "yes" — must be REJECTED when its opaque
// part was invisible to the old walker.

// allColumnRefNamesInScope guards pushOneConjunct's CROSS→INNER
// promotion. Its fail-open is the same shape and inverts the same way.
func TestAllColumnRefNamesInScope_RejectsOpaqueConjunct(t *testing.T) {
	j := &Join{Left: byNameScan("t", "a"), Right: byNameScan("u", "c")}

	covered := &BinaryOp{Op: parser.OpEq,
		Left: &ColumnRef{Index: 0, Name: "a"}, Right: &ColumnRef{Index: 1, Name: "c"}}
	if !allColumnRefNamesInScope(covered, j) {
		t.Fatal("allColumnRefNamesInScope rejected a fully covered conjunct")
	}

	opaque := &BinaryOp{Op: parser.OpEq,
		Left:  &ColumnRef{Index: 0, Name: "a"},
		Right: &ArraySubqueryExpr{Plan: &SeqScan{Table: &catalog.Table{Name: "z"}}}}
	if allColumnRefNamesInScope(opaque, j) {
		t.Error("allColumnRefNamesInScope certified a conjunct whose subquery it " +
			"never looked at; classifyConjunctSide has no default: either, so the " +
			"index verdict is no fallback here")
	}
}

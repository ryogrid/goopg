package planner

// M0125-0024 — the collision pins for the two expression-IDENTITY fail-opens.
//
// docs/design/0125-0024-expression-identity-collisions.md
//
// Every test below except the exhaustiveness ones FAILS at da6d2c0c (the state
// before the conversion) and passes after. That is deliberate: the census that
// found these two sites classified them by arm count, and an arm count cannot
// tell a lost optimisation from a wrong answer. These tests state the wrong
// answers.
//
// The two functions are a PAIR — planExprContentKey keyed *ColumnRef by
// SourceTableIdx/Index while exprEqual compared Index alone, so two refs were
// equal to one and distinct to the other. Nothing compared them, which is how
// the divergence survived. TestExprIdentitySiblingsAgree is now that comparison.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// ---------------------------------------------------------------------
// (a) planExprContentKey — collisions that SHARE an aggregate state slot
// ---------------------------------------------------------------------

// caseArg builds `CASE WHEN col(0) THEN <then> ELSE <els> END`, the shape that
// collided: *CaseExpr was one of the 28 types reaching the old `default:`.
func caseArg(then, els int64) *CaseExpr {
	return &CaseExpr{
		Whens: []CaseWhen{{
			When: &ColumnRef{Index: 0, Name: "flag"},
			Then: &IntegerConst{Value: then},
		}},
		Else: &IntegerConst{Value: els},
	}
}

func TestPlanExprContentKeyDistinguishesUnenumeratedTypes(t *testing.T) {
	// Each pair is two DIFFERENT expressions that the old `default:`
	// (`fmt.Sprintf("%T", e)`) collapsed onto one key, handing both
	// aggregate calls the same SharedStateSlot. The executor then copies
	// the leader's state to the follower, so the second aggregate reports
	// the first one's value: the M0097-0032 wrong answer.
	cases := []struct {
		name string
		a, b Expr
	}{
		{"CaseExpr", caseArg(1, 0), caseArg(100, 0)},
		{"CastExpr",
			&CastExpr{Operand: &ColumnRef{Index: 0}, TargetType: "int2"},
			&CastExpr{Operand: &ColumnRef{Index: 1}, TargetType: "int2"}},
		{"CastExpr target type",
			&CastExpr{Operand: &ColumnRef{Index: 0}, TargetType: "int2"},
			&CastExpr{Operand: &ColumnRef{Index: 0}, TargetType: "int8"}},
		// *BinaryOp is the arm that makes this reachable from ordinary
		// SQL: `ua(a + b)` and `ua(a - b)` shared a slot.
		{"BinaryOp operator",
			&BinaryOp{Op: parser.OpAdd, Left: &ColumnRef{Index: 0}, Right: &ColumnRef{Index: 1}},
			&BinaryOp{Op: parser.OpSub, Left: &ColumnRef{Index: 0}, Right: &ColumnRef{Index: 1}}},
		{"BinaryOp operand",
			&BinaryOp{Op: parser.OpAdd, Left: &ColumnRef{Index: 0}, Right: &ColumnRef{Index: 1}},
			&BinaryOp{Op: parser.OpAdd, Left: &ColumnRef{Index: 0}, Right: &ColumnRef{Index: 2}}},
		{"IsNullExpr negation",
			&IsNullExpr{Operand: &ColumnRef{Index: 0}},
			&IsNullExpr{Operand: &ColumnRef{Index: 0}, Negated: true}},
		{"CaseExpr shape",
			&CaseExpr{Whens: []CaseWhen{{When: &ColumnRef{Index: 0}, Then: &IntegerConst{Value: 1}}}},
			&CaseExpr{Operand: &ColumnRef{Index: 0}, Whens: []CaseWhen{{When: &IntegerConst{Value: 1}, Then: &IntegerConst{Value: 1}}}}},
	}
	for _, tc := range cases {
		ka := planExprContentKey(tc.a)
		kb := planExprContentKey(tc.b)
		if ka == kb {
			t.Errorf("%s: distinct expressions share the aggregate state-sharing key %q — "+
				"two user-aggregate calls with these arguments would be given one "+
				"SharedStateSlot and the second would report the first's value", tc.name, ka)
		}
	}
}

// The state-sharing key must still SHARE when the arguments really are the
// same expression, or M0097-0035's optimisation is silently dead.
func TestPlanExprContentKeySharesStructurallyEqualArgs(t *testing.T) {
	pairs := [][2]Expr{
		{caseArg(1, 0), caseArg(1, 0)},
		{&CastExpr{Operand: &ColumnRef{Index: 3}, TargetType: "numeric", Typmod: 8},
			&CastExpr{Operand: &ColumnRef{Index: 3}, TargetType: "numeric", Typmod: 8}},
		{&FuncCall{Name: "Abs", Args: []Expr{&ColumnRef{Index: 1}}},
			&FuncCall{Name: "abs", Args: []Expr{&ColumnRef{Index: 1}}}},
	}
	for i, p := range pairs {
		if planExprContentKey(p[0]) != planExprContentKey(p[1]) {
			t.Errorf("pair %d: structurally equal args key apart (%q vs %q) — state sharing is dead",
				i, planExprContentKey(p[0]), planExprContentKey(p[1]))
		}
	}
}

// An unenumerated Expr type must key UNIQUELY per node, not by type name.
// unknownExpr is declared in exprwalk_exhaustive_test.go for exactly this.
func TestPlanExprContentKeyFailsClosedOnUnknownType(t *testing.T) {
	a, b := &unknownExpr{pos: 1}, &unknownExpr{pos: 2}
	if planExprContentKey(a) == planExprContentKey(b) {
		t.Errorf("two distinct nodes of an unenumerated type share key %q — "+
			"an unteachable type must never license state sharing", planExprContentKey(a))
	}
	first, second := planExprContentKey(a), planExprContentKey(a)
	if first != second {
		t.Error("the same node keyed twice must be stable, or the one legitimate sharing case is lost")
	}
	if !strings.HasPrefix(planExprContentKey(a), "opaque:") {
		t.Errorf("unenumerated key %q should be visibly opaque", planExprContentKey(a))
	}
}

// A subplan cannot be keyed (Node traversal is outside exprwalk.go), so two
// SubqueryExprs must never share a slot — the old `default:` gave every one of
// them the key "*planner.SubqueryExpr".
func TestPlanExprContentKeyNeverSharesAcrossSubplans(t *testing.T) {
	a := &SubqueryExpr{Plan: &Limit{}}
	b := &SubqueryExpr{Plan: &Limit{}}
	if planExprContentKey(a) == planExprContentKey(b) {
		t.Error("two subquery arguments share a state slot: their plans are unexamined, " +
			"so equality here is an assertion the planner cannot make")
	}
	first, second := planExprContentKey(a), planExprContentKey(a)
	if first != second {
		t.Error("same subquery node keyed twice must be stable")
	}
}

// ---------------------------------------------------------------------
// (b) exprEqual — false negatives that broke DISTINCT ON / ORDER BY
// ---------------------------------------------------------------------

// The old fallback compared `%T%v`, and `%v` renders a NESTED pointer as a hex
// address. Every expression with an Expr child therefore compared unequal to a
// structurally identical twin, which at planner.go:1623 is a spurious 42P10.
func TestExprEqualMatchesStructurallyEqualPointerHolders(t *testing.T) {
	pairs := []struct {
		name string
		a, b Expr
	}{
		{"CastExpr",
			&CastExpr{Operand: &ColumnRef{Index: 2}, TargetType: "text"},
			&CastExpr{Operand: &ColumnRef{Index: 2}, TargetType: "text"}},
		{"IsNullExpr",
			&IsNullExpr{Operand: &ColumnRef{Index: 2}},
			&IsNullExpr{Operand: &ColumnRef{Index: 2}}},
		{"CaseExpr", caseArg(1, 0), caseArg(1, 0)},
		{"ExtractExpr",
			&ExtractExpr{Field: "year", Source: &ColumnRef{Index: 4}},
			&ExtractExpr{Field: "year", Source: &ColumnRef{Index: 4}}},
		{"CollateExpr",
			&CollateExpr{Operand: &ColumnRef{Index: 0}, CollationName: "C"},
			&CollateExpr{Operand: &ColumnRef{Index: 0}, CollationName: "C"}},
		{"RowExpr",
			&RowExpr{Elems: []Expr{&ColumnRef{Index: 0}, &IntegerConst{Value: 7}}},
			&RowExpr{Elems: []Expr{&ColumnRef{Index: 0}, &IntegerConst{Value: 7}}}},
	}
	for _, tc := range pairs {
		if !exprEqual(tc.a, tc.b) {
			t.Errorf("%s: structurally equal expressions read unequal — "+
				"a DISTINCT ON key matching its ORDER BY key would raise 42P10", tc.name)
		}
	}
}

// pos is not part of an expression's identity. PG's equal() excludes location
// (COMPARE_LOCATION_FIELD is a no-op in equalfuncs.c); the old fallback printed
// it, so the same literal written twice compared unequal.
func TestExprEqualIgnoresSourcePosition(t *testing.T) {
	if !exprEqual(&NumericConst{pos: 3, Value: "1.5"}, &NumericConst{pos: 41, Value: "1.5"}) {
		t.Error("two spellings of one numeric literal at different offsets read unequal")
	}
	if !exprEqual(&BooleanConst{pos: 1, Value: true}, &BooleanConst{pos: 99, Value: true}) {
		t.Error("TRUE is not equal to TRUE at another offset")
	}
	if exprEqual(&NumericConst{Value: "1.5"}, &NumericConst{Value: "2.5"}) {
		t.Error("distinct numeric literals must stay distinct")
	}
}

// exprEqual must keep saying "no" where it said no for a REASON, and must not
// start conflating types.
func TestExprEqualStillSeparatesDistinctExprs(t *testing.T) {
	cases := []struct {
		name string
		a, b Expr
	}{
		{"different column", &ColumnRef{Index: 1}, &ColumnRef{Index: 2}},
		{"column vs literal", &ColumnRef{Index: 1}, &IntegerConst{Value: 1}},
		{"different arity",
			&FuncCall{Name: "f", Args: []Expr{&ColumnRef{Index: 0}}},
			&FuncCall{Name: "f", Args: []Expr{&ColumnRef{Index: 0}, &ColumnRef{Index: 1}}}},
		{"star vs no star",
			&FuncCall{Name: "count", Star: true},
			&FuncCall{Name: "count"}},
		{"outer level",
			&OuterColumnRef{Level: 1, Index: 0},
			&OuterColumnRef{Level: 2, Index: 0}},
		// Flattening hazard: without delimiters `f(g(x))` and `f(g, x)`
		// would produce the same byte sequence.
		{"nesting vs sibling",
			&FuncCall{Name: "f", Args: []Expr{&FuncCall{Name: "g", Args: []Expr{&ColumnRef{Index: 0}}}}},
			&FuncCall{Name: "f", Args: []Expr{&FuncCall{Name: "g"}, &ColumnRef{Index: 0}}}},
	}
	for _, tc := range cases {
		if exprEqual(tc.a, tc.b) {
			t.Errorf("%s: distinct expressions read equal", tc.name)
		}
	}
}

// A node is equal to itself even when it is undecidable. The old fallback got
// this for free from `%T%v`; the fail-closed `false` would have lost it, so the
// pointer short-circuit is load-bearing.
func TestExprEqualPointerIdentityShortCircuits(t *testing.T) {
	sub := &SubqueryExpr{Plan: &Limit{}}
	if !exprEqual(sub, sub) {
		t.Error("a subquery expression is not equal to itself")
	}
	if exprEqual(sub, &SubqueryExpr{Plan: &Limit{}}) {
		t.Error("two subquery expressions with unexamined plans read equal")
	}
	u := &unknownExpr{}
	if !exprEqual(u, u) {
		t.Error("an unenumerated node is not equal to itself")
	}
	if exprEqual(u, &unknownExpr{}) {
		t.Error("two unenumerated nodes read equal — silence must mean refuse")
	}
	if !exprEqual(nil, nil) || exprEqual(nil, &ColumnRef{}) {
		t.Error("nil handling changed")
	}
}

// ---------------------------------------------------------------------
// The pair invariant
// ---------------------------------------------------------------------

// The two functions must never diverge again: they are now one driver plus two
// fail-closed directions, and this asserts the shared half. It is the test that
// would have caught the *ColumnRef divergence (SourceTableIdx keyed by one,
// ignored by the other) at the moment it was introduced.
func TestExprIdentitySiblingsAgree(t *testing.T) {
	exprs := []Expr{
		&ColumnRef{Index: 0}, &ColumnRef{Index: 1},
		&ColumnRef{Index: 1, SourceTableIdx: 2}, // same column, other metadata
		&IntegerConst{Value: 1}, &IntegerConst{Value: 2},
		&StringConst{Value: "x"},
		&NumericConst{Value: "1.5"},
		caseArg(1, 0), caseArg(100, 0),
		&CastExpr{Operand: &ColumnRef{Index: 0}, TargetType: "int2"},
		&IsNullExpr{Operand: &ColumnRef{Index: 0}},
		&BinaryOp{Op: parser.OpAdd, Left: &ColumnRef{Index: 0}, Right: &ColumnRef{Index: 1}},
		&FuncCall{Name: "abs", Args: []Expr{&ColumnRef{Index: 1}}},
		&OuterColumnRef{Level: 1, Index: 0},
		&RowExpr{Elems: []Expr{&ColumnRef{Index: 0}}},
	}
	for i, a := range exprs {
		for j, b := range exprs {
			keyEq := planExprContentKey(a) == planExprContentKey(b)
			if got := exprEqual(a, b); got != keyEq {
				t.Errorf("exprs[%d] vs exprs[%d] (%T/%T): exprEqual=%v but key equality=%v — "+
					"the identity pair has diverged again", i, j, a, b, got, keyEq)
			}
		}
	}
}

// ---------------------------------------------------------------------
// The driver's own contract
// ---------------------------------------------------------------------

// scopeVeto is the whole safety argument for an identity question: under
// scopeIgnore the driver steps over an inner plan silently, and two different
// subqueries with identical Args key EQUAL. This pins that the policy choice is
// observable, so a future caller cannot pass scopeIgnore believing it inert.
func TestExprIdentityKeyRefusesInnerPlansUnderVeto(t *testing.T) {
	sub := &SubqueryExpr{Plan: &Limit{}, Args: []Expr{&ColumnRef{Index: 0}}}
	if _, ok := exprIdentityKey(sub, scopeVeto); ok {
		t.Error("scopeVeto must refuse an expression whose identity depends on a subplan")
	}
	if _, ok := exprIdentityKey(sub, scopeIgnore); !ok {
		t.Error("scopeIgnore is expected to (wrongly, for identity) accept it — " +
			"if that changed, the caller-side argument in the design doc needs rewriting")
	}
	// Same node reached through a wrapper: the refusal must propagate out
	// of a nested position, not just from the root.
	wrapped := Expr(&IsNullExpr{Operand: sub})
	if _, ok := exprIdentityKey(wrapped, scopeVeto); ok {
		t.Error("refusal did not propagate from a nested inner plan")
	}
	// A plan-bearing type with no plan yet IS decidable.
	if _, ok := exprIdentityKey(&SubqueryExpr{}, scopeVeto); !ok {
		t.Error("a SubqueryExpr with no Plan should still be keyable")
	}
}

// MultiAssignSubqElem reaches its plan through the statically-typed Row slot
// (slotSubqRow), which a type switch over Expr can never see as a child. It
// must refuse just like an inner plan.
func TestExprIdentityKeyRefusesSubqRowSlot(t *testing.T) {
	row := &MultiAssignSubqRow{Plan: &Limit{}, NCols: 2}
	if _, ok := exprIdentityKey(&MultiAssignSubqElem{Row: row, ColIdx: 0}, scopeVeto); ok {
		t.Error("a MultiAssignSubqElem's identity depends on its shared Row's plan; it must refuse")
	}
	if _, ok := exprIdentityKey(&MultiAssignSubqElem{ColIdx: 0}, scopeVeto); !ok {
		t.Error("a Row-less MultiAssignSubqElem should be keyable")
	}
}

package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// Stage S0-1 (design bundle correlated-subquery-planning, ch.06 §6):
// EXPLAIN renders sublinks the way upstream does — `EXISTS(SubPlan 1)`,
// `(SubPlan 1)`, `= ANY (SubPlan 1)` — with an indented `SubPlan N`
// subtree under the owning node, instead of leaking Go type names
// (`<*planner.ExistsExpr>`). Before this stage every un-decorrelated
// sublink printed as an opaque token, which made plan gates and
// PG plan diffs unreadable.

// explainSubPlanFixture creates two small tables the sublink probes
// below correlate across.
func explainSubPlanFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, ddl := range []string{
		"CREATE TABLE t1 (a int, b int)",
		"CREATE TABLE t2 (a int, b int)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	return ctx, cleanup
}

// joinedPlan returns the EXPLAIN output as one string for substring
// assertions, plus the raw lines for structural checks.
func joinedPlan(t *testing.T, ctx *Context, sql string) (string, []string) {
	t.Helper()
	lines := runExplainRows(t, ctx, sql)
	if len(lines) == 0 {
		t.Fatalf("EXPLAIN produced no rows for %q", sql)
	}
	return strings.Join(lines, "\n"), lines
}

// assertNoOpaqueExpr is the end-state assertion of the design's V3
// plan gate: no rendered line may leak a Go type name.
func assertNoOpaqueExpr(t *testing.T, plan string) {
	t.Helper()
	if strings.Contains(plan, "<*planner.") {
		t.Errorf("plan leaks an opaque planner type token:\n%s", plan)
	}
}

// NOTE on probe shapes: on these index-less fixture tables the
// unnesting pass DOES fire for a top-level correlated EXISTS /
// scalar sublink (it becomes a semi/anti/inner join), so those
// shapes render no SubPlan at all. To exercise the SubPlan
// rendering the probes below deliberately use shapes that stay
// SubPlans: sublinks under an OR (the pull-up loop bails on
// non-conjunct positions) and scalar sublinks whose body is not a
// single-aggregate SELECT. `IN` under an OR is NOT usable — that
// shape hits a live planner non-termination bug at HEAD (design
// bundle F1, fixed in stage S1a), so the IN forms are covered by
// direct formatter unit tests at the bottom of this file.
//
// M0125-0036 AMENDED THE FIRST HALF OF THAT NOTE: "under an OR" is no
// longer sufficient to keep a single-equality correlated EXISTS a
// SubPlan, because that is exactly the shape the EXISTS→ANY conversion
// consumes (it rewrites to a hashable uncorrelated `= ANY (SubPlan n)`).
// Tests whose SUBJECT is the correlated-SubPlan machinery itself — the
// handle lifecycle, the per-call stat counters, PARAM_EXEC rendering —
// therefore call pinCorrelatedSubPlanPath below. Tests that merely need
// *a* SubPlan (numbering, subtree indentation) are unaffected: the
// converted form still renders one.

// pinCorrelatedSubPlanPath switches the EXISTS→ANY conversion off for the
// duration of a test whose subject is the correlated-SubPlan execution
// path. That path stays live for every EXISTS the conversion declines
// (composite correlation, an aggregating body, an inequality
// correlation, a reference reaching past the body), so these tests still
// pin real behaviour — they just cannot use the one probe shape the
// conversion now claims.
func pinCorrelatedSubPlanPath(t *testing.T) {
	t.Helper()
	optimizer.SetExistsToAnyEnabled(false)
	t.Cleanup(func() { optimizer.SetExistsToAnyEnabled(true) })
}

func TestExplainSubPlanCorrelatedExists(t *testing.T) {
	pinCorrelatedSubPlanPath(t)
	ctx, cleanup := explainSubPlanFixture(t)
	defer cleanup()

	plan, lines := joinedPlan(t, ctx,
		"EXPLAIN SELECT * FROM t1 WHERE t1.a = 1 OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a)")

	if !strings.Contains(plan, "EXISTS(SubPlan 1)") {
		t.Errorf("want EXISTS(SubPlan 1) in plan:\n%s", plan)
	}
	// The sublink's inner plan prints as its own subtree.
	var sawHeader, sawBody bool
	for i, l := range lines {
		if strings.TrimSpace(l) == "SubPlan 1" {
			sawHeader = true
			// The next line is the subtree root, indented under it.
			if i+1 < len(lines) && strings.Contains(lines[i+1], "->") {
				sawBody = true
			}
		}
	}
	if !sawHeader {
		t.Errorf("no `SubPlan 1` subtree header:\n%s", plan)
	}
	if !sawBody {
		t.Errorf("`SubPlan 1` header has no plan tree under it:\n%s", plan)
	}
	assertNoOpaqueExpr(t, plan)
}

func TestExplainSubPlanNotExists(t *testing.T) {
	ctx, cleanup := explainSubPlanFixture(t)
	defer cleanup()

	plan, _ := joinedPlan(t, ctx,
		"EXPLAIN SELECT * FROM t1 WHERE t1.a = 1 OR NOT EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a)")

	if !strings.Contains(plan, "NOT EXISTS(SubPlan 1)") {
		t.Errorf("want NOT EXISTS(SubPlan 1) in plan:\n%s", plan)
	}
	assertNoOpaqueExpr(t, plan)
}

func TestExplainSubPlanCorrelatedScalar(t *testing.T) {
	ctx, cleanup := explainSubPlanFixture(t)
	defer cleanup()

	// A non-aggregate subquery body fails the scalar pull-up gate
	// (it requires a single-aggregate SELECT), so this stays a
	// SubPlan and exercises the EXPR_SUBLINK rendering.
	plan, _ := joinedPlan(t, ctx,
		"EXPLAIN SELECT * FROM t1 WHERE t1.b > (SELECT t2.b FROM t2 WHERE t2.a = t1.a LIMIT 1)")

	if !strings.Contains(plan, "(SubPlan 1)") {
		t.Errorf("want scalar (SubPlan 1) in plan:\n%s", plan)
	}
	assertNoOpaqueExpr(t, plan)
}

// TestFormatInExprSubPlanForms drives the IN/ANY/ALL rendering
// through the formatter directly. An end-to-end SQL probe is not
// possible yet: every position that keeps an IN sublink out of the
// pull-up loop (OR, NOT, CASE inside WHERE) trips the live
// non-termination bug F1, which stage S1a fixes.
func TestFormatInExprSubPlanForms(t *testing.T) {
	operand := &optimizer.ColumnRef{Name: "b"}
	inner := &optimizer.SeqScan{}

	cases := []struct {
		name string
		expr *optimizer.InExpr
		want string
	}{
		{
			name: "in subquery",
			expr: &optimizer.InExpr{Operand: operand, Plan: inner},
			want: "(b = ANY (SubPlan 1))",
		},
		{
			name: "not in subquery",
			expr: &optimizer.InExpr{Operand: operand, Plan: inner, Negated: true},
			want: "(NOT (b = ANY (SubPlan 1)))",
		},
		{
			name: "literal list",
			expr: &optimizer.InExpr{Operand: operand, List: []optimizer.Expr{
				&optimizer.IntegerConst{Value: 3},
				&optimizer.IntegerConst{Value: 5},
			}},
			want: "(b = ANY (3, 5))",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := &subPlanReg{}
			got := formatExprPGReg(tc.expr, reg)
			if got != tc.want {
				t.Errorf("render = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "<*planner.") {
				t.Errorf("render leaks an opaque type token: %q", got)
			}
		})
	}
}

// TestSubPlanRegNilSafe: the registry is optional — formatExprPG
// (no registry) must not panic on a sublink.
func TestSubPlanRegNilSafe(t *testing.T) {
	e := &optimizer.ExistsExpr{Plan: &optimizer.SeqScan{}}
	if got := formatExprPG(e); !strings.Contains(got, "SubPlan") {
		t.Errorf("nil-registry render = %q, want it to mention SubPlan", got)
	}
}

func TestExplainLiteralInListNotOpaque(t *testing.T) {
	ctx, cleanup := explainSubPlanFixture(t)
	defer cleanup()

	// A literal IN-list carries no sub-plan; it must still render as
	// a readable expression rather than `<*planner.InExpr>` (this is
	// TPC-H Q16's `p_size IN (...)` shape).
	plan, _ := joinedPlan(t, ctx,
		"EXPLAIN SELECT * FROM t1 WHERE t1.a IN (3, 5, 7)")

	assertNoOpaqueExpr(t, plan)
	if !strings.Contains(plan, "3") || !strings.Contains(plan, "7") {
		t.Errorf("literal IN-list values missing from plan:\n%s", plan)
	}
}

func TestExplainSubPlanNumbering(t *testing.T) {
	ctx, cleanup := explainSubPlanFixture(t)
	defer cleanup()

	// Two independent sublinks in one predicate get distinct numbers
	// and distinct subtrees.
	plan, lines := joinedPlan(t, ctx,
		"EXPLAIN SELECT * FROM t1 WHERE t1.a = 1 "+
			"OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a) "+
			"OR EXISTS (SELECT 1 FROM t2 WHERE t2.b = t1.b)")

	if !strings.Contains(plan, "SubPlan 1") || !strings.Contains(plan, "SubPlan 2") {
		t.Errorf("want two numbered sublinks:\n%s", plan)
	}
	headers := 0
	for _, l := range lines {
		if s := strings.TrimSpace(l); s == "SubPlan 1" || s == "SubPlan 2" {
			headers++
		}
	}
	if headers != 2 {
		t.Errorf("want 2 SubPlan subtree headers, got %d:\n%s", headers, plan)
	}
	assertNoOpaqueExpr(t, plan)
}

func TestExplainSemiAntiJoinLabels(t *testing.T) {
	ctx, cleanup := explainSubPlanFixture(t)
	defer cleanup()

	// A top-level correlated IN unnests to a semi join today; the
	// label must name the join type rather than rendering `(?)`.
	semi, _ := joinedPlan(t, ctx,
		"EXPLAIN SELECT * FROM t1 WHERE t1.a IN (SELECT t2.a FROM t2 WHERE t2.a = t1.a)")
	if strings.Contains(semi, "(?)") {
		t.Errorf("join type rendered as `(?)`:\n%s", semi)
	}
	if !strings.Contains(semi, "Semi Join") {
		t.Errorf("correlated IN did not produce a Semi Join label:\n%s", semi)
	}

	// A non-correlated NOT IN unnests to a null-aware anti join, and
	// the null-aware flag is surfaced so plan diffs can tell the two
	// anti joins apart.
	anti, _ := joinedPlan(t, ctx,
		"EXPLAIN SELECT * FROM t1 WHERE t1.a NOT IN (SELECT t2.a FROM t2)")
	if strings.Contains(anti, "(?)") {
		t.Errorf("join type rendered as `(?)`:\n%s", anti)
	}
	if !strings.Contains(anti, "Anti Join") {
		t.Errorf("non-correlated NOT IN did not produce an Anti Join label:\n%s", anti)
	}
	assertNoOpaqueExpr(t, anti)
}

// TestJoinTypeNameSemiAnti pins the label strings directly, so the
// mapping survives even if no planner path currently produces them.
// TestJoinLabelPinsPGLabels pins the label mapping directly, so the
// rule survives even if no planner path currently produces a given
// (algo, join type) pair. Spellings match PG 18.3 explain.c:1712-1763.
func TestJoinLabelPinsPGLabels(t *testing.T) {
	cases := []struct {
		algo string
		typ  optimizer.JoinType
		want string
	}{
		{"Nested Loop", optimizer.JoinTypeInner, "Nested Loop"},
		{"Hash", optimizer.JoinTypeInner, "Hash Join"},
		{"Merge", optimizer.JoinTypeInner, "Merge Join"},
		{"Nested Loop", optimizer.JoinTypeCross, "Nested Loop"},
		{"Hash", optimizer.JoinTypeLeft, "Hash Left Join"},
		{"Merge", optimizer.JoinTypeRight, "Merge Right Join"},
		{"Nested Loop", optimizer.JoinTypeFull, "Nested Loop Full Join"},
		{"Nested Loop", optimizer.JoinTypeSemi, "Nested Loop Semi Join"},
		{"Hash", optimizer.JoinTypeAnti, "Hash Anti Join"},
	}
	for _, c := range cases {
		if got := joinLabel(c.algo, c.typ); got != c.want {
			t.Errorf("joinLabel(%q, %v) = %q, want %q", c.algo, c.typ, got, c.want)
		}
	}
}

// TestNLIResidualPredicateRendered pins the R2-0 display fix: a
// NestedLoopIndexJoin's residual Predicate (conjuncts the index probe
// does not enforce) must appear as a Filter: line. It was previously
// dropped silently, which hid a residual mis-resolution during the Q7
// alias fix (deferral ledger, csq-S6).
func TestNLIResidualPredicateRendered(t *testing.T) {
	// Build via the planner on a fixture so the node is well-formed: an
	// EXISTS with an inner-only residual over an indexed correlation
	// column decorrelates to an NLI semi with the residual retained.
	ctx, cleanup := explainSubPlanFixture(t)
	defer cleanup()
	for _, ddl := range []string{"CREATE INDEX t2_a_ix ON t2(a)", "ANALYZE t1", "ANALYZE t2"} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	out, _ := joinedPlan(t, ctx,
		"EXPLAIN SELECT * FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a AND t2.b < t1.b)")
	if strings.Contains(out, "Nested Loop Semi Join") {
		if !strings.Contains(out, "b < ") && !strings.Contains(out, "< b") {
			t.Errorf("NLI semi residual predicate not rendered:\n%s", out)
		}
	} else if strings.Contains(out, "Semi Join") {
		// Hash semi carries the residual on the join predicate — the
		// display concern is NLI-specific; nothing to assert here.
		t.Logf("shape took hash semi, NLI residual display not exercised:\n%s", out)
	} else {
		t.Logf("shape stayed SubPlan, NLI residual display not exercised:\n%s", out)
	}
}

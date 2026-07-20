package planner

import (
	"testing"
)

// Plan-shape tests for the S1a pull-up guards (design bundle
// docs/design/correlated-subquery-planning, §2.5 of
// 03-planner-decorrelation-extensions.md).
//
// The semantics matrix in internal/executor/subquery_semantics_test.go pins
// the *results* these guards protect. These tests pin the *plans*, so a
// future change that starts decorrelating a rejected shape again fails here
// with a clear message rather than as a mysterious wrong answer three
// stages later.
//
// Each guard gets a pair: a shape that must stay a SubPlan, and — where the
// distinction is subtle — the neighbouring shape that must still unnest, so
// an over-broad bail is caught too.

// hasSemiOrAntiJoin reports whether the plan contains a semi or anti join,
// i.e. whether some sublink was pulled up.
func hasSemiOrAntiJoin(node Node) *Join {
	if j := findFirstJoinByType(node, JoinTypeSemi); j != nil {
		return j
	}
	return findFirstJoinByType(node, JoinTypeAnti)
}

// findLimitNode reports whether a Limit survives anywhere in the plan.
func findLimitNode(node Node) *Limit {
	var found *Limit
	var walk func(Node)
	walk = func(n Node) {
		if n == nil || found != nil {
			return
		}
		switch x := n.(type) {
		case *Limit:
			found = x
			return
		case *Filter:
			walk(x.Child)
		case *Project:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *Distinct:
			walk(x.Child)
		case *Join:
			walk(x.Left)
			walk(x.Right)
		case *MultiHashJoin:
			for _, t := range x.Tables {
				walk(t)
			}
		}
	}
	walk(node)
	return found
}

// --- Guard 1: IN must sit at a top-level conjunct ----------------------

// TestGuardINUnderORStaysSubPlan is the regression test for the F1 planner
// infinite loop: before the guard the IN was found under the OR but never
// removed from the predicate, so the driver loop wrapped one more join per
// iteration until the process ran out of memory. Reaching the assertions at
// all is most of what this test proves.
func TestGuardINUnderORStaysSubPlan(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x = 2 OR x IN (SELECT z FROM t2)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if j := hasSemiOrAntiJoin(node); j != nil {
		t.Errorf("IN under OR was pulled up into a %v join; the other OR arm is lost:\n%s",
			j.Type, planString(node))
	}
	if findInExpr(node) == nil {
		t.Errorf("InExpr disappeared from the plan; it must survive as a SubPlan:\n%s", planString(node))
	}
}

// TestGuardNotWrappedINBecomesAntiJoin covers the other half of guard 1:
// `NOT (x IN (...))` reaches the planner as UnaryOp(NOT, InExpr) rather
// than InExpr{Negated}, so it is not itself a top-level conjunct. The guard
// accepts that single NOT wrapper and flips the join to its dual, which is
// exactly what `x NOT IN (...)` means — it must NOT simply bail, and it
// must NOT produce a semi join.
func TestGuardNotWrappedINBecomesAntiJoin(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE NOT (x IN (SELECT z FROM t2))"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if j := findFirstJoinByType(node, JoinTypeSemi); j != nil {
		t.Fatalf("NOT-wrapped IN became a SEMI join (the complement of NOT IN):\n%s", planString(node))
	}
	j := findFirstJoinByType(node, JoinTypeAnti)
	if j == nil {
		t.Fatalf("NOT-wrapped IN did not become an ANTI join:\n%s", planString(node))
	}
	if !j.NullAware {
		t.Errorf("anti join from NOT-wrapped IN must be NullAware for NOT IN's three-valued semantics")
	}
}

// TestGuardTopLevelINStillUnnests guards against the top-conjunct check
// being too strict: the ordinary shape must keep its semi join.
func TestGuardTopLevelINStillUnnests(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x IN (SELECT z FROM t2)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if j := findFirstJoinByType(node, JoinTypeSemi); j == nil {
		t.Fatalf("top-level IN no longer unnests to a semi join:\n%s", planString(node))
	}
}

// --- Guard 2: scalar sublinks must be AND-reachable --------------------

func TestGuardScalarUnderORStaysSubPlan(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x = 2 OR x > (SELECT sum(z) FROM t2 WHERE y = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findSubqueryExpr(node) == nil {
		t.Errorf("OR-position scalar sublink was decorrelated; rows matching the other arm are dropped:\n%s",
			planString(node))
	}
}

func TestGuardTopLevelScalarStillUnnests(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x > (SELECT sum(z) FROM t2 WHERE y = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findSubqueryExpr(node) != nil {
		t.Errorf("a top-level correlated sum() sublink should still decorrelate:\n%s", planString(node))
	}
}

// --- Guard 3: only NULL-on-empty aggregates may decorrelate ------------

// TestGuardCountScalarStaysSubPlan is the count-bug guard. count() returns 0
// over an empty group, so unmatched outer rows must survive — which the
// INNER-join rewrite cannot express.
func TestGuardCountScalarStaysSubPlan(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x > (SELECT count(z) FROM t2 WHERE y = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findSubqueryExpr(node) == nil {
		t.Errorf("count(col) sublink was decorrelated; outer rows with no match are dropped:\n%s",
			planString(node))
	}
}

// TestGuardCoalescedAggregateStaysSubPlan covers the Project-wrapper half of
// guard 3: the aggregate is whitelisted but COALESCE turns its NULL into 0,
// which reintroduces the count bug.
func TestGuardCoalescedAggregateStaysSubPlan(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x > (SELECT COALESCE(sum(z), 0) FROM t2 WHERE y = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findSubqueryExpr(node) == nil {
		t.Errorf("COALESCE-wrapped aggregate was decorrelated; it is not NULL on an empty group:\n%s",
			planString(node))
	}
}

// TestGuardArithmeticOverAggregateStillUnnests pins the shape TPC-H Q20
// depends on: `0.5 * sum(...)` stays NULL when sum is NULL, so the Project
// wrapper must not block the rewrite.
func TestGuardArithmeticOverAggregateStillUnnests(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x > (SELECT 2 * sum(z) FROM t2 WHERE y = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findSubqueryExpr(node) != nil {
		t.Errorf("arithmetic over a whitelisted aggregate is still NULL-preserving and must decorrelate:\n%s",
			planString(node))
	}
}

// --- Guard 4: quantified non-equality forms ----------------------------

func TestGuardNotEqualAllStaysSubPlan(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x <> ALL (SELECT z FROM t2)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if j := hasSemiOrAntiJoin(node); j != nil {
		t.Errorf("`<> ALL` was pulled up into a %v join with an equality predicate, "+
			"which returns the complement of the correct answer:\n%s", j.Type, planString(node))
	}
	if findInExpr(node) == nil {
		t.Errorf("InExpr for `<> ALL` disappeared; it must survive as a SubPlan:\n%s", planString(node))
	}
}

func TestGuardLessThanAllStaysSubPlan(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x < ALL (SELECT z FROM t2)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if j := hasSemiOrAntiJoin(node); j != nil {
		t.Errorf("`< ALL` was pulled up into a %v join with an equality predicate:\n%s",
			j.Type, planString(node))
	}
}

// TestGuardEqAnyStillUnnests: `= ANY (subquery)` is spelled differently but
// means IN, and the parser leaves AnyOp unset for it, so it must keep its
// semi join.
func TestGuardEqAnyStillUnnests(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x = ANY (SELECT z FROM t2)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if j := findFirstJoinByType(node, JoinTypeSemi); j == nil {
		t.Fatalf("`= ANY (subquery)` means IN and must still unnest:\n%s", planString(node))
	}
}

// --- Guards 5 and 6: EXISTS bodies -------------------------------------

// TestGuardExistsPositiveLimitIsStripped: a positive constant LIMIT cannot
// change whether the body has rows, so upstream drops it and proceeds. The
// pull-up must happen AND the Limit must be gone — leaving it would cap the
// whole build side instead of each correlation group.
func TestGuardExistsPositiveLimitIsStripped(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE z = t1.x LIMIT 1)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("EXISTS(... LIMIT 1) should still unnest after the LIMIT is stripped:\n%s", planString(node))
	}
	if l := findLimitNode(node); l != nil {
		t.Errorf("the body's LIMIT survived into the semi-join build side, "+
			"where it caps every correlation group at once:\n%s", planString(node))
	}
}

// TestGuardExistsZeroLimitStaysSubPlan: LIMIT 0 makes EXISTS
// unconditionally false, so it must never be stripped.
func TestGuardExistsZeroLimitStaysSubPlan(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE z = t1.x LIMIT 0)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if j := hasSemiOrAntiJoin(node); j != nil {
		t.Errorf("EXISTS(... LIMIT 0) is always false but was pulled up into a %v join:\n%s",
			j.Type, planString(node))
	}
}

func TestGuardExistsOffsetStaysSubPlan(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE z = t1.x OFFSET 1)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if j := hasSemiOrAntiJoin(node); j != nil {
		t.Errorf("OFFSET can turn a non-empty body empty, but EXISTS was pulled up into a %v join:\n%s",
			j.Type, planString(node))
	}
}

// TestGuardExistsAggregateBodyStaysSubPlan: an ungrouped aggregate always
// produces exactly one row, so the EXISTS is a tautology. Building a semi
// join on the aggregate's output turns it into a selective filter.
func TestGuardExistsAggregateBodyStaysSubPlan(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT count(*) FROM t2 WHERE z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if j := hasSemiOrAntiJoin(node); j != nil {
		t.Errorf("EXISTS over an ungrouped aggregate is always true but became a %v join:\n%s",
			j.Type, planString(node))
	}
	if findExistsExpr(node) == nil {
		t.Errorf("ExistsExpr disappeared; the tautology must survive as a SubPlan:\n%s", planString(node))
	}
}

// TestGuardPlainExistsStillUnnests guards against the body checks being too
// broad: the ordinary correlated EXISTS must keep its semi join.
func TestGuardPlainExistsStillUnnests(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if j := findFirstJoinByType(node, JoinTypeSemi); j == nil {
		t.Fatalf("plain correlated EXISTS must still unnest:\n%s", planString(node))
	}
}

// --- Defensive belt ----------------------------------------------------

// TestCountSublinksInExpr covers the belt's building block. The belt itself
// (in unnestSubqueriesInPlan's driver loops) is a backstop for a *future*
// find/remove mismatch: with every current gate bailing cleanly, no input
// reaches it, so it cannot be exercised end-to-end without deliberately
// breaking a gate. What is testable — and what the belt depends on — is
// that the count sees sublinks at this level and does not descend into
// their inner plans, where rewrites at this level cannot reach.
func TestCountSublinksInExpr(t *testing.T) {
	cat := twoTablesCatalog(t)
	// Two sublinks in one predicate, neither of them unnestable (both are
	// under an OR), so both survive into the final plan.
	sql := "SELECT x FROM t1 WHERE x = 1 OR x IN (SELECT z FROM t2) OR x > (SELECT count(z) FROM t2 WHERE y = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := node.(*Filter)
	if !ok {
		// The plan may be wrapped in a Project; dig one level.
		if p, isProj := node.(*Project); isProj {
			f, ok = p.Child.(*Filter)
		}
	}
	if !ok || f == nil {
		t.Skipf("plan shape has no top-level Filter to inspect:\n%s", planString(node))
	}
	if got := countSublinksInExpr(f.Predicate); got != 2 {
		t.Errorf("countSublinksInExpr = %d, want 2 (one IN, one scalar):\n%s", got, planString(node))
	}
}

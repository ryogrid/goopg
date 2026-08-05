package executor

import (
	"strings"
	"testing"
)

// joinCondFixture creates two joinable tables.
func joinCondFixture(t *testing.T) (ctx *Context, cleanup func()) {
	t.Helper()
	c, _, cl := newDDLFixture(t)
	if err := runDDL(t, c, "CREATE TABLE jl (a int, b int, v int)"); err != nil {
		cl()
		t.Fatal(err)
	}
	if err := runDDL(t, c, "CREATE TABLE jr (a int, b int, w int)"); err != nil {
		cl()
		t.Fatal(err)
	}
	return c, cl
}

// TestExplainRendersHashCond pins M0127-P2.1's EXPLAIN half. goopg
// emitted NO condition line under a join node at all before this, so a
// plan's key choice was invisible — the exact property M0125-0035b's
// degeneracy bug turned on, where a constant-pinned key column produced
// a PG-identical-LOOKING plan that ran quadratically.
//
// The shape is upstream's show_upper_qual: `Hash Cond: (a = b)` for one
// pair, `((a = b) AND (c = d))` for a list (make_ands_explicit).
func TestExplainRendersHashCond(t *testing.T) {
	ctx, cleanup := joinCondFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT jl.v FROM jl JOIN jr ON jl.a = jr.a")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Hash Join") {
		t.Skipf("planner did not pick a hash join; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Hash Cond: ") {
		t.Fatalf("no Hash Cond line under the join; got:\n%s", joined)
	}
	cond := condLine(t, joined, "Hash Cond: ")
	if strings.Count(cond, "=") != 1 || strings.Contains(cond, " AND ") {
		t.Errorf("single-key join rendered %q, want one equality", cond)
	}
	if !strings.HasPrefix(cond, "(") || !strings.HasSuffix(cond, ")") {
		t.Errorf("Hash Cond %q is not parenthesised the way show_qual renders it", cond)
	}
}

// TestExplainRendersMultiColumnHashCond is the visible consequence of
// P2.1's planner change: BOTH equalities of a two-column join now
// appear. Before P2.1 the plan carried a single (LeftKey, RightKey) and
// the second equality existed only as a per-match residual re-check, so
// even a rendered condition could have shown only half the truth.
func TestExplainRendersMultiColumnHashCond(t *testing.T) {
	ctx, cleanup := joinCondFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT jl.v FROM jl JOIN jr ON jl.a = jr.a AND jl.b = jr.b")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Hash Join") {
		t.Skipf("planner did not pick a hash join; got:\n%s", joined)
	}
	cond := condLine(t, joined, "Hash Cond: ")
	if !strings.Contains(cond, " AND ") {
		t.Fatalf("two-column join rendered %q, want both pairs AND-ed", cond)
	}
	if strings.Count(cond, "=") != 2 {
		t.Errorf("Hash Cond %q does not carry exactly two equalities", cond)
	}
	// make_ands_explicit wraps the chain, so the whole list is one
	// parenthesised group whose members are themselves parenthesised.
	if !strings.HasPrefix(cond, "((") {
		t.Errorf("Hash Cond %q is not the ((a = b) AND (c = d)) shape PG emits", cond)
	}
}

// TestExplainRendersJoinFilterResidual pins M0127-P5.9-o. `Hash Cond:`
// (P2.1) made the join's KEY visible; the conjuncts the key does not
// enforce stayed invisible, so `ON jl.a = jr.a AND jl.v < jr.w` printed
// a plan in which the second conjunct appeared nowhere — the rows were
// right, but nothing in the text said the join re-checks anything.
//
// Both the label and the slot are upstream's: ExplainNode prints
// show_upper_qual(join.joinqual, "Join Filter") immediately after the
// Cond line for T_HashJoin / T_MergeJoin / T_NestLoop
// (postgres/src/backend/commands/explain.c). Verified against PostgreSQL
// 18.3 (throwaway 5533 cluster, same DDL, enable_mergejoin/nestloop off):
//
//	Hash Join
//	  Hash Cond: (jl.a = jr.a)
//	  Join Filter: (jl.v < jr.w)
//
// goopg now emits those two lines byte-for-byte.
func TestExplainRendersJoinFilterResidual(t *testing.T) {
	ctx, cleanup := joinCondFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT jl.v FROM jl JOIN jr ON jl.a = jr.a AND jl.v < jr.w")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Hash Join") {
		t.Skipf("planner did not pick a hash join; got:\n%s", joined)
	}
	if got := condLine(t, joined, "Hash Cond: "); got != "(jl.a = jr.a)" {
		t.Errorf("Hash Cond = %q, want %q\nplan:\n%s", got, "(jl.a = jr.a)", joined)
	}
	if got := condLine(t, joined, "Join Filter: "); got != "(jl.v < jr.w)" {
		t.Errorf("Join Filter = %q, want %q (PG 18.3's line)\nplan:\n%s",
			got, "(jl.v < jr.w)", joined)
	}
	// Slot order is part of the parity: Cond first, residual second.
	if strings.Index(joined, "Hash Cond:") > strings.Index(joined, "Join Filter:") {
		t.Errorf("Join Filter printed above Hash Cond; explain.c emits it after:\n%s", joined)
	}
	// Two residual conjuncts render as make_ands_explicit's chain, the
	// same shape the multi-pair Hash Cond uses. PG 18.3 prints
	// `Join Filter: ((jl.v < jr.w) AND (jl.b <> jr.b))`.
	lines = runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT jl.v FROM jl JOIN jr ON jl.a = jr.a AND jl.v < jr.w AND jl.b <> jr.b")
	joined = strings.Join(lines, "\n")
	if got := condLine(t, joined, "Join Filter: "); got != "((jl.v < jr.w) AND (jl.b <> jr.b))" {
		t.Errorf("two-conjunct Join Filter = %q, want %q\nplan:\n%s",
			got, "((jl.v < jr.w) AND (jl.b <> jr.b))", joined)
	}
}

// TestExplainNoJoinFilterWhenKeysCoverThePredicate is the other half of
// the rule, and the one that keeps the new line honest: every conjunct
// must print exactly ONCE. `create_hashjoin_plan` builds joinqual as
// `list_difference(joinclauses, hashclauses)` (createplan.c), so an
// all-equijoin join has an empty joinqual and PG prints no Join Filter
// line at all — verified on 18.3 for this exact two-key query.
//
// goopg derives the same split from `ExecHashKeyPlan`, the method the
// EXECUTOR uses to decide what it re-checks per match; a residual that
// printed here but was not evaluated (or vice versa) would reintroduce
// the disagreement the line exists to expose.
func TestExplainNoJoinFilterWhenKeysCoverThePredicate(t *testing.T) {
	ctx, cleanup := joinCondFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT jl.v FROM jl JOIN jr ON jl.a = jr.a AND jl.b = jr.b")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Hash Join") {
		t.Skipf("planner did not pick a hash join; got:\n%s", joined)
	}
	if strings.Contains(joined, "Join Filter") {
		t.Errorf("all-equijoin join printed a residual it does not evaluate:\n%s", joined)
	}
}

// TestExplainNoHashCondForNestedLoop — a join with no usable equality
// must not grow a condition line out of nowhere.
func TestExplainNoHashCondForNestedLoop(t *testing.T) {
	ctx, cleanup := joinCondFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT jl.v FROM jl JOIN jr ON jl.a < jr.a")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "Hash Cond") || strings.Contains(joined, "Merge Cond") {
		t.Errorf("non-equi join grew a key-condition line:\n%s", joined)
	}
}

// TestExplainHashCondSurvivesQueryResults is the correctness anchor for
// the pair: P2.1 publishes the list but must not change a single row.
// A two-column join whose FIRST column is constant-pinned on both sides
// is the Q78-class shape (M0125-0035b) — the one where the key choice
// matters most — so it is the one worth executing.
func TestExplainHashCondSurvivesQueryResults(t *testing.T) {
	ctx, cleanup := joinCondFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		"INSERT INTO jl VALUES (1, 10, 100), (1, 11, 101), (1, 12, 102)",
		"INSERT INTO jr VALUES (1, 10, 200), (1, 11, 201), (1, 99, 202)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	rows := runQueryRows(t, ctx,
		"SELECT jl.v FROM jl JOIN jr ON jl.a = jr.a AND jl.b = jr.b ORDER BY jl.v")
	vals := make([]string, 0, len(rows))
	for _, r := range rows {
		vals = append(vals, r[0].Format())
	}
	got := strings.Join(vals, ",")
	if got != "100,101" {
		t.Errorf("join returned %q, want \"100,101\"", got)
	}
}

// condLine extracts the text after the first line carrying label.
func condLine(t *testing.T, plan, label string) string {
	t.Helper()
	for _, ln := range strings.Split(plan, "\n") {
		if i := strings.Index(ln, label); i >= 0 {
			return strings.TrimSpace(ln[i+len(label):])
		}
	}
	t.Fatalf("no %q line in:\n%s", label, plan)
	return ""
}

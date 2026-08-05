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

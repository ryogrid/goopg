package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestExplainSortGroupKeyParenthesisation pins M0134-0001 S18: `Sort Key:`
// and `Group Key:` wrap a non-Var key expression in an EXTRA parenthesis
// pair exactly when the value arrives through a non-evaluating node — PG's
// `get_special_variable` "force parentheses for a non-Var referent"
// (ruleutils.c), reached whenever the printed key is an OUTER_VAR reference
// chased into a child's target list (docs/design/0134-0001-p2-explain-format.md
// § S18). A `Sort` is always such a reference (it never evaluates
// expressions); a `GroupAggregate` inherits the wrap from its child `Sort`,
// while a `HashAggregate` computing the key itself does not.
//
// Oracle rows for the same expression, both directions:
// postgres/src/test/regress/expected/aggregates.out:3464-3465
// (GroupAggregate → `Group Key: ((g % 10000))`) vs :3500-3501
// (HashAggregate → `Group Key: (g % 10000)`).
func TestExplainSortGroupKeyParenthesisation(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE agg_data (g int)"); err != nil {
		t.Fatal(err)
	}

	// Criterion 1: enable_hashagg=off forces GroupAggregate over a Sort —
	// both the Group Key and the child Sort's Sort Key must carry the extra
	// pair for the non-Var expression g % 10000.
	t.Run("GroupAggregateWrapsNonVarKey", func(t *testing.T) {
		// take2 P2-02c: enable_hashagg is a per-STATEMENT planner input now.
		// This package's EXPLAIN harness plans through optimizer.Plan, which
		// takes the DEFAULTS — the session-GUC-to-planner wiring lives in
		// internal/postmaster (ctxPlannerSettings), not here. So the setting is
		// applied where this test can reach it: the process-global seed that
		// DefaultPlannerSettings reads. Verified separately against a live
		// server that `SET enable_hashagg = off` yields GroupAggregate and is
		// per-session.
		defer hashAggSeed(false)()

		joined := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT g % 10000, sum(g), count(*) FROM agg_data GROUP BY g % 10000"), "\n")
		if !strings.Contains(joined, "GroupAggregate") {
			t.Fatalf("expected a GroupAggregate node; got:\n%s", joined)
		}
		if !strings.Contains(joined, "Group Key: ((g % 10000))") {
			t.Errorf("expected `Group Key: ((g %% 10000))`; got:\n%s", joined)
		}
		if !strings.Contains(joined, "Sort Key: ((g % 10000))") {
			t.Errorf("expected `Sort Key: ((g %% 10000))`; got:\n%s", joined)
		}
	})

	// Criterion 2: the carve-out. A HashAggregate directly over the scan
	// computes the key itself and keeps the single (unwrapped) form — this
	// must be TESTED, not assumed, per the brief.
	t.Run("HashAggregateKeepsSingleForm", func(t *testing.T) {
		joined := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT g % 10000, sum(g), count(*) FROM agg_data GROUP BY g % 10000"), "\n")
		if !strings.Contains(joined, "HashAggregate") {
			t.Fatalf("expected a HashAggregate node; got:\n%s", joined)
		}
		if strings.Contains(joined, "Group Key: ((g % 10000))") {
			t.Errorf("HashAggregate must not double-wrap the group key; got:\n%s", joined)
		}
		if !strings.Contains(joined, "Group Key: (g % 10000)") {
			t.Errorf("expected the single-wrap `Group Key: (g %% 10000)`; got:\n%s", joined)
		}
	})

	// Criterion 3: bare-Var keys are unchanged — no added pair, in either
	// emitter.
	t.Run("BareVarKeysUnchanged", func(t *testing.T) {
		if err := runDDL(t, ctx, "CREATE TABLE tv (a int, b int)"); err != nil {
			t.Fatal(err)
		}
		groupJoined := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT a, b, count(*) FROM tv GROUP BY a, b"), "\n")
		if !strings.Contains(groupJoined, "Group Key: a, b") {
			t.Errorf("expected bare `Group Key: a, b`; got:\n%s", groupJoined)
		}
		if strings.Contains(groupJoined, "Group Key: (a") {
			t.Errorf("bare-Var group key must not be wrapped; got:\n%s", groupJoined)
		}

		sortJoined := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT a FROM tv ORDER BY a"), "\n")
		if !strings.Contains(sortJoined, "Sort Key: a") {
			t.Errorf("expected bare `Sort Key: a`; got:\n%s", sortJoined)
		}
		if strings.Contains(sortJoined, "Sort Key: (a") {
			t.Errorf("bare-Var sort key must not be wrapped; got:\n%s", sortJoined)
		}
	})

	// Criterion 4: sort-order decorations (DESC / NULLS FIRST / NULLS LAST)
	// sit OUTSIDE the added pair.
	t.Run("SortDecorationOutsideParens", func(t *testing.T) {
		nonVarJoined := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT g % 10000 FROM agg_data ORDER BY g % 10000 DESC"), "\n")
		if !strings.Contains(nonVarJoined, "Sort Key: ((g % 10000)) DESC") {
			t.Errorf("expected `Sort Key: ((g %% 10000)) DESC`; got:\n%s", nonVarJoined)
		}

		varJoined := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT a FROM tv ORDER BY a DESC"), "\n")
		if !strings.Contains(varJoined, "Sort Key: a DESC") {
			t.Errorf("expected `Sort Key: a DESC`; got:\n%s", varJoined)
		}
	})

	// Criterion 5: EXPLAIN ANALYZE and EXPLAIN VERBOSE render the same
	// Group Key:/Sort Key: lines as the plain form (this is a rendering
	// property of the key expression, not of the ANALYZE/VERBOSE options).
	t.Run("AnalyzeAndVerboseAgreeWithPlain", func(t *testing.T) {
		// take2 P2-02c: enable_hashagg is a per-STATEMENT planner input now.
		// This package's EXPLAIN harness plans through optimizer.Plan, which
		// takes the DEFAULTS — the session-GUC-to-planner wiring lives in
		// internal/postmaster (ctxPlannerSettings), not here. So the setting is
		// applied where this test can reach it: the process-global seed that
		// DefaultPlannerSettings reads. Verified separately against a live
		// server that `SET enable_hashagg = off` yields GroupAggregate and is
		// per-session.
		defer hashAggSeed(false)()

		sql := "SELECT g % 10000, sum(g), count(*) FROM agg_data GROUP BY g % 10000"
		plain := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+sql), "\n")
		analyze := strings.Join(runExplainRows(t, ctx, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) "+sql), "\n")
		verbose := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF, VERBOSE) "+sql), "\n")

		if !strings.Contains(plain, "Group Key: ((g % 10000))") || !strings.Contains(plain, "Sort Key: ((g % 10000))") {
			t.Fatalf("plain EXPLAIN missing expected wrapped keys; got:\n%s", plain)
		}
		if !strings.Contains(analyze, "Group Key: ((g % 10000))") || !strings.Contains(analyze, "Sort Key: ((g % 10000))") {
			t.Errorf("EXPLAIN ANALYZE diverges from plain form; got:\n%s", analyze)
		}
		if !strings.Contains(verbose, "Group Key: ((g % 10000))") || !strings.Contains(verbose, "Sort Key: ((g % 10000))") {
			t.Errorf("EXPLAIN VERBOSE diverges from plain form; got:\n%s", verbose)
		}
	})
}

// hashAggSeed flips the process-global enable_hashagg seed that
// optimizer.DefaultPlannerSettings reads, and returns the restore func.
//
// take2 P2-02c made enable_hashagg a per-statement planner input, so the SEED
// is all a process global still does: it supplies the default for a planner
// call that has no session. That is exactly this package's EXPLAIN harness,
// which plans through optimizer.Plan.
func hashAggSeed(on bool) func() {
	optimizer.SetHashAggEnabled(on)
	return func() { optimizer.SetHashAggEnabled(true) }
}

package executor

// M0125-0024's VALUE gate, executor half — the end-to-end arm the fix's own
// ledger row (2026-07-30) recorded as owed.
//
// docs/design/0125-0024-expression-identity-collisions.md §5.
//
// internal/planner/agg_state_sharing_test.go asserts the planner's decision
// (two calls must not land on one SharedStateSlot). That is where the defect is
// DECIDED, but not where it becomes a wrong answer: the executor designates the
// first call on a slot the "leader", runs sfunc for it alone, and copies the
// finished state into every follower (operators_join_agg.go:1699-1775). A slot
// collision therefore makes the second aggregate report the FIRST one's value,
// with exactly the right result shape — one row, plausible numbers — which is
// why no row-count gate can see it.
//
// These tests drive that copy through a real CREATE AGGREGATE over a real
// plpgsql sfunc, so both halves of the consequence are observable: the VALUES
// (a follower must not inherit the leader's total) and the SIDE EFFECTS (the
// sfunc must run once per call per row, which is how M0097-0032 was originally
// observed — a NOTICE that fired once instead of twice).

import (
	"strings"
	"testing"
)

// newUserAggFixture builds a table, a NOTICE-emitting plpgsql sfunc and a
// user-defined aggregate over it. Only user-defined aggregates share state
// (buildAggregate leaves SharedStateSlot = -1 for built-ins), and only a
// user-defined sfunc can be observed running, so both halves are required to
// reach the leader/follower copy at all.
//
// Rows are chosen so that every quantity this file asserts is distinct: the
// flagged-only sums (3 / 30) differ from each other and from the whole-column
// sums, so a follower inheriting the leader's state can never coincide with the
// right answer.
func newUserAggFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)

	for _, stmt := range []string{
		`CREATE TABLE agg_t (flag boolean, a bigint, b bigint)`,
		`INSERT INTO agg_t VALUES (true, 1, 10), (true, 2, 20), (false, 4, 40)`,
		`CREATE FUNCTION ua_accum(st bigint, v bigint) RETURNS bigint
		   LANGUAGE plpgsql AS $$
		   BEGIN
		     RAISE NOTICE 'ua_accum %', v;
		     RETURN st + v;
		   END $$`,
		`CREATE AGGREGATE ua_sum(bigint) (SFUNC = ua_accum, STYPE = bigint, INITCOND = '0')`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture statement failed: %v\nSQL: %s", err, stmt)
		}
	}
	return ctx, cleanup
}

// noticeCount counts the sfunc's NOTICEs accumulated on the context. The sfunc
// emits exactly one per invocation, so this is the number of sfunc calls.
func noticeCount(ctx *Context) int {
	n := 0
	for _, m := range ctx.Notices {
		if strings.Contains(m, "ua_accum") {
			n++
		}
	}
	return n
}

// aggPair runs a two-aggregate query and returns its single row plus the number
// of sfunc invocations it caused.
func aggPair(t *testing.T, ctx *Context, sql string) (Row, int) {
	t.Helper()
	before := noticeCount(ctx)
	rows := runQuery(t, ctx, sql)
	if len(rows) != 1 {
		t.Fatalf("%s\nreturned %d rows, want 1", sql, len(rows))
	}
	if len(rows[0]) < 2 {
		t.Fatalf("%s\nreturned %d columns, want 2", sql, len(rows[0]))
	}
	return rows[0], noticeCount(ctx) - before
}

// TestUserAggFollowerDoesNotInheritLeaderState is the wrong-answer gate. Both
// argument shapes are types the pre-M0125-0024 planExprContentKey did not
// enumerate, so it keyed them by Go type name alone and the two calls collided:
//
//   - *BinaryOp — reachable from the plainest SQL there is, and the reason
//     M0125-0024 was raised above the seven-walker scope it was found in.
//   - *CaseExpr — M0097-0032's original shape.
//
// A collision makes column 2 echo column 1 and halves the sfunc's side effects.
func TestUserAggFollowerDoesNotInheritLeaderState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sql        string
		want0      int64
		want1      int64
		wantSFuncs int
	}{
		{
			// a+b = 11,22,44 → 77;  a-b = -9,-18,-36 → -63.
			name:       "BinaryOp",
			sql:        `SELECT ua_sum(a + b), ua_sum(a - b) FROM agg_t`,
			want0:      77,
			want1:      -63,
			wantSFuncs: 6, // 3 rows x 2 unshared calls
		},
		{
			// flagged a = 1,2 → 3;  flagged b = 10,20 → 30.
			name:       "CaseExpr",
			sql:        `SELECT ua_sum(CASE WHEN flag THEN a ELSE 0 END), ua_sum(CASE WHEN flag THEN b ELSE 0 END) FROM agg_t`,
			want0:      3,
			want1:      30,
			wantSFuncs: 6,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cleanup := newUserAggFixture(t)
			defer cleanup()

			row, sfuncs := aggPair(t, ctx, tc.sql)
			got0, _ := datumInt64(row[0])
			got1, _ := datumInt64(row[1])

			if got0 == got1 {
				t.Fatalf("both aggregates returned %d: the follower inherited the "+
					"leader's state, so the second column is the first one's total "+
					"(M0097-0032's wrong answer, reached through an identity-key "+
					"collision on %s)", got0, tc.name)
			}
			if got0 != tc.want0 || got1 != tc.want1 {
				t.Errorf("got (%d, %d), want (%d, %d)", got0, got1, tc.want0, tc.want1)
			}
			if sfuncs != tc.wantSFuncs {
				t.Errorf("sfunc ran %d times, want %d — the aggregates' state is not "+
					"being accumulated independently", sfuncs, tc.wantSFuncs)
			}
		})
	}
}

// TestDistinctOnPointerHoldingExprMatchesOrderBy is the second consequence of
// the same fix, on its other caller. exprEqual backs DISTINCT ON / ORDER BY
// matching, and its pre-M0125-0024 fallback formatted unenumerated types with
// "%T%v", which prints a node's nested pointers as ADDRESSES. Two structurally
// identical CASE expressions are separate parse nodes, so they compared UNEQUAL
// and goopg raised a spurious 42P10 for a statement PostgreSQL accepts.
//
// The fix's laxer direction was argued from PG's equal() ignoring node location
// (COMPARE_LOCATION_FIELD is a no-op in equalfuncs.c) rather than measured, which
// its ledger row recorded as owed. Measured here: PG 18.3 on 127.0.0.1:65438
// returns exactly these three rows for the same query shape over the same
// values,
//
//	SELECT DISTINCT ON (CASE WHEN flag THEN a ELSE b END) a, b
//	  FROM (VALUES (true,1,10),(true,2,20),(false,4,40)) AS t(flag,a,b)
//	  ORDER BY CASE WHEN flag THEN a ELSE b END, a;   -->  (1,10) (2,20) (4,40)
//
// so the removed error really was spurious, not a divergence goopg needed.
func TestDistinctOnPointerHoldingExprMatchesOrderBy(t *testing.T) {
	ctx, cleanup := newUserAggFixture(t)
	defer cleanup()

	// DISTINCT ON keys are 1, 2 and 40, all distinct, so all three rows survive
	// and the answer isolates the MATCHING decision from any dedup behaviour.
	rows := runQuery(t, ctx, `SELECT DISTINCT ON (CASE WHEN flag THEN a ELSE b END) a, b
		FROM agg_t ORDER BY CASE WHEN flag THEN a ELSE b END, a`)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (PG 18.3 returns 3)", len(rows))
	}
	wantA := []int64{1, 2, 4}
	wantB := []int64{10, 20, 40}
	for i, r := range rows {
		gotA, _ := datumInt64(r[0])
		gotB, _ := datumInt64(r[1])
		if gotA != wantA[i] || gotB != wantB[i] {
			t.Errorf("row %d = (%d, %d), want (%d, %d)", i, gotA, gotB, wantA[i], wantB[i])
		}
	}
}

// TestUserAggSharesStateForEqualArgs is the other direction: the sharing
// OPTIMISATION must survive the fix. Two calls whose arguments really are the
// same expression keep one slot, so the sfunc runs once per row rather than
// twice and a side-effecting sfunc fires once — the behaviour M0097-0035 built
// the leader/follower copy for. A fix that made identity too strict would show
// up here as doubled NOTICEs, not as a wrong value.
func TestUserAggSharesStateForEqualArgs(t *testing.T) {
	ctx, cleanup := newUserAggFixture(t)
	defer cleanup()

	row, sfuncs := aggPair(t, ctx, `SELECT ua_sum(a + b), ua_sum(a + b) FROM agg_t`)
	got0, _ := datumInt64(row[0])
	got1, _ := datumInt64(row[1])

	if got0 != 77 || got1 != 77 {
		t.Errorf("got (%d, %d), want (77, 77)", got0, got1)
	}
	if sfuncs != 3 {
		t.Errorf("sfunc ran %d times for 3 rows, want 3: identical arguments must "+
			"share one state slot so a side-effecting sfunc is not run twice", sfuncs)
	}
}

package executor

// M0125-0025 — a user-defined aggregate's state / combine / final function that
// RAISES must abort the statement, as PG does.
//
// docs/design/0125-0025-sfunc-error-propagation.md
//
// Found by reading executeSFuncCall while writing M0125-0024's value gate, and
// recorded as a ledger row (2026-07-30) that deliberately claimed a code path
// rather than an observed wrong answer. It is an observed wrong answer: before
// the fix every query below returned a plausible number or NULL with no error.
//
// Two distinct swallows produced that:
//
//   - executeSFuncCall itself discarded each candidate routine's error and fell
//     through to `42883 aggregate state function "x" does not exist`, so a
//     PRESENT sfunc that failed was reported as MISSING.
//   - every caller then tested `if serr == nil { state = newState }`, keeping
//     the PREVIOUS state, so the aggregate reported the total of the rows that
//     happened to succeed before the raise.
//
// PG has no such gap: advance_transition_function invokes the transition
// function through FunctionCallInvoke
// (postgres/src/backend/executor/nodeAgg.c), so an ereport(ERROR) inside it
// aborts the statement carrying the function's own SQLSTATE.
//
// Every `want` below is MEASURED against PG 18.3 on 127.0.0.1:65438 (the
// TPC-DS reference cluster, in a rolled-back transaction so nothing was left
// behind), not derived from the source:
//
//	SELECT p_rsum(a) FROM p_t;                        ERROR:  boom 2
//	SELECT p_rsum(DISTINCT a), p_rsum(DISTINCT a) …;  ERROR:  boom 2
//	SELECT p_fsum(a) FROM p_t;                        ERROR:  final boom 6
//
// goopg returned 1, (1, 1) and NULL respectively.

import (
	"strings"
	"testing"
)

// newRaisingAggFixture builds three user-defined aggregates over plpgsql
// routines that RAISE, one per function slot an aggregate has: SFUNC, FINALFUNC
// and COMBINEFUNC. Each slot is reached by a different call site, and every one
// of them swallowed independently, so a fix that plumbed only the transition
// function would still ship two silent wrong answers.
func newRaisingAggFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)

	for _, stmt := range []string{
		`CREATE TABLE raise_t (a bigint)`,
		`INSERT INTO raise_t VALUES (1), (2), (3)`,

		// Raises on the SECOND row, so the pre-fix wrong answer is the partial
		// sum 1 — a plausible bigint, which is why no row-count gate saw it.
		`CREATE FUNCTION p_raise(st bigint, v bigint) RETURNS bigint
		   LANGUAGE plpgsql AS $$
		   BEGIN
		     IF v > 1 THEN RAISE EXCEPTION 'boom %', v; END IF;
		     RETURN st + v;
		   END $$`,
		`CREATE AGGREGATE p_rsum(bigint) (SFUNC = p_raise, STYPE = bigint, INITCOND = '0')`,

		`CREATE FUNCTION p_ok(st bigint, v bigint) RETURNS bigint
		   LANGUAGE plpgsql AS $$ BEGIN RETURN st + v; END $$`,
		`CREATE FUNCTION p_fin(st bigint) RETURNS bigint
		   LANGUAGE plpgsql AS $$
		   BEGIN RAISE EXCEPTION 'final boom %', st; END $$`,
		`CREATE AGGREGATE p_fsum(bigint)
		   (SFUNC = p_ok, STYPE = bigint, INITCOND = '0', FINALFUNC = p_fin)`,

		`CREATE FUNCTION p_comb(a bigint, b bigint) RETURNS bigint
		   LANGUAGE plpgsql AS $$
		   BEGIN RAISE EXCEPTION 'combine boom'; END $$`,
		`CREATE AGGREGATE p_csum(bigint)
		   (SFUNC = p_ok, STYPE = bigint, INITCOND = '0', COMBINEFUNC = p_comb)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture statement failed: %v\nSQL: %s", err, stmt)
		}
	}
	return ctx, cleanup
}

// TestRaisingSFuncAbortsStatement is the wrong-answer gate. Each case asserts
// BOTH halves of the fix: an error is returned at all (pre-fix: nil), and it is
// the routine's OWN error rather than the misleading 42883 executeSFuncCall
// used to synthesise once every candidate had failed.
func TestRaisingSFuncAbortsStatement(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
		// wantMsg is PG 18.3's message for the same statement, measured.
		wantMsg string
		// preFix is what goopg silently returned before M0125-0025, quoted in
		// the failure text so a regression is self-explaining.
		preFix string
	}{
		{
			name:    "SFunc",
			sql:     `SELECT p_rsum(a) FROM raise_t`,
			wantMsg: "boom 2",
			preFix:  "1 (the sum of the rows before the raise)",
		},
		{
			// The DISTINCT/ORDER BY path is a SECOND sfunc loop, in finishAgg
			// rather than applyAgg, and it swallowed separately.
			name:    "SFuncUnderDistinct",
			sql:     `SELECT p_rsum(DISTINCT a) FROM raise_t`,
			wantMsg: "boom 2",
			preFix:  "1 (the sum of the rows before the raise)",
		},
		{
			// A THIRD sfunc loop: the leader/follower pre-compute in
			// aggregateOp.Open, reached only when two calls share a state slot.
			// This is the site M0125-0024's ledger row named as the worst of
			// the three ("drops the error and keeps the stale state").
			name:    "SFuncUnderSharedDistinctSlot",
			sql:     `SELECT p_rsum(DISTINCT a), p_rsum(DISTINCT a) FROM raise_t`,
			wantMsg: "boom 2",
			preFix:  "(1, 1)",
		},
		{
			name:    "FinalFunc",
			sql:     `SELECT p_fsum(a) FROM raise_t`,
			wantMsg: "final boom 6",
			preFix:  "NULL",
		},
		{
			name:    "CombineFunc",
			sql:     `SELECT p_csum(a) FROM raise_t`,
			wantMsg: "combine boom",
			preFix:  "6 (the un-combined partial state)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cleanup := newRaisingAggFixture(t)
			defer cleanup()

			rows, err := runQueryErr(t, ctx, tc.sql)
			if err == nil {
				t.Fatalf("%s\nreturned %v with NO error; PG 18.3 aborts with %q. "+
					"Before M0125-0025 goopg answered %s — a silently wrong value, "+
					"because the sfunc's error was discarded and the previous state kept.",
					tc.sql, rows, tc.wantMsg, tc.preFix)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("%s\ngot error %q, want one containing %q — the routine's own "+
					"error must survive; executeSFuncCall used to replace it with "+
					"42883 \"does not exist\", reporting a PRESENT routine as MISSING.",
					tc.sql, err.Error(), tc.wantMsg)
			}
		})
	}
}

// TestRaisingSFuncInWindowFrameDoesNotAnswerSilently covers the other consumer
// of the aggregate finalizer. windowOp borrows aggregateOp as a helper
// (operators_window.go: evalFrameAggFuncs / evalExplicitFrameAggFuncs) and
// already propagated applyAgg's error, but finishAgg had no error to give it, so
// the frame path is the one place a raising aggregate could still be finalized
// silently. Both callers now return it.
//
// That plumbing is currently unreachable, and this test is why we know: goopg's
// v0 analyzer refuses a user-defined aggregate in OVER (...) with 0A000 before
// the executor is involved, so no user-defined sfunc can run in a window frame
// at all (ledger row 2026-07-30). PG accepts it. The assertion is therefore the
// invariant rather than the message — a raising user-defined aggregate in a
// frame must not yield a silent value — which stays meaningful when window
// support for user-defined aggregates lands and the message becomes 'final
// boom'.
func TestRaisingSFuncInWindowFrameDoesNotAnswerSilently(t *testing.T) {
	ctx, cleanup := newRaisingAggFixture(t)
	defer cleanup()

	rows, err := runQueryErr(t, ctx, `SELECT p_fsum(a) OVER (ORDER BY a) FROM raise_t`)
	if err == nil {
		t.Fatalf("windowed aggregate returned %v with no error; its FINALFUNC raises "+
			"'final boom' and PG 18.3 aborts the statement", rows)
	}
	// Accept either the analyzer's refusal (today) or the routine's own error
	// (once user-defined aggregates are allowed in a window frame).
	msg := err.Error()
	if !strings.Contains(msg, "final boom") && !strings.Contains(msg, "not supported") {
		t.Errorf("got error %q, want either the sfunc's own \"final boom\" or the "+
			"analyzer's unsupported-window-function refusal", msg)
	}
}

// TestMissingSFuncStillFallsThrough pins the half of executeSFuncCall's
// behaviour that must NOT change, and is the reason the fix needed a
// decidability distinction rather than a blanket propagate.
//
// "No routine of that name exists" is not a failure: executeSFuncCall doubles
// as the lookup for the built-in transition functions it models inline, and an
// aggregate declared with no FINALFUNC finishes on its state alone. Propagating
// 42883 from those paths would have turned every such aggregate into an error.
// errSFuncNotFound / sfuncRaised exist exactly to keep this query green.
func TestMissingSFuncStillFallsThrough(t *testing.T) {
	ctx, cleanup := newRaisingAggFixture(t)
	defer cleanup()

	// p_ok is a plain accumulator and p_csum's declaration has no FINALFUNC, so
	// finalization asks for nothing and the state IS the answer: 1+2+3.
	rows := runQuery(t, ctx, `SELECT p_ok_sum FROM (SELECT sum(a) AS p_ok_sum FROM raise_t) s`)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if got, _ := datumInt64(rows[0][0]); got != 6 {
		t.Errorf("built-in sum = %d, want 6 — a built-in aggregate's finalization "+
			"must not be affected by the user-defined error path", got)
	}
}

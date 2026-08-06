package executor

import (
	"testing"
)

// M-NIGHTLY AI-20260806-191958-001 — a VOLATILE routine called from a
// data-modifying WITH runs one command id LATER than the statement that called
// it, so it must SEE what that statement has written so far.
//
// PostgreSQL advances the transaction command counter before every statement of
// a routine that is not `readonly_func` — i.e. every VOLATILE one — and bumps
// the active snapshot's command id to match:
// `postgres/src/backend/executor/functions.c` `postquel_getnext`, "If not
// read-only, be sure to advance the command counter for each command, so that
// all work to date in this transaction is visible"; `readonly_func` comes from
// `provolatile != PROVOLATILE_VOLATILE` in `init_sql_fcache`. The visibility
// consequence is HeapTupleSatisfiesMVCC's `cmin >= curcid ⇒ invisible` arm
// (`postgres/src/backend/access/heap/heapam_visibility.c`): with curcid now one
// higher, the statement's own writes stop being hidden.
//
// goopg's heap carries no per-tuple command id, so ctx.CTEWriteFence stands in
// for cmin — and until this fix it hid the statement's writes from EVERY nested
// scan regardless of command id. That made the volatile function's UPDATE below
// a silent no-op: goopg left `checking` at 1700 where live PG 18.3 leaves 1701.
//
// Oracle values were captured on live PG 18.3 (throwaway cluster, PG's own
// `postgres/src/test/isolation/specs/eval-plan-qual.spec` fixture) on
// 2026-08-06. The concurrent twin of this case — where the same divergence
// additionally suppressed PG's TM_SelfModified error — is covered end-to-end by
// TestPort_IsolationEvalPlanQual permutations `wx1 updwctefail c1 c2 read` and
// `wx1 delwctefail c1 c2 read`.
func TestCTEDMLVolatileRoutineSeesStatementWrites(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE cc_acct (accountid text, balance int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO cc_acct VALUES ('checking', 600), ('savings', 600)")

	// VOLATILE is the default; spelled out because it is the whole point.
	if err := runDDL(t, ctx, `CREATE FUNCTION cc_bump() RETURNS bool VOLATILE LANGUAGE sql AS $$
	    UPDATE cc_acct SET balance = balance + 1 WHERE accountid = 'checking'; SELECT true;$$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}

	// The CTE writes 'checking' = 1700; cc_bump() then runs as a LATER command
	// and must find that very row, taking it to 1701. The outer UPDATE runs
	// back at the statement's own command id, so it sees neither the CTE's row
	// nor the function's — only 'savings'.
	rows := runDMLRows(t, ctx, `WITH doup AS (
	        UPDATE cc_acct SET balance = balance + 1100 WHERE accountid = 'checking'
	        RETURNING accountid, balance, cc_bump())
	    UPDATE cc_acct a SET balance = doup.balance + 100 FROM doup RETURNING a.accountid`)
	if len(rows) != 1 {
		t.Fatalf("outer UPDATE returned %d rows, want 1 (PG 18.3: only 'savings')", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "savings" {
		t.Fatalf("outer UPDATE touched %q, want \"savings\"", got)
	}

	got := balancesByAccount(t, ctx, "cc_acct")
	// PG 18.3: checking 1701 (600 + 1100 by the CTE, + 1 by the volatile
	// function), savings 1800 (600 + doup.balance 1700 … minus its own 600 =
	// 1100+700). Pre-fix goopg left checking at 1700.
	if got["checking"] != 1701 {
		t.Errorf("checking = %d, want 1701 — the volatile routine's UPDATE was a no-op "+
			"(the command-blind CTE write fence hid the row the CTE had just written)", got["checking"])
	}
	if got["savings"] != 1800 {
		t.Errorf("savings = %d, want 1800", got["savings"])
	}
}

// TestCTEDMLStableRoutineDoesNotSeeStatementWrites is the negative twin: a
// STABLE routine is `readonly_func` upstream, gets NO CommandCounterIncrement,
// and therefore keeps its caller's command id — so the statement's own writes
// stay hidden from it. Without this, "advance the command id" could be
// implemented unconditionally and still pass the volatile test above.
func TestCTEDMLStableRoutineDoesNotSeeStatementWrites(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE cc_st (accountid text, balance int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO cc_st VALUES ('checking', 600), ('savings', 600)")

	if err := runDDL(t, ctx, `CREATE FUNCTION cc_peek() RETURNS int STABLE LANGUAGE sql AS $$
	    SELECT count(*)::int FROM cc_st WHERE balance > 1000;$$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}

	rows := runDMLRows(t, ctx, `WITH doup AS (
	        UPDATE cc_st SET balance = balance + 1100 WHERE accountid = 'checking'
	        RETURNING accountid, cc_peek())
	    SELECT doup.cc_peek FROM doup`)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	// The CTE has just written balance 1700, but cc_peek is STABLE: no command
	// counter increment, so the fence (PG: the cmin test) still hides that row
	// and the count is 0.
	if got := rows[0][0].Int; got != 0 {
		t.Errorf("cc_peek() = %d, want 0 — a STABLE routine is readonly_func upstream and "+
			"must NOT see the writes of the statement that called it", got)
	}
}

// balancesByAccount reads the fixture table into accountid → balance.
func balancesByAccount(t *testing.T, ctx *Context, table string) map[string]int64 {
	t.Helper()
	rows := runDMLRows(t, ctx, "SELECT accountid, balance FROM "+table+" ORDER BY accountid")
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		if len(r) < 2 || r[0].Kind == KindNull {
			continue
		}
		out[r[0].StringValue()] = r[1].Int
	}
	return out
}

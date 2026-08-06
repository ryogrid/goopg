(idle — nothing in flight)

Last loop: **M0127-S7's sole engine-side blocker is CLOSED (M0125-0055).
`TestPort_IsolationEvalPlanQual` PASSES.**

1. Two loops of bisect had named the blocker "goopg does not implement PG's
   `TM_SelfModified` error". That was the visible half. **Shrinking the
   isolation permutation until the second session disappeared** exposed the
   real defect: a plain wrong answer, one session, no harness —
   `WITH doup AS (UPDATE accounts … RETURNING *, update_checking(999))
   UPDATE accounts a … FROM doup` leaves `checking` at **1701** on live PG 18.3
   and **1700** on goopg. The volatile function's UPDATE was a silent 0-row
   no-op; the RETURNING output agreed, which is why five loops of evidence did.
2. Cause: goopg's data-modifying-WITH fence stands in for PG's per-tuple `cmin`
   and was **command-blind**. PG hides the tuple only while `cmin >= curcid`,
   and `curcid` is not frozen at `es_output_cid` — `functions.c
   postquel_getnext` does `CommandCounterIncrement()` before every statement of
   a routine that is not `readonly_func` (= `provolatile != 'v'`).
3. Fix: `Context.CmdID` + `routineCommandCounterIncrement` (6 child-Context
   sites in plpgsql_runtime.go); both fence maps re-valued by the writing /
   killing command id, so `cteFenced`/`cteRevealed` *are* the cmin/cmax arms.
4. That unmasked the error site — `ExecUpdate:2656`, INSIDE the EPQ path (the
   chain-follow applies no cmin test), raised at `updateWithFrom` /
   `deleteWithUsing`. SQLSTATE corrected `09000` → **`27000`**.
   `CTENewToOld`/`CTESelfModifiedErrors`/`CTESelfModErr` deleted — unreachable,
   and beside the counter fix they would have fired where PG succeeds.

Files: `internal/executor/{context,operators_cte_dml,operators_storage,
plpgsql_runtime}.go`, `internal/server/dispatch.go`, new
`cte_dml_command_counter_test.go`; design `0125-0055-…` + README index +
`leftdeep-joins/09-…` §3.26; fix_plan (item ticked + S7 status); 2 ledger rows.

Gates run: PG-vs-goopg byte-comparison on all 4 forms (`updwctefail`/
`delwctefail` × with/without the concurrent session), rows AND final heap —
identical; `TestPort_IsolationEvalPlanQual` PASS (22 s); full `TestPort_Isolation*`
(407 s) PASS; full `TestPort_RegressSuite` (322 s) PASS; executor/server/planner/
mvcc PASS; units gate PASS; `tpch-spotcheck` Q12=2/Q13=35; TPC-DS SF0.5 sweep
PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plan-shape 99 same/0 changed;
`make ralph-state-guard`; pgbench smoke via the commit hook.

NEXT LOOP (banner: M0124 closed → M0125 closed → M0127 → M-NIGHTLY → M0123).
**Re-run `make nightly-batch`** — S7's engine side is now clear and the
remaining 9 items of run `20260806-191958` are the single `regress/suite-wedge`
phenomenon, which produces no engine-side item. If the stage table comes back
`status: pass`, take **M0127-P6.1 (delete fusion)**.

In-flight: none.

(idle — nothing in flight)

Last loop: **M-NIGHTLY / S7-gate loop #4. Selected `regress/delete`
(AI-20260806-011323-016) under the same carve-out the harness fix used — of the
three 20260806 regress divergences it was the only one reproducing
deterministically, so it alone could have kept the S7 cycle red forever.
FIXED: `SKIP(deferred)` → `PASS`. Committed.**

1. **The ledger's DELETE scoping was too narrow.** goopg emitted the bald
   `missing FROM-clause entry` for **all five** shapes upstream distinguishes
   (`delete.out`, `update.out`, `returning.out`, `insert_conflict.out`, and
   plain `SELECT t.a` / `t.*` over an aliased FROM entry). The missing piece is
   a *diagnosis*, not wording: `errorMissingRTE()` (`parse_relation.c`) looks
   the refname up a **second time ignoring aliases**
   (`searchRangeTableForRel`) and reports "you wrote the table's own name where
   only its alias is visible". goopg did the first lookup and stopped.
2. **The trap avoided:** `blockOriginalName` (M0097-0003) already produced the
   right text — but only at the two sites that SET the flag, and
   `analyzeDelete` was not one, so the analyzer's bald error pre-empted the
   planner's correct one. Patching that one call site would have turned
   `delete` green and left the other four wrong.
3. **Fixed (root-0039):** one helper per resolver —
   `analyzer.errorMissingRTE` + `planner.errorMissingRTEPlan`. **Both twins
   moved**: RETURNING scopes are built after analysis, so `RETURNING t.*` is
   served only by the planner. Skips `qualifiedOnly` rels (ON CONFLICT's
   `excluded` is a keyword, not a user rename) and self-aliased entries
   (upstream's `strcmp(aliasname, relname) != 0`). Also 42712
   (`duplicate_alias`) → **42P01** at the two `blockOriginalName` sites.

Files: `internal/analyzer/analyzer.go`, `internal/planner/planner.go`, two new
guard tests (`*/missing_rte_test.go`),
`docs/design/root-0039-error-missing-rte-alias-hint.md` + README index,
fix_plan (item note + S7 status amendment), 2 ledger rows.

Gates run: UNITS 0 FAIL; SPOT PASS (Q12=2, Q13=35); both guards verified
NON-VACUOUS (revert the two source files, keep the tests → both fail with
`missing FROM-clause entry`); A/B on builds differing only in those two files —
`delete` SKIP→PASS while `insert_conflict`/`returning`/`subselect`/`update`
SKIP on BOTH sides and `join` + `rowtypes` (the only corpus files asserting the
bald message) SKIP identically on both; pgbench smoke via hook. DS05 NOT run —
deliberate: both helpers are reachable only on the `len(matches)==0` error
return, which already returned an error, so the delta is error text/SQLSTATE
only and cannot move a row count or a plan shape.

NEXT LOOP (banner: M0124 closed → M0125 → **M0127** → M-NIGHTLY → M0123).
`ci/logs/action-items.md` is STILL run `20260806-011323` — no new nightly has
run, so P6.1 stays unselectable; re-read it first, and if it is `status: pass`
take P6.1. The 4 genuine items of that run now stand at: `select` FIXED,
`delete` FIXED, `portals_p2` never reproduces, and
**`TestPort_IsolationEvalPlanQual` (AI-…-001) is the last open engine-side
blocker**. Four repro conditions are falsified — do NOT re-attempt an
isolation-level repro; the designed next step is the **prefix bisect** of
`internal/testport` (regress / pg_dump / pg_basebackup / pgoutput blocks +
EvalPlanQual) to find the predecessor that poisons it.

In-flight: none.

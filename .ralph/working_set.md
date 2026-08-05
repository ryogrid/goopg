(idle — nothing in flight)

Last loop: **M0127-P5.9-i CLOSED** — the 7 TPC-DS plan-time aborts were the
CHECKER's disagreement, not the arms'. Do NOT re-derive:

1. `reresolveJoinByName`'s `predRebind` (`internal/planner/bushy.go`) resolves a
   predicate operand against the side its index suggests and falls back to the
   other side on -1. `resolveSide` returned -1 for BOTH "name not on this side"
   (a miss — crossing over is the point) and "name on this side twice" (an
   ambiguity — crossing over is a guess). On the ambiguity it rebound a
   correctly-bound ref onto another relation's identically-named column: a
   predicate comparing a column to itself ⇒ cross product.
2. **`(Name, SourceTableIdx)` is a column identity only WITHIN one scope.**
   M0071-0009 added it for Q21's `l1/l2/l3` (three RTEs of one scope). TPC-DS
   Q83's three `item_id`s each descend from `item.i_item_id` inside a SEPARATE
   WITH arm, and each arm numbers its own range table ⇒ same source identity,
   genuinely ambiguous. Ledgered as the deeper gap (PG has no analogue: `Var`
   is (varno,varattno) and `setrefs.c` flattens).
3. Fix = report the duplicate case separately (`lookupColumnIndexByName`,
   `lookupColumnIndexByNameAndSource`) and ABSTAIN in `predRebind`; the miss
   fallback is untouched and pinned by
   `TestReresolveStillCrossesSidesOnAPlainMiss`. Old helpers survive as
   wrappers ⇒ forced-side join-key/NLI rebind sites unchanged.
4. **Measured** (DS05 subset sweep, flag ON): `ERROR=7` →
   `PASS=6 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1`; 5 of 6 carry
   PG-identical value checksums.
5. **Q47 = the TIMEOUT = NEW defect P5.9-j, not -i's remainder.** Correct 100
   rows, **8m40s** timed alone on a fresh server vs **11–13 s** flag-OFF.
   Uncovered (not caused) by -i — it used to abort before it could run.

Files: `internal/planner/bushy.go`,
`internal/planner/reconcile_ambiguousside_test.go` (new, 4 tests). Docs: 09
§3.7, bundle README, docs/design/README.md, IMPLEMENTATION-TODO P5.9-i [x] +
P5.9-j, fix_plan P5.9-i [x] + P5.9-j, 2 ledger rows,
`analysis/leftdeep-joins/2026-08-05-p59i-ds05-on.txt`.

Gates run: `go test ./internal/planner/` (green), UNITS precommit (green),
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35), DS05 subset sweep
(7 queries, flag ON), pgbench smoke via the commit hook, `make
ralph-state-guard` (repaired a stale progress marker). **NOT run: full DS05
`sweep` (~1 h) and `make plan-diff`** — still ledgered from P5.9-h, discharge
at run 4.

Nightly triage 20260805-014309: unchanged run, both items already filed under
M-NIGHTLY, left unchecked per the banner.

Next step: **M0127-P5.9-j** (Q47 ~40×) together with the cost half of
**P5.9-h** — one COST question, two symptoms; both are "a plan the search
prefers and should not". Run 4 of the bar comes after, with the DS05 clause no
longer optional.

In-flight: none.

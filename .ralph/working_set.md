(idle — nothing in flight)

Last loop: **M0127-P5.6-f-vi** — LANDED, all three named gates green,
committed + pushed (`c47e9132` fix, `fb68cb6f` audit evidence).
Facts the next loop must NOT re-derive:

1. **`estimateJoin` is EXONERATED.** Server instrumentation: for the SF0.5
   probe's `store` join under `d_year > 1999` it returns the CORRECT 726 987
   while EXPLAIN printed 367 128. Its pair loop, `splitAllEqualitiesForHash`
   and `joinResidualSelectivity`'s guard are all clean. Doc 09 §5.17's
   attribution was wrong and is corrected in place by **§5.18**.
2. **Real cause:** `pushInnerJoinInputQuals`
   (`internal/planner/inner_join_qual_pushdown.go`) DUPLICATES a
   single-relation restriction onto its relation and leaves `f.Predicate`
   untouched ("property 2"), so the SAME conjunct (same source `pos`) was
   priced by the leaf `*Filter` AND the residual `*Filter` above the join.
   PG needs no equivalent because `distribute_restrictinfo_to_rels`
   (initsplan.c) MOVES the clause.
3. **Fix:** `Filter.PushedBelow` (plan.go) + `filterSelectivity`
   (cardinality.go). Stamped at BOTH duplicating passes (binary +
   `pushResidualQualsIntoMHJTables`). The two MOVING siblings —
   `pushOuterQualsIntoLaterals`, `pushSingleSourceFiltersIntoMHJTables` —
   were checked and correctly need no stamp; do not re-audit them.
4. **Q47 is still TIMEOUT.** `v1` is now 3 626 (was 18; PG 7 643) and Q47
   *still* plans `Nested Loop rows=1958` over `Hash Join rows=108` +
   `CTE Scan on v1 v1_lag rows=3626`. §5.17's chain was necessary, not
   sufficient. Filed as **M0127-P5.6-f-viii** with the resume point (the
   `CTE Scan on v1 rows=6` outer / missing rescan cost).
5. Useful technique, reusable: a synthetic unit-scale rebuild of the join
   returned the CORRECT number, so it could not localise the bug. What did
   was instrumenting the real server (`GOOPG_DEBUG_RESIDUAL` prints in the
   `*Filter` and `*Join` estimate arms, built to `/tmp/goopg-dbg`, run on
   :5533 over the sf05 data dir). Reach for that first next time.

Gates run: UNITS green; DS05 sweep `sweep-20260805-101345.txt` exit 0
(PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0, per-query verdicts BYTE-IDENTICAL
to the f05b5329 baseline, named TIMEOUT set still exactly {Q47}, 50/99 plan
shapes changed vs 0 previously); estimate audit
`2026-08-05-p56fvi-postfix.txt` — no new violation, ±1–4 % moves, Q18's
standing violation improved 25 182×→23 433×; commit-hook pgbench smoke ×2.

Nightly triage 20260805-014309: unchanged, both items already filed under
M-NIGHTLY and left unchecked per the banner. No new nightly run since.

Next step: per the banner (M0124 → M0125 → M0127), take
**M0127-P5.6-f-vii** — `estimateAggregate`'s `child/2` vs upstream
`estimate_num_groups` (selfuncs.c) — BEFORE -f-viii, one variable per
estimate-audit delta per doc 09 §6.

In-flight: none.

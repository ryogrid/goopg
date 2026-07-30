(idle — nothing in flight)

Loop #10 (2026-07-31) took **M0125-0036** (C3) per the banner. Landed, gated,
committed and pushed (`cae0b44d` + the gate-evidence commit on top).

`internal/planner/exists_to_any.go` (new pass, `GOOPG_EXISTS_TO_ANY=off`),
wired from `planner.Plan` between the last index-rewriting pass and
`lowerSubPlanParams`. Design
`docs/design/0125-0036-exists-to-any-hashed-subplan.md`, evidence
`analysis/m0125-0036-exists-to-any/`. goopg now has PG's
`convert_EXISTS_to_ANY`: an OR-ed, non-negated, single-equality correlated
EXISTS becomes an uncorrelated `= ANY (SubPlan n)` that the Stage-11 hash
probe builds once.

Five findings the next loop should not rediscover:
1. **The task's acceptance row was already green.** -0036 said "Q10 completes
   and matches `10|OK|0|1f18d650…`"; the loop-#9 gate already had `Q10 PASS
   35s`. The query that actually moves is **Q35, TIMEOUT 327 s → PASS 18 s**.
   -0026's acceptance rows predate -0005/-0007/-0008/-0034/-0035a — re-read
   the latest `sweep-*.txt` before treating one as a target (ledger row).
2. **Q10's oracle is 0 rows, so it cannot detect an empty value set.** The
   first version of the pass read a **stale post-MHJ column index** and
   returned 0 rows for Q35's 100 while passing Q10. Fixed by
   `resolveHostOperandIdx`; MHJ packing OID-re-sorts its output and treats a
   sublink body as opaque, so an index recorded inside one is not trustworthy
   after it (same class as M0071-0003).
3. **A silent wrong answer was found while probing and is NOT mine**: two
   hand-written OR-ed uncorrelated `IN (subquery)` sublinks under a
   SEMI-over-MHJ answer 1329 where PG says 1294 (either alone is exact).
   Filed **M0125-0042**; it outranks a timeout on severity.
4. **Q30/Q81 are NOT closed** — C3's correlated-scalar-aggregate half. Filed
   **M0125-0041** with a warning that this pass does not generalise (their
   shareable object is a grouped aggregate, not a value set).
5. Scope is bounded by NULL semantics (`IN` three-valued vs EXISTS
   two-valued), pinned by a new `boundedQualSpine` walker role — widening the
   AND/OR-only walk would make the pass WRONG, not merely incomplete.

Gates (quiet host, nightly not running): units PASS; `tpch-spotcheck.sh`
`RESULT=PASS` (Q12=2 Q13=35, 32.7 s); TPC-H plan-diff vs
`m0125-0035-c2-qual-placement` **1/22 (Q17) and the SAME 1/22 with the switch
OFF ⇒ plan-neutral on all 22** (Q17 is -0035a's); full 99-query SF0.5 gate
**PASS=90 (54 ck) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=5 SKIP=4**, one
changed cell out of 99, all 89 common PASSes identical in status AND checksum.

Per the banner the next selection is **`M0125-0037` stage (ii)**. Re-read the
banner first; it outranks this note.

Clusters: SF0.5 :65437 left UP (my binary `tmp/goopg-m0125-0036-bin`); TPC-H
:65433 stopped; :65438 (PG) was already UP and is left UP.
Filed this loop, unworked per the banner: nothing new — AI-20260731-001201-001
was already on the M-NIGHTLY list at line 461.

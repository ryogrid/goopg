# M0145-0024 — Q74's residual collapse and the `rows<=1` arm's remaining coverage (recon)

Status: complete 2026-09-23. Task: `.ralph/fix_plan.md` M0145-0024 (Kind:
recon, Parent: M0145-0012). No production code.

## Question

M0145-0012 found that removing the goopg-only `rows<=1` CTE fallback
(`GOOPG_CTE_ROWS_FALLBACK=off`) moved Q31, Q39 and Q74 into the "collapse
class": `CTE Scan` estimates of 1 row and equi-joins demoted to Nested Loop
Join Filters. Its design doc concluded that PG "does not collapse in the
first place", because `examine_simple_variable` resolves CTE output columns
through to base-table statistics. M0145-0020 ported that arm, and M0145-0020a
repaired the grouped-output factor. The 99-query A/B was never re-run after
0020a. This recon re-runs it and asks why `year_total` (a UNION ALL of
grouped selects) still collapses.

## Apparatus

TPC-DS SF0.25, current tree (`324bd3ffb`), default pipeline, all 99 queries.
Each arm's plans come from the EXPLAIN-only capture
(`scripts/tpcds-sf025-regression.sh plans`) into a scratch results dir, with
`GOOPG_PGSHAPED_DP_TRACE=1` so each engagement logs a `CTEROWSFALLBACK`
line. Engagements are isolated per run by server-log byte offset. Q74 is
timed with a `QUERIES=74` probe on each arm (fresh server, no gate stamp).
PG 18.3's plan comes from a read-only EXPLAIN on the reference cluster
(`:65438`, db `tpcds025`). PG's time is the git-tracked oracle's.

## Findings

1. **The arm now engages on one query only.** With the arm ON:
   `cte=year_total collapsed=1 body=8325` ×4 (Q74). Q31's `ws` and Q39's
   `inv` no longer collapse, so M0145-0020a's grouped-output fix removed
   them from the arm's coverage. With the arm OFF: zero engagements.
2. **The plan A/B moves exactly one query.** `PLAN-SHAPE: queries=99
   same=98 changed=1` (Q74). Its three Hash Joins over 8325-row CTE Scans
   become three Nested Loops with Join Filters over 1-row CTE Scans.
3. **PG 18.3 elects the arm-OFF plan.** PG's Q74 is
   `NL(NL(NL(t_s_firstyear, t_s_secyear), t_w_firstyear), t_w_secyear)`,
   every `CTE Scan on year_total` estimated at `rows=1`, and every
   equi-condition in a `Join Filter`. That is the same join order and
   method as goopg's arm-OFF plan. **M0145-0012's premise does not hold for
   the remaining fire:** PG does collapse on Q74.
4. **Mechanism: PG's set-operation punt.** `examine_simple_variable`
   returns without statistics when the CTE's query has `setOperations` or
   `groupingSets` (`postgres/src/backend/utils/adt/selfuncs.c:5845`).
   `year_total` is a UNION ALL, so each qual falls to the defaults:
   `sale_type = 's'` and `year = 1999` at `DEFAULT_EQ_SEL` (0.005 each),
   `year_total > 0` at `DEFAULT_INEQ_SEL` (1/3). For t\_s\_firstyear that
   is 8345 × 0.005 × 0.005 × 1/3 ≈ 0.07 rows; for t\_s\_secyear
   8345 × 0.005² ≈ 0.21 rows. Both clamp to 1. goopg reproduces this
   exactly without the arm. Neither candidate (a) nor (b) in the task
   exists: PG has no appendrel or set-op-crossing estimate channel here.
   It punts, and so does goopg.
5. **Timing, values identical (ck `1f18d650d205d71d`, 0 rows):**

   | engine / plan | Q74 |
   |---|---|
   | goopg, arm ON (Hash Joins, non-PG shape) | 2.27 s |
   | goopg, arm OFF (PG's shape) | 29.61 s |
   | PG 18.3 (the same collapsed shape, oracle) | 53.49 s |

   Removing the arm makes goopg 13x slower on Q74 than it is today, yet
   still 1.8x faster than PG on the plan PG itself runs.

## What this means for M0145-0012

The arm's entire remaining coverage is one query, where it makes goopg
diverge from PG's plan to run faster than PG. M0145-0012's removal criterion
("a collapse-class regression on removal is an automatic no-go") assumed PG
avoids the collapse. On the current tree the collapse class removal produces
is PG's own election. The recon's task-stated condition ("only if the A/B
shows no collapse-class move") is not met literally: the A/B does show one.
The move is *toward* PG, though, so the removal plan below is conditional on
an owner decision. The loop does not pick.

- **Option (a), retire the arm (plan parity):** delete the
  `cteRowsFallbackEnabled` arm (`internal/optimizer/nlicensus.go:344` and
  its consumer in `joinsearch.go`), the `GOOPG_CTE_ROWS_FALLBACK` flag and
  its `flaglabels.go` / `scripts/planner-flags.env` entries. Expected
  movement: Q74 matches PG's shape; SF0.25 sweep total rises by ~27 s. Full
  default-arm gate set plus the fire-set gate.
- **Option (b), keep the arm as a deliberate performance divergence:**
  record it as such (a named, owner-accepted non-PG plan on one query) and
  close M0145-0012 as won't-do.
- In either case, M0145-0012's design doc §"What PG actually does" must
  carry the correction for set-operation CTEs. It is added there with a
  pointer to this doc.

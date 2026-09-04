# EX3-03 step-2 (work_mem threading) — DEFERRED on spill-cost calibration

```
label: EX3-03-step2-deferred | date: 2026-09-04
plumbing: full F1/F2/F5 implementation, unit-green, saved as
  plumbing.patch (this dir) — applies to 1175c37f4
design: docs/design/executor-ex3-03-workmem/DESIGN.md (F1–F7 folded)
```

## What was built and verified

Threading `PlannerSettings` by value through set-op recursion, CTE
bodies, subquery interiors (`planSelectWithParent` + 7 sites, zero-guard
fail-closed to defaults), DML entry points (threaded, not scoped out),
F5 literals, F7 grep-gate (exactly-three production
`defaultCostParams` callers). New pins: inner-SELECT/CTE/EXISTS/funnel/
INSERT…SELECT `WorkMem` carriage (non-vacuous: fail with sources
reverted). Full optimizer suite green; H2 1GiB pins unchanged.

## Why it cannot land

The bench session carries `work_mem = 64MB`
(`bench/tpch/runtime_goopg/data/postgresql.conf:421`), so plumbing alone
moves bench plans — the design's "changed=0" assumed a default-valued
gate context. The movement is the DESIGNED 8× fix (interiors now price
the 128 MB budget, agreeing with the executor), but the newly-chosen
plans are slower:

| | pre-plumbing | post-plumbing |
|---|---|---|
| Q9 plan | hash cascade + Gather (683k) | merge + index scan (1.04M) |
| Q9 time (mid-sweep) | 10.98 s | 14.38 s (+31%) |
| Q7 time (mid-sweep) | 11.58 s | 18.33 s (+58%) |
| values | 24/24 MATCH both arms, colsigs identical | |

Same-server proof (ex303p, post-sweep): forced
`enable_mergejoin = off` hash plan runs **10.65 s** with byte-identical
values hash (`2f8fdfe8…` = the slice-3 gate hash) vs the chosen merge
plan's 14.38 s — while the model prices hash 1.10M ABOVE merge 1.04M.
The cost model ranks merge above hash; runtime disagrees by 35%.

## Resume point

Landing needs spill-cost calibration first: the model overcharges hash
spill (phantom cold-storage I/O? page-cache-blind re-read term?) and/or
undercharges merge (index-scan side at 151k, sort inputs?). PG at 64 MB
still picks hash — goopg's honest pricing must too before the threading
lands, else E1 (>1.2×) fails on Q7/Q9. Candidates: measure spill I/O
actuals on Q4/Q7/Q13 at both budgets; page-cache-aware re-read term;
merge input-sort charging. Q7 same-class (merge picked) needs its own
forced-shape confirmation at resume. Growth tripwire
(`TestExplainAnalyzeHashJoinReportsGrownBatches`) stayed green throughout.

Ledger: `take3-EX3-03-step2-blocked`.

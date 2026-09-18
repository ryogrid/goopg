# M0141-S2a-fix2r — re-apply `hashAggEntrySize` fixed-overhead currency (owner Q4: no reverts)

Status: landed 2026-09-18

## Task

`.ralph/fix_plan.md` banner item 2, `M0141-S2a-fix2r`: re-apply exactly the
`costAgg` spill-arm currency correction M0141-S2a-fix2 implemented, measured
and reverted on 2026-09-15
(`m0141-s2a-fix2-hashaggentrysize-currency-attempt.md`). The owner ruled
(Q4) that a PG-faithful change is not withdrawn because parity/category
counts got worse; fix2's revert is superseded and the code is reinstated
permanently, default-on, no flag. Any query whose categories worsen under it
is filed as its own task with `Parent: M0141-S2a-fix2r`, not grounds to
revert this one.

## Change

`internal/optimizer/cost_funcs.go`, `costAgg`'s SPILL ARM (added by R3,
`plan-parity-fix-take2/r3-hashagg-spill/DESIGN.md`):

- Guard widened from `inAvgVarBytes > 0` to `inNcols > 0 || inAvgVarBytes >
  0` — a FIXED-width aggregate input (no variable payload, but a known
  column count) now also prices a real footprint, restoring "Arm C" from the
  R120/R124/fix2 history.
- The `tupleWidth` argument to `hashAggEntrySize`, and the `pages` term's
  width, changed from bare `inAvgVarBytes` (the variable payload alone) to
  `hashsize.EntryBytes(inNcols, inAvgVarBytes)` — goopg's already-defined
  PG-equivalent full-row currency (`48*ncols + 24 + avgVar`), the SAME
  function `costSortRunWithWidth` already uses to price the SORTED rival in
  this exact contest.
- The `(0, 0)` case (neither ncols nor payload known — `addDistinctPaths`'s
  call shape) still declines: there is no PG-faithful width to substitute
  for "unknown".

**PG citation (C2):** `postgres/src/backend/executor/nodeAgg.c:1701-1730`
(`hash_agg_entry_size`, called with the aggregate input's FULL per-row width
from `outerplan->plan_width`, `nodeAgg.c:3701-3703`); the same `input_width`
feeds `cost_agg`'s `pages`/`relation_byte_size` term
(`postgres/src/backend/optimizer/path/costsize.c:2801-2802,2824`) and the
SORTED rival's `cost_tuplesort` (`costsize.c:1903`). Absorption (C3/B2):
`hashsize.EntryBytes(inNcols, inAvgVarBytes)` is the already-derived
PG-equivalent quantity for goopg's oversized Datum/map row representation
(`internal/executor/hashsize/hashsize.go`); this task applies that same
substitution to the one call site (`hashAggEntrySize`'s argument) that had
not yet received it. No new derivation — this restates M0141-S2a-fix2's own
(B2, derive-before-measure).

**Test changes**, mirroring fix2's originally-planned set:

- `cost_funcs_test.go`: the two "blind" no-spill sentinels
  (`TestCostAggHashSpillInertBelowThreshold`,
  `TestCostAggHashSpillChargedAboveThreshold`) changed from `(ncols=8,
  avgVar=0)` to `(0, 0)` — under the widened guard, `(8, 0)` now fires the
  arm, so the "no width at all" opt-out these tests rely on needs both
  inputs zeroed.
- `groupingpaths_test.go`: `TestCostAggHashedNeverChargesSpill` renamed to
  `TestCostAggHashedFixedWidthChargesSpill` and inverted — a fixed-width
  (`avgVar=0`, `ncols=16`) input engineered to overflow `work_mem` now
  MUST price a strictly larger startup/total than the in-memory baseline
  (previously it asserted no spill charge at all). Added
  `TestCostAggHashedUnknownWidthNeverChargesSpill`, pinning the ONE
  surviving opt-out (`ncols=0, avgVar=0`) at the same overflow scale.
- `partialaggpaths_test.go`: `TestPartialAggVerdictIsScaleFree`'s row range
  narrowed from `{100_000, 1_000_000, 10_000_000}` to `{100_000, 1_000_000,
  3_000_000}` — the wider guard lowers the all-`int4` fixture's spill
  threshold from "never crossed in this range" to ~4.6M groups, which the
  old top row (10M) now crosses (this bound is real, not a test
  convenience — see `TestPartialAggVerdictCrossesTheSpillThreshold`'s own
  comment, unchanged).

## Measurement

### Method

Same private-clone/private-port procedure as fix1/fix2 — never touching the
shared `:65432`/`:65433`/`:65437`/`:65438` clusters beyond read-only
`pg_basebackup`/`SELECT`.

- **TPC-H values (G table row 2, "statistics, costing or executor
  change"):** `scripts/tpch-acceptance-arm.sh`, full 22-query digest run,
  `PGSHAPED=1` (today's actual default — an unset flag is not well-defined,
  per the script's own note), private port 5583. Baseline binary built from
  a detached `git worktree add` at HEAD (`683663605`, before this task's
  diff); after-binary from the working tree with this task's diff staged.
  `tmp/tpch-acceptance-runner -diff` reported **VERDICT: PASS — every label
  matched on values, not merely on row count**, 24/24 labels (22 queries +
  Q15's 3-statement split), rows unchanged for every query. This is the
  values gate this diff is a "costing, never a cardinality/wrong-rows"
  change — confirmed directly, not merely argued.
- **TPC-H shape/category (G table row "floor capture + pg-plan-parity-diff.py"):**
  `scripts/tpch-estimate-audit-arm.sh`, `PLAN_ONLY=1 PGSHAPED=1`, same two
  binaries, private port 5582, `--ref-port 65432` (live PG capture, one per
  arm). Artefacts: `analysis/m0141/m0141-s2a-fix2r-{before,after}.{txt,plans.txt,pg.plans.txt}`.
  `shape-delta.sh` between the two goopg-only captures (no PG needed, avoids
  PG-sampling noise entirely):
  ```
  SHAPE-DELTA: queries=22 text-changed=22 shape-changed=2
  shape-changed: Q3 Q8
  ```
  Category counts computed with a **same-PG-reference control** (both
  goopg captures diffed against the `before` arm's own live PG capture, to
  strip PG-side block-sampling noise between independent captures — the
  same technique fix2's own doc used):
  ```
  BEFORE (own PG ref): PLAN-PARITY: queries=22 match=7 shapediff=14 unparsed=0 missingnode=1 error=0 timeout=0
  CATEGORIES:            join-order=13 join-method=9 scan-type=8 parameterisation=5 aggregation-strategy=9 sort-strategy=8 parallelism=0 qual-placement=3 rendering=1
  CATEGORIES-EXCL-MATCH: join-order=13 join-method=9 scan-type=8 parameterisation=5 aggregation-strategy=9 sort-strategy=8 parallelism=0 qual-placement=3 rendering=0

  AFTER (before's PG ref, same-PG control): PLAN-PARITY: queries=22 match=8 shapediff=13 unparsed=0 missingnode=1 error=0 timeout=0
  CATEGORIES:            join-order=12 join-method=9 scan-type=8 parameterisation=4 aggregation-strategy=8 sort-strategy=7 parallelism=0 qual-placement=3 rendering=1
  CATEGORIES-EXCL-MATCH: join-order=12 join-method=9 scan-type=8 parameterisation=4 aggregation-strategy=8 sort-strategy=7 parallelism=0 qual-placement=3 rendering=0
  ```
  **MATCH rose 7 -> 8 (Q3).** Every category that moved (`join-order`
  13->12, `parameterisation` 5->4, `aggregation-strategy` 9->8,
  `sort-strategy` 8->7) FELL by exactly 1; none rose. Per-query detail for
  the two shape-changed queries:
  - **Q3**: `[join-order,aggregation-strategy,sort-strategy]` (SHAPE-DIFF)
    -> `[]` (**MATCH**) — a clean win, not a lateral swap.
  - **Q8**: `[join-order,join-method,scan-type,parameterisation,
    aggregation-strategy,sort-strategy]` (6 tags) -> `[join-order,
    join-method,scan-type,aggregation-strategy,sort-strategy]` (5 tags) —
    LOSES the `parameterisation` tag (goopg's bushy spine for Q8 now
    matches PG's own bushy choice, per the estimate-audit clause-6 spine
    summary: `bushy spine chosen by: goopg 3 (Q2, Q8, Q20) | PG 3 (Q7, Q8,
    Q20)` after, vs `goopg 2 (Q2, Q20) | PG 3 (Q7, Q8, Q20)` before).
    Improves; stays SHAPE-DIFF on its remaining 5 tags.
  - **No other query's shape or category set changed at all** — confirmed
    by `shape-delta.sh`'s `shape-changed=2` (not merely inferred from
    aggregate counts).

  **This is a clean win, not the net-neutral/lateral result fix2's original
  attempt measured on 2026-09-15.** The corpus has moved since then (fix1
  landed and was measured on top of this same guard, plus
  M0141-S2a-fix1-sweep's recon and other intervening M0141 work), so the
  interaction this currency correction has with the rest of the cost model
  is different today than it was against fix2's 2026-09-15 baseline. The
  measurement above supersedes the historical one; it is not evidence the
  original measurement was wrong at the time it was taken.

- **TPC-DS (values + shape, `scripts/tpcds-sf025-regression.sh sweep`,
  required "any planner/executor change" row):** ran against the working
  tree with this task's diff staged (built fresh by the sweep script from
  the current source tree — no separate before/after binaries needed since
  the sweep's own `status-delta`/`plan-diff` channels compare this run
  against the last committed sweep snapshot, which pre-dates this task):
  ```
  SUMMARY: PASS=96 (60 ck-verified, 36 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3
  STATUS-DELTA: compared=99 verdict-changes=none runtime-moves=2 total-delta=-4.0%
  PLAN-SHAPE: queries=99 same=99 changed=0 added=0 removed=0
  ```
  **Zero plan-shape change across the full 99-query SF0.25 corpus** — the
  strongest available non-regression signal (not merely "no category
  worsened", but "nothing moved at all"). This differs from fix2's original
  2026-09-15 measurement, which found Q31 regress three categories at
  SF0.25 on that day's baseline; the same corpus-drift explanation applies
  (M0141-S1/fix1/fix1-sweep already landed on top of the guard by the time
  this task ran, and evidently already absorbed whatever interaction Q31
  had with this currency term). Measured directly rather than assumed: no
  new task is filed for TPC-DS under this Parent, because nothing moved to
  file.
- **`make ea-ratchet`**: N/A — this is a COSTING change (a cost-function
  currency substitution), not an estimate/selectivity/cardinality change;
  `hashAggEntrySize`/`hashsize.EntryBytes` feed `Cost`, never `EstimateRows`
  or any selectivity computation, so no joinrel row-estimate the ratchet
  scores can move. Confirmed by inspection of the diff (touches only
  `costAgg`'s `Cost{}` return path) and by the acceptance-arm's row-count
  column being unchanged for all 22 TPC-H queries.
- **Seam-decline census**: N/A — no join-enumeration seam is touched
  (aggregate strategy costing only).

### Category movement summary (D2 report)

| corpus | match | shape-changed | category movement |
|---|---|---|---|
| TPC-H | 7 -> 8 | 2 (Q3, Q8) | Q3: SHAPE-DIFF -> MATCH (win). Q8: loses 1 of its 6 tags (win). No category rose. |
| TPC-DS (SF0.25) | unchanged | 0 | no plan changed at all |

`Movement: yes — TPC-H MATCH 7->8 (Q3), CATEGORIES-EXCL-MATCH join-order
13->12/parameterisation 5->4/aggregation-strategy 9->8/sort-strategy 8->7,
zero categories rose`.

No query worsened in either corpus, so **no `Parent: M0141-S2a-fix2r`
follow-up task is filed** — the fix_plan item's own instruction ("File
every query whose categories worsen... as its own task") has nothing to
file against.

## Gates run

| gate | result |
|---|---|
| `go build ./...` | clean |
| `go test ./internal/optimizer/...` | PASS (full package, includes the 4 renamed/added/adjusted tests) |
| `scripts/tpch-spotcheck.sh` | PASS (Q12=2, Q13=34, canonical), gate-stamp PASS against staged code |
| `scripts/tpch-acceptance-arm.sh` (before/after diff) | PASS — 24/24 labels value-identical |
| `scripts/tpcds-sf025-regression.sh sweep` | PASS — MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plan-shape changed=0 |
| `pg-plan-parity-diff.py` (TPC-H, same-PG-reference control) | MATCH 7->8, no category regressed |
| `make ea-ratchet` | N/A — costing change, not an estimate/selectivity change (reasoned above) |

## Cleanup

Detached worktree `/tmp/wt-fix2r-baseline` (built the pre-change baseline
binary) removed after use; all binaries/arm output files under `/tmp/`
removed; private-port servers (5582, 5583) stopped by their scripts' own
`cleanup`/`EXIT` traps, verified down. No shared cluster
(`:65432`/`:65433`/`:65437`/`:65438`) was started, stopped, reset or
written beyond read-only `pg_basebackup`/`SELECT`/`EXPLAIN`.

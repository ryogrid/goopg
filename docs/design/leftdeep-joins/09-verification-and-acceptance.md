# 09 — Verification and Acceptance

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-08-02 |
| inherits | the M0126 acceptance discipline (symmetric timeouts, per-class attribution, "a documented no-go is a successful completion; an unmeasured outcome is the only failure") and the standing repo gates (units, pgbench-smoke hook, spotcheck, plan-gate, SF0.5, race-gate for concurrency-adjacent stages) |

## 1. Correctness floor (every stage, non-negotiable)

- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` green; the
  pre-commit pgbench smoke on every commit (never `--no-verify`).
- `scripts/tpch-spotcheck.sh` (Q12/Q13 canonical row counts, fresh capped
  server) on every planner/executor/codec commit.
- TPC-DS SF0.5 gate (`scripts/tpcds-sf05-regression.sh sweep`): **zero**
  row-count deltas and **zero** checksum deltas vs the git-tracked oracle.
  This is the gate that caught fusion (Q14 100 vs 200) — it is the primary
  correctness instrument for every executor stage, especially E1 (the seam
  rewrite) and S3 (spill), and runs per stage, not just at the end.
- Full regress-port suite after E1, E4, S3, S4 (codec/format-adjacent
  changes; the M0106 six-silent-regressions precedent).
- Race gate (`make race-gate`) for stages touching shared state (E3's
  build-path changes interact with `parallel_hash_build.go`; S3's temp-file
  registry is per-query but Close paths run under cancellation).
- Sibling-path audits, explicitly listed per stage in code review: E4
  (planner keys ↔ executor key encode), §2.1 of
  [06](06-hash-spill-and-memory.md) (planner nbatch ↔ executor nbatch),
  E5 (compiled ↔ interpreted evaluators).

## 2. Stage gates (advancement criteria)

Recorded per stage in [08](08-migration-and-removal.md) §2's table; the
binding numeric ones:

| gate | bar | evidence file convention |
|---|---|---|
| S1 exit | Q3, Q10, Q18, Q7 each ≤ 1.2× their R0 times (8.46 / 6.04 / 27.58 / 25.13 s; R0 = integer+MHJ pinned baseline, total 493.31 s); no other query > 1.2× vs pre-S1 HEAD | `analysis/leftdeep-joins/<date>-s1-ab.txt` |
| S3 exit | Q21 completes at SF1 under the standard cgroup cap with `work_mem` at default; a forced-spill run (`work_mem` lowered until nbatch ≥ 4 on Q3) returns byte-identical results to the no-spill run | `…-s3-spill.txt` |
| S5 exit | the full acceptance bar, §3 | `…-s5-acceptance.txt` |

## 3. The S5 acceptance bar (successor to M0126-0012's)

TPC-H SF1, fresh capped server per arm, symmetric 600 s timeouts, server age
held constant across arms (sweep-tail discipline):

1. **22/22 complete** — zero hang / OOM / timeout / row-count mismatch.
2. **Total wall time ≤ 1.2×** the better of pinned R0 (493.31 s) and a
   contemporaneous integer-arm run at the same HEAD.
3. **No single query > 2× its R0 time** — Q9 explicitly: ≤ 170.9 s
   (2 × R0's 85.46 s; the integer default arm's 58.83 s from
   `stage3-order-ab.txt` is the aspirational target beyond the bar).
4. TPC-DS SF0.5: zero deltas (as §1).
5. **No `MultiHashJoin` in any emitted plan; fusion never triggers**
   (assert via EXPLAIN sweep over both suites).
6. **Bushy-plan capability (PG-identical search):** on every searched query
   where PG 18.3's EXPLAIN shows a bushy join spine (composite ⋈ composite),
   goopg must be able to produce the same bushy tree shape — the same
   composite⋈composite pairing over the same relset partition — verified
   through the §4 parity gate's spine diff. Alternative shapes chosen on
   cost-constant or stats-fidelity grounds stay admitted under the ratchet;
   a bushy shape PG can produce that the goopg search *cannot express* is a
   hard failure (the [02](02-plan-shape-contract.md) contract is
   PG-identical shape, not a trade).

A documented no-go with attribution (§6) is an acceptable S5 outcome — the
flag then stays OFF and the bundle's planner half returns to design. The
executor stages S0–S4 stand on their own gates regardless.

## 4. The PG plan-shape parity gate (new instrument)

Once the P-PG shape contract holds ([02](02-plan-shape-contract.md) §1),
goopg's join spines are structurally comparable to PG's for the first time.
Add `scripts/pg-plan-shape-diff.sh --strict` (the existing
script is report-only): normalise both EXPLAIN outputs to a join-spine
skeleton — node type, join type, build/probe side (PG: which child is under
`Hash`; goopg: `BuildLeft`), base-rel leaf names — and diff.

- Scope: TPC-H 22 + TPC-DS 99 against PG 18.3 with matched stats
  (both sides ANALYZEd; goopg per-session ANALYZE caveat applies).
- Bar at S5: **report-mode with a pinned mismatch budget** — the count of
  mismatching spines is recorded in the evidence file and must not grow
  across subsequent commits (ratchet). A hard match-all bar is wrong while
  cost constants and stats fidelity still differ; the ratchet makes drift
  visible without blocking on estimator parity.
- **There is no `expected-bushy` category.** goopg implements PG's full
  three-phase search, bushy phase included
  ([03](03-join-search-pg-dp.md) §4.3), so a bushy spine PG chose and goopg
  cannot produce is a genuine divergence, not an accepted trade — it is
  classed per §6 (usually (b) plan shape) and fixed. Spine mismatches driven
  by cost-constant or stats fidelity stay under the ratchet as usual, and
  are re-reviewed at each ratchet update.

### 4.1 The ratchet is per-joinrel PARITY, not an absolute factor (P5.6-g-iii, landed 2026-08-05)

§5's absolute tripwire answers "is this estimate good?". The question this
milestone has to answer is "**is this estimate worse than PG's?**". On TPC-H
the two questions disagree on every joinrel the absolute bar flags — measured,
`analysis/leftdeep-joins/2026-08-05-p56giii-parity-README.md` §1:

| joinrel | goopg | PG 18.3 | absolute bar | parity bar |
|---|---|---|---|---|
| Q18 final | 42 837× over | **5 387× over** | violation | PG trips the 10³ tripwire too |
| Q21 final | 4 003× under | **4 178× under** | violation | excess **1.0×** — parity |
| Q19 final | 131× under | 1.0× | *silent* (<10³) | **violation, 126.5× worse** |

The absolute bar flagged the one joinrel where goopg matches PG exactly and
stayed silent on the one where goopg is two orders of magnitude worse. So the
bar P5.9 certifies is:

- **Unit of comparison: the joinrel, identified by its base-relation SET**
  (upstream's `RelOptInfo.relids`), reconstructed from the printed plan. Two
  engines that reach `{customer,orders}` by different join orders still built
  the same joinrel, and its ACTUAL row count is a property of the query and the
  data, not of the plan — so the misestimate factors are directly comparable.
- **Two conditions, both required** (`estimateaudit.ParityBar`): goopg's factor
  exceeds the reference's by more than `Slack` (default 10×, because this §
  already declines a match-all bar while cost constants and stats fidelity
  differ) **and** goopg's own factor exceeds `Floor` (default 100×, so a
  joinrel PG nails and goopg gets within 20× does not enter the ratchet).
- **A joinrel only one engine built is a SHAPE divergence**, counted separately
  and classed per §6 — there is nothing to compare it against. This is the
  spine-mismatch budget above, now countable per joinrel rather than per query.
- The absolute tripwire of §5 **stays**, as a coarse tripwire and as the home
  of the per-query bars (Q9's ≤10², Q21's PG-parity 5 000×).

**Baseline pinned 2026-08-05** (TPC-H 22, LEGACY planner, goopg plans replayed
from the committed P5.6-g capture, PG 18.3 reference captured live on 65432):
`parity_violations=1 shape_mismatches=67`, 21 joinrels matched, 3 ambiguous.
The single violation is Q19 `{lineitem,part}`. **`shape_mismatches` is an upper
bound**: goopg's EXPLAIN does not deduplicate repeated relation names the way
`select_rtable_names` (ruleutils.c) does (`lineitem_1`, `n1`/`n2`), so Q8, Q17
and Q18 lose their final-joinrel comparison to a rendering gap rather than a
planning difference (deferral-ledger row, 2026-08-05).

## 5. Estimate audit (class-(a) regression tripwire)

Automate the order-attribution methodology: for each TPC-H query, EXPLAIN
(without ANALYZE) at each join level vs actuals from a one-off instrumented
run; flag any joinrel whose estimate is > 10³× off. Q9's chain must show the
[04](04-cost-and-cardinality.md) §3 improvement (≤ 10²× at the final
joinrel). Runs at S5 and on any later selectivity change; output committed
under `analysis/leftdeep-joins/`.

### 5.1 The instrument (P5.6-e-i, landed 2026-08-04)

`cmd/estimate-audit` + `internal/estimateaudit`. One `EXPLAIN ANALYZE` per
query supplies BOTH sides of the comparison (the `rows=` of the cost
parenthetical and the `actual rows=` of the instrumentation), so the "one-off
instrumented run" above is not a separate build:

```
go build -o /tmp/estimate-audit ./cmd/estimate-audit
bench/tpch/setup_goopg.sh                 # cluster on 65433
/tmp/estimate-audit --label <YYYY-MM-DD>-<slug>   # all 22, ~13 min
bench/tpch/stop_goopg.sh
```

It writes `analysis/leftdeep-joins/<label>.txt` (the audit) and
`<label>.plans.txt` (the raw plans, so a later reader can re-derive any row),
and exits 1 when a joinrel is over threshold — instrument and tripwire in one
binary. The unit of audit is the JOINREL: a badly misestimated *scan* is not
a §5 violation, because §5's criterion is stated at join levels.

Three measurement conditions are forced by goopg-specific behaviour, and each
would silently corrupt the audit if left alone:

- **`--serial` (default).** goopg does not propagate worker instrumentation
  out of a `Gather` (upstream merges it in `execParallel.c`
  `ExecParallelRetrieveInstrumentation`), so in a parallel plan every node
  *below* the Gather reports estimates only. Q9 — the query this section
  states its acceptance criterion on — plans entirely below a Gather, so it
  is **unmeasurable in parallel**. The first run of the instrument recorded
  exactly that and is kept as evidence:
  `analysis/leftdeep-joins/2026-08-04-p56e-parallel-uninstrumented.txt`.
  The flag sets `max_parallel_workers_per_gather = 0`; the join tree under
  audit is the same one, minus the Gather.
- **`--warm-stats` (default).** goopg's ANALYZE statistics are
  per-connection and bare `ANALYZE;` is a no-op, so the run holds one
  stats-warmed session (explicit `ANALYZE <table>` per table) for every
  query. Without it the audit measures the no-stats planner.
- **Cumulative, not per-loop, actuals.** goopg's `actual rows=` is a raw
  cumulative counter (`instrumentedOp.stats.rowsOut`), where upstream prints
  the per-loop average. The tool consumes the printed value as-is; a reader
  who assumes PG semantics and multiplies by `loops` inflates every
  nested-loop inner node by exactly the loop count. Ledgered.

Two modes were added by P5.6-g-iii (2026-08-05) and are what make §4.1
runnable:

```
# capture the reference in the same run (PG 18.3 on 65432, bench/tpch/setup_pg.sh)
/tmp/estimate-audit --label <label> --ref-port 65432

# or apply a NEW instrument to OLD committed evidence — no 13-minute rerun
/tmp/estimate-audit --label <label> \
    --from-plans analysis/leftdeep-joins/<earlier>.plans.txt \
    --reference  analysis/leftdeep-joins/<earlier>.pg.plans.txt
```

`--from-plans` replays a committed `.plans.txt` instead of connecting: the
estimator is not consulted, so the replayed audit is bit-identical to the
original run's. A freshly captured reference is written to
`<label>.pg.plans.txt` so the comparison stays re-derivable. The reference is
captured through the same code path as goopg (same queries, same `--serial`,
ANALYZE first) — a reference measured under a different protocol would compare
two protocols rather than two planners.

### 5.2 Baseline, 2026-08-04 (pre-flip, `GOOPG_PGSHAPED_DP` OFF)

`analysis/leftdeep-joins/2026-08-04-p56e-baseline.txt`, all 22 queries, TPC-H
SF=1. Five joinrels over the 10³ tripwire, all but one an OVER-estimate:

| query | joinrel | est | actual | factor |
|---|---|---|---|---|
| Q18 | final (SEMI) | 1 756 987 324 | 70 | 2.5 × 10⁷ over |
| Q19 | final | 43 060 427 | 131 | 3.3 × 10⁵ over |
| Q3 | final | 91 875 163 | 30 401 | 3.0 × 10³ over |
| Q20 | inner (SEMI) | 6 772 315 | 2 568 | 2.6 × 10³ over |
| Q7 | inner (build=left) | 126 | 150 000 | 1.2 × 10³ **under** |

**Q9's final joinrel is 124.7× over (est 39 447 200 vs actual 316 264)** —
just outside this section's ≤ 10² bar, and the number P5.9 re-measures once
[04](04-cost-and-cardinality.md) §3's sizing is on the live path. The shape
of the miss is the compounding §3 exists to end: Q9's three outermost
joinrels all carry the SAME estimate (39 447 200) while the actual collapses
from 5 997 241 to 316 264 across them — two joins that cost nothing in the
estimate.

The violations split into two class-(a) causes, both filed as P5.6-e-ii and
neither fixed here — §6 forbids a constant moving without its class
diagnosis, and these need the diagnosis first:

- **A SEMI/ANTI joinrel is priced at its outer input verbatim.** Q18's final
  SEMI carries the identical estimate to the join beneath it
  (1 756 987 324), against 70 actual rows: the match fraction is not applied
  at all, where `calc_joinrel_size_estimate`'s JOIN_SEMI arm (costsize.c)
  scales the outer's rows by the semi-join selectivity. Q20's inner SEMI
  (2.6 × 10³ over) and Q22's ANTI final (643×, under the tripwire) share the
  shape. Note that Q18's outer is *itself* 293× over — the
  `lineitem ⋈ orders` FK equality priced at 1.76 × 10⁹ against 5 997 241
  actual, which is the eqjoinsel/FK-superkey miss P5.6-a…-c reproduce
  upstream's answer for; the SEMI defect stacks on top of it rather than
  causing it.
- **A joinrel's non-equi restriction contributes no selectivity.** Q19's
  final joinrel is a plain INNER over two *unfiltered* scans (5 997 241 ×
  200 000) whose entire WHERE is one three-branch OR over `part` and
  `lineitem` columns; the plan shows only the `Hash Cond`, and the estimate
  (4.3 × 10⁷) credits the OR nothing, against 131 actual rows. Q3's final
  (3.0 × 10³ over) is the same omission over the three-conjunct `Filter:`
  the plan re-applies at the join.

Two instrumentation gaps the run surfaced, both ledgered and neither fixed
here: the Gather gap above, and Q11's `InitPlan`/`SubPlan` joins, which
report no `actual rows=` even in serial mode.

### 5.3 Both causes closed, 2026-08-04 (P5.6-e-ii)

`analysis/leftdeep-joins/2026-08-04-p56eii.txt`, same instrument and same run
conditions, LEGACY planner still (`GOOPG_PGSHAPED_DP` OFF) — only
`estimateJoin` changed. Provenance and the full before/after table:
`2026-08-04-p56eii-README.md`.

What landed, both in `internal/planner/cardinality.go`:

- **SEMI/ANTI are sized from the OUTER.** `estimateJoin` gained the arms
  `calc_joinrel_size_estimate` has: `outer_rows · jselec` for JOIN_SEMI and
  `outer_rows · (1 − jselec)` for JOIN_ANTI, with the match fraction from
  `eqjoinsel_semi`'s no-MCV branch — `nd1 ≤ nd2 → 1.0`, else `nd2/nd1`, and
  0.5 when either side is a default. `nd2` carries upstream's asymmetric
  clamp to the inner relation's row count (the only pathway by which an
  inner-side restriction reaches a semi/anti estimate; clamping `nd1` too
  would double-count the outer's own restrictions).
- **The non-equi restriction is priced.** The conjuncts of `Predicate` that
  `HashKeys` does not already answer are run through `clauseSelectivity`,
  which required `columnStatsForChild` to resolve a column THROUGH a join
  (`Predicate` is written in the merged left‖right space) and to remap
  through a `Project`'s target list, as its ndistinct twin already did.
  Only conjuncts referencing BOTH sides count: a single-sided conjunct is a
  baserestrictinfo upstream and is already priced into the component rel's
  size, even though goopg also leaves a copy on the join for the executor.

| query | joinrel | §5.2 baseline | now |
|---|---|---|---|
| Q19 | final | 328 705× over | 13.1× under |
| Q20 | final (SEMI) | 891× over | 9.5× under |
| Q21 | final (ANTI) | 499× over | 9.7× under |
| Q22 | final (ANTI) | 643× over | 1.8× over |
| Q4 | final (SEMI) | 485× over | 7.3× over |
| Q18 | final (SEMI) | 2.5 × 10⁷ over | 1.26 × 10⁷ over |
| Q9 | final | 124.7× over | 124.7× over |

Five joinrels remain over threshold, one fewer than the baseline and with no
new ones. Q18, Q3 and Q20's inner SEMI all still fail for a cause §5.2 named
but did not own: their OUTER input is 293× / 5.8× / 86× over on its own.

**Why the third cause is NOT fixed here.** The first cut also corrected the
join-key ndistinct lookup — `RightKey.Index` is a MERGED index and was being
resolved against the right child's own schema, so the right side of an
equi-join never entered `max(nd)` — and let both column lookups resolve
through a join, which is what `examine_variable` does. Measured
(`2026-08-04-p56eii-postfix.txt`), every joinrel it touched got more accurate
and the queries above them got far worse: **Q9's final 124.7× → 176 424×
over, Q8's final 1.9× under → 2 171× over**, with Q9's two deepest joins
landing exactly on their actuals. The missing `nd` was cancelling two
pre-existing defects — ANALYZE storing a SAMPLE distinct count with no
Haas–Stokes scale-up (a 1.5 M-row unique key reads as ≈ 30 000), and the
M0126-0010 `max(|l|,|r|)` cap firing only on the nd-unavailable path, so
supplying `nd` also removes the bound. Per §6 that is a class-(a) diagnosis
with its own mechanism, filed as **P5.6-e-iii**: de-saturate ANALYZE, then
land the coordinate correction with it. The rejected run is committed
because the conclusion is only defensible with its numbers present.

### 5.4 The third cause closed, 2026-08-04 (P5.6-e-iii)

`analysis/leftdeep-joins/2026-08-04-p56eiii.txt`, same instrument and same run
conditions, LEGACY planner still (`GOOPG_PGSHAPED_DP` OFF).

What landed:

- **ANALYZE de-saturated** (`internal/executor/operators_analyze.go`,
  `ndistinctEstimate`). goopg stored the SAMPLE's distinct count as the
  table's ndistinct, so with the default statistics target a 1.5 M-row unique
  key read as ≈ 30 000. `ndistinctEstimate` mirrors `compute_scalar_stats`'s
  ndistinct block (analyze.c:2588-2648) branch for branch: the
  `nmultiple == 0` unique-column arm, the `nmultiple == ndistinct`
  whole-value-set arm, and Haas–Stokes Duj1 `n·d/(n − f1 + f1·n/N)` clamped to
  `[d, N]`. `ColumnStats.NDistinct` and `NDistinctFrac` are now two renderings
  of that one estimate, and `StaDistinct()` picks between them with upstream's
  own 10 %-of-rows rule instead of always preferring the fraction.
- **The join keys resolve in the merged coordinate space**
  (`internal/planner/cardinality.go`). `estimateJoin`'s equi arm reads the
  right key through `rightKeyNDistinct`, the same left-width shift the
  SEMI/ANTI path has used since §5.3, and `columnNDistinctForChild` gained the
  `*Join` arm its `columnStatsForChild` twin already had. The two column
  lookups no longer diverge; the divergence tripwire test is retired.
- **The M0126-0010 cap was re-examined and deliberately left alone** — it
  still fires only on the nd-unavailable fallback path. It is a non-PG
  heuristic standing in for upstream's FK-driven `fkselec`
  (`get_foreign_key_join_selectivity`), and a genuine many-to-many join
  legitimately exceeds `max(|l|,|r|)`. What made it look load-bearing in §5.3
  was the saturated `nd` it was compensating for.

Violations: **5 → 2.** Q3, Q7 and Q20's inner SEMI are closed outright; Q18's
final SEMI improved by 293× and still violates.

| query | joinrel | §5.3 | now |
|---|---|---|---|
| Q3 | final | 2 967× over | 10.4× over |
| Q5 | d4 | 447.7× over | 1.5× over |
| Q7 | d4 | 1 190× under | 1.4× over |
| Q8 | d3 | 20.7× over | 1.3× over |
| Q17 | final | 7.5× over | 1.0× under |
| Q18 | final (SEMI) | 1.26 × 10⁷ over | 42 837× over |
| Q20 | inner SEMI | 1 311× over | 129× over |
| Q16 | final (ANTI) | 16.0× under | 85.1× under |
| Q19 | final | 13.1× under | 131× under |
| Q21 | final (ANTI) | 9.7× under | 4 003× under |

Two regressions came out of it, both filed rather than papered over:

- **SEMI/ANTI collapse to `est=1`** (Q21, Q19, Q16, and Q21's inner SEMI). A
  truthful `nd` makes `eqjoinsel_semi`'s `nd1 ≤ nd2` test succeed, the match
  fraction becomes exactly 1.0, and JOIN_ANTI's `outer · (1 − jselec)` floors
  at `clamp_row_est`'s 1. Upstream reaches 1.0 far less often because it takes
  the MCV branch of `eqjoinsel_semi` first, which goopg has no join-level MCV
  list for.
- **Q9 is UNMEASURED** — it exceeded the audit's 150 s timeout where §5.3
  measured it at 93.9 s. Attributed per §6 before anything landed: the
  ANALYZE half is the whole cause (reverting only the planner half reproduces
  the identical plan shape, `rows=160406045` vs `159924827`). The mechanism is
  a class-(a) defect this change UNMASKED rather than introduced — Q9's
  `l_suppkey = ps_suppkey AND l_partkey = ps_partkey` is a TWO-pair equi-join
  that `estimateJoin` prices on ONE pair while excluding BOTH from the
  residual, so it reads `6 M · 800 k / 10 000 = 481 M` and the DP puts it
  under the `part` filter instead of above it. Pricing every pair the way
  `clauselist_selectivity` does swings it the other way (≈ 2 rows) without
  upstream's FK selectivity to bound it, so the fix is
  `get_foreign_key_join_selectivity`, not a constant. **P5.9 cannot certify
  Q9's ≤ 10² bar until this lands.**

Q9's deepest joins are now exact (`5 997 241` on both), which is the evidence
that the ndistinct itself is right and the remaining error is the multi-key
pricing above it.

### 5.5 The multi-key cause closed, 2026-08-04 (P5.6-f)

`analysis/leftdeep-joins/2026-08-04-p56f.txt`, LEGACY planner
(`GOOPG_PGSHAPED_DP` OFF) as before — but **on a different schema from every
audit before it**, so it is diffed against a re-baseline rather than against
§5.4.

**Step 0: the baseline moved.** M0127-P5.6-f-pre proved goopg's `tpch` database
carried 0 user indexes against the PG 18.3 reference's 16, and its fix is
forward-only. Since half 2 of P5.6-f reads a UNIQUE index, the eight UNIQUE
indexes the reference declares were re-created before anything was measured
(`partsupp_pk (ps_partkey, ps_suppkey)` is the one Q9 turns on), and confirmed
to survive a restart — the first end-to-end validation of the P5.6-f-pre fix on
a real cluster. The eight NON-unique FK indexes were deliberately left out:
they carry no uniqueness evidence and would have moved plan SHAPE inside a
cardinality measurement, which §6 forbids.
`2026-08-04-p56f-baseline-idx.txt` is that cluster with the OLD planner, and it
reports the identical two violations and the identical UNMEASURED Q9 as §5.4 —
so the index creation contributed nothing to the delta below.

What landed (`internal/planner/joinkeyproof.go`, `cardinality.go`):

- **Every equi-pair is priced.** `estimateJoin` charged ONE pair while
  `Join.Residual()` excluded them all, so Q9's second equated column vanished
  from the estimate entirely. The pair list is `joinEquiPairs`, and the same
  list is now what `joinResidualSelectivity` excludes — the two can no longer
  disagree. It is derived from `Predicate` when `Join.HashKeys` is empty, which
  is the state EVERY estimate taken during join-order search sees
  (`fillJoinHashKeys` is one late pass at the tail of `Plan()`).
- **`get_foreign_key_join_selectivity` (costsize.c:5651) for the legacy
  estimator.** The same algorithm as `superkeyJoinSelectivity`
  (joinrelsize.go), arm for arm, over `*Join` nodes instead of `RelOptInfo`s:
  the covered pairs are removed and ONE `1/raw_ntuples` substituted for the key
  as a whole, largest divisor first, whole key or nothing. Half 1 alone prices
  Q9's composite key as `1/(200 000 · 10 000)` — 2 400 rows against 5 997 241
  actual, a bigger error than the defect and in the other direction. This is
  why the item always said the halves must land together.
- **The evidence reaches a catalog-free estimator by being stamped.** A table's
  indexes live only in the catalog, and `estimateJoin` takes a bare `Node`
  (EXPLAIN in the executor calls it too). `SeqScan.UniqueKeys` /
  `IndexScan.UniqueKeys` are stamped at the sites that already stamp
  `SmallDim`, through the planner's own `cat` — which also settles the dbOid
  hazard of cost-model/14 §2 that a bare `InMemory` lookup would reintroduce.
- **The proof resolves each end independently.** Requiring BOTH ends of a pair
  to reach a base relation was the mechanism's first shape and was measured
  wrong: Q20's `partsupp ⋈ (SELECT … GROUP BY …)` has a HashAggregate on one
  side that no resolver sees through, the proof went unmade, and the joinrel
  read 283 against 236 624. The uniqueness argument only ever concerns the KEY
  side. Only the declared-FK arm needs the far end, because it has to name the
  referenced parent.

Violations: **2 → 2** — Q18's final SEMI (42 837× over) and Q21's final ANTI
(4 003× under), both owned by P5.6-g and both untouched. **No joinrel got
worse.**

| query | joinrel | re-baseline | now |
|---|---|---|---|
| Q9 | `lineitem ⋈ partsupp` | 479 779 280 (80× over) | **5 997 241 — exact** |
| Q20 | d3 INNER (`partsupp ⋈ agg`) | 12.2× over | 3.1× over |
| Q20 | d2 SEMI | 125.0× over | 31.7× over |
| Q5 | d6 INNER | 5 996 041 | 5 997 241 — exact |

Everything else moved by under 2 %, which is the residual-accounting half.

**Q9 is measurable again — at 291.8 s, not within the audit's 150 s.** The
sequence is 93.9 s (§5.3) → unmeasured (§5.4) → 291.8 s. Its cardinality defect
is closed and the remaining error is class (b), plan shape: all three hash
joins carry the full 5 997 241 rows because the `part` filter (5.3 % selective)
is applied ABOVE them, where PG filters `part` first and index-scans lineitem
through `lineitem_part_supp_fkidx`.

**Why an exact estimate did not move the shape, and what owns that.** The
legacy planner does not size its join-order search with `estimateJoin` at all.
`estimateJoinCost` (bushy.go:1257) has its own cardinality arm, and its
PRODUCTION branch — the integer DP, `costDrivenJoinOrder` OFF — computes `ndv`
as the maximum NDistinct over *every column of the edge's two tables*, ignoring
the join key. The multi-edge enumeration and superkey probe that do exist there
(`crossEdgesBetween` + `uniqueNoFanoutRawCount`, whose FK arm additionally
divides by the CHILD's count where upstream divides by the parent's) sit in the
`costDrivenJoinOrder` branch that M0126 closed as a no-go and left OFF. So
P5.6-f reaches every printed estimate and every post-search decision, and the
search itself not at all. Filed as **P5.6-f-ii**; P5.9 still cannot certify
Q9's ≤ 10² runtime bar, but it can now certify its cardinality.

### 5.6 The search reached, 2026-08-05 (P5.6-f-ii)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56fii{,-halfway}.txt`,
`-README.md`. Same cluster and schema as §5.5, so the diff carries no schema
variable.

§5.5 named one cause — the integer DP's `ndv` being a table-wide maximum. It was
real and it was not sufficient. Instrumenting the DP (rather than reading it)
surfaced two more, both of which had been latent for as long as the accurate
path existed and neither of which any unit test could have caught, because both
concern how a coordinate is *interpreted* rather than what is computed from it:

1. **A `joinEdge`'s key expressions are in GLOBAL FROM-list coordinates.**
   `accurateKeyDistinct` indexed `Stats.Columns` with `ColumnRef.Index`, so on
   Q5 it read out of range for *every* join key (`c_nationkey` is `Index: 16`
   against 8 columns) and returned 0 — silently handing the search back to the
   table-wide maximum it was supposed to replace. On `nation`, `Index: 3` was in
   range and answered with `n_comment`'s distinct count for `n_nationkey`.
   `edgeColName`'s `cr.Name` fallback masked the whole thing by still returning
   the right *name*. Third instance of the P5.6-e-ii `RightKey` class.
2. **`accurateKeyDistinct` bypassed `StaDistinct()`**, multiplying
   `NDistinctFrac × RowCount` unconditionally — the branch P5.6-e-iii created
   `StaDistinct()` to arbitrate (`get_variable_numdistinct`, selfuncs.c).

**The half-fix is recorded because it is the argument for the whole one.**
Landing only the superkey proof made Q5's `lineitem ⋈ supplier` truthful
(39 981 → 5 997 241) while its rival `customer ⋈ supplier` kept reading 10 000
against a real 60 000 000; the DP took the cartesian product and Q5 went 65.9 s →
over the 150 s timeout. A search selects on *comparisons*, so a partially
truthful estimator is not a partial fix — it is a new class-(b) defect. This is
the §6 protocol's own logic applied to the estimator: the class was (b) both
before and after, and only a uniform divisor closes it.

Landed together: one divisor for both search modes (`graphJoinKeyDivisor`, the
graph-space twin of P5.6-f's `superkeyJoinEstimate`, plus the §4 per-clause
product for the unproven remainder); name-based column resolution;
`StaDistinct()` rendering. `uniqueNoFanoutRawCount` is deleted — its FK arm
divided by the child's raw count where costsize.c:5847 divides by the parent's.

Result: violations **2 → 2** (Q18, Q21 — both P5.6-g's), **no joinrel worse**,
and **Q9 measured for the first time**: UNMEASURED (>150 s) → 6.3× over, inside
its `≤100×` override. Runtime, which is what class (b) is judged on: **zero
regressions across 22 queries**, Q5 65.9 s → 17.1 s, Q7 38.9 → 27.2, Q21 125.1 →
90.5, common-measured total 546.8 s → 445.1 s (**0.81×**), and Q9 291.8 s →
16.6 s off-instrument. Plan-gate diverged 19/22 against the old baseline — the
intended outcome — and is 22/22 MATCH against the re-pinned
`plan_snapshots/m0127-p56fii.txt`.

**P5.9 can now certify Q9's ≤ 10² runtime bar as well as its cardinality.**

### 5.7 The semi-join arms completed, and what the two violations actually are, 2026-08-05 (P5.6-g)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56g.txt`, `-README.md`. Same
cluster and schema as §5.5/§5.6.

Landed in `internal/planner/cardinality.go` (+ `joinkeyproof.go` publishing the
whole statistics row): `eqjoinsel_semi`'s **MCV arm** — match the two MCV lists,
take the matched frequency mass as exact, and run the nd heuristic only on the
uncertain remainder with both distinct counts discounted by the match count —
and the **`(1 - nullfrac1)` factor** on every branch including the 0.5 punt.
`CLAMP_PROBABILITY` came with them. 13 tests.

**Result: a measured no-op on TPC-H.** Violations 2 → 2, both bit-identical;
every other joinrel moved under 5 %, in both directions, on INNER joins this
change cannot reach — ANALYZE's sampling noise between runs, which is now the
documented noise floor for reading this instrument.

**A no-op is indistinguishable from a broken wire until you separate them.**
Both halves were probed on a throwaway cluster with the same binary, varying
only the data: a semi-join whose inner has an MCV list estimates **5 010 against
an actual 5 010**, and the identical join whose inner ANALYZE gave no MCV list
(uniform data — upstream discards a list whose values are not more common than
average) estimates **20**. A 25 %-NULL outer key estimates **750 against an
actual 750** where it previously said 1 000. The mechanism works; TPC-H's
near-uniform surrogate keys and NOT NULL join columns cannot exercise it.

**The finding that reframes the milestone: neither violation belongs to this
item, and one of them is not a defect.** Both were re-measured against the PG
18.3 reference (port 65432, same dataset), which nobody had done:

- **Q21's ANTI — PG 18.3 estimates `rows=1` too**, against the same actual of
  4 003. `neqjoinsel` (selfuncs.c) does not price a `<>` clause through
  `eqjoinsel` for `JOIN_SEMI`/`JOIN_ANTI` at all; it returns `1 - nullfrac` by
  documented design. The eq clause is a self-join on `lineitem.l_orderkey`, so
  `nd1 = nd2` and every branch — including the new MCV one, whose
  `uncertainfrac` is then exactly 1.0 — returns 1.0, and `outer · (1 - jselec)`
  floors at one row in both engines. **Closing this would mean diverging from
  PG.** It is an audit-override, not an estimator task.
- **Q18's SEMI is a real divergence of a different mechanism.** PG never plans
  it as a semi-join: `GROUP BY l_orderkey` makes the subquery unique on the
  join key, so PG dedups to a plain inner join and estimates 117 159 (1 674×
  over). goopg keeps the SEMI and lands on the **0.5 punt** — `5 997 241 × 0.5`
  exactly — because `resolveBaseColumn` has no `*HashAggregate` arm and the
  inner's 1 210 559-row estimate is far above `defaultNumDistinct`, so the
  clamp never rescues `nd2`. Neither new arm participates.

**Consequence for §4 and P5.9: at 1 674× PG itself trips this audit's 1 000×
tripwire on Q18.** An absolute factor is the wrong bar for a PG-parity
milestone; the ratchet P5.9 certifies should be per-joinrel parity against the
reference, with the absolute tripwire kept only as a coarse tripwire. Filed as
**P5.6-g-ii** (the `*HashAggregate` arm and Q18's dedup-to-inner shape) and
**P5.6-g-iii** (the Q21 override + the parity bar).

DS05 could not run: the gate self-refuses while the nightly CI batch holds the
host (`FATAL: the nightly CI batch is running`), and the batch was mid-run with
a wedged testport stage for this loop's whole duration. Carried, with the exact
command, in `.ralph/working_set.md`.

### 5.8 The instrument corrected, 2026-08-05 (P5.6-g-iii)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56giii-parity.txt` (+
`.pg.plans.txt`, `-README.md`). **No estimator code changed**: the goopg side is
the committed P5.6-g capture replayed with `--from-plans`, so every goopg number
is bit-identical to §5.7's. The only new measurement is the PG 18.3 reference.

Landed: Q21's per-query bar beside Q9's (`estimateaudit.Q21AntiJoinMax`, 5 000×,
with its justification rendered into the artifact rather than left as a bare
number), and §4.1's per-joinrel parity gate (`internal/estimateaudit/parity.go`).
Absolute violations on TPC-H **2 → 1**: Q18 stays, Q21 is measured parity
(excess 1.0× against PG's own 4 178×).

Two findings the parity column produced on its first run:

- **Q19 `{lineitem,part}` is the only estimator defect TPC-H can prove**:
  goopg est 1 vs actual 131, PG est 116 vs actual 112 — 126.5× worse than the
  reference, and *invisible* to the absolute tripwire at 131× < 10³. Neither
  scan carries a filter, so Q19's three OR'd conjunction groups all ride as the
  join's residual and the whole predicate is priced at the join level, landing
  on the 1-row clamp. Filed as **P5.6-g-iv**.
- **goopg's EXPLAIN cannot name a repeated relation.** Upstream deduplicates
  printed relation names (`select_rtable_names`, ruleutils.c): a subquery's
  second scan of `lineitem` prints as `lineitem_1`, Q8's two `nation` RTEs as
  `n1`/`n2`. goopg prints the bare name, so two range-table entries are
  indistinguishable in the text and Q8/Q17/Q18 lose their final-joinrel
  comparison to a rendering gap. The gate reports the collision (`~` marker,
  `N ambiguous`) instead of silently picking one; the fix is in the renderer.
  Ledgered 2026-08-05.

Watch list (>10× the reference but under the 100× floor, so not yet ratcheted):
Q16 `{part,partsupp,supplier}` 84.9× vs 2.0×, Q20 `{lineitem,part,partsupp}`
32.1× vs 1.1×, Q14 `{lineitem,part}` 12.4× vs 1.0×.

### 5.9 The Q19 defect closed — a missing preprocessing pass, 2026-08-05 (P5.6-g-iv)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56giv.txt` (+ `.plans.txt`,
`-README.md`). Q12 and Q19 measured; see "why only two queries" below.

§5.8 predicted the defect was in how the residual was priced. It was one level
earlier than that: **goopg never ran PG's `canonicalize_qual`**
(`process_duplicate_ors`, prepqual.c), so the OR was never reduced before the
qual was distributed.

Q19's whole WHERE is `(A ∧ …) ∨ (A ∧ …) ∨ (A ∧ …)` where `A` is the join clause
`p_partkey = l_partkey`, repeated verbatim in every arm. Upstream hoists `A` —
along with `l_shipmode IN (…)`, `l_shipinstruct = '…'` and `p_size >= 1`, which
are also in all three arms — leaving `A ∧ (rest₁ ∨ rest₂ ∨ rest₃)`. goopg did
not, with three consequences that compounded:

1. **The join clause was priced twice.** Once as the equi-join key
   (`l·r/nd` = 1/200 000), and again inside each OR arm, where
   `eqOpSelectivity` sees two columns and no constant and returns
   DEFAULT_EQ_SEL. Three arms at ~5·10⁻⁹ apiece drove the product to ~0.1 rows,
   i.e. the 1-row clamp.
2. **Three real restrictions were priced nowhere.** The single-relation
   conjuncts common to all arms stayed trapped inside the OR, so neither scan
   could be filtered and `joinResidualSelectivity` — which correctly skips
   single-sided conjuncts as "already priced at the scan" — had nothing to skip
   and nothing had been priced.
3. **M0058-0004 had already computed the intersection and thrown it away.**
   `commonEquijoinsAcrossOr` (joinorder.go) extracts exactly `A` so the join
   EDGE exists, which is why goopg emitted a Hash Join at all; the qual itself
   stayed opaque. That workaround is the same computation as
   `process_duplicate_ors`, applied to one consumer instead of to the qual.

Landed: `internal/planner/qual_canonical.go` (`canonicalizeQual`, upstream's
`find_duplicate_ors` over goopg's binary AND/OR tree), applied in `planSelect`
at upstream's own placement — after parse analysis, before the qual is
distributed. The parse tree is **not** mutated; it is shared with the view/rule
deparsers, which must keep rendering the query as written.

The equality test is `strictParserExprKey` (exprkey.go), not `parserExprKey`.
That distinction is load-bearing: `parserExprKey` deliberately drops a
ColumnRef's table qualifier (M0097-0003), under which `a.x = 1` and `b.x = 1`
compare equal, and hoisting one of them out of an OR rewrites a qual that admits
rows from either table into one that demands both. Pinned by
`TestCanonicalizeQualDoesNotHoistAcrossTableQualifiers`.

Result — Q19 `{lineitem, part}`:

| | est | actual | ratio | PG 18.3 | excess |
|---|---|---|---|---|---|
| before (§5.8) | 1 | 131 | 131.0× under | 1.0× | **126.5×** |
| after | 309 | 131 | 2.4× over | 1.0× | **2.3×** |

`RATCHET parity_violations=0 shape_mismatches=0`. The plan now shows PG's own
Q19 shape: `Filter: (l_shipmode = ANY …) AND (l_shipinstruct = …)` on the
lineitem scan, `Filter: (p_size >= 1)` on part, the reduced OR at the join.

**Why only Q12 and Q19 were measured.** This pass can only change a query whose
WHERE contains an OR; on every other input `canonicalizeQual` returns its
argument unchanged. Exactly three of the 22 TPC-H texts contain `or`, and Q15's
is `CREATE OR REPLACE VIEW`. Q12 is therefore the control, and it is
bit-identical to §5.7's baseline (1.5× / excess 1.3×; est 45 793 → 46 222 is
ANALYZE sampling noise between sessions) — its OR is a two-arm disjunction of
bare equalities with no common conjunct, so it correctly finds no winners. The
19 OR-free queries were not re-run and are not claimed to have moved; the claim
is that the pass is a structural no-op on them.

**What is deliberately not reproduced.** `find_duplicate_ors` also drops
constant TRUE/FALSE/NULL inputs as it recurses, with different rules for WHERE
quals and CHECK constraints. goopg folds constants in `FoldConstants`, and
duplicating that logic here would give two passes an opportunity to disagree
about three-valued logic. The pass is also applied to SELECT only, not to
UPDATE/DELETE quals (planner.go:9167ff). Both ledgered 2026-08-05.

**Not yet discharged:** the TPC-DS SF0.5 gate. TPC-DS has far more OR-bearing
queries than TPC-H, so it — not TPC-H — is where this pass's plan-shape blast
radius actually gets measured. It self-refuses while the nightly CI batch holds
the host, and is carried on `M0127-P5.6-g-i` together with P5.6-g's own
undischarged sweep.

## 6. Attribution protocol for regressions (inherited, binding)

Any per-query regression during S1–S5 gets classed before any fix lands:

- **(a) cardinality** — estimate wrong → fix in [04](04-cost-and-cardinality.md)
  §3 mechanisms only;
- **(b) plan shape** — estimate right, order/method wrong → enumerator or
  cost-function bug, cite the PG analogue;
- **(c) cost-model realism** — plan matches intent, runtime disagrees →
  missing cost term (nbatch, seam) with `costsize.c` citation;
- **(d) executor** — same plan, slower run → seam/allocation regression;
  pprof before patch.

No constant may change without its class diagnosis in the commit message —
the "no unfalsifiable tuning" rule made procedural.

## 7. Microbenchmarks (executor stages)

`go test -bench` fixtures under `internal/executor/`:

- seam benchmark: 3-level cascade, 1M synthetic probe rows — allocs/op must
  be 0 in steady state after E1+E2 (the assertion that kills the pool
  round-trip class);
- build benchmark: single-pass vs two-pass (guards E3 against regression);
- key benchmark: composite int64 pair vs string keys (E4);
- spill benchmark: nbatch ∈ {1, 4, 16} at fixed input size (S3 overhead
  curve; nbatch=1 must be within noise of pre-S3).

Benchmarks are tripwires with recorded baselines in the evidence directory,
not CI gates (WSL2 noise); regressions > 20 % require investigation before
the stage advances.

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

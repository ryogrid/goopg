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

A documented no-go with attribution (§6) is an acceptable S5 outcome — the
flag then stays OFF and the bundle's planner half returns to design. The
executor stages S0–S4 stand on their own gates regardless.

## 4. The PG plan-shape parity gate (new instrument)

Once P-LD holds, goopg's join spines are structurally comparable to PG's for
the first time. Add `scripts/pg-plan-shape-diff.sh --strict` (the existing
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
- Divergences PG-explains (bushy spines PG chose where we cannot) are
  annotated `expected-bushy` in the pin list — they are the measured cost of
  the [02](02-plan-shape-contract.md) trade and reviewed at each ratchet
  update.

## 5. Estimate audit (class-(a) regression tripwire)

Automate the order-attribution methodology: for each TPC-H query, EXPLAIN
(without ANALYZE) at each join level vs actuals from a one-off instrumented
run; flag any joinrel whose estimate is > 10³× off. Q9's chain must show the
[04](04-cost-and-cardinality.md) §3 improvement (≤ 10²× at the final
joinrel). Runs at S5 and on any later selectivity change; output committed
under `analysis/leftdeep-joins/`.

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

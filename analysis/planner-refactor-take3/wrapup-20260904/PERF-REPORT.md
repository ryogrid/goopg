# Planner + Executor Refactor — Performance Report (2026-09-04)

Branch: `plan-narrowing-and-etc`. Workstream: take3 TODO_EXECUTOR.md
executor items (+ take2/take3 planner dependencies as needed: P4-01
slices 1–3). Close-out is owner-directed after EX3-02 Cut 2; deferred
items carry resume points (ledger `take3-wrapup-deferred`) and are NOT
claimed here.

## Method and honesty caveats

- Every landed commit gated: TPC-H SF=1 values 24/24 MATCH
  (`tpch-runner --digest` + `-diff`), plan-gate 22/22 MATCH, TPC-DS SF0.5
  sweep PASS=95 MISMATCH=0, plus unit suites (never `-count=1` in a gate)
  and `GOOPG_ASSERT_ROW_SHAPE=1` on narrowing commits.
- Regime per EX0-02 protocol: fresh server per arm via
  `scripts/goopg-test-run.sh` + cgroup cap, GOGC=100/GOMEMLIMIT=12GiB,
  S-cold serial, `work_mem` 64 MB, port 65433 `tpch@tpch` (unless noted).
- Alloc arm: pprof heap `alloc_space -base` before/after, endpoint
  verified owned by the measured server PID (a stale `:6060` holder burned
  one attempt; affected profiles discarded).
- CAVEATS (read before quoting any number): live timings are SINGLE
  SAMPLES unless stated; fresh-server Q9 spans 13.9–20.1 s across runs
  (±20% band — page cache, GC phase), so only deltas far outside the band
  are claimed as wins. No full-suite TOTAL A/B was run this session; all
  timing claims are per-witness. Unit alloc figures (`AllocsPerRun`) are
  exact and are the hard gate for the dense-build cuts.

## Landed work and measured effects

### P4-01 PathTarget + projection (planner; take3 TODO.md dependency)

Slices 1–3 landed (`588aa5fb5`, `8458b7552`, `1d804ae02`;
design `docs/design/planner-p4-01-target/DESIGN.md`, reviewed).

| stage | Q9 serial | widths (4 join levels) | witness Batches |
|---|---|---|---|
| rev-4 baseline (2026-09-03) | 63.8 s | 1098 / 1642 / 2090 / 3164 B | 8 |
| HEAD re-take (NARROW_BUILD default-ON) | 14.66 s | 1096 / 896 / 896 / 710 B | 2 |
| Slice 3 (per-joinrel keep-sets, 10→7 cols) | **10.80 s** | 776 / 640 / 736 / 582 B | **1** |

Model currency: DPPATH join.hash 866417→669589, crossing the 754717 bar;
narrowed inner bytes ≈115.6 MB (≈100-class). Three-budget value hashes
(64/4/512 MB) identical. Deferred follow-ups: (a) merge/NL input policy,
(b) scan-node application, (c) upper targets.

### EX1-04 Cut 0 — alloc arm on the P4-01 flip (measurement only)

Q9 20.14→13.88 s, alloc window 9.43→8.52 GB (−10%), values identical —
half of EX1-04's goal met by the planner flip with zero executor risk.
Cut 1 (test-only): 5 poison tests on the Project shape, 9/9 green.
Sort/merged halves still blocked on P4-01 (c)/(a); general fix
needs-design-revision (no second truncation alongside the Project).

### EX2-01 retention audit → EX2-02a drain fold → EX2-02c gather transfer

- Audit: 45 sites (18 cloneRowOwned + 14 MaterializeArena + 13
  acquireRow), reviewed 8/8 families exact, rework completed.
- EX2-02a (`drainRowsCtx`/`drainRowsCtxCTID` make+copy → single
  `cloneRowOwned`): Q15b-MAIN 25.29→20.93 s, alloc 12.57→11.56 GB,
  values identical (single-sample).
- EX2-02c (`transferRowForQueue`: VirtualSlot fast path; others
  byte-identical): TPC-H 24/24 serial AND 24/24 parallel
  (arms values-identical) + 4 new transfer tests.
- EX2-02b DROPPED as infeasible (all 12 agg M-sites fail sole-owner
  transfer both ends; `MaterializeArena` already minimal).
- EX2-03 closed MEASURE-ONLY (pool already optimal: buckets 0–64 pool all
  widths identically; per-row hit rate ≈0% by construction and correctly
  so; predicted effect on clone slice ~0).

### EX3-01 buffered spill reader + Stack-elimination close-out

Elimination confirmed in-tree (zero per-row `runtime.Stack`; gls fast
path first). Reader cut: `bufio` symmetric with the writer (8192),
`rewind`+`br.Reset`, framing/codec + WaitEvent pairs untouched.
Spill shapes: Q7 14.97→11.48 s (−23%), Q13 6.89→4.69 s (−32%), values
identical (single-sample). Residual spill is single-digit %, not the
69–86% Stack era.

### EX3-02 dense-chunk build rows (Cuts 0–2)

Design reviewed (F1–F7 folded — incl. the GC-noscan Cut-2 blocker and the
cooperative-build correction). Unit allocs/row (exact):

| shape | legacy | Cut 1 (stratum B) | Cut 2 (+stratum D) |
|---|---|---|---|
| A Q9-class | 5.00 | 2.00 | headers 2.002→0.005 |
| C Q8-class | 6.00 | 2.00 | full 4.002→0.007 |
| B int lane | 1.00 | 1.00 | 1.00 (untouched by construction) |

Live Q9 windows (single-sample): 9.43 → 8.52 → 8.58 → **5.46 GB**;
`retainBuildRow`/`packDense` confirmed on-path; values identical
throughout. Timing in-band (no wall win claimed — the win is structural:
~6 M build rows × per-row allocs removed + Q8 Perm-leak fix).

### EX3-03 step-1 ruler arms; step-2 blocked with evidence

`estimatedRowBytes` now counts enum-label + big-numeric bytes,
byte-identical to the stats ruler (planner untouched, zero plan
movement). Probe established the symmetric spill machinery is landed;
the remaining gap is budget disagreement (planner 1 GiB vs executor
128 MB at 64 MB). Step-2 plumbing implemented + unit-green (F1 subquery
interiors REQUIRED — the Q9 witness plans through `planSelectWithParent`;
F2 DML; F5; F7 grep-gate) but BENCH-REGRESSES: honest budgets pick merge
over hash — Q7 +58%, Q9 +31%, values identical; same-server forced-hash
proof 10.65 s vs chosen merge 14.38 s while the model prices hash 1.10M
above merge 1.04M. BLOCKED on spill-cost calibration (model misranks;
PG at 64 MB still picks hash). Resume: design + clean-applying
`plumbing.patch` + evidence in
`analysis/planner-refactor-take3/ex303-step2-deferred-20260904/`.

### EX3-05 Cut A landed; Cut B declined

Cut A (`sortOp.wantCTIDs` gates the TID side-channel; markers at
LockRows + project/filter/aggregate/result + slab twins; resultOp hole
found in review and pinned with negative-controlled tests): Q16
timing-neutral, all gates green. Cut B (kind-specialized comparator)
DROPPED as verified no-win: proven to fire (count→KindInt) yet Q16
wall-neutral — Stage 1 already captured the comparator win.

## Bottom line

- Proven at unit level: build-path allocs/row down 60–99% on covered
  lanes; per-row pool churn already ~0; comparator/pool/ruler gaps
  closed or measured shut.
- Measured on wall (single-sample, in-band caveats apply): spill shapes
  −23/−32%, drain shape −17%, P4-01 Q9 arc 63.8→10.8 s, planner-flip
  alloc −10%.
- Correctness: zero values regressions across the session (every landed
  commit 24/24 + 22/22 + 95/0); two faster-and-wrong-class hazards caught
  before landing (P4-01b precedent honored; EX3-03 step-2 held out).
- Not claimed: full-suite TOTAL speedups, multi-sample timing bands,
  or any deferred/blocked item above.

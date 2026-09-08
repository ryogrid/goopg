# EX0-06 Design — Q6 chain + per-query baseline re-measurement

Item: `TODO_EXECUTOR.md` EX0-06 (gate: conforming artifact — Q6 chain +
per-query tables both suites; no behaviour change). Status: design for
review. This closes EX0: the exit demands the protocol "used once
end-to-end", worker stats surfaced (EX0-03/03b/03c), slices published
(EX0-04), batch counters reporting (EX0-05).

## 1. What gets measured (three parts, one artifact)

- (a) Q6 chain, serial AND parallel, TPC-H SF=1: full §4 capture set
  each (headline profiler-detached wall ×3 stabilized, pprof cpu +
  heap `-base` deltas, `perf stat :u` event set, value identity).
  Serial exists (EX0-02: 5.48 s; EX0-04 anchor) — re-run under the
  EX0-06 label for a single provenance; parallel is new (take7's 0.838 s
  was GOGC=off — re-measure at GOGC=100, expect a different number, NOT
  comparable, stated).
- (b) TPC-H SF=1 per-query timing table: 22-query power test
  (`run_power_test_goopg.sh` pattern) on a fresh capped server, serial
  control + suite-default arms. TIMING ONLY — HammerDB attests errors
  (`pg_raise_query_error`), not values; values are pinned by plan-gate
  + digest elsewhere, stated, not claimed here.
- (c) TPC-DS SF0.5 per-query timing table: `tpcds-sf05-regression.sh
  sweep` in TWO arms (suite-default + serial control, 09 §6) with the
  sweep loop patched to ms resolution (EPOCHREALTIME, additive new
  field beside the existing %4ss — same technique as the oracle path;
  the 1 s quantum cannot discriminate the 1.2× ceiling). Values gated
  by the sweep's own PASS/MISMATCH contract — reused, not re-gated.
  TOTAL = derived sum-of-query-ms with TIMEOUT entries listed
  separately (a 300 s timeout in a sum is a lie — cap rule stated in
  the artifact). Every mid-sweep restart position logged (the guard
  lines exist — required in the report, else the arm splits).

## 2. What is explicitly NOT measured per query

Alloc profiles per query (22+99 pprof captures) are disproportionate
for a baseline: alloc arms exist on the Q6 chain (a) + the EX0-04
witness corpus (Q6/Q9/Q4/Q7/Q13 cpu+heap, committed by reference).
Later items measure their own timing+alloc arms against (a)/(b)/(c).
This matches E1-as-landed ("Q6 chain + witness shapes timed per query;
suite TOTALs as the backstop") — and the EX0-06 TODO item text is
amended accordingly at close (it still says "timing+alloc baselines"
per query; E1's narrowing never propagated back). The EX0-04 corpus is
committed by reference ONLY after verifying same GOGC/work_mem/header
regime at close. Stated here so the gate cannot be misread either way.

## 3. Regimes and provenance

TPC-H `GOGC=100 GOMEMLIMIT=12GiB`, TPC-DS harness `GOGC=off` default
(09 §6 / env files); fresh server per arm; cgroup cap; full §2 header
per arm (label EX0-06-*, ports, work_mem+ecs, planner-flags, host
load). Stats regime stamped PER SUITE, never one label: TPC-H =
stats-absent S-cold (goopg ANALYZE fails on the per-DB scoping gap);
TPC-DS = ANALYZEd, stats survive restart = WARM-pinned
(`load-goopg` ANALYZEs all tables). A/B never mixes regimes — later
items inherit these exact stamps. Artifact:
`analysis/executor-refactor/ex0-06-20260903/` (Q6 chain README +
per-query tables + header). No behaviour change — `git diff --stat`
docs/analysis-only (+ the additive ms-timing patch to the sweep
script, which is harness, not engine); plan-gate re-run at close only
if the tree moved under the measurement (it must not).

## 4. Acceptance

E1's denominator: every later item diffs its per-query deltas against
(b)/(c) and its witness slices against (a)/EX0-04. The artifact is
rejected if any arm mixes regimes, any table lacks its header, or any
value check fails.

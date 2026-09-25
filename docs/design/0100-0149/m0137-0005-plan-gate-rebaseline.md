# M0137-0005 — re-baseline `make plan-gate`

Status: accepted (landed 2026-09-15)

## Context

`make plan-gate` (`Makefile:431-453`) is a **goopg-vs-committed-goopg**
baseline pin, not a diff against live PG (`AGENT.md`'s "Known-stale claims"
already states this — the claim that it "cannot pass until the goal is met"
is wrong and appears in two round reports). It picks the *most recently
modified* file under `plan_snapshots/*.txt` (`ls -t … | head -1`) and diffs
the live TPC-H bench cluster (`:65433`) against it in `structural` mode.

By filesystem mtime the picked baseline was `warm-pin-20260905.txt`
(captured 2026-09-05/07, ~9 days and roughly 125 rounds of intentional
plan-shape work stale — the M0137 milestone doc's own estimate). A pin that
fails on every commit regardless of what changed is not a live signal; this
task's job (per the M0137-0005 fix_plan line) was to re-capture and land a
current baseline, having adjudicated why the old one no longer matches
rather than blindly overwriting it.

## What was measured before re-baselining

`make plan-gate` against the stale baseline: **20 / 22 queries DIFFER**
(only `Q15a-VIEWBODY` and `Q19` MATCH) — reproducing the fix_plan line's own
figure exactly, confirming the "20 diverging queries" claim is current, not
stale in the other direction.

A per-query structural-diff node-type census (added/removed EXPLAIN node
counts, from the full `plan-gate` output) shows the divergence is
concentrated in exactly the mechanism classes the M0137 harness's own
"Known-stale claims" and `METHODOLOGY3` history already name as landed,
gated changes since 2026-09-05:

- **Parallel-plan adoption**: `Gather` / `Parallel Seq Scan` newly appear on
  Q1, Q3, Q5, Q9, Q12, Q16, Q18, Q22 where the old baseline planned serially.
- **Index-scan / Nested-Loop narrowing**: Q7, Q8, Q9, Q17, Q21 shift from
  `Hash Join` / `Merge Join` over `Seq Scan` to `Nested Loop` over
  `Index Scan` / `Bitmap Heap Scan`, consistent with the index-probe and
  join-order work landed across the M0125/M0127/"take2"/"take3" rounds this
  baseline predates.
- **`Memoize` adoption**: Q8 gains a `Memoize` node the old baseline lacks
  (`GOOPG_MEMOIZE=unset(on)` — already on by default at HEAD per the
  spot-check gate's own flag dump below).
- **Aggregate strategy flips** (`HashAggregate` <-> `GroupAggregate`): Q3,
  Q13, Q16, Q18, Q7 — narrowing/upper-planner work changing which grouping
  strategy the cost model prefers.
- A few queries (Q1, Q10, Q11, Q14, Q20) report identical added/removed
  node-type *counts* but still DIFFER — the structural differ is sensitive
  to more than the node-type multiset (e.g. operand-side or qual-clause
  ordering), i.e. these moved plans **sideways** (shape-changed, not
  necessarily category-changed) rather than not at all. Not further
  root-caused here — that is what `M0137-0010`'s planned qual-placement
  census exists for, and out of scope for a baseline-pin task.

None of this is new information this task discovered; it is the expected
shape of "~125 rounds of intentional plan change" the fix_plan line already
named. The adjudication this task owed was confirming the *current* state is
not itself broken before pinning it as the new floor.

## Correctness check before pinning

`scripts/tpch-spotcheck.sh` (fresh capped server, canonical Q12/Q13
row-count anchors) — **PASS**: Q12 rows=2 (expected 2), Q13 rows=34
(expected 34). This is the project's standard tell for a silent row-count
regression (the single most expensive failure mode per `AGENT.md`) and it is
clean, so the 20-query structural drift above is plan-*shape* movement over
this window, not a correctness break riding along with it.

No production planner/executor/catalog code was touched by this task — it
is a pure instrument re-pin, so the M0137 harness's "recon task" gates
(measurement + design note, no production diff) apply even though this task
isn't in the harness's explicit recon-task list. The TPC-DS SF0.25 sweep was
not additionally run: this task's diff surface is TPC-H-only
(`plan_snapshots/` + `Makefile`'s TPC-H-port defaults), and no TPC-DS code or
fixture was touched.

## The re-baseline

```
make plan-snapshot-capture LABEL=m0137-0005-rebaseline-20260915
```

against the goopg TPC-H bench cluster (`:65433`, started via
`bench/tpch/setup_goopg.sh` — the persisted `tpch` database from the
2026-07-27 HammerDB rebuild, no reload needed). Verification:
`make plan-gate` now reports **22 / 22 MATCH** against the new baseline
(trivially, since it diffs the cluster against a snapshot of itself, but
this confirms the capture round-tripped through `pg-plan-parity-diff.py`'s
structural comparator cleanly with no `UNPARSED` verdicts).

`plan_snapshots/m0137-0005-rebaseline-20260915.txt` is the new file the
`ls -t` "most recent" rule will pick from the next commit onward. The 18
pre-existing snapshot files (`m0126-*`, `m0127-*`, `pq-*`, `r5-*`,
`tpcds-round2-*`, `warm-stats-base.txt`, `warm-pin-20260905.txt`,
`take2-p0-20260903.txt`, `rowest-b1a1-fair-20260906.txt`,
`probe-mult2-20260905.txt`, `c20a-c06s-plancost-rows-20260907.txt`, …) are
left in place as frozen history, same precedent as M0137-0001's round
directories — no cleanup was in this task's scope.

## What was not done (scope boundary)

- **The sideways-shape-changed-but-same-node-count queries** (Q1, Q10, Q11,
  Q14, Q20) are noted, not root-caused — `M0137-0010`'s qual-placement
  census is the designated tool for that.
- **No stale-snapshot cleanup.** `plan_snapshots/` still holds 18 older
  files; only the mtime-newest one is ever consulted by `plan-gate`, so they
  are inert, not broken, and pruning them is optional future polish.
- **No TPC-DS re-baseline.** `make plan-gate` is TPC-H-only
  (`PLAN_PORT ?= 65433`, `PLAN_DB ?= tpch`); TPC-DS has its own,
  already-current regression instrument (the SF0.25 sweep, M0137-0004's
  subject).

## Verification

- `make plan-gate` against the pre-existing (`warm-pin-20260905`) baseline:
  20/22 DIFFER, 2/22 MATCH (`Q15a-VIEWBODY`, `Q19`) — reproduces the
  fix_plan line's own figure.
- `scripts/tpch-spotcheck.sh` — PASS (Q12 rows=2, Q13 rows=34; fresh capped
  server, scope `goopg-spotcheck`, stopped by the script's own EXIT trap).
- `make plan-snapshot-capture LABEL=m0137-0005-rebaseline-20260915` — wrote
  `plan_snapshots/m0137-0005-rebaseline-20260915.txt` (22 queries).
- `make plan-gate` against the new baseline: **22/22 MATCH**.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` and
  `make ralph-state-guard` — see the loop's status block / working-set note
  for this loop's run.

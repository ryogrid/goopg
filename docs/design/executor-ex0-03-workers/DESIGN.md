# EX0-03 Design — Surface per-worker hash/sort counters in EXPLAIN ANALYZE

Item: `TODO_EXECUTOR.md` EX0-03 (13 §2.2; gate: golden test, plans
byte-identical, no timing claim). Status: design for review.

## 1. Problem

PG's EXPLAIN ANALYZE shows per-worker state (`Workers Launched:`,
per-worker time/rows/loops, one `Sort Method:` line per worker,
worker-merged hash line — `postgres/.../explain.c:1371-1374, 1887-1929,
3125-3166, 3375-3422`). goopg renders none of it: `Workers Launched:`
has accessors (`operators_gather.go:115-118`,
`operators_gather_merge.go:78-79`) with zero EXPLAIN callers; worker hash
stats publish into private worker `Context` maps and die there
(`NewWorkerContext` starts fresh maps, `parallel_worker_ctx.go:144-148`;
`MergeWorkerContext` folds back only notices/warnings, `:172-179`);
worker subtrees are never instrumented (the `instrumentScope` is restored
before `runWorker` builds the worker tree, `instrument.go:299-309` vs
`operators_gather.go:336-344`); and `sortOp` has no method/space counters
at all (`operators.go:760-804`). 11 §10 records the surfacing gap.

## 2. Scope (plumbing + golden test only — no new estimation, no timing)

EX0-03 proper is (a)+(b) only. (c) and (d) build new measurement
mechanisms the item's premise ("counting already happens") does not
cover, so per the one-checkbox-one-commit rule they split into
sequenced follow-ups EX0-03b / EX0-03c below — each its own commit.

IN (EX0-03):
- (a) Fold worker `HashJoinStats` into the leader map with the existing
  MAX rule (`join_batch.go:156-158`, `operators_join_agg.go:643`) —
  extend `MergeWorkerContext` (+ `gatherOp.Close` `:474-482` and
  `gatherMergeOp` `:405` paths) to max-merge the stats maps per key
  (independent-field MAX, PG `explain.c:3398-3422` — including its
  cross-worker field-mixing quirk, not "improved"), not just notices.
  Workers already publish (`hashBatchState.publish`,
  `joinOp.recordBuildTime`); this only carries the values across. Note:
  under shared prebuilds (`operators_gather.go:236-245`) workers usually
  build nothing and the merge is a no-op — the test must force a
  worker-side build to exercise the path.
- (b) Render `Workers Launched:` on Gather/GatherMerge from the existing
  accessors (PG `explain.c:2030-2063` shape). Carrier problem: the
  renderer (`walkPlanAnalyzeFiltered`, `:1517`) sees plan nodes, never
  operators — so the launched count must travel in a Context-keyed table
  by `*optimizer.Gather` (same reason hash stats travel via
  `ctx.HashJoinStats`), not via the operator accessor. Same carrier
  carries the merged hash stats if needed.

SEQUENCED FOLLOW-UPS (own commits, own TODO lines):
- EX0-03b Per-worker rows/loops/time lines: reinstate an instrument
  scope inside `runWorker` around `buildChild()` (mirror the
  `setInstrumentScope` pattern, `operators_cte_dml.go:90-113`) — PLUS,
  all mandatory per review: (i) a FRESH instrumenter per worker, never
  the leader table (shared `optimizer.Node` keys + concurrent map
  writes = process death, cf. `parallel_worker_ctx.go:30-35`);
  (ii) a Context-keyed report carrier by plan node (renderer cannot
  reach operator fields); (iii) the same treatment for the
  leader-participation build (`operators_gather.go:295-308`) and
  `gatherMergeOp.runWorker` (`operators_gather_merge.go:193-194`) +
  its `Open`-time build (`:133`); (iv) golden asserts shape/presence +
  self-consistent max only — worker row distribution is
  schedule-dependent, so TIMING OFF does not make exact `rows=`
  deterministic.
- EX0-03c Minimal `sortOp` method/space counters (quicksort vs
  spill-merge + bytes from existing `Open` accounting) with one `Sort
  Method:` line per worker (PG `:3125-3166`).

OUT (explicit non-goals, filed as follow-ups if met): per-worker
Buffers/WAL (global `Pool` deltas in `accountBuffers`,
`instrument.go:155-175`, unattributable per worker), VERBOSE-gating
parity (goopg renders ungated; PG gates at `:1887` — noted divergence).

## 3. Why this shape

- Hash uses MAX-merge, not per-worker lines (PG `:3398-3422`) — so (a)
  needs no new render path, only the merge; the existing leader hash line
  (`formatHashJoinInfoLine`, `:210-232`) stays byte-identical in content,
  values can only grow toward PG's.
- Sort uses per-worker lines (PG `:3125-3166`) — so (d) is required; there
  is no leader-merge alternative. Counters are new but tiny (enum +
  existing byte accounting).
- (c) is the load-bearing half: without the scope fix there are no
  per-worker rows/loops to collect. The `cteDMLPrefixOp` precedent keeps
  it a pattern reuse, not a new mechanism.
- Reports ride the join edge (`workerReports[idx]`), not the gather
  queue (`rowBatch` stays rows-only) — no concurrent-protocol change,
  bounded by #workers × partial-subtree nodes.

## 4. Verification (gate)

- New `explain_parallel_workers_test.go`: deterministic small parallel
  plan forcing a worker-side hash build, `EXPLAIN (ANALYZE, TIMING OFF)`
  twice — `Workers Launched: N` present, leader `Buckets:` line equals
  the worker-built max (read from the same output); serial plans emit
  no `Workers Launched:` line.
- Existing goldens stay byte-identical: `explain_render_test.go`,
  `explain_analyze_test.go`, `parallel_label_test.go`,
  `join_batch_explain_test.go`. (`TestExplainParallelSeqScanLabel/Analyze`
  calls `walkPlanAnalyzeFiltered` directly — passes unchanged iff the
  fixture plan has no Gather node with launched workers; if it does, the
  fixture is updated deliberately with the new line recorded, not
  blindly.)
- Plan-shape pin: `changed=0` on both suites per ground rule 3
  (goopg-vs-goopg until planner P0 lands).
- `git diff --stat` shows only `internal/executor/` + the new test +
  TODO line. No timing claim; S-cold/WARM untouched.

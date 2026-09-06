# E-10 (EX5-03) — Gather Merge ordering + exchange: the claim-set gap

Status: implemented (correctness half). Performance half: **out of scope**, see §5.
Owner package: `internal/executor/`. Planner follow-up in §6.

Design lineage: take3 13 §7; parallel-query chapter 05 §4 (P7);
`docs/design/planner-c19f-parallel-hashjoin/DESIGN.md` (C-19f, which reported
this defect but could not fix it — it owned only `internal/optimizer/`).

## 1. The defect

`gatherOp.Open`/`runWorker` attached three kinds of shared work-claim state to
each participant's child tree:

- `attachParallelScan` — the sequential-scan block allocator (P4);
- `attachParallelBitmapScan` — the pre-built TIDBitmap page allocator (S5.6);
- `attachParallelIndexScan` — the btree leaf-block claim set (M0134-0189,
  extended to plain index scans by C-19c).

`gatherMergeOp` attached only the first, and ran no bitmap pre-pass at all.
Consequence: a Gather Merge whose partial subtree is driven by an **index** scan
had every participant (workers *and* the participating leader) walk the whole
index, and the merge returned `(workers+1)` copies of every row — **in the
correct order**, which is what made it silent. A row-count check catches this
one; the ordering check does not, and neither would a `LIMIT`-shaped smoke test.

Reproduced before any change, on the C-19c fixture (3000-row table, plain range
index scan, `GatherMerge(IndexScan)` spliced directly):

| workers | rows returned | serial |
|---|---|---|
| 1 | 5802 | 2901 |
| 2 | 8703 | 2901 |
| 4 | 14505 | 2901 |

Exactly `(workers+1)×` — leader participation is on, so every participant is a
full scan. This is the same failure the P6 serial-vs-parallel identity check
caught the first time `gatherOp` and the block allocator were connected
(`attachParallelScan`'s own comment records it: 240298 rows where serial
returned 120149). It recurred because the sibling was never given the same
check, and because the planner's producer refuses index-driven subpaths, which
made the shape unreachable — and unreachable means untested.

## 2. Fix

Introduce `parallelClaimSet` (`parallel_scan.go`): the complete set of claim
state a Gather hands its participants, one field per kind, with

- `newParallelClaimSet()` — the kinds that need no pre-pass;
- `prebuildBitmap(ctx, planChild, buildChild)` — hoisted verbatim off
  `gatherOp.prebuildBitmapScan`, so both consumers run the S5.6 pre-pass;
- `attachAll(op)` — the single place where a kind is wired.

`gatherOp` and `gatherMergeOp` both **embed** `*parallelClaimSet`, so `o.pscan`,
`o.pbm`, `o.pidx` still name the individual kinds (no call-site churn in
`operators_storage.go`, `operators_bitmap.go`, `operators_index*.go`) and a
future claim kind reaches both consumers by construction rather than by
discipline.

Second, unrelated bug found in the same function and fixed here:
`gatherMergeOp.runWorker` registered `defer close(o.chans[idx])` **after** the
child build and `child.Open`, so a failure in either left a live channel with no
closer. `Close` drains with `for range ch`, which then never ends: the statement
parks forever inside `Close` at 0 % CPU with the real error never reaching the
client. That is precisely M0127-P5.9's Q17 "hang", which `gatherOp` fixed with
`startChannelCloser` and which this sibling still carried. The `defer` now
precedes the first `return`.

## 3. `attachParallelScan`'s ignored return value — assessment

Both operators ignore it, and the doc comment claims an unmodelled node means
"the tree simply stays serial". **That claim is false for a worker tree.** A
participant whose driving scan was not wired runs a *complete* scan, so the node
returns N copies — the very failure mode above. The return value is therefore an
**unchecked precondition, not a safe fallback**.

It is nevertheless left ignored, deliberately:

- the invariant is genuinely owned by the planner's producer, which refuses to
  build a partial subtree whose driving scan it cannot model (`createGatherPlan`
  panics if `drivingScan` does not reach the built subtree's scan) — the
  executor cannot re-derive that decision;
- the executor cannot turn `false` into an error, because the injected-builder
  test harness (`parallel_scan_gather_test.go`'s `scriptedOp`) legitimately
  supplies a non-scan source whose participants are disjoint by construction,
  and PG's own equivalent (`nodeGather.c` / `execParallel.c`) likewise assumes
  the plan is parallel-safe rather than re-checking at execution time.

So: contract kept, comment corrected to say what it actually guarantees. Ledger
row `e10-attachall-precondition-unenforced`.

## 4. What the forced-shape test proves

`TestGatherMergeOverParallelIndexScanIdentity` splices
`GatherMerge(IndexScan)` — no Sort — over the C-19c fixture at 1/2/4 workers and
asserts the **values**: every `id` appears exactly once, ascending, equal to the
serial result position for position, and the leaf claim set handed out ≥ 2
blocks (so the partition was genuinely exercised rather than one worker taking
everything). Row counts alone would miss a partition that dropped or garbled
rows; the ordering alone would miss the duplication.

`TestParallelClaimSetAttachesEveryKind` is the anti-drift guard: it asserts each
kind is wired by `attachAll`, **and** that `reflect.TypeOf(parallelClaimSet{}).
NumField()` equals the number of kinds covered — so adding a field without an
arm and a case fails the build's test gate.
`TestGatherSiblingsShareOneClaimSet` asserts neither operator declares claim
state of its own.

## 5. E-10's performance half — out of scope, with numbers

E-10 as written also covers "worker-sorted slices, leader heap merge" as a
*performance* item. Both halves of that mechanism already exist and are
exercised (`sortOp` per worker under `GatherMerge`, `gmHeap` in the leader).
What remains would be tuning it.

There is no runtime witness for that on either corpus:

- the C-13a census over all 99 TPC-DS SF0.5 queries
  (`analysis/planner-refactor-take3/c13a-limit-sort-census-20260906/` and its
  addendum) found **all sorting is ≤ 0.015 % of corpus wall time**, median sort
  input **145 rows**, and **0 of 100 sorts spilled**;
- TPC-H contains **no `LIMIT` at all**, so the shape Gather Merge exists to make
  cheap never terminates early there.

A merge-tuning change measured on these corpora would be reading noise. The
performance half is therefore marked **out of scope, verified**, on the same
evidence that stopped C-13a before a line was written. The correctness half
above stands on its own merits and is done.

## 6. Planner change needed (NOT made here — `internal/optimizer/` is held)

With the executor correct, C-19f's producer restriction is the only thing
keeping the shape out of production. It is one line,
`internal/optimizer/gatherpaths.go:267-272`:

```go
func gatherMergeSubpathIsRunnable(sub *Path) bool {
	if !gatherSubpathIsRunnable(sub) {
		return false
	}
	return partialPathDrivingKind(sub) == PathSeqScan   // <- drop this
}
```

The `== PathSeqScan` line is the workaround, and its doc comment names this
executor gap verbatim ("`gatherMergeOp` attaches only `attachParallelScan` …
would therefore give every worker the whole index and return N copies of every
row"). Dropping it leaves `gatherSubpathIsRunnable`'s existing whitelist
(`partialPathShapeIsGatherable` → `partialPathDrivingKind != PathPrebuilt`),
which already admits exactly the set `attachAll` now models: `PathSeqScan`,
`PathIndexScan` (with the `RequiredOuter == 0` refusal intact),
`PathBitmapHeapScan`, `PathHashJoin`. Nothing further is needed for bitmap
safety: `generateUsefulGatherPaths` skips any subpath with no `Pathkeys`, and a
bitmap heap scan carries none. The whole function then reduces to
`return gatherSubpathIsRunnable(sub)` and the comment above it should be
rewritten to cite this document instead of the gap.

Consequence: C-19c's parallel index scans become gatherable in order, and C-19e
can cost `Gather Merge → Parallel Index Scan` against
`Gather Merge → Sort → Parallel Seq Scan`.

Related but **not** to be changed: `sortPartialRootPays`
(`internal/optimizer/parallel.go:406`) declines an index-driven partial Sort
root for two stated reasons. The second ("the Gather Merge operator attaches
only the seq-scan block allocator … would return every row once per worker") is
now false and its comment should be updated to point here. The first — the
measured one, q16 1.5 s → 2.3 s and q13 4.2 s → 6.8 s with the per-worker sort
enabled — still holds, so the function's behaviour must stay as it is.

Gate for the planner change: the executor tests above already cover
correctness; the planner side needs a parallel-mode plan capture
(`estimate-audit -serial=false`), because the default serial arm is
structurally blind to parallel plan shape (ledger
`take3-plan-capture-is-serial-only`).

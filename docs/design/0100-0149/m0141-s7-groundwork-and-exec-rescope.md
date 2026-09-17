# M0141-S7 — Incremental Sort groundwork primitives (Finding 3 rows 1-3) and the executor-step re-scope

Status: accepted (landed `fd5537907`, `65351372a`, `9c4884d36`, `ff5234006`, 2026-09-17). Production change: `pathkeysCountContainedIn` and `costIncrementalSort` (zero callers at landing) plus `addIncrementalSortPaths`, `addOrderedPaths`'s third arm, gated off by default behind `GOOPG_INCREMENTAL_SORT`. The 2026-09-17c row landed as M0141-S2b-2c (`9c4884d36`); its own writeup lives in `m0141-s2b-scoping-decomposition.md` §"S2b-2c landed" and is recorded here from M0141-S7's implementation-order view.

Parent: M0141-S7 — see [m0141-s7-readjudicate-and-scope-incremental-sort.md](m0141-s7-readjudicate-and-scope-incremental-sort.md)

## Update 2026-09-17 — prefix-count helper landed (Finding 3, table row 1)

Landed the first primitive Finding 3's implementation-order table names:
`pathkeysCountContainedIn(keys, required []PathKey) (contained bool, nCommon
int)` (`internal/optimizer/pathkeys.go`, next to `pathkeysContainedIn`),
reproducing `pathkeys_count_contained_in` (`postgres/.../pathkeys.c:558`).
Unlike the boolean-only sibling, it reports the length of the longest common
prefix even when `required` is not fully satisfied — the exact quantity an
Incremental Sort candidate needs to know how much of `required` still needs
sorting once a producing node's own order (GROUP KEY / PARTITION BY / Merge
Cond / outer-child order, per Finding 1) is credited.

This is **groundwork, not the S2b/S7 wiring** — same posture as the executor's
already-landed `sortPrefixEqual` (E-15): a tested, fully-specified primitive
with **zero production callers today**, landed ahead of its consumer because
it is independently correct and independently testable (4 new unit tests:
full-prefix-equals-`pathkeysContainedIn`, partial-prefix count, immediate
divergence, empty requirement). It does not touch `createOrderedPaths`,
`addOrderedPaths`, or any call site, so it cannot move a plan and the
S2b-2a-style "byte-identical" gate does not apply — there is nothing yet for
it to change.

- **Category movement**: none — no call site wired, so no plan can change.
- **shape-delta**: 0 (no capture taken; nothing calls the new function).
- **Stats epoch**: not applicable.
- **Seam-decline census**: not applicable.
- **Planning route**: not applicable.

Resume point unchanged from the Verdict section above: still gated on
`M0141-S2b`'s relevant sub-task landing per witness group before the next
Finding-3 step (the `cost_incremental_sort` composition) can be built and
exercised against real candidates.

## Update 2026-09-17b — cost_incremental_sort composition landed (Finding 3, table row 2)

Landed `costIncrementalSort` (`internal/optimizer/cost_funcs.go`, next to
`sortByteBranch`/before `costAgg`), Finding 3's table row 2:
`cost_incremental_sort` (`postgres/.../costsize.c:2000-2126`) composed over
the already-existing `costSortRunWithWidth` (`cost_tuplesort`). Same
groundwork posture as the prefix-count helper from the 2026-09-17 update
above: zero production callers, independently unit-tested (4 new tests in
`internal/optimizer/cost_incremental_sort_test.go`), does not touch
`addOrderedPaths` or any candidate-producing path.

**Resolved the open question from the prior update**: whether this step
needs S2b's real multi-candidate `Pathlist` to be meaningfully testable, or
is testable standalone like the prefix-count helper. Reading
`cost_incremental_sort` closely shows every one of its inputs is a plain
scalar (`input_tuples`, `width`, `input_startup_cost`/`input_total_cost`,
`presorted_keys`/`pathkeys` length, `sort_mem`, `limit_tuples`) except
`input_groups`, which upstream derives via `estimate_num_groups` *inside the
same function*. This composition splits that one call out into a caller-
supplied `inputGroups float64` parameter instead of computing it inline —
the same split `costSortRunWithWidth` already uses for `ncols`/
`avgVarBytes`/`width` (caller-computed, not re-derived). That makes the
formula itself, which is what actually needed pinning against PG, fully
testable with synthetic numbers; only the *wiring* that will call
`estimateNumGroups` over a real presorted-key prefix needs S2b's rel to
exist, and that wiring is a separate later step (the `addOrderedPaths` third
arm, Finding 3 table row 4), not this one.

One correctness note found while writing the independent pin: the function's
per-tuple overhead term uses `comparisonCost=0` (folded, not a parameter),
matching upstream — `cost_incremental_sort`'s only real caller
(`costsize.c:3701`, the merge-join outer-sort case) always passes
`comparison_cost=0.0`, and `cost_tuplesort` only adds `2*cpu_operator_cost`
to its own local copy of that parameter, not the caller's, so PG's own
per-tuple overhead term never sees the `+2*cpu_operator_cost` addition
either. `costSortRunWithWidth` already encodes that same "always 0"
convention (no external `comparisonCost` parameter at all), so
`costIncrementalSort` follows it rather than inventing a parameter nothing
upstream would ever set.

Also found, while property-testing: the formula is **not monotonic** in
`inputGroups` — cost decreases as groups grow (smaller per-group full-sorts
dominate) until the fixed per-group reset overhead (`2*cpu_tuple_cost` per
group) turns the curve back up near one-row-per-group. Verified by direct
probing (`inputTuples=50000`: cost falls from groups=1 to a minimum near
groups=25000, then rises again by groups=50000, staying below the groups=1
baseline throughout the swept range) — an initial test asserting plain
monotonicity was wrong and was corrected to the real invariant (bounded
above by the single-group/fully-presorted case) before landing.

Also updated `sort_pgrelationbytes_test.go`'s
`TestCostSortRunWithWidthProductionCallersAreComplete` census (`cost_funcs.go`
count 2 -> 3): `costIncrementalSort` is a genuine new `costSortRunWithWidth`
call site (the per-group full-sort price) even though it has no callers of
its own yet — the census tracks call sites, not reachability.

- **Category movement**: none — still zero callers into any
  candidate-producing path.
- **shape-delta**: 0 (TPC-DS SF0.25 sweep re-run: PASS=96 MISMATCH=0,
  PLAN-SHAPE changed=0).
- **Stats epoch**: not applicable.
- **Seam-decline census**: not applicable.
- **Planning route**: not applicable.

Resume point: Finding 3's table rows 1-2 (prefix-count helper, cost
composition) are both now landed and independently tested. Still blocked on
`M0141-S2b`'s relevant per-witness sub-task before row 3
(`PathIncrementalSort`/`addOrderedPaths` third arm) can be attempted — that
step needs a real multi-candidate `Pathlist` to build a presorted-prefix
candidate over, which is exactly what S2b provides and today's callers do
not. Re-check after each S2b sub-task lands whether the executor operator
(row 5, built on the already-landed `sortPrefixEqual`/E-15) and EXPLAIN
rendering (row 6) can also be staged ahead of the `addOrderedPaths` wiring,
following the same "independently testable primitive first" pattern this
task and the 2026-09-17 update both used.

## Update 2026-09-17c — Finding 3 row 3 landed, gated off by default

`M0141-S2b-2c` (`m0141-s2b-scoping-decomposition.md` §"S2b-2c landed") is
this row: `addOrderedPaths`'s third arm now exists
(`internal/optimizer/incrementalsortpaths.go`, `addIncrementalSortPaths`),
consuming `ordered.SearchCandidates`/`SearchCandidateKeys` (S2b-2a/2b),
`pathkeysCountContainedIn`, and `costIncrementalSort` exactly as this doc's
own "implement in order" list named. The prior update's open risk —
"reaching `createPlanNode` with no node kind to emit" if the new arm ever
won — is resolved by gating the whole arm behind `GOOPG_INCREMENTAL_SORT`
(default off), the SAME convention `GOOPG_PARTIAL_SORT_PATHS`/
`GOOPG_PARTIAL_AGG_PATHS` already use, rather than resequencing the executor
operator ahead of this step. `createPlanNode` still has no
`PathIncrementalSort` arm — reaching it panics via `createplan.go`'s
existing `default` case, deliberately, per that file's own "panic loudly
rather than silently mis-build" philosophy — and the flag's off default
keeps that panic unreachable in production. Verified live in a test: an
early version of this change ran the new arm through `createOrderedPaths`
end-to-end with the flag on and hit exactly that panic, because the
synthetic candidate genuinely won; the test was restructured to call
`addOrderedPaths` directly instead, so the arm is exercised without
materializing a winner the executor cannot emit yet.

TPC-DS SF0.25 sweep at the default (flag off): `PASS=96 MISMATCH=0`,
`PLAN-SHAPE: changed=0` — byte-identical, confirming this row's landing
moved nothing in production, as it must at this stage.

**Still unchecked.** Finding 3's remaining rows — flip
`GOOPG_INCREMENTAL_SORT` on and re-measure against the corpus, row 5 (the
executor operator), row 4 continuation (`createplansimple.go` wiring), and
row 6 (EXPLAIN rendering) — are the resume point for whichever loop picks
this back up next. The executor operator is now the actual blocker for
`GOOPG_INCREMENTAL_SORT=on` to be safe to measure with at all (today it
would panic on the first query where the arm wins), so it is the natural
next step rather than an arbitrary pick.

## Update 2026-09-17d — row 5 ("the executor operator") re-scoped: Finding 3
## understated its integration surface; split into S7-exec-a/b/c/d

Attempted to start row 5 directly this loop and stopped before writing any
production code once the actual touch-point count came in far above Finding
3's "one operator built on an already-published contract" estimate. Finding
3 correctly sized the *algorithm* (group via `sortPrefixEqual`, full-sort
each group via `sortOp`'s existing machinery) but not the *plumbing*: a new
`optimizer.Node` kind is not an isolated leaf in this codebase, it is a case
arm that a fixed roster of switches over `optimizer.Node` must all handle
once a real plan can produce one. Direct grep/read count for `*optimizer.Sort`
(the sibling every one of these needs to mirror):

1. **Two separate execution engines build a Sort node**, not one:
   `executor.go:179`'s classic `buildNode` recursion (`newSortOp` over an
   `Operator`-interface child) AND `executor.go:675`'s slab/tree fast path
   (`tree.buildRec`, wraps the child in `opNodeOperator` then calls the SAME
   `newSortOp`). Both must gain an arm — this is exactly the
   `pattern_sibling_paths_must_agree` class this project has been burned by
   before (fast-path vs. interpreted evaluator), just for plan-node
   *construction* rather than expression evaluation.
2. **Five mechanical `case *optimizer.Sort:` tree-walkers** need a mirrored
   arm once `IncrementalSort` can appear in a real tree: `scan_deform.go:229`
   (propagate key-expr refs for deform pushdown — an OMITTED arm here is a
   silent-wrong-answer risk, not a panic: a needed column would fail to
   deform), `scan_deform.go:327` (child-of-node for deform-bound
   determination), `subplan.go:202` (rescan-kind classification,
   `rescanReOpen`→`rescanCloseOpen`), `operators_cte_dml.go:357`
   (work-table-scan detection for recursive CTEs).
3. **`operators_explain.go`: 6 call sites**, of varying weight —
   `childNodeOf`/one `resolveKeySource`-family site (trivial, return
   `.Child`), three "children of node" walkers (trivial, return
   `[]Node{p.Child}`), one node-label switch (trivial, `"Sort"`-shaped
   string), and one **non-trivial** site (`:1195`) that renders the actual
   `Sort Key:` text — PG's Incremental Sort EXPLAIN output additionally
   prints a `Presorted Key:` line (`nodeIncrementalSort.c`/`explain.c`) this
   site does not have a shape for yet.
4. **Stats plumbing**: `context.go`'s `SortStats`/`SortWorkerStats` are keyed
   `map[*optimizer.Sort]SortStat` — an Incremental Sort's own per-group stats
   (PG reports a MIN/MAX/AVG spread across groups, not one number) need
   either a parallel map keyed by a new node type or a shared keying
   abstraction; `parallel_worker_ctx.go` mirrors the worker-side half of the
   same map.

None of this changes Finding 3's algorithmic verdict (the grouping and
per-group-sort primitives really do already exist and are the easy part).
What it changes is the sizing: "the executor operator" is not a single
loop-sized unit once the goal is *end-to-end safe to flip
`GOOPG_INCREMENTAL_SORT=on` and measure the corpus*, because a real
Incremental Sort node reaching `scan_deform.go`'s omitted arm would produce
a **silently wrong answer** (a column that should have deformed doesn't),
not a loud panic like the `createPlanNode` default arm — this is a
correctness gate, not a nice-to-have, before any corpus measurement.

**Split into four loop-sized sub-tasks** (filed in `.ralph/fix_plan.md`
under M0141-S7):

- **M0141-S7-exec-a — `IncrementalSort` optimizer Node type + the executor
  operator itself**, built and unit-tested standalone (constructed directly
  in tests, no `createPlanNode`/`Plan()` path), same "zero production
  callers yet" posture `pathkeysCountContainedIn`/`costIncrementalSort` used.
  Groups the child's rows via `sortPrefixEqual`
  (`internal/executor/sort_presorted.go`, E-15's own contract), full-sorts
  each group, streams groups out in arrival order. Scope explicitly
  EXCLUDES spill-to-disk, packed-tuple retention, and ctid passthrough —
  `sortOp`'s harder features — deferred by ledger row (see below); an
  all-in-memory, full-Row (`[]Row`), no-ctid first cut is enough to prove
  the algorithm and is what PG's own `nodeIncrementalSort.c` group-batch
  shape maps to most directly (it re-tuplesorts per group too).
- **M0141-S7-exec-b — make it structurally and semantically reachable
  end-to-end**: `createplansimple.go`'s `createPlanNode` arm (replaces the
  `default` panic for `PathIncrementalSort`), BOTH `executor.go` builder
  sites (classic + slab), and the four mechanical tree-walkers in
  §2 above (`scan_deform.go` x2, `subplan.go`, `operators_cte_dml.go`).
  This is the correctness-gating step: it must land, in full, before
  `GOOPG_INCREMENTAL_SORT=on` is ever pointed at the corpus, because a
  missing `scan_deform.go` arm is a silent wrong-answer risk, not a build
  break.
- **M0141-S7-exec-c — EXPLAIN rendering**: the five trivial
  `operators_explain.go` arms plus the one real one (`Sort Key:` shape
  extended with PG's `Presorted Key:` line, `nodeIncrementalSort.c`/
  `explain.c` as oracle) and the node-label switch's `"Incremental Sort"`
  string (already reserved for this purpose in
  `estimateaudit/parity_test.go:58`'s map key).
- **M0141-S7-exec-d (deferred, ledger row filed)** — parity with `sortOp`'s
  harder features once exec-a/b/c land and the corpus measurement runs:
  spill-to-disk for an oversized group, packed-tuple retention
  (`GOOPG_SORT_PACKED`), ctid passthrough for `ORDER BY ... FOR UPDATE`
  over an Incremental Sort, and per-group `SortStat`
  (`context.go`'s `SortStats`/`SortWorkerStats` keying). None of these are
  required to prove the plan-parity metric (14 TPC-DS witnesses are plain
  read queries), so they are explicitly out of scope for the metric-moving
  path and only need doing if/when a corpus query actually needs one.

Resume point: implement exec-a next (no plumbing dependency, fully
standalone-testable, same posture as the two already-landed groundwork
primitives).

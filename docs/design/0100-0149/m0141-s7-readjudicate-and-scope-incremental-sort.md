# M0141-S7 — re-adjudicate Incremental Sort under the plan-parity goal, and scope it

Status: accepted (recon landed 2026-09-16, no production change)

## Task

`.ralph/fix_plan.md` M0141-S7 reads: "re-adjudicate and implement Incremental
Sort" — PG emits `Incremental Sort` in 14/99 TPC-DS reference plans
(`bench/tpcds/plans-pg/`), goopg has zero implementation (only 7 comment/string
hits, no executor or planner node), and the ledger row `take3-C-14-dropped`
(`.ralph/deferral_ledger.md:2136`, 2026-09-07) declined building it — but on the
*previous* (performance) goal, which this milestone group's goal explicitly
overrides. This task does the re-adjudication and, per this milestone's own
recon-before-implementation discipline (K50; the M0140-0006 and M0142-0005/0016
precedent), scopes the implementation rather than attempting it in the same
loop — a "re-adjudicate and implement" task that turns out to need real
multi-file surgery is exactly the shape those precedents warn against landing
in one sitting.

## Re-adjudication: GO (was NO-GO under the performance goal)

`take3-C-14-dropped`'s entire case for dropping the feature was that PG's own
witness (`Q67`) spends 1.26 ms of a 15 s query (0.008%) sorting, that TPC-H has
no `LIMIT` at all, and that 0/100 TPC-DS sorts spill — i.e. **the feature is
performance-irrelevant** on this corpus. That row even says outright: *"Plan
parity alone is NOT sufficient grounds."*

Under `AGENT.md`'s plan-parity harness (binding for M0137–M0143): *"Every
currently executable TPC-H and TPC-DS query must produce the same plan as PG
18.3 ... a slower plan that matches is not a regression."* Plan parity alone
**is** the grounds now — it is the only grounds this milestone group is
permitted to use. The 14 TPC-DS queries with an `Incremental Sort` node in
PG's reference plan **cannot reach `MATCH` by any costing or estimation fix**,
because goopg has no plan node capable of rendering as that shape at all — this
is a structural ceiling, not a tuning gap, exactly like M0141-S3–S6's AggSplit
machinery. **Verdict: build it.** The 2026-09-07 decline is superseded by the
2026-09-14 goal change, not overturned on its own terms — its performance
measurement was correct and remains true; it is simply no longer the deciding
question.

## Method

Read PG's `create_incremental_sort_path` (`postgres/src/backend/optimizer/util/pathnode.c:3171`)
and `cost_incremental_sort` (`postgres/src/backend/optimizer/path/costsize.c:2000`)
directly (`global -x`), then, rather than assuming the naive reading ("port 7
independent call sites and a new executor node"), checked what of that PG
machinery goopg already has under a different name (`sortPathForBounded`,
`pathkeysContainedIn`, `estimateNumGroups`, the already-landed-but-unused
executor presorted-prefix contract), and — the actual center of this
task — extracted the **child node type directly above which PG places
`Incremental Sort`** for all 14 TPC-DS witnesses, to determine what upstream
prerequisite each one needs before an Incremental Sort candidate could ever be
considered. `pg-plan-parity-diff.py`'s own captured fixtures
(`bench/tpcds/plans-pg/*.txt`) were read directly (`awk` over each file's first
`Incremental Sort` match plus 4 following lines), not re-derived, per the
harness's "way of working" — no new capture, no server touched.

## Finding 1 — the corpus census: all 14 witnesses sit directly above a node whose own pathkeys already exist in goopg's planner

| query | child under Incremental Sort | Presorted Key |
|---|---|---|
| Q3 | `GroupAggregate` | `dt.d_year` (a GROUP BY key) |
| Q43 | `Finalize GroupAggregate` | `store.s_store_name, store.s_store_id` (GROUP BY keys) |
| Q54 | `GroupAggregate` | the GROUP BY key |
| Q60 | `GroupAggregate` | `item.i_item_id` (GROUP BY key) |
| Q89 | `GroupAggregate` | `item.i_category` (a prefix of the GROUP BY key list) |
| Q67 | `WindowAgg` | `dw1.i_category` (the `PARTITION BY` key) |
| Q58 | `Merge Join` | `item_1.i_item_id` (the join's own `Merge Cond` key) |
| Q83 | `Merge Join` | `item.i_item_id` (the join's own `Merge Cond` key) |
| Q11 | `Nested Loop` | `t_s_secyear.customer_id` (outer side's own order) |
| Q4 | `Nested Loop` | `t_s_secyear.customer_id` (outer side's own order) |
| Q35 | `Nested Loop` | `ca.ca_state` (outer side's own order) |
| Q64 | `Nested Loop` | `item.i_item_sk` (outer side's own order) |
| Q49 | `Unique` | a literal constant (trivially "sorted") |
| Q63 | `Subquery Scan` | `tmp1.i_manager_id` (passthrough of the subquery's own order) |

Zero counterexamples: no witness needs an Incremental Sort over an
arbitrarily-ordered `SeqScan`/`HashJoin` with no pathkey relationship to its
child at all. Every single one is PG using the fact that the **immediately
preceding plan node already produces its output ordered by (a prefix of) the
downstream `ORDER BY`'s keys**, for a structural reason: `GroupAggregate`'s
output is ordered by its `GROUP KEY`, `WindowAgg`'s by its `PARTITION BY`,
`Merge Join`'s by its own `Merge Cond`, and a `Nested Loop`'s output inherits
its outer child's order. goopg's planner already computes and carries exactly
this information — `PathAgg`'s Sorted-strategy candidate carries its GROUP BY
columns as `Pathkeys` (M0141-S1/S2 already established the Sorted `PathAgg`
candidate is always generated), `Path.Pathkeys` exists on every `*Path`
generically, and `pathkeysContainedIn` (`pathkeys.go:64`) already exists to
compare a candidate's pathkeys against a target. **The missing piece is not
pathkey metadata.**

## Finding 2 — the real blocker is the exact one M0141-S2 already named, now corpus-confirmed for Incremental Sort specifically, not just aggregation-strategy

Read `createOrderedPaths`/`addOrderedPaths` (`upperordered.go:63-127`)
directly. `createOrderedPaths(u *upperRels, input Node, ...)` takes `input`
typed as `Node` — goopg's **executor plan-node** type, not `*Path` — because
every one of its 4 callers in `planner.go` (lines 799, 1965, 2023, 11022,
covering the aggregate, window, and plain-query-tail cases respectively) has
already collapsed its producing rel's `Pathlist` down to one `setCheapest`
winner and built the executor node for it *before* calling here.
`createOrderedPaths` then does the best it can with only that one collapsed
node: `newPrebuiltPath` synthesizes a single-candidate `*Path` around it, and
`inputNodePathkeys(input)` (documented in the function's own comment as "the
C-07 seam half... derived from the finished Node") re-derives pathkeys **from
the executor node's own output shape** — a second-hand reconstruction, not the
producing rel's real candidate-with-pathkeys. **Only that one reconstructed
candidate ever reaches `addOrderedPaths`'s contest** (`upperordered.go:121-127`),
which today can do exactly one thing with it: `pathkeysContainedIn` (skip the
Sort entirely) or else `sortPathForBounded` (a full Sort). There is no third
arm, and there is no second *candidate* to try a partial-prefix Incremental
Sort against even if a third arm existed — `input` is singular by
construction.

This is precisely M0141-S2/M0141-S2b's already-diagnosed mechanism ("the upper
planner receives a finished `Node`, not the join rel's paths... K24's largest
single item in the workstream"), and S2b's fix_plan text is *already scoped
generally enough to cover this*: "change `createOrderedPaths`'s callers
(**every** `createXPaths` -> `createOrderedPaths` call site in `planner.go`)
to hand it the producing rel's `Pathlist` instead of a pre-collapsed `Node`" —
not "the GROUP_AGG rel" specifically, despite the task's short title. Verified
against all 4 real call sites in `planner.go` (799/1965/2023/11022): all 4
collapse to a single `Node` before calling `createOrderedPaths`, with no
GROUP_AGG-only special case — the join-rel witnesses (Q11/Q4/Q35/Q64 Nested
Loop, Q58/Q83 Merge Join, Q49 Unique, Q63 Subquery Scan) go through the exact
same seam as the GroupAggregate/WindowAgg witnesses. **S2b, as already filed,
is the single prerequisite for all 14 of this task's witnesses, not just the
5 GroupAggregate ones** — this task found no need to widen or re-split S2b's
scope, only to confirm it reaches every shape this corpus needs.

## Finding 3 — once S2b lands, the Incremental Sort-specific work is comparatively small; almost every primitive it needs already exists under a different name

| PG mechanism | goopg equivalent, already present |
|---|---|
| `pathkeys_count_contained_in` (count a shared prefix, not just yes/no) | `pathkeysContainedIn` (`pathkeys.go:64`) exists for the boolean case; a sibling prefix-*count* function is new but trivial — same loop shape, `break` instead of `return false` on first mismatch |
| `estimate_num_groups` over the presorted-key prefix | `estimateNumGroups`/`EstimateNumGroups` (`cardinality.go`) already exists for `GROUP BY` sizing — the same call, given the prefix key list |
| `cost_tuplesort` per group, scaled by `input_groups` | `costSortRunWithWidth` (`joinpathsmerge.go`, used by `sortPathForBounded` today) already prices a full sort; `cost_incremental_sort`'s formula (`costsize.c:2000-2126`) is `group_startup_cost + input_startup_cost + group_input_run_cost` for startup, plus a run-cost blend across `input_groups`, plus two small per-tuple/per-group overhead terms — a **composition** of the existing full-sort cost function over `input_tuples/input_groups`, not a new cost model |
| the executor's per-group re-sort / group-boundary detection | `internal/executor/sort_presorted.go`'s `sortPrefixEqual` — **already landed** (E-15, `065182d`+`c7f1f45`), fully specified (INPUT/EXECUTOR guarantee doc comment), and has **zero production callers today** — this is exactly the executor-side half C-14 was always meant to consume |
| the `IncrementalSort`/`SortPath` plan node, `PresortedKeys`/`nPresortedCols` | new: a `PathIncrementalSort` `Path.Kind` (or a `PresortedKeys int` field on the existing `PathSort`, since `sortPathForBounded` is the sole `PathSort` producer today — cheaper to extend than to fork) plus a `createplansimple.go` arm building the executor node |
| the executor `IncrementalSort` node itself | new: an operator that groups its input via `sortPrefixEqual`, full-sorts each group via the existing `sortOp` logic, and streams groups out in order — the grouping and per-group-sort primitives both already exist; the new code is the operator that wires them together |
| `EXPLAIN`'s `Incremental Sort` / `Presorted Key:` lines | new: a small addition to `operators_explain.go`, same shape as the existing `Sort`/`Sort Key:` rendering |

None of this contradicts K24's framing of the **surgery** (S2b) as the
workstream's largest item — it is. But the Incremental-Sort-specific
increment *on top of* S2b is not itself another programme-sized effort: it is
one new prefix-count helper, one cost function that composes two
already-existing cost primitives, one executor operator built on an
already-published and already-tested contract, and one EXPLAIN rendering
arm — closer in size to M0142-0012 (a single cardinality-estimation slice)
than to M0141-S3–S6 (the multi-round AggSplit programme).

## Verdict and resume point

**GO, but gated on M0141-S2b landing first.** Do not attempt the Incremental
Sort candidate/executor work before S2b lands — every one of the 14 known
witnesses needs S2b's Pathlist-forwarding surgery to have anything to build a
presorted-prefix candidate over; attempting it first would have nothing to
test against except a synthetic fixture, violating this milestone's
"never force a shape" / measure-against-the-real-corpus discipline.

Resume point once S2b lands: re-run this task's own 14-query census (the table
under Finding 1) against a post-S2b capture to confirm the `Pathlist` reaching
`addOrderedPaths` now includes the presorted-prefix candidates it names, then
implement in the order Finding 3's table lists (prefix-count helper -> cost
function -> `PathIncrementalSort`/`addOrderedPaths` third arm -> executor
operator -> `createplansimple.go` wiring -> EXPLAIN rendering), each pinned by
a test before the next, per this milestone's established slice discipline.
Full floor-measurement suite (TPC-H/TPC-DS plan-parity, SF0.25 sweep, `make
ea-ratchet`) required on landing, same treatment M0141-S2a-fix1/M0142-0012 got.

`M0141-S7`'s own fix_plan line is left **unchecked**: this task supplies the
re-adjudication (GO) and the scope, not the implementation, which cannot start
before S2b — per `AGENT.md`'s Completion and Deferral Discipline, "partially
complete is still incomplete."

## What every M0137-M0143 task report must contain (per AGENT.md)

- **Category movement**: none — no production code changed this task
  (recon/reading only: PG source via `global -x`, goopg source via Serena
  symbol reads, `bench/tpcds/plans-pg/*.txt` via `awk`; no server, no
  capture, no build).
- **shape-delta**: 0.
- **Stats epoch**: not applicable — no capture taken.
- **Seam-decline census**: not applicable.
- **Planning route**: not applicable — no plan produced or altered.

## Verification

- No code changed; `git status`/`git diff` on every `internal/` and
  `postgres/` path is empty after this task. `go build ./...` not re-run
  (no Go source touched).

## Cross-references

- Supersedes `take3-C-14-dropped`'s performance-only verdict for the
  plan-parity metric specifically (does not dispute its performance numbers).
- Depends on `M0141-S2b` (`.ralph/fix_plan.md`), which now carries this task's
  corpus evidence as an additional justification beyond its original
  aggregation-strategy motivation.
- Ledger row filed: `.ralph/deferral_ledger.md` (2026-09-16, task-id
  `m0141-s7`).

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

## Update 2026-09-17e — M0141-S7-exec-a LANDED

Landed both halves exec-a scoped:

- `internal/optimizer/incrementalsort.go` — the `IncrementalSort` Node type.
  Mirrors `Sort` (`plan.go`) field-for-field (`PlanCost`, `searchedTree`,
  `pos`, `Child`, `Keys`) plus one addition, `PresortedCount` (PG's
  `nPresortedCols`, `plannodes.h`). Zero production callers: no
  `createPlanNode` arm exists yet (that is exec-b). 1 test file
  (`incrementalsort_test.go`), pins `Pos()`/`Output()`/field storage and
  that the type satisfies `Node`.
- `internal/executor/operators_incremental_sort.go` — the `incrementalSortOp`
  executor operator. Pull-based (`Open`/`Next`/`Close`/`Schema`), all in
  `Open`: drains the child fully, splits into presorted-prefix groups as
  they arrive using `sortPrefixEqual` (E-15's contract — a group closes the
  moment a row's leading `PresortedCount` keys stop matching the previous
  row's), sorts each closed group **by all keys** (not just the trailing
  ones) via a permutation-based `sort.SliceStable`, streams groups out in
  arrival order from `Next`. Sorting by all keys rather than just the
  trailing keys is deliberately simpler than PG's own per-group tuplesort
  scope and is behaviourally identical: every row in a closed group already
  shares the same leading-key values, so comparing the full key vector adds
  ties that never break, never a wrong order. 5 tests
  (`operators_incremental_sort_test.go`), including two that cross-check
  output against `sortOp` (the existing full-sort oracle) for order-
  equivalence over presorted and NULL/DESC-prefixed input, one for the
  `PresortedCount=0` degenerate (single group, must equal a plain full
  sort), and one that pins the actual per-group sort (not just end-to-end
  order) by feeding each group in reverse-sorted arrival order.

**Discovery not in the original four-way split**: adding the `IncrementalSort`
Go type — even with zero production callers — immediately failed two
existing hard gates in `internal/executor`:
`TestEveryPlanNodeTypeHasAnExplainArm` and `TestEveryPlanNodeWithChildrenIsWalked`
(`explain_node_coverage_test.go`). Both enumerate plan node types by
reflection/AST-scan over the whole package, not by construction reachability
— they fire the moment a new `optimizer.Node` implementer exists at all,
regardless of whether the planner can ever build one. Of exec-c's own
"6 `operators_explain.go` sites", exactly 2 are behind these hard gates:
`describePlanMode`'s label switch and `planChildren`'s child-walk switch.
Declining to fix these (via `explainCoverageExempt`/`childWalkExempt`) was
rejected as the wrong call here: the fix is one line each, mirrors `Sort`'s
own arm exactly, and the exempt maps' own header comment frames exemption as
an argued-for exception, not a default — so both arms were added now rather
than deferred:

- `describePlanMode`: `case *optimizer.IncrementalSort: return "Incremental
  Sort"` (the label string was already reserved for this in
  `estimateaudit/parity_test.go:58`'s map key).
- `planChildren`: `case *optimizer.IncrementalSort: return
  []optimizer.Node{p.Child}` (identical shape to `Sort`'s own arm).

This **narrows exec-c's remaining scope** to the other 4 of the original 6
sites: `resolveKeySource`, `childNodeOf`, `execParamOwnerChildren` (all
decline gracefully today, matching every other not-yet-relevant Node type —
safe, not gated by any test) and `emitNodeDetailLines`'s `Sort Key:` line,
which still needs the real `Presorted Key:` extension
(`nodeIncrementalSort.c`/`explain.c` oracle) — that is exec-c's one
substantive remaining item.

Gates run: `go build ./...` clean; `go test ./internal/optimizer/...` and
`./internal/executor/...` both green (including the two previously-failing
coverage tests, now passing); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` — only pre-existing failure is
`internal/parser` (the tracked `GroupedJoinUnaliased` AST-drift issue,
`.ralph/fix_plan.md`'s "Manually discovered" entry, filed 2026-09-15,
untouched by this change). `scripts/tpch-spotcheck.sh` SKIPPED (TPC-H bench
schema not currently loaded — CLAUDE.md's M0142-0003k blocker, pre-existing
and unrelated); moot regardless since this task has zero production callers,
so no plan can change. `GOOPG_INCREMENTAL_SORT` stays default-off; nothing
in this update touches the flag or `addIncrementalSortPaths`.

Resume point: **M0141-S7-exec-b** — `createplansimple.go`'s `createPlanNode`
arm for `PathIncrementalSort` (needs to decide the real `PresortedCount`
value from the winning `*Path`'s recorded prefix — `addIncrementalSortPaths`
computes `nCommon` today but does not stash it on the `*Path`; check whether
a new `Path` field or a re-derivation via `pathkeysCountContainedIn` at
`createPlanNode` time is cheaper), both `executor.go` builder sites (classic
`buildNode` + slab/tree fast path), and the four mechanical tree-walkers
(`scan_deform.go` x2, `subplan.go`, `operators_cte_dml.go`). This is the
correctness-gating step before `GOOPG_INCREMENTAL_SORT=on` can ever be
pointed at the corpus.

## Update 2026-09-17f — M0141-S7-exec-b LANDED

**Sizing decision (the open question from -17e): stash, not re-derive.**
`Path.PresortedCount` (new field, `path.go`) is set once by
`addIncrementalSortPaths` (`incrementalsortpaths.go`) at the exact point
`nCommon` is already in scope, and read verbatim by the new
`createIncrementalSortPlan` arm. Re-deriving it at `createPlanNode` time via
`pathkeysCountContainedIn` against the winning candidate's own
`SearchCandidateKeys` entry was rejected: nothing guarantees the candidate
that survives `setCheapest` still indexes the same `SearchCandidateKeys`
slot it was built against (the tournament can re-order/mutate `Pathlist`
between the two points), so stashing is both cheaper and the only version
that is exact by construction rather than by coincidence.

Landed, in order:

- `internal/optimizer/path.go` — `Path.PresortedCount int` (documented,
  zero for every kind but `PathIncrementalSort`).
- `internal/optimizer/incrementalsortpaths.go` — `addIncrementalSortPaths`
  now stamps it; added `SetIncrementalSortPathsMode` (the same
  cross-package test hook `SetPartialSortPathsMode`/`SetPartialAggPathsMode`
  already provide, for a future consumer test to flip the flag without
  reaching into the package).
- `internal/optimizer/createplansimple.go` — `createIncrementalSortPlan`,
  `createSortPlan`'s structural twin: same child recursion, same pathkey
  translation through `translateToLayout`, plus one extra precondition
  `PathSort` has none of — `0 < PresortedCount < len(Pathkeys)`
  (`IncrementalSort`'s own contract, `incrementalsort.go`) — checked and
  panicked on by name, matching every other `createPlan` arm's "a
  constructed-but-unhandled/malformed shape is a producer bug" convention.
  Wired into `createPlan.go`'s switch right after `PathSort`.
- `internal/executor/executor.go` — the classic `buildNode` arm
  (`*optimizer.IncrementalSort` case, right after `*optimizer.Sort`,
  identical shape: recurse on the deform-bound child, wrap in
  `newIncrementalSortOp`, run through `maybeInstrument`). **The slab/tree
  fast path (`BuildFast`'s `buildRec`) needed NO new code**: unlike
  `OpSort`, `IncrementalSort` is not one of the concrete-dispatch slab
  kinds (`OpSeqScan`/`OpFilter`/`OpProject`/`OpLimit`/`OpSort`/`OpUpdate`/
  `OpDelete`/`OpInsert`/`OpJoin`) — it falls through to `buildRec`'s
  existing `default:` arm, which calls the now-updated `buildNode` and
  wraps the result in `opAdapterState`. This is the SAME posture
  `*optimizer.Distinct`/`*optimizer.WindowAgg`/`*optimizer.SetOp` already
  have (confirmed by grep: none of the three has an explicit `buildRec`
  case either) — a new node kind gets slab-native dispatch only when a
  later phase specifically migrates it, not automatically. "Both
  `executor.go` builder sites" (the exec-b task line's own phrasing) is
  satisfied because both entry points (`Build` and `BuildFast`) now reach a
  working operator, not because both have bespoke code.
- Tree-walkers, all four, each mirroring the existing `*optimizer.Sort`
  arm exactly:
  - `scan_deform.go` `deformBoundBelow` — folds `Keys` the same way Sort
    does (the presorted prefix is still evaluated per row by
    `sortPrefixEqual`'s group split, so it is a genuine consumer too).
  - `scan_deform.go` `deformSideWidth` — descends through the child like
    every other single-child pass-through in that walk.
  - `subplan.go` `classifySubPlan` — classified `rescanCloseOpen`, same as
    `Sort` (not yet audited for bare re-Open safety despite `Open`
    resetting its own `groups`/`gi`/`ri` state, so treated identically
    until that audit happens — the conservative choice, not a proven one).
  - `operators_cte_dml.go` `planContainsWorkTableScan` — the one entry
    with a real correctness stake: a recursive CTE body that picks up an
    Incremental Sort over its `WorkTableScan` must still be detected and
    streamed, not silently materialized (`cteScanOp.Open`'s decision reads
    this function's answer directly).

**Verification.** New tests, all passing:

- `internal/optimizer/createplansimple_test.go` —
  `TestCreateIncrementalSortPlanOverPrebuilt` (recursion + pathkey
  translation + `PresortedCount` carry-through) and
  `TestCreateIncrementalSortPlanPanics` (6 precondition cases, including
  both `PresortedCount` boundary violations `PathSort` has no equivalent
  of).
- `internal/executor/operators_incremental_sort_build_test.go` (new file)
  — `TestIncrementalSortReachesBothBuilders`: a real 2-column, 4-group,
  80-row table (`inc_items`), rows inserted in ascending-`grp` BLOCKS with
  a deliberately unsorted `v` within each block (so a correct answer needs
  the operator's real per-group sort, not just "trust the input"). Takes
  the planner's OWN real `Sort` node for `SELECT grp, v FROM inc_items
  ORDER BY grp, v` (via `planOne`), swaps it for a hand-built
  `IncrementalSort{PresortedCount: 1}` over the identical child/keys, and
  checks (1) the rendered rows are byte-identical to the un-swapped
  planner Sort's own output — the value-correctness gate — and (2)
  `Build`/`Run` agrees with `BuildFast`/`RunFast` (`runBothAndCompare`,
  the existing Phase-C harness) — the "both builder sites" gate. The plan
  is hand-built rather than tournament-won for the same reason
  `TestBuildFastNodeKinds` (`phase_c_test.go`) already hand-builds its own
  cases: getting a real query to WIN the (still off-by-default) tournament
  is the broader M0141-S7 measurement goal, not exec-b's "does the wiring
  work" scope.
- `internal/executor/subplan_handle_test.go` —
  `TestClassifySubPlanKinds` gained an `IncrementalSort` case (asserts
  `rescanCloseOpen`, mirroring `Sort`'s existing case in the same table);
  new `TestPlanContainsWorkTableScanSeesThroughIncrementalSort` (positive
  and negative case for the correctness-stakes walker above).
- `internal/executor/scan_deform_bound_test.go` — the existing
  `sort-limit-lockrows-fold` subtest gained an `IncrementalSort` narrowing
  check (`Keys` bound 7/8, same as its `Sort` sibling immediately above
  it); `seqLeafBound`/`joinSideBounds` (the file's own operator-tree
  walkers) gained `*incrementalSortOp` cases alongside their existing
  `*sortOp` ones — without these the new build test's own deform-adjacent
  assertions would have false-failed with "no SeqScan leaf under
  `*executor.incrementalSortOp`" (caught during this loop; fixed before
  landing).

Gates run: `go build ./...` clean; `go test ./internal/optimizer/...
./internal/executor/...` both green. `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` — only failure is the pre-existing,
already-tracked `internal/parser` `GroupedJoinUnaliased` AST-drift issue
(`.ralph/fix_plan.md`'s "Manually discovered" entry, filed 2026-09-15,
confirmed untouched: `internal/optimizer` and `internal/executor` both
`ok`). `scripts/tpch-spotcheck.sh` SKIPPED (TPC-H bench schema still not
loaded — CLAUDE.md's M0142-0003k blocker, pre-existing and unrelated) —
moot regardless: `GOOPG_INCREMENTAL_SORT` stays default-off
(`incrementalSortPathsMode` unchanged by this update), so no production
plan can move.

Resume point: **M0141-S7-exec-c** — the one substantive remaining
`operators_explain.go` item, `emitNodeDetailLines`'s `Presorted Key:` line
extension (`nodeIncrementalSort.c`/`explain.c` oracle), now unblocked
(exec-c's own fix_plan entry says it "needs exec-b: nothing to render
before a real node can reach EXPLAIN" — a real node can now reach EXPLAIN).
After exec-c, exec-d (deferred, ledger row already filed) is `sortOp`
feature parity (spill-to-disk, packed retention, ctid passthrough,
per-group `SortStat`) — measure against the corpus first per its own entry,
none of the 14 witnesses need it today.

## Update 2026-09-17g — M0141-S7-exec-c LANDED

**PG oracle read (`explain.c:2583-2823`):** `show_incremental_sort_keys`
calls the SAME `show_sort_group_keys` a plain `Sort`/`MergeAppend` calls,
just with `nPresortedKeys = plan->nPresortedCols` instead of 0. Inside that
shared function, every key renders into the `Sort Key:` list exactly as
before (decorated with its DESC/COLLATE/NULLS suffix), but the function
ALSO captures each key's undecorated `exprstr` — the deparse output
*before* `show_sortorder_options` appends the direction/NULLS text — into a
second list, and if `nPresortedKeys > 0` emits that list's leading
`nPresortedKeys` entries as a second property, `Presorted Key:`
(`explain.c:2816-2822`). Two bits worth stressing: (1) `Presorted Key:`
carries NO direction/NULLS decoration even for a DESC key — PG's own
oracle strips it, this is not a goopg simplification; (2) both lines
render off the exact same per-key `keycols`/deparse loop, so goopg's
existing `*optimizer.Sort` key-formatting logic (the R65/S18 chase-through-
agg/Project machinery already landed for `Sort Key:`) is exactly the code
`Presorted Key:` needs too — a second independent implementation would risk
the "sibling paths must agree" trap for zero reason.

**Landed**, `internal/executor/operators_explain.go`:

- Extracted the `*optimizer.Sort` case's entire per-key loop (the R65 Arm-A
  agg chase, the Entry-(ii) Project chase, the S18 force-paren rule, the
  DESC/NULLS suffix logic) into a new shared helper, `sortKeyParts(child
  optimizer.Node, keys []optimizer.SortKey, reg *subPlanReg, qualify bool)
  (full, bare []string)`. `full` is the existing decorated per-key string
  (unchanged behaviour — captured `bare` before appending the suffix, so
  `Sort Key:`'s output is byte-identical to before this change); `bare` is
  the newly-captured pre-suffix string, goopg's analogue of PG's `exprstr`.
  The `*optimizer.Sort` case now just calls the helper and keeps its
  single `Sort Key:` line.
- New `case *optimizer.IncrementalSort:` right after it: calls the same
  `sortKeyParts` helper (same child/keys shape, so no new chase logic
  needed), emits `Sort Key:` from `full` exactly like `Sort`, then a second
  row `Presorted Key: ` + `strings.Join(bare[:p.PresortedCount], ", ")`.
  `PresortedCount` is always in `(0, len(Keys))` — enforced by
  `createIncrementalSortPlan`'s own panic check (exec-b) — so PG's `if
  (nPresortedKeys > 0)` guard always fires here; no conditional needed.
- The 3 remaining `operators_explain.go` sites named in the exec-c
  fix_plan entry (`resolveKeySource`, `childNodeOf`,
  `execParamOwnerChildren`) are confirmed still correct to leave declining
  — none is gated by a coverage test and none has a `*optimizer.
  IncrementalSort`-shaped reason to change; re-checked, not touched.

**Verification.** New test
`internal/executor/operators_incremental_sort_build_test.go`'s
`TestExplainIncrementalSortPresortedKey`: reuses exec-b's hand-built-plan
technique (`firstSort`/`replaceSort` over a real planner `Sort` for
`SELECT grp, v FROM inc_explain ORDER BY grp, v DESC`, swapped for
`IncrementalSort{PresortedCount: 1}`), wraps it in `&optimizer.
Explain{Options: {Costs off}}`, builds/opens/drains it directly (no SQL
`EXPLAIN` parse path exists for a hand-built node), and asserts (1) `Sort
Key: grp, v DESC` renders unchanged, (2) `Presorted Key: grp` renders (the
one presorted key, DESC-key excluded since `PresortedCount=1`), and (3) the
presorted line carries neither `DESC` nor the second key — pinning PG's
"bare exprstr, prefix only" rule against a false pass. Existing
`TestExplainEmitsSortKeyDetail` (plain `Sort`, no regression) and
`TestIncrementalSortReachesBothBuilders` (exec-b's own gate, unaffected by
an EXPLAIN-only change) re-verified passing.

Gates run: `go build ./...` clean; `go test ./internal/optimizer/...
./internal/executor/...` both green (`internal/executor` includes the new
test). `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — only
failure is the same pre-existing, already-tracked `internal/parser`
`GroupedJoinUnaliased` AST-drift issue (optimizer/executor both `ok`,
confirmed untouched). `scripts/tpch-spotcheck.sh` SKIPPED (TPC-H bench
schema still not loaded, CLAUDE.md's M0142-0003k blocker, pre-existing) —
moot regardless: `GOOPG_INCREMENTAL_SORT` stays default-off, so no
production plan can reach this rendering path yet.

Resume point: **M0141-S7-exec-d** (deferred, ledger row already filed) —
`sortOp` feature parity (spill-to-disk, packed retention, ctid passthrough,
per-group `SortStat`) once the corpus measurement (running the 14 TPC-DS
witnesses with `GOOPG_INCREMENTAL_SORT=on`) shows a query actually needs
one of those. exec-a/b/c are now all landed, so that measurement is the
next open action in this milestone, though it is a measurement task, not
necessarily the next loop-sized code change — re-check the fix_plan banner
before selecting.

## Update 2026-09-17h — the corpus measurement, and why it is 0/14 and 0/99

**Ran the measurement this row has been pointing at since 2026-09-17d.**
Started the goopg TPC-DS SF0.25 cluster (`bench/tpcds/server.sh start
sf025`) with `GOOPG_INCREMENTAL_SORT=on` exported before the start call, and
confirmed the *live server process* actually saw it —
`tr '\0' '\n' < /proc/<pid>/environ | grep GOOPG_INCREMENTAL_SORT` on the
running goopg binary read back `GOOPG_INCREMENTAL_SORT=on` — not just a
capture-header label (`scripts/planner-flags.sh`'s `unset(off)` line reflects
the *capturing shell's* environment, not the server's, and is not proof the
flag reached the server; the two Bash tool calls that started the server vs.
ran the capture are separate shells, which is exactly why the direct
`/proc/<pid>/environ` check mattered here).

**Result: zero.** `scripts/capture-tpcds.sh` against the flag-on server,
all 99 queries, EXPLAIN only:

```
grep -c "Incremental Sort" analysis/m0141/m0141-s7-full99-incsort-on.txt
0
```

Not just the 14 witnesses — the **entire TPC-DS corpus** produces zero
Incremental Sort nodes with the arm turned on at HEAD. Since `setCheapest`
picks a deterministic minimum-cost winner and a losing candidate cannot
change which path wins, "zero Incremental Sort nodes in the output" is
equivalent to "byte-identical to flag-off across all 99 queries" — a second,
paired flag-off capture would only reconfirm what the zero-count already
proves, so it was not taken.

**Root cause, found by reading the caller graph rather than guessing.**
`addIncrementalSortPaths` (the third arm, `incrementalsortpaths.go:140`)
reads `ordered.SearchCandidates` / `ordered.SearchCandidateKeys` — fields on
the `*RelOptInfo` passed in, **not** anything derived from its own `input`
argument. Those two fields are populated in exactly one place:
`createOrderedPaths` (`upperordered.go:106-118`), from `searchedRelOf(input)`
— i.e. only when `input` is itself the search's own top node.

`electOrderedGrouping` (`upperorderedgrouping.go:178`) — the loop that
decides every GROUP_AGG-shaped ORDER BY in the corpus (`planner.go:1955-1965`:
`if selectSrfPending == nil { loopBuilt, loopElected =
electOrderedGrouping(...) }`; `createOrderedPaths` only runs in the `else`
branch, i.e. when `electOrderedGrouping` **declines**) calls
`addOrderedPaths` directly, once per surviving `PathAgg` candidate
(`upperorderedgrouping.go:236`), **without ever going through
`createOrderedPaths` first**. `ordered.SearchCandidates` is therefore still
whatever an unrelated earlier call left it (typically nil, since
`fetchUpperRel`'s freshly-sized rel starts empty) for every one of
`electOrderedGrouping`'s own `addOrderedPaths` calls — so
`addIncrementalSortPaths` iterates zero or stale-and-irrelevant candidates
and adds nothing, **structurally, regardless of `anyTranslated`'s state**.

This means the S2b-5/S2b-6 line of work (fixing `groupingEmissionPathkeys`'s
over-restriction so `anyTranslated` stops declining) **cannot by itself**
connect the 5 GroupAgg-shaped witnesses (Q3, Q43, Q54, Q58, Q60, Q63, Q83 —
re-tallied below, 7 not 5) to Incremental Sort: fixing `anyTranslated` only
changes whether `electOrderedGrouping`'s *own* Sort-vs-no-Sort election runs;
that election has no Incremental Sort awareness of its own, and it never
reaches the arm that does. A further, distinct wiring step is needed inside
`electOrderedGrouping` itself — either populate
`ordered.SearchCandidates`/`SearchCandidateKeys` before its per-candidate
`addOrderedPaths` calls (mirroring what `createOrderedPaths` does, scoped to
`cands` instead of a searched join/scan tree), or give `electOrderedGrouping`
its own incremental-sort-over-PathAgg-candidate offer. **Filed as
M0141-S2b-7 below** (fix_plan) rather than folded into S2b-5/S2b-6, because
it is a different call site with a different fix, not a continuation of the
`anyTranslated` chase.

**Re-verified the 14 witnesses' PG-side child node directly** (read the AST
node immediately below each `Incremental Sort` in `bench/tpcds/plans-pg/`,
not just the Sort Key text — Q58's leading sort key is a plain column but its
actual child is a `Merge Join`, which the Sort Key text alone would have
mis-classified; this re-confirms the original 2026-09-16 tally rather than
replacing it):

| producer shape | queries | count |
|---|---|---|
| GroupAggregate (incl. one `Finalize GroupAggregate`) | Q3, Q43, Q54, Q60, Q89 | 5 |
| Merge Join | Q58, Q83 | 2 |
| Nested Loop | Q4, Q11, Q35, Q64 | 4 |
| WindowAgg | Q67 | 1 |
| Unique (SETOP, per the 2026-09-16 S2b-1-result correction) | Q49 | 1 |
| Subquery Scan | Q63 | 1 |

**The 5 GroupAggregate witnesses are conclusively explained** by the
`electOrderedGrouping`-bypasses-`SearchCandidates` gap above — that call
site is the only one any GROUP BY's own ORDER BY can reach, and it never
populates the fields the third arm reads.

**The other 9 are NOT yet root-caused to the same mechanism, and this
update does not claim they are.** The 2 `Merge Join` + 4 `Nested Loop`
witnesses are exactly the shape S2b-2a/2b/2c targeted (a plain join/scan
search tree feeding a top-level ORDER BY with no aggregation of its own,
where `createOrderedPaths` — not `electOrderedGrouping` — runs and
`searchedRelOf(input)` should succeed), so the corpus-wide zero is a genuine
open question for that bucket: either `searchedRelOf` does not recognize
these particular inputs as search roots (e.g. a correlated-subquery-heavy
FROM list like Q58's), or `validatedSearchCandidateKeys` rejects every
candidate's translated ordering, or no candidate's cost ever beats a full
Sort even when offered. Distinguishing those needs a live
`GOOPG_PGSHAPED_DP_TRACE=1` trace on one of these 6 queries, which this
measurement loop did not have scope left to run. `Q67` (WindowAgg, S2b-3)
and `Q49` (SETOP, S2b-4) and `Q63` (Subquery Scan) are each their own
unimplemented call site (no `electOrderedWindow`/SETOP-loop/subquery-scan
equivalent of `electOrderedGrouping` exists yet to even check for the same
bypass) and were already known-blocked before this update.

Practical upshot for this milestone: this update conclusively explains 5 of
14 witnesses and files their fix (M0141-S2b-7); the remaining 9 stay exactly
as blocked as before this update, with one new open question (the 6
join/scan witnesses' unexplained zero) added rather than resolved.

**exec-d verdict, definitively answered by this measurement**: still not
needed. Zero corpus queries reach the executor operator at all — not "reach
it but degrade for lack of spill/packed-retention/ctid", but never reach it.
exec-d stays deferred with no change to its ledger row.

**GOOPG_INCREMENTAL_SORT stays default-off.** Turning it on today is
provably a no-op (byte-identical plans, so also byte-identical row counts —
no regression risk either way), but a no-op default flip has no benefit to
justify the churn of a provenance-label change across every future capture.
Revisit once M0141-S2b-7 lands and the corpus is re-measured.

Gates run: `go build ./internal/optimizer/...` clean after the two comment
corrections this update made (`path.go`'s `PathIncrementalSort` doc comment,
`incrementalsortpaths.go`'s file-header "WHY GATED OFF BY DEFAULT" section —
both previously said `createPlanNode`/the executor lacked an arm, which
exec-a/b already fixed on 2026-09-17e/f; corrected to cite this update's
actual reason instead). No production logic changed — this update is
measurement plus two doc-comment corrections. `scripts/capture-tpcds.sh`
runs: `analysis/m0141/m0141-s7-witnesses-incsort-on-goopg.txt` (14-query
targeted capture, provenance-stamped, confirms live-server flag state via
`/proc/<pid>/environ`) and `analysis/m0141/m0141-s7-full99-incsort-on.txt`
(full 99-query capture, confirms the 0/14 result generalizes to 0/99).

Resume point: **M0141-S2b-7** (filed below, fix_plan) — wire
`electOrderedGrouping`'s own `addOrderedPaths` calls to a genuine candidate
set (`ordered.SearchCandidates`/`SearchCandidateKeys` populated from `cands`,
or a dedicated offer), the same posture S2b-2a/2b already established for
`createOrderedPaths`'s own call site. Re-run this measurement after it lands
to see how many of the 7 GroupAgg witnesses move.

**Update 2026-09-17i — M0141-S2b-7 LANDED, corpus re-measured: still 0/99,
but the bypass is now structurally CLOSED (proven by trace, not inferred).**
`upperorderedgrouping.go`'s `electOrderedGrouping` now populates
`ordered.SearchCandidates = cands` and `ordered.SearchCandidateKeys =
translated` (the same per-candidate slices it already computes for its own
no-sort/Sort-over election) right after the `anyTranslated` gate, with both
fields added to the existing save/restore-on-decline snapshot (the function's
own contract: a decline must leave `ordered` byte-identical to the loop never
having run, since the SAME `*RelOptInfo` — same registry, kind, relids,
tupleFraction — is reused by a subsequent plain `createOrderedPaths` call in
the `else` branch at `planner.go:1955-1965` when the loop declines, and that
call's own population is conditional on `searchedRelOf(input) != nil`, so a
leftover mutation would otherwise leak a GROUP_AGG-shaped candidate set into
an unrelated plain-Node ORDER BY). Also added the `*IncrementalSort` winner
shape to the function's `createPlanNode(best)` switch (previously only
`*Sort`/`*Aggregate` were handled; an Incremental-Sort win would have hit
`default: return restore("winner-shape-unexpected")` and silently thrown the
win away) — mirrors the existing `*Sort` case's one-level descend-to-`*Aggregate`
copy-back exactly, since `createIncrementalSortPlan` wraps exactly one child
built from the offered `PathAgg` candidate.

**Proof the bypass is closed, not just "no longer obviously wrong":** a
`GOOPG_INCREMENTAL_SORT=on GOOPG_PGSHAPED_DP_TRACE=1` server against Q3 (one
of the 5 GroupAgg witnesses) now emits, where before it emitted nothing for
this producer at all —

```
DPPATH path producer=upper.ordered.sort           relids=- kind=7  ... total=3730.8918 ... verdict=accepted
DPPATH path producer=upper.ordered.incrementalsort relids=- kind=20 ... total=3733.0068 ... verdict=dominated
```

— i.e. the third arm is reached and offers a real `PathIncrementalSort`
candidate (child = the Sorted `PathAgg`, `PresortedCount=1` — matching PG's
own `Presorted Key: dt.d_year` in `bench/tpcds/plans-pg/Q3.txt`, since the
GroupAggregate's group-key emission order is `(d_year, i_brand,
i_brand_id)` while the ORDER BY is `(d_year, sum(...) DESC, i_brand_id)` — only
the leading `d_year` column is a genuine positional match, `nCommon=1`). It
loses the tournament to the plain `upper.ordered.sort` candidate
(3733.0068 > 3730.8918) — **a cost question, not a structural one**, and
exactly the "necessary but likely not sufficient on its own" outcome this
task's fix_plan entry predicted (S2b-5/S2b-6's still-open Hashed-vs-Sorted
`PathAgg` cost-tie sits upstream of the SAME candidates this arm now offers).
Q3's own goopg plan shape also diverges from PG's well before the ORDER BY
node — goopg elects a Nested-Loop/Bitmap-Heap-Scan join with no Gather Merge
at all, where PG parallelizes a Sort under a Gather Merge below its own
GroupAggregate — so even a won Incremental Sort tournament here would not by
itself have produced a PG-matching plan; the cost-tie is one of several
compounding divergences for this query, not the last one.

**Corpus re-measurement**: `scripts/capture-tpcds.sh` against a fresh
`GOOPG_INCREMENTAL_SORT=on` SF0.25 server (flag state re-verified via
`/proc/<pid>/environ`, same discipline as the 2026-09-17h measurement) —
`analysis/m0141/m0141-s2b7-full99-incsort-on.txt`. `grep -c "Incremental
Sort"` = 0, still. `diff` against the pre-fix capture
(`analysis/m0141/m0141-s7-full99-incsort-on.txt`), header/tmp-path lines
aside, is **byte-identical** — the fix changes which candidates are offered
and priced, but not which one wins, on this corpus. `scripts/pg-plan-parity-diff.py`
against `bench/tpcds/plans-pg/`: `PLAN-PARITY: queries=99 match=2 shapediff=67
unparsed=0 missingnode=27 error=3 timeout=0`, `CATEGORIES-EXCL-MATCH:
join-order=91 join-method=69 scan-type=60 parameterisation=45
aggregation-strategy=71 sort-strategy=77 parallelism=84 qual-placement=20
rendering=23` — the M0137-0004 floor (TPC-DS match >= 2, Q9 + Q41) holds,
category counts unchanged (shape-delta=0 from the diff above already implies
this, confirmed rather than re-derived).

**What this means for the remaining 4 of the 5 GroupAgg witnesses (Q43,
Q54, Q60, Q89) and the 9 non-GroupAgg witnesses**: not separately traced this
loop (budget), but Q3's finding generalizes structurally — the arm is
reachable for all of them now (the populate-and-restore fix is unconditional,
not query-specific), so any of them still failing to flip is a cost or
upstream-shape question, the same category as Q3's, not a repeat of the
`SearchCandidates`-empty bypass. `M0141-S2b-6-resume` (real SF1 cardinalities
on the reloaded TPC-H cluster, gated on M0142-0003k) is the filed follow-up
for the cost-tie side; no new ledger row is needed for "which of the 9 flip
once the cost-tie resolves" since M0141-S2b-6-resume's own scope already
covers a Hashed-vs-Sorted `PathAgg` cost comparison that would affect these
too.

**GOOPG_INCREMENTAL_SORT stays default-off** — unchanged reasoning from the
2026-09-17h update: turning it on is still provably a no-op for this corpus
(now proven by trace to be an inert-but-reachable arm rather than an
unreachable one, which does not change the default-off verdict).

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...
./internal/executor/...` both green. `scripts/capture-tpcds.sh` (full 99,
flag-on, flag state verified against the live PID's `/proc/<pid>/environ`).
`scripts/pg-plan-parity-diff.py` against `bench/tpcds/plans-pg/` (floor
check, see numbers above). One traced single-query EXPLAIN (Q3) with
`GOOPG_PGSHAPED_DP_TRACE=1` to read the `DPPATH`/`DPGROUP` lines quoted
above. `make ralph-state-guard` passed. Pre-commit hook's pgbench smoke:
PASS. TPC-DS SF0.25 full row-count regression sweep was NOT separately
re-run: the diff-against-pre-fix-capture already shows byte-identical
EXPLAIN shapes corpus-wide (0 plan changes), which by construction implies 0
row-count changes — a sweep could not add evidence beyond what that diff
already establishes.

Resume point: **M0141-S2b-6-resume** (cost-tie, gated on M0142-0003k) is now
the direct blocker for moving any of the 14 Incremental-Sort witnesses,
having subsumed the structural blocker this update closes. A future loop
with that cluster reloaded should re-run this same corpus measurement to see
how many of the 5 GroupAgg witnesses (and possibly some of the 9 others,
which share upstream `PathAgg`/join cost machinery) flip once real
cardinalities separate the Hashed/Sorted cost tie.

## Update 2026-09-18 — cost-breakdown diagnosis for the remaining 13 witnesses (banner item 4)

Banner item 4 (`.ralph/fix_plan.md` Current Priority, written 2026-09-17):
"M0141-S7 — cost diagnosis only... Compare the cost breakdown with PG for the
14 queries. No executor work." Only Q3 had been traced so far (2026-09-17i
above). This update traces the remaining 13 on the private `:65437` TPC-DS
SF0.25 cluster (started with `GOOPG_INCREMENTAL_SORT=on
GOOPG_PGSHAPED_DP_TRACE=1`, both confirmed via `/proc/<pid>/environ` before
capturing — same discipline as every prior trace in this doc) against the 13
witness queries' own `.sql` files
(`bench/tpcds/runtime_goopg/tpcds-data/queries/query{N}.sql`, loaded into the
cluster's `postgres` database). Raw per-query psql output + the log-slice
captured between `wc -l` markers before/after each run: `tmp/m0141-s7-trace/`
(not committed — scratch, same precedent as every earlier probe in this file).
Recon-only: no `internal/`/`cmd/` file touched, `go build ./...` unaffected.

### Which of the 13 even reach the arm

Grepping each query's log slice for `producer=upper.ordered.(sort|
incrementalsort)`:

| witness | `upper.ordered.incrementalsort` line present? |
|---|---|
| Q4, Q11, Q43, Q54, Q58, Q60, Q83 | yes (7 of 13) |
| Q35, Q49, Q63, Q64, Q67, Q89 | **no** (6 of 13) |

For 5 of those 6 "no" cases the reason is already known and tracked, not a
new finding:

- **Q35** — its `ORDER BY` list is byte-identical to its `GROUP BY` list
  (`ca_state, cd_gender, cd_marital_status, cd_dep_count,
  cd_dep_employed_count, cd_dep_college_count`), so `addIncrementalSortPaths`'s
  own `contained` check (`incrementalsortpaths.go:166`) is true for the
  GROUP_AGG seed candidate — a full match is arm 1/2's case by design, not a
  gap arm 3 should fill (`incrementalsortpaths.go`'s own header: "a full
  match is arm 1's case for the seed... buys nothing new here").
- **Q63, Q67, Q89** — re-reading these three's SQL text (not just the design
  doc's earlier "shape" label, which named the *witness's PG plan's own*
  aggregate strategy) shows all three wrap their aggregate in an OUTER query
  whose `ORDER BY` sits above a **window function** (`avg(...) OVER
  (PARTITION BY ...)` for Q63/Q89, `rank() OVER (...)` for Q67) — this
  reclassifies Q89 from "GroupAggregate" to the same bucket as Q63/Q67. All
  three are **M0141-S2b-3b**'s stated gate ("TPC-DS Q67... is the sole corpus
  witness and the gate" — Q63/Q89 share the identical shape and are an
  implicit second/third witness for the same still-open task): `addWindowPaths`
  sees one collapsed input `Node`, never a `Pathlist`, so there is nothing for
  arm 3 to iterate over yet. Zero lines is exactly what an unbuilt call site
  produces — not a new defect.
- **Q49** — outer `UNION` (`Unique` node in PG's plan), `M0141-S2b-4`'s
  already-filed "SETOP rel-identity fix" gate, same unbuilt-call-site shape.

**Q64 is the one genuine open question.** Its classification in the table
above ("Nested Loop, outer side's own order") predicted it would behave like
its siblings Q4/Q11/Q35 — but grepping its full log slice (134106 lines, the
largest of the 13 — a 17-table self-join of two CTE references) for
`producer=upper.ordered` finds **exactly one line, the seed `sort`, and zero
`incrementalsort` attempts, zero of any other ordered-rel producer**. This
cannot be told apart, from the trace alone, between "`ordered.SearchCandidates`
is empty for this rel" and "every candidate's `SearchCandidateKeys` has
`len(keys)==0`" — `addIncrementalSortPaths`'s loop (`incrementalsortpaths.go:161-166`)
`continue`s silently in both cases; only an *offered* candidate ever emits a
DPPATH line. A plausible (unverified) hypothesis, consistent with
[[cte_leaves_reach_search_wrapped_in_filter]]: Q64's outer join is over two
references to the same materialized CTE (`cross_sales cs1, cross_sales cs2`),
and a CTE-scan boundary may be where the search's own ordering-claim tracking
(`SearchCandidateKeys`) gets lost for one or both self-join legs — the same
family of gap that memory names for a different mechanism (residual-filter
placement), not yet confirmed for pathkey propagation specifically. Filed as
**M0141-S7-cd-q64** below rather than root-caused now: distinguishing the two
cases needs either instrumenting `createOrderedPaths`'s candidate-count at
population time or a debugger session, both of which are `internal/`-file
changes even if read-only in spirit, and this update's mandate is diagnosis
without executor/planner-file edits.

### Cost breakdown for the 7 that DO reach the arm

Each row decomposes the incrementalsort-vs-sort **total cost gap** into two
parts: how much comes from the two arms pricing a **different input
candidate** (`inputtotal` field — arm 1/2's seed vs. whichever
`ordered.SearchCandidates[i]` arm 3 built its `PathIncrementalSort` over), vs.
how much comes from the **sort-vs-incrementalsort pricing formula itself**
(`total - inputtotal`, i.e. each arm's own added overhead). Numbers are the
DPPATH line's own `total`/`inputtotal` fields, cheapest instance per producer
when a query offered more than one (Q43/Q54/Q60 each show two, one per
Hashed/Sorted `PathAgg` seed — see M0141-S2b-6/-6-resume; only the eventual
tournament winner's numbers matter for "why did Sort win," so the table uses
the accepted/cheapest pair):

| witness | shape | Δinput (arm-3 seed − arm-1/2 seed) | Δoverhead (incsort own − sort own) | Δtotal | input-divergence share |
|---|---|---|---|---|---|
| Q4  | Nested Loop | ~0 (7.7250 both) | +0.040 | +0.040 | ~0% |
| Q11 | Nested Loop | +0.010 | +0.040 | +0.050 | ~20% |
| Q83 | Merge Join  | +0.055 | +0.040 | +0.095 | ~58% |
| Q58 | Merge Join  | +0.762 | +0.040 | +0.802 | ~95% |
| Q54 | GroupAgg (2-level, `Subquery Scan`-nested) | +0.545 | +0.445 | +0.990 | ~55% |
| Q60 | GroupAgg (`Merge Append`) | +1.021 | +0.599 | +1.620 | ~63% |
| Q43 | GroupAgg (`Finalize`, parallel in PG) | +198.790 | +0.220 | +199.010 | **~99.9%** |

(Q43's absolute numbers: Sort's seed totals 47505.972, Sort itself
47506.112 — a mere +0.140 own-overhead; IncrementalSort's seed totals
47704.762, IncrementalSort itself 47705.122 — a comparably small +0.360
own-overhead. The two arms are pricing their OWN sort step almost
identically; Sort wins here almost entirely because its seed candidate is
~199 cost units cheaper, not because `costIncrementalSort` misprices
anything.)

**Reading this table**: in every case but Q4 (and to a lesser extent Q11),
most-to-nearly-all of why Incremental Sort loses is that
`addIncrementalSortPaths` (`incrementalsortpaths.go:161`) iterates
`ordered.SearchCandidates` and can only stack a `PathIncrementalSort` over a
candidate whose own claimed ordering (`SearchCandidateKeys[i]`) shares a
**partial, non-full, non-empty** prefix with the required sort keys
(`incrementalsortpaths.go:166`: `if contained || nCommon == 0 { continue }`).
The single cheapest candidate at each of these rels apparently fails that
test — either it has no claimed ordering at all (parallel/hash-shaped, most
likely for Q43's `Finalize`-labelled PG counterpart, which not surprisingly is
the biggest gap: PG parallelizes this witness with a `Gather Merge`, which
goopg's own plan for the same query does not reach — see the 2026-09-17i
Q3 finding's identical caveat about compounding upstream divergences) or a
FULL match (already arm 1's case, so it never reaches arm 3's loop as a
distinct offer). The candidates arm 3 CAN attach to are, by construction,
the ones with a partially-useful existing order — which on this corpus are
consistently the pricier siblings. This is a sharper, corpus-wide version of
what 2026-09-17i already flagged as a caveat for Q3 alone; it does not
contradict `M0141-S2b-6-resume`'s open Hashed-vs-Sorted tie question (that
tie is specifically about which `PathAgg` STRATEGY the Sort/IncrementalSort
arms are each fed for the SAME query, and both arms in the table above
already reflect whichever strategy each individually cheapest option was) —
it is a distinct, corpus-general observation that no amount of resolving the
Hashed/Sorted tie can fix on its own, since it is about candidate-set
MEMBERSHIP (which candidates even have a usable partial order to credit),
not about the tie itself. Filed as **M0141-S7-cd-candidatepool** below.

### What this changes

Nothing production-facing (recon only, per the banner's "no executor work").
It refines the resume point: **M0141-S2b-6-resume** (real SF1 cardinalities)
answers "does the Hashed-vs-Sorted tie flip with real data," which is
necessary but this update shows it is **not sufficient** even if it flips —
Q43's ~199-unit gap dwarfs anything a tie-break could produce, and it comes
entirely from candidate-set membership, a question SF1 cardinalities cannot
answer by themselves. `GOOPG_INCREMENTAL_SORT` stays default-off (unchanged
verdict). No ledger row: this is pure diagnosis of already-declined/deferred
scope, not a new gap discovered outside the ledger's own definition (every
component named above already has a filed, tracked task).

Gates run: none beyond the trace captures themselves — no production file
touched. `make ralph-state-guard` passed at commit time. Pre-commit hook's
pgbench smoke: PASS.

## Update 2026-09-18b — M0141-S7-cd-q64 root-caused: Q64 was miscategorized,
not under-served

The prior update left Q64 undecided between two hypotheses ("SearchCandidates
empty" vs. "every candidate's keys `len==0`"), both reachable only by
instrumenting `createOrderedPaths`'s population step — an `internal/`-file
change that update's own diagnosis-only mandate declined. This update lands
that instrumentation (two new DPPATH producer strings, both gated on the
existing `pathTraceEnabled`/`GOOPG_PGSHAPED_DP_TRACE=1` flag so the change is
inert with the flag at its default-off setting — same posture as every other
groundwork step in this file):

- `traceOrderedCandidatePopulation` (`pathtrace.go`), called from
  `createOrderedPaths` (`upperordered.go:109-131`) right where
  `RelOptInfo.SearchCandidates`/`SearchCandidateKeys` are populated: emits
  `producer=upper.ordered.candidates searchedrel=<bool> candidates=<n>
  nonemptykeys=<n>` — settling exactly the ambiguity the prior update left
  open.
- `traceIncrementalSortCandidate` (`pathtrace.go`), called from
  `addIncrementalSortPaths`'s per-candidate loop
  (`incrementalsortpaths.go:160-163`) right after
  `pathkeysCountContainedIn`: emits `producer=upper.ordered.incrementalsort.candidate
  index=<i> kind=<PathKind> keys=<len> contained=<bool> ncommon=<n>` for
  every candidate considered, whether or not it survives to an offer — the
  finer sibling `M0141-S7-cd-candidatepool` (below) also benefits from,
  since it answers "which candidates were even in the running" per query
  without a debugger attach.

### How Q64 was traced without touching `:65437`

The RALPH_LOOP guard (added since the 2026-09-17h/2026-09-18 traces in this
file, which DID restart `:65437` directly) now blocks restarting any shared
TPC-DS server from this loop. Traced instead on a fully private, throwaway
cluster: `go build -o tmp/m0141s7cdq64/goopg ./cmd/goopg`, `goopg init -D
tmp/m0141s7cdq64/data`, started via `scripts/goopg-test-run.sh` on port 5533
(cgroup-capped, own `GOOPG_CG_UNIT`) with `GOOPG_PGSHAPED_DP_TRACE=1
GOOPG_INCREMENTAL_SORT=on` confirmed via `/proc/<pid>/environ` before
capturing — same discipline as every prior trace in this doc, just on a
private port instead of `:65437`. Schema + the already-sampled SF0.25 TSVs
(`bench/tpcds/runtime_goopg/tpcds-data-sf025/*.tsv`, git-untracked scratch
already on disk from a prior `tpcds-sf025-regression.sh build-data` run) were
loaded fresh into this private cluster's own `postgres` database via the same
`tpcds.sql` + per-table `COPY ... FROM` + `ANALYZE` sequence
`cmd_load_goopg` (`scripts/tpcds-sf025-regression.sh:412`) uses against
`:65437` — no read from, or write to, the live `:65437` data directory at
any point. `bench/tpcds/runtime_goopg/tpcds-data/queries/query64.sql` (the
SF1 query text; TPC-DS query bodies are scale-independent, only the tables'
row counts differ) was then run against it directly.

### The trace result

```
DPPATH candidates producer=upper.ordered.candidates searchedrel=true candidates=7 nonemptykeys=6
DPPATH path producer=upper.ordered.sort ... pathkeys=5 verdict=accepted ...
DPPATH candidate producer=upper.ordered.incrementalsort.candidate index=0 kind=4 keys=3 contained=false ncommon=0
DPPATH candidate producer=upper.ordered.incrementalsort.candidate index=1 kind=4 keys=3 contained=false ncommon=0
DPPATH candidate producer=upper.ordered.incrementalsort.candidate index=2 kind=4 keys=3 contained=false ncommon=0
DPPATH candidate producer=upper.ordered.incrementalsort.candidate index=4 kind=4 keys=3 contained=false ncommon=0
DPPATH candidate producer=upper.ordered.incrementalsort.candidate index=5 kind=4 keys=3 contained=false ncommon=0
DPPATH candidate producer=upper.ordered.incrementalsort.candidate index=6 kind=4 keys=3 contained=false ncommon=0
```

**Neither of the prior update's two hypotheses holds.** `searchedRelOf(input)`
returns a real rel (`searchedrel=true`); `SearchCandidates` has 7 entries, 6
with a non-empty validated ordering claim (`nonemptykeys=6`, matching the 6
traced candidates — index 3 is the seventh, the one with `len(keys)==0`,
which the loop's own `continue` skips before reaching
`traceIncrementalSortCandidate`). Every one of those 6 is `kind=4`
(`PathMergeJoin`) carrying a 3-key ordering claim, and every one scores
`ncommon=0` against the outer query's 5 sort keys — **zero shared columns,
not a partial-prefix miss**. So `addIncrementalSortPaths`'s `nCommon == 0`
branch is exactly what fires, on every candidate, every time.

### Why zero is the CORRECT answer here — Q64 was never an outer-ORDER-BY
witness at all

Re-reading `query64.sql` (`bench/tpcds/runtime_goopg/tpcds-data/queries/query64.sql`)
side by side with PG's plan (`bench/tpcds/plans-pg/Q64.txt`) resolves why:
the query has **two separate, unrelated sort-shaped points**, and the
`Incremental Sort` node PG's plan actually contains belongs to the one this
whole investigation (M0141-S7-cd-q64/candidatepool, and — going back further
— the original 2026-09-16/17 witness classification's own "Nested Loop /
`item.i_item_sk`" row for Q64, quoted near the top of this file) was never
looking at:

1. The **outer** query (`select ... from cross_sales cs1, cross_sales cs2
   where ... order by cs1.product_name, cs1.store_name, cs2.cnt, cs1.s1,
   cs2.s1`) is what reaches `createOrderedPaths` — its `ORDER BY` is over
   **aggregate OUTPUT columns of the CTE** (`product_name`, `store_name`,
   `cnt`, `s1` are all `SELECT`-list items of `cross_sales`'s own
   `GROUP BY`/aggregate clause, not raw join columns). In PG's own plan this
   level is satisfied by a **plain `Sort`** — not an Incremental Sort at
   all; PG's own planner reaches the identical `nCommon == 0` conclusion,
   it just has no separate DPPATH-style trace to say so explicitly.
2. The `Incremental Sort` node PG's plan DOES contain
   (`Presorted Key: item.i_item_sk`) sits **inside the CTE's own
   definition**, feeding `cross_sales`'s `GroupAggregate` — a completely
   different planning stage (`cross_sales`'s inner `GROUP BY
   i_product_name, i_item_sk, s_store_name, ...` needs its input sorted by
   the group key, and PG's chosen Nested-Loop-shaped input happens to
   already deliver a leading prefix of that group key, `item.i_item_sk`,
   for free). This is a **sorted-GroupAgg input-sort** decision, made by
   whichever machinery elects the CTE's own aggregate strategy — nothing
   `createOrderedPaths`/`addOrderedPaths` (a statement's outer `ORDER BY`
   only) is ever called for.

So the original witness-classification table's "Q64 → Nested Loop →
`item.i_item_sk`" row was reading the RIGHT plan node but filing it under the
WRONG mechanism family: it is CTE-internal-aggregation-shaped (the same
family as the 5 `GroupAggregate` witnesses already tracked under
**M0141-S2b-0**/**M0141-S2b-7**), not outer-ORDER-BY-shaped (the
**M0141-S2b-2**/**2a**/**2b**/**2c** `Nested Loop`/`Merge Join` family Q64 had
been grouped with since the 2026-09-16 NARROWED update). The `createOrderedPaths`
call this update traced is doing exactly the right thing for the outer
`ORDER BY` it is actually responsible for — the outer ORDER BY's columns are
aggregate outputs no join-shaped candidate could ever carry a prefix of, so
`nCommon == 0` on every candidate is the CORRECT verdict, not a gap. **There is
no outer-ORDER-BY fix for Q64 to make** — M0141-S7-cd-q64's own premise (that
Q64 has an unexplained miss at this call site) does not survive contact with
the query text.

**Consequence for the corpus tally**: the "Nested Loop x4 (Q4, Q11, Q35, Q64)"
row in this file's producer-shape table (and the "6 of 13" no-incrementalsort
tally in the prior update) should read Nested Loop x3 (Q4, Q11, Q35) once Q64
is moved to the CTE-internal-aggregation bucket — **not implemented in this
recon-only loop** (re-tallying and deciding whether Q64 is a genuinely new
6th member of the GroupAggregate family, or already covered by
M0141-S2b-0/S2b-7's own scope, needs its own pass over `electOrderedGrouping`'s
CTE-materialization callers, which is out of this update's one-task budget).
Filed as **M0141-S7-cd-q64-reclassify** below. `M0141-S7-cd-candidatepool`'s
own 7-witness table is unaffected: Q64 was never one of the 7 "reach the arm"
witnesses it covers.

`GOOPG_INCREMENTAL_SORT` stays default-off (unchanged verdict; this update
only adds trace lines, both gated on the pre-existing `GOOPG_PGSHAPED_DP_TRACE`
flag, so the ORDERED tournament is byte-identical to before at every default
setting). No ledger row: this closes an open recon question with a definite
answer using only already-filed mechanism names, and does not itself leave
anything newly unimplemented — the follow-up reclassification task is filed
directly in `fix_plan.md`, same as every other recon task in this
programme.

Gates run: `go build ./...` clean; `go vet ./internal/optimizer/`; `go test
./internal/optimizer/...` PASS (pre-existing suite, unchanged — the two new
trace functions have no test of their own by design, same as
`tracePath`/`formatPathLine`'s sibling coverage via the existing DPPATH
consumers, since the property under test is "identical output with the flag
off," not the trace TEXT itself). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS. No TPC-H dependency. The private
`tmp/m0141s7cdq64/` cluster was stopped (not left running) before this
commit; `:65437`/`:65438` were never started, stopped, or restarted by this
loop — confirmed via `bench/tpcds/server.sh status` before and after.

## Update 2026-09-18c — M0141-S7-cd-q64-reclassify: Q64's gap is NOT covered
by the tracked GroupAggregate family — new task filed

The prior update's (a)/(b)/(c) decision tree needed one fact:
does `electOrderedGrouping`'s CTE-materialization caller shape already cover
a CTE's own internal aggregate-strategy election, the same way
M0141-S2b-0/S2b-7 cover a top-level statement's? Answer, from reading (not
tracing — no server needed for this half): **the question as posed doesn't
apply, because `electOrderedGrouping` is structurally irrelevant to
`cross_sales`'s own planning pass, independent of caller wiring.**

`preplanWithClause` (`with.go:210`) does plan a CTE's body through the exact
same `planSelectWithSettings` function the outer statement uses (`body, err
:= planSelectWithSettings(cte.Query, cat, ps, scope)` — confirmed by
reading, `cte.Query` is `cross_sales`'s own `parser.SelectStmt`). But
`electOrderedGrouping`'s call site (`planner.go:1960`) sits inside the block
that only runs `if len(s.OrderBy) > 0` (`planner.go` ~1870-1965, keys built
from `s.OrderBy`) — and `cross_sales`'s own `SELECT` has **no `ORDER BY`
clause at all** (`bench/tpcds/runtime_goopg/tpcds-data/queries/query64.sql`:
the CTE body ends at `GROUP BY ... d3.d_year`, no `ORDER BY`; only the outer
statement referencing `cross_sales cs1, cross_sales cs2` has one). So
`electOrderedGrouping` never runs for the CTE body regardless of whether its
caller reaches it — there is no ORDER BY for it to adjudicate.

**The actual mechanism PG's `Presorted Key: item.i_item_sk` / `GroupAggregate`
node exercises is a different one: choosing a sorted-input GROUP BY strategy
with no ORDER BY in the query at all**, purely because the join order PG
picked already happens to deliver (or partially deliver, via Incremental
Sort) the group key's order more cheaply than a HashAgg. goopg's equivalent
decision point is `addGroupingPaths` (`groupingpaths.go:379`), specifically
its SORTED arm (`groupingpaths.go:441-495`, gated on `aggNode.GroupingSets ==
nil`): when the presorted-keys shortcut doesn't apply, it always calls
`sortPathForBounded(seed, pathkeysForSortKeys(keys), cp, -1)`
(`groupingpaths.go:474`) — and `sortPathForBounded`
(`joinpathsmerge.go:480-515`) unconditionally builds a `PathSort` (full
`Kind: PathSort`) over the seed. It never inspects `seed.Pathkeys` for a
partial prefix match against the group keys the way
`addIncrementalSortPaths` (the M0141-S7 third arm) does for the OUTER
ORDER BY case — there is no Incremental-Sort-over-partially-ordered-seed
candidate offered to `addGroupingPaths`'s SORTED arm at all, for ANY query,
CTE-internal or not.

This is genuinely **case (c)** from the prior update's decision tree: NOT
covered by M0141-S2b-0/S2b-7's scope (which is specifically about
`electOrderedGrouping`'s handling of a GROUP BY that also has an ORDER BY —
an entirely different call site and condition than plain `addGroupingPaths`
choosing between Hashed and Sorted for a GROUP BY with no ORDER BY at all).
Q64 does NOT become a 6th witness of the already-tracked GroupAggregate
family (that family's fix — `electOrderedGrouping` populating
`SearchCandidates` — has nothing to do with `addGroupingPaths`, a different
file, a different call site, reached only when there is no outer ORDER BY at
all to route through `electOrderedGrouping` in the first place).

**Corpus tally correction**: the producer-shape table above ("Nested Loop
x4: Q4, Q11, Q35, Q64") should read **Nested Loop x3 (Q4, Q11, Q35)** — Q64
is not an outer-ORDER-BY `Nested Loop` witness (Update 2026-09-18b) and is
now confirmed not a member of the 5-query GroupAggregate family either. It
is the sole known witness (so far) of a newly-named gap: **`addGroupingPaths`'s
SORTED arm has no Incremental-Sort-over-partial-prefix-seed offer.** Filed
as **M0141-S2b-8** in `fix_plan.md` (M0141 section), scoped as an
implementation task (not recon) since closing it requires an actual new
candidate-builder arm in `groupingpaths.go`, consistent with this
milestone's item-4 "cost diagnosis only, no executor work" restriction —
the task is filed, not worked, this loop.

`GOOPG_INCREMENTAL_SORT` stays default-off (no code changed this update —
read-only recon closing an open question, same posture as 18b). No ledger
row: this closes M0141-S7-cd-q64-reclassify's own open question with a
definite, code-read-confirmed answer and files its own follow-up task
directly, the same pattern 18b used for M0141-S7-cd-q64-reclassify itself.

Gates run: none needed (no code changed; `go build ./...` re-confirmed
clean as a courtesy, no test/gate re-run required for a docs+fix_plan-only
change). No TPC-H/TPC-DS dependency — no server started, no cluster touched.

## Update 2026-09-18d — M0141-S7-cd-candidatepool: the seed itself carries an
unexploited partial-prefix claim on the createOrderedPaths-mechanism side
(Q4); the electOrderedGrouping-mechanism side (Q43/Q54/Q60) has no gap

The task asked two things: (a) does the cheap seed always structurally fail
`addIncrementalSortPaths`'s partial-prefix test (nothing to fix), or (b) does
it sometimes carry a genuine usable partial key the loop is simply never
offered because of how `ordered.SearchCandidates` is populated? Reading
`createOrderedPaths`/`electOrderedGrouping` side by side first surfaced a
fact the prior updates hadn't separated: **the 7-witness corpus splits across
two entirely different `addOrderedPaths` callers**, each populating
`SearchCandidates` a different way:

- `createOrderedPaths` (Q11/Q58/Q83, and Q4) populates `SearchCandidates` from
  `searchedRelOf(input).Pathlist` — the join search's OWN candidate pool for
  the relset that produced `input`.
- `electOrderedGrouping` (Q43/Q54/Q60 — the Hashed-vs-Sorted GroupAgg tie
  family, M0141-S2b-0/S2b-7) populates `SearchCandidates` **directly from its
  own `cands` slice** (the Hashed/Sorted `PathAgg` candidates), explicitly
  because — its own comment says — `searchedRelOf(input)` "does not exist
  here (the input is a GROUP_AGG rel's PathAgg, never a searched join/scan
  root)".

Verifying which of (a)/(b) applies needed more than the existing
`traceIncrementalSortCandidate`/`traceOrderedCandidatePopulation` lines:
neither one reports what the SEED's *own* claim is, so there was no way to
compare the seed against the SearchCandidates entries side by side. Landed
`traceOrderedSeedCandidate` (`pathtrace.go`), called from `addOrderedPaths`
(`upperordered.go`) right before the arm-1 containment check — same
`pathTraceEnabled`-gated, inert-by-default shape as the two existing trace
functions — emitting the seed's own `kind`/`keys`/`contained`/`ncommon`
**plus `totalcost`** on both this new function and
`traceIncrementalSortCandidate` (a small additive change to the latter's
signature): cost is what lets a same-shaped seed/candidate pair be told apart
from a coincidence, since every entry sharing one `ordered.SearchCandidates`
list also shares the same relset and therefore the same `Rows` — cost is the
only field that discriminates "this really is the same Path" from "this just
happens to have the same key count".

### Trace setup

Same private-cluster discipline as M0141-S7-cd-q64 (the RALPH_LOOP guard
still blocks restarting `:65437` directly): `go build -o
tmp/m0141s7candpool/goopg ./cmd/goopg`, `goopg init -D
tmp/m0141s7candpool/data`, started via `scripts/goopg-test-run.sh` on port
5533 (cgroup-capped, own `GOOPG_CG_UNIT`) with `GOOPG_PGSHAPED_DP_TRACE=1
GOOPG_INCREMENTAL_SORT=on` (confirmed via `/proc/<pid>/environ`). Schema +
the same already-sampled SF0.25 TSVs
(`bench/tpcds/runtime_goopg/tpcds-data-sf025/*.tsv`) loaded into this
cluster's own `postgres` database via the standard `tpcds.sql` + per-table
`COPY` + `ANALYZE` sequence. All seven witnesses
(`bench/tpcds/runtime_goopg/tpcds-data/queries/query{4,11,43,54,58,60,83}.sql`)
were `EXPLAIN`'d directly against it; no read from or write to `:65437`/
`:65438` at any point.

### Trace result (seed vs. SearchCandidates, cost-annotated)

```
Q4:  seed  kind=0 keys=1 contained=false ncommon=1 totalcost=13.7925
     cand2 kind=4 keys=1 contained=false ncommon=1 totalcost=13.7825   <- closest, NOT equal
Q11: seed  kind=0 keys=0 contained=false ncommon=0 totalcost=6.7775    <- no claim at all
Q43: seed  kind=6 keys=2 contained=false ncommon=2 totalcost=47682.78310407106
     cand1 kind=6 keys=2 contained=false ncommon=2 totalcost=47682.78310407106  <- EXACT match
Q54: seed  kind=6 keys=1 contained=false ncommon=1 totalcost=1.150537478050103
     cand1 kind=6 keys=1 contained=false ncommon=1 totalcost=1.150537478050103  <- EXACT match
Q58: seed  kind=0 keys=0 contained=false ncommon=0 totalcost=4.78              <- no claim at all
Q60: seed  kind=6 keys=1 contained=false ncommon=1 totalcost=2.9476245279653694
     cand1 kind=6 keys=1 contained=false ncommon=1 totalcost=2.9476245279653694 <- EXACT match
Q83: seed  kind=0 keys=0 contained=false ncommon=0 totalcost=3.5725            <- no claim at all
```

(Full stderr captures: `/tmp/candpool2-q{4,11,43,54,58,60,83}.explain.log`
against `/tmp/candpool-start2.log`, this loop's scratch — not committed.)

### Reading the result: two different verdicts for two different mechanisms

**`electOrderedGrouping` side (Q43/Q54/Q60) — answer (a), no gap.** Every
time the loop iterates to the Sorted-agg candidate (`cands[1]`, the one with
a non-empty translated key), the `offer` it builds (`offer := *c; offer.Pathkeys
= translated[i]`) IS a shallow copy of that same candidate — so the "seed"
`addOrderedPaths` receives on that iteration and the `SearchCandidates[1]`
entry the arm-3 loop later re-examines are cost-identical, not just
shape-identical (47682.78310407106 / 1.150537478050103 / 2.9476245279653694,
exact bit-for-bit matches across all three witnesses). `electOrderedGrouping`
was already engineered (M0141-S2b-7) specifically to make its own candidate
pool double as `SearchCandidates`, and this trace confirms that engineering
does what it says: whenever the seed itself carries a partial claim, an
Incremental Sort candidate over it is already built and already costed
(and already loses to the plain Sort in these three cases per the
2026-09-18 update's cost table — a cost-model finding, not a coverage gap).
**Nothing to fix for this family.**

**`createOrderedPaths` side, no-claim witnesses (Q11/Q58/Q83) — answer (a),
no gap.** The seed's own `keys=0`: the cheapest-by-fraction join order
literally has no natural ordering to offer a partial prefix from (this is
the "hash-shaped" case the task's own (a) branch anticipated, generalized to
any Kind with an empty pathkeys claim, not just hash joins specifically —
here it is `PathPrebuilt`-wrapped output of a plan that happens to carry no
propagated order). Other, pricier `SearchCandidates` entries DO carry
partial claims (e.g. Q83's `index=0`, `ncommon=1`) and are correctly offered
— and correctly lose on cost, the same "input-candidate divergence"
already tracked. **Nothing to fix for these three.**

**`createOrderedPaths` side, Q4 — answer (b), confirmed gap.** Q4's seed has
`keys=1 ncommon=1`, a genuine, non-empty, non-full partial-prefix claim — the
exact shape `addIncrementalSortPaths` exists to exploit. But no
`SearchCandidates` entry matches its cost exactly: the closest,
`candidate[2]`, is 0.01 cheaper (13.7825 vs. 13.7925). This is not noise —
Q4 carries a `LIMIT 100` (`query4.sql:113`), so the seed was chosen by
`getCheapestFractionalPath`'s **fractional** (startup-weighted) cost metric,
not raw `Total`; `candidate[2]` is a genuinely different Path (better on raw
Total, presumably worse on Startup, which is why the fractional metric didn't
pick it) that merely happens to share the same 1-key ordering claim. The
seed itself — the one `sortPathForBounded` actually builds arm-2's full Sort
over — is confirmed **absent** from `ordered.SearchCandidates`: that list is
literally `sr.Pathlist`, populated independently of whichever entry
`getCheapestFractionalPath` picked to become `input`, so
`addIncrementalSortPaths`'s loop can only ever attach a `PathIncrementalSort`
to one of `sr.Pathlist`'s OTHER members — never to the seed itself, even when
the seed's own claim would qualify. **Real, structural, confirmed gap.**

### Disposition

`GOOPG_INCREMENTAL_SORT` stays default-off; no cost-model or plan-shape
change lands this loop (banner item 4 restricts M0141-S7 to cost diagnosis).
The confirmed Q4-side gap is filed as **M0141-S2b-9** below: teach
`addIncrementalSortPaths` (or its caller) to also score `input` itself
against `sortPathkeys` and offer a `PathIncrementalSort` built over the seed
when `0 < nCommon < len(sortPathkeys)` — using the SAME fractional-cost-aware
comparison `getCheapestFractionalPath` already applies, since Q4's own LIMIT
is exactly why a naive Total-cost seed-candidate would be the wrong thing to
build here. No ledger row (same posture as 18b/18c: a filed follow-up task
carries the deferral, not an undocumented gap).

Gates: `go build ./...` clean, `go vet ./internal/optimizer/` clean, `go test
./internal/optimizer/...` PASS (existing `captureTrace`-based tests in
`upperordered_test.go`/`incrementalsortpaths_test.go` still compile against
the two trace functions' extended signatures — no assertion on the new
`totalcost=` field was needed since none of those tests parse trace output
by field count). `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS. No TPC-H dependency; the private port-5533 cluster and its throwaway
`tmp/m0141s7candpool/` tree are scratch, stopped and left for the next loop
to reap or reuse (not committed, `tmp/` is git-ignored).

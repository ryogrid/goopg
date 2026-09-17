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

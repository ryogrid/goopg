# M0141-S7 — re-adjudicate Incremental Sort under the plan-parity goal, and scope it

Status: accepted — scoping recon (landed `c69c57e77`, 2026-09-16, no
production change *in this doc's own task*). No longer "no production
change" overall: the implementation this doc scoped landed through its
sub-tasks — M0141-S7's own groundwork (`fd5537907`, `65351372a`,
`ff5234006`), M0141-S2b-2c (`9c4884d36`), M0141-S7-exec-a/b/c
(`99a0a39a6`, `9697099c6`, `c3f10a329`), the corpus measurement
(`004f02bf1`, `7c27145b1`) and the three `-cd-*` trace tasks
(`073ab2748`, `18390e924`, `c7e231ae1`). See the index below; the
M0141-S7 fix_plan line itself is still unchecked (the feature is built
but wins nothing on the corpus, and the flag stays default-off)

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

## Index of the split-out sub-task documents

This doc is the scoping recon only. Everything that landed through it lives in
one document per `.ralph/fix_plan.md` task id (D3: one design doc per task id,
each under 800 lines):

| document | task id | what it covers |
|---|---|---|
| [m0141-s7-groundwork-and-exec-rescope.md](m0141-s7-groundwork-and-exec-rescope.md) | M0141-S7 (+ M0141-S2b-2c) | Finding 3 rows 1-3: `pathkeysCountContainedIn`, `costIncrementalSort`, and `addOrderedPaths`'s third arm `addIncrementalSortPaths` (gated off behind `GOOPG_INCREMENTAL_SORT`); then the 2026-09-17d re-scope that found row 5's real integration surface and split it into exec-a/b/c/d. |
| [m0141-s7-exec-a-incremental-sort-node-and-operator.md](m0141-s7-exec-a-incremental-sort-node-and-operator.md) | M0141-S7-exec-a | The `IncrementalSort` optimizer Node type and the `incrementalSortOp` executor operator, standalone-tested; plus the two `operators_explain.go` arms the package's hard coverage gates forced early. |
| [m0141-s7-exec-b-end-to-end-reachability.md](m0141-s7-exec-b-end-to-end-reachability.md) | M0141-S7-exec-b | `Path.PresortedCount` (stash, not re-derive), `createIncrementalSortPlan`, the `executor.go` builder arm, and the four `*optimizer.Sort` tree-walkers mirrored — the correctness gate before the flag may be turned on. |
| [m0141-s7-exec-c-explain-presorted-key.md](m0141-s7-exec-c-explain-presorted-key.md) | M0141-S7-exec-c | PG's `show_incremental_sort_keys` oracle and the shared `sortKeyParts` helper rendering the undecorated `Presorted Key:` line. |
| [m0141-s7-corpus-measurement-and-cost-diagnosis.md](m0141-s7-corpus-measurement-and-cost-diagnosis.md) | M0141-S7 (+ M0141-S2b-7) | The flag-on corpus measurement (0/14 and 0/99) and its root cause (`electOrderedGrouping` bypassed `SearchCandidates`), S2b-7's landing and the re-measurement that stayed 0/99 with the arm proven reachable by trace, then banner item 4's cost breakdown for the remaining 13 witnesses. |
| [m0141-s7-cd-q64-root-cause.md](m0141-s7-cd-q64-root-cause.md) | M0141-S7-cd-q64 | Env-gated candidate-population/candidate-consideration traces, and the finding that Q64's `Incremental Sort` is CTE-internal, so `nCommon == 0` at the outer ORDER BY is the correct answer, not a gap. |
| [m0141-s7-cd-q64-reclassify-grouping-paths-gap.md](m0141-s7-cd-q64-reclassify-grouping-paths-gap.md) | M0141-S7-cd-q64-reclassify | Where Q64 actually belongs: case (c), a new gap in `addGroupingPaths`'s SORTED arm, filed as M0141-S2b-8; the Nested Loop tally drops to x3. |
| [m0141-s7-cd-candidatepool-seed-partial-prefix.md](m0141-s7-cd-candidatepool-seed-partial-prefix.md) | M0141-S7-cd-candidatepool | Seed-vs-`SearchCandidates` cost-annotated trace across the 7 arm-reaching witnesses: no gap on the `electOrderedGrouping` side or for the no-claim seeds, one real structural gap for Q4, filed as M0141-S2b-9. |

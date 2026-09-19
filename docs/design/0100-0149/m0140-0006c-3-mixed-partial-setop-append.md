# M0140-0006c-3 — Mixed partial/claimed-whole SetOp Append (`pa_subpaths` arm)

Task: `.ralph/fix_plan.md` **M0140-0006c-3**. Parent chain: M0140-0006 →
0006a → 0006b → 0006c → 0006c-2 (branch-kind widening) → **0006c-3 (this
doc)**. Sibling doc: `m0140-0006c-2-join-branch-partial-setop-admission.md`
(§"Remaining-branch scoping" covers the partial-branch kinds).

Status: **implemented** — see §"Implementation record" at the bottom for
what landed, including two places the implementation deliberately diverged
from this scoping plan (`nppath` source; boundary-wrapper handling).

## PG oracle mechanics

### Planner: which children go where (`allpaths.c:1329-1453`)

`add_paths_to_append_rel` maintains three lists per appendrel:
`partial_subpaths` (pure arm — every child MUST offer a partial path or
`partial_subpaths_valid` dies), `pa_partial_subpaths` / `pa_nonpartial_subpaths`
(mixed arm — per-child pick). Per child (`allpaths.c:1412-1453`):

- `nppath = get_cheapest_parallel_safe_total_inner(childrel->pathlist)` —
  cheapest **parallel-safe** non-partial total path (a plan that may legally
  run inside a worker).
- Neither partial nor parallel-safe path → `pa_subpaths_valid = false`;
  the whole mixed arm is dead.
- Partial path wins iff `nppath == NULL ||
  cheapest_partial_path->total_cost < nppath->total_cost` — **strictly**
  cheaper; ties land in the non-partial list.
- Otherwise `nppath` → `pa_nonpartial_subpaths`.

### Mixed arm (`allpaths.c:1588-1627`)

- Condition: `pa_subpaths_valid && pa_nonpartial_subpaths != NIL`. The
  partial list may even be EMPTY — an all-claimed Append is legal (workers
  divide branches, not rows).
- `parallel_workers = Max(max over partial children's workers,
  pg_leftmost_one_pos32(#children)+1)`, capped by
  `max_parallel_workers_per_gather`. Non-partial children contribute
  nothing to the worker count — the log₂ bump exists precisely for them.
- `create_append_path(root, rel, pa_nonpartial, pa_partial, ..., workers,
  true, partial_rows)` — the final arg overrides `path.rows` with the pure
  arm's estimate **when the pure arm also ran** (`partial_rows` stays
  undefined/`-1` otherwise and `cost_append`'s own sum stands).

### Ordering and `first_partial_path` (`pathnode.c:1303-1422`)

- `subpaths = nonpartial ++ partial`; `first_partial_path = len(nonpartial)`
  — the executor's claim boundary.
- Non-partial sorted by `total_cost` DESC (workers claim expensive branches
  first → minimises makespan); partial sorted by `startup_cost` DESC
  (workers take expensive-startup partials first; the leader prefers the
  cheapest startup).
- Single-child Append inherits the child's cost/rows wholesale when
  `parallel_aware` matches (no-op Append, discarded by setrefs).

### Costing (`costsize.c:2168-2243`, `2250-2403`)

- `cost_append` parallel-aware arm: `startup` = min startup over the first
  `parallel_workers` subpaths. Rows: non-partial child contributes
  `rows / parallel_divisor`; partial child contributes
  `rows * (child_divisor / append_divisor)` — then the `partial_rows`
  override above can replace the whole sum.
- `total_cost` = Σ partial children's totals + `append_nonpartial_cost` +
  `cpu_tuple_cost * APPEND_CPU_COST_MULTIPLIER * rows`.
- `append_nonpartial_cost` is a greedy **LPT scheduling simulation**:
  `min(workers, n_nonpartial)` worker buckets; walk non-partial children in
  descending cost, each into the currently-lightest bucket; return the
  heaviest bucket. With exactly ONE non-partial child (goopg's only
  reachable case) it reduces to that child's `total_cost`.

### Executor (`nodeAppend.c`)

- `as_first_partial_plan` boundary mirrors `first_partial_path`.
- Non-partial plans: **exclusive whole-plan claim** — a worker CASes
  `pa_finished`; the claimer runs the plan alone, start to finish.
- Partial plans: **cooperative** — multiple workers may select the same
  in-flight partial plan and each emits its share; `pa_finished` set on
  completion.
- Every participant sets up (ExecInit) every subplan regardless; a worker
  that exhausts its claims joins the partial plans — claim state is shared,
  execution is per-worker.

## Corpus witness

**Q5 is the sole witness** (verified `bench/tpcds/plans-pg/`): its third
`Parallel Append` mixes a **non-partial** `Subquery Scan -> Hash Right Join`
branch with a partial seq-scan branch. No other TPC-H/TPC-DS plan in the
corpus shows a mixed Parallel Append. The branch `Subquery Scan` node is a
label, not a barrier — PG's `SubqueryScan` sits atop the child's real plan.

## goopg gap map

The pure-partial arm exists (`addPartialSetOpPath`,
`internal/optimizer/windowsetoppaths.go:525`, M0140-0006b) guarded by
`len(left.PartialPathlist)==0 || len(right.PartialPathlist)==0` at
:550-555. The mixed arm slots in as a second, independent producer body in
the same function. Per-file changes:

### Planner — `windowsetoppaths.go` / `path.go`

1. Per-branch pick (PG `allpaths.c:1412-1453`): `bp =
   branch.PartialPathlist[0]` (if any) vs `bnp =` cheapest `ParallelSafe`
   member of `branch.Pathlist` (goopg's equivalent of
   `get_cheapest_parallel_safe_total_inner`; branches here are
   unparameterised so the `_inner` nuance is moot — verify at impl).
   Pick `bp` iff `bnp == nil || bp.Cost.Total < bnp.Cost.Total`; else `bnp`
   → claimed-whole slot. A branch with neither kills the arm.
2. Require ≥1 claimed-whole pick (PG's `pa_nonpartial_subpaths != NIL`).
   goopg's two-child shape gives ≤1 such branch; the all-claimed
   (both-nonpartial) case is legal in PG and falls out naturally —
   decide at impl whether to admit it in the same slice or gate it.
3. `Path` needs a per-branch partialness marker — positional `Children`
   alone can't say which side is partial (proposed: `SetOpPartialMask`
   bitfield or `NonPartialChildren`; both `addSetOpPaths` and the mixed
   arm must agree on the field). `Children` stays `[left, right]` —
   claimed-whole child is the branch's chosen non-partial `Path`.
4. `ParallelWorkers = max(chosen partials' workers, 2)` capped at
   `cp.maxParallelWorkersPerGather` (log₂(2)+1 = 2 — the bump is a no-op
   beyond the floor for a two-child shape).
5. Costing (`cost_append` mixed arm, specialised to two children):
   - `startup` = min over both children's startup (workers ≥ 2 covers
     both, matching the pure arm's existing min rule).
   - `rows`: claimed-whole child `np.Rows / divisor`; partial child
     `p.Rows * (pDivisor / divisor)`; `clampRowEst`. If the pure arm
     ALSO produced a path this round (both branches had partials), the
     mixed path's `Rows` must equal the pure path's `Rows` (PG's
     `partial_rows` override, `pathnode.c:1417-1419`) — the function
     must therefore run the pure arm first and remember its `Rows`.
   - `total` = Σ partial children's `Cost.Total` + Σ claimed-whole
     children's `Cost.Total` (LPT collapses for one claimed-whole child)
     + `cp.cpuTupleCost * appendCPUCostMultiplier * rows`.
   - `ParallelSafe = parallelSafeWith(setOpRel, ...actual children...)`,
     `ParallelAware = true`, producer `setOpPartialAppendProducer`.

### Plan node — `*SetOp` (optimizer node)

6. Carries the per-branch partialness from the Path (e.g.
   `LeftPartial`/`RightPartial` bools) so every node-walk below can
   distinguish claimed-whole branches without re-deriving cost picks.

### Plan-tree walks — `internal/optimizer/parallel.go`

7. `stampParallelScan` `*SetOp` arm (:625): stamps BOTH branches today —
   must stamp only partial branches; a claimed-whole branch's driving
   scan stays `.Parallel = false` (the claiming worker runs it serially;
   stamping it would mark a scan no claim set will ever serve).
8. `drivingScan` / `partialPathDrivingKind` `*SetOp` arms: a claimed-whole
   branch must not be required to yield a driving scan — marker-aware
   skip, not a refusal (verify `terminatesPartial`/`findPartialSubtree`/
   `rebuildWithGather` need the same awareness — they already bail on
   two-child SetOp shapes per 0006c-2's handling, but the mixed path
   changes which branch is "the partial one").
9. `parallelChildren` `*SetOp` arm (:1389): **unchanged** — the
   conservative walks (`subtreeHasUnsafeNode`, `subtreeHasGather`,
   `HasShareableHashJoin`, `HasBitmapScan`) must still see both branches.

### Executor — `internal/executor/parallel_scan.go`

10. `newLeafParallelClaimSet` gains `claimedWhole atomic.Bool` (one per
    branch slot — `setOpLeft`/`setOpRight` already give each branch its
    own claim set).
11. `attachAll` `*setOp` arm (:596-599): per branch, switch on the
    operator's plan-stamped partialness —
    - partial: `cs.setOp{Left,Right}.attachAll(branch)` as today;
    - claimed-whole: wire `so.claim{Left,Right} =
      &cs.setOp{Left,Right}.claimedWhole`, attach NOTHING to the branch
      subtree (the claiming worker's private copy runs serially).

### Executor — `internal/executor/operators_setop.go`

12. `*setOp` gains `leftWhole`/`rightWhole` (plan-stamped at build) and
    `claimLeft`/`claimRight *atomic.Bool` (wired by attachAll).
    `nextStreaming` (:101-125): before draining a claimed-whole branch,
    `CompareAndSwap` the flag — the winner drains it whole; losers mark
    `leftDone`/`rightDone` and move on. `Open` still opens both children
    in every worker (PG likewise ExecInits all subplans in all
    participants; lazily deferring the open is an optional refinement).

### Executor — shared prebuilds (correctness audit, likely no change)

13. `collectShareableJoins`/`prebuildSharedHashJoins`: a hash join inside
    a claimed-whole branch still receives the shared build — CORRECT and
    beneficial (leader builds once; the single claiming worker probes).
    Keep.
14. `prebuildBitmap`/`collectBitmapScans`: a bitmap inside a claimed-whole
    branch finds the leaf `pbm` nil forever → the claiming worker builds
    a private bitmap serially (correct). The leader's top-level
    `HasBitmapScan`-driven prebuild would then be wasted work — decide at
    impl whether to exclude claimed-whole subtrees from the prebuild
    decision or accept the waste. (A claimed-whole branch is still a
    serial subtree; treating it like a top-level parallel bitmap would
    be the bug to avoid.)

## Interaction with sibling work

- Independent of M0140-0006c-2's remaining branch-kind arms (NL / merge /
  bitmap): those expand which *partial* branches are admissible; this arm
  admits *claimed-whole* branches. Either order lands.
- Q5 flips when this arm lands — its partial side is a bare seq scan
  (already admitted); no 0006c-2 arm needed for Q5 specifically.
- `setOpBranchDrivingKindIsSupported` is consulted for partial branches
  only; the claimed-whole branch needs no driving-kind check at all
  (any parallel-safe serial plan qualifies — matching PG, which puts
  `nppath` in regardless of its driving shape).

## Test shape (for the impl slice)

- Unit: a `UNION ALL` whose right branch has no partial path (or whose
  non-partial wins the tie/strict-cheaper rule) → `PartialPathlist` gains
  the mixed `PathSetOp` with the marker set; serial-vs-parallel identity
  at 1/2/4 workers — the mutation witness is the claim flag: neutered
  claim → N× rows on the claimed-whole branch (same style as
  `TestGatherOverSetOpIdentity` / `parallel_hash_path_consumer_test.go`).
- EXPLAIN: claimed-whole branch renders as an ordinary serial subtree
  under the Gather's Parallel Append — matching PG's display.
- Q5 plan-shape check under the TPC-DS parity harness (post-HOLD).

## Deferral bookkeeping

This doc discharges the recon resume point recorded in
`addPartialSetOpPath`'s header comment ("a branch with no partial path at
all simply gets no partial SetOp path — a resume point") and the
corresponding deferral-ledger row. The implementation commit itself is
separate and still requires the TPC-H gates (HOLD-blocked at write time).

## Implementation record (2026-09-19)

The arm landed per this map, with two deliberate divergences discovered
during implementation.

### What landed

- `addPartialSetOpPath` (`internal/optimizer/windowsetoppaths.go`) now
  files BOTH arms: the pure arm first (remembering its `Rows` for the
  `partial_rows` override, `pathnode.c:1417-1419`), then the mixed arm —
  per branch `setOpBranchPick` compares `branch.PartialPathlist[0]`
  against the non-partial candidate with PG's strict `<` (ties land the
  branch claimed-whole), refuses the arm when a branch offers neither
  (`pa_subpaths_valid = false`), and requires ≥1 claimed-whole pick
  (`pa_nonpartial_subpaths != NIL`). `parallel_workers` = max over the
  chosen PARTIAL subpaths floored at `log2(2)+1 = 2` (the all-claimed
  corner gets exactly 2); total cost = Σ partial totals +
  `appendNonPartialCost` (LPT makespan over min(workers,n) buckets —
  `costsize.c:2168-2243`) + append CPU overhead. Markers:
  `Path.SetOpLeftNonPartial`/`SetOpRightNonPartial`, copied by
  `createSetOpPlan` onto the emitted `*SetOp` node as
  `LeftNonPartial`/`RightNonPartial`.
- Plan-tree walks (`internal/optimizer/parallel.go`): `stampParallelScan`
  skips a marked branch (its scan stays `.Parallel = false` — PG shows
  the non-partial child as an ordinary serial subtree); `drivingScan`'s
  `*SetOp` arm treats the marker itself as satisfying that side (the
  `gatherChildPlan` panic guard is answered by the claim flag, not by a
  stamped scan). `partialPathDrivingKind` (`gatherpaths.go`) admits a
  claimed-whole child on `ParallelSafe` alone — no driving-kind check,
  matching PG putting `nppath` in regardless of shape.
- Executor: `parallelClaimSet` gained `claimedWhole atomic.Bool` on the
  per-branch leaf sets (`parallel_scan.go`); `attachAll`'s `*setOp` arm
  wires `so.claimLeft`/`so.claimRight` to the shared leaf flag for a
  marked branch and attaches no scan state there; `nextStreaming`
  (`operators_setop.go`) CAS-claims at first touch — the winner drains
  its private copy serially, losers close theirs and mark the branch
  done. Claim-on-first-touch matches nodeAppend's claim-on-select: a
  participant still draining the other branch must not hold this one's
  claim. `bitmapPrebuildTargets` skips marked branches (their leaf `pbm`
  stays nil → the claiming worker builds a private serial bitmap, item
  14's "exclude" decision). Shared-hash prebuild unchanged per item 13
  (leader builds once, the sole claimer probes — correct and beneficial).

### Divergence 1 — `nppath` is the branch's own serial plan, not a
`branch.Pathlist` member

The scoping plan proposed `bnp = cheapest ParallelSafe member of
branch.Pathlist`. Implementation found that wrong on two axes:

- **Schema/rows**: a rel pathlist member emits the rel's INTERNAL schema
  (the searched subtree — e.g. `Join(cols=2)`), not the branch's
  boundary-projected output (`Project(cols=1)` over it). PG's
  `get_cheapest_parallel_safe_total_inner` returns a path of the child
  the Append will actually run — the whole child plan. goopg's exact
  analog is a fresh `PathPrebuilt` seed over the branch's finished node
  (`seedPathForNode`), row- and wrapper-exact by construction, priced
  with the wrappers' cost like PG's `nppath->total_cost`, and built back
  to that node pointer-identically by `createPlanNode` (no splice).
- **Gather tops**: a branch whose serial winner carried a `*Gather` must
  still offer a claimed-whole plan — PG stamps `parallel_safe = false`
  on gather paths (`create_gather_path`), so its scan of the pathlist
  lands on the child's non-gather serial alternative. goopg's analog is
  `StripGather(branchNode)` before seeding; `statementIsParallelSafe`
  on the stripped node is PG's `parallel_safe` on the child plan.

### Divergence 2 — boundary wrappers gate the PARTIAL pick too
(pre-existing hazard, fixed here)

Substituting a searched-rel path for a branch node must preserve the
wrapper chain between the branch root and the searched emission —
dropping it emits wrong arity/rows. Two mechanisms:

- `setOpBranchPartialChainOK`: a branch offers its searched rel's
  partial path only when every wrapper on the chain is either
  per-worker-safe and stamp-descendable (`*Project`, `*Filter`, `*Sort`)
  or a drop-with-no-row-effect (`*Gather`/`*GatherMerge`). Anything else
  (`*Limit`, `*Distinct`, `*Aggregate`, `*WindowAgg`, `*LockRows`,
  `*Memoize`, a nested `*SetOp`, an unsearched branch) yields no partial
  pick — PG's `partial_pathlist` never carries such paths either, so the
  verdict matches. The branch can still be claimed whole.
- `spliceBranchEmission` (`createplansimple.go`): when a searched
  emission IS substituted (either arm's partial pick), the wrapper chain
  is rebuilt over it — the first non-wrapper node is the emission point
  and is replaced; `*Gather`/`*GatherMerge` are descended and dropped
  (a partial subtree cannot carry the serial winner's gather — nested
  parallelism); `*Memoize` cannot be descended (typed `*IndexScan`
  child) so it is itself the emission point. `PathPrebuilt` children
  skip the splice entirely. This fixes a latent wrong-arity/wrong-rows
  defect in the pure arm too — the end-to-end witness is the permanent
  `TestGatherOverSetOpPlannerWinnerIdentity`.

### Adjacent fix — `createSetOpPaths` accepts Gather winners

Once a partial SetOp can actually win, the upper-rel `PathGather` filed
over it (`generateUpperRelGatherPaths`) is the winning path — its
`createPlanNode` returns `*Gather`/`*GatherMerge`, not `*SetOp`. The
tail check was widened to return `Node` and accept all three; first
reached by this arm's corpus probe (a comma-join `UNION ALL` producing
`Gather > SetOp` with spliced `Project > Join` children).

### Placement correction — the mixed arm is nested-scope only
(late parity fix, caught by the TPC-DS sweep's plan-diff channel)

The first cut filed the mixed arm wherever `addPartialSetOpPath` ran —
and TPC-DS Q66 immediately regressed: its top-level `UNION ALL` picked
up an all-claimed `Gather > Append` shape that PG's own Q66 plan does
not contain (serial `Append`, each branch carrying its own
`Finalize GroupAggregate > Gather Merge`). The oracle resolves why:

- `generate_union_paths` (prepunion.c — the path builder for a
  statement-level set operation) files **only the pure arm**: its
  `partial_paths_valid` is "every child has a partial path", and a
  `Gather` over the `Append` is added iff that holds. There is no
  `pa_subpaths` arm in prepunion.c.
- The mixed arm lives solely in `add_paths_to_append_rel`
  (allpaths.c:1321) — reached for **appendrels**: UNION ALLs flattened
  out of FROM-clause/CTE subqueries by `prepjointree`, and
  inheritance/partition fan-outs. Never for a top-level
  `a UNION ALL b`. TPC-DS Q5's three `Parallel Append` witnesses are
  all inside CTEs/subqueries — consistent.

goopg does not flatten union-all subqueries (no `pull_up_union_all`
analog), so `addPartialSetOpPath` serves both structures. The available
top-level-vs-nested marker is `ps.ParallelStatementOK`: set only for
the statement's top-level plain SELECT, cleared for every nested scope
(`planSelectWithParent`). It is now passed in as `topLevel`, and the
mixed arm files only when `!topLevel` — the SetOp then stands where PG
would have built an appendrel. The pure arm still files at both levels
(`generate_union_paths` does so upstream).

Measured: with the gate, the SF0.25 sweep's plan-diff channel went from
"Q66 changed" back to `same=99 changed=0` — the all-claimed
`Gather > Append` disappeared from the top-level setop exactly as PG's
pipeline dictates.

Residual divergence (documented, not fixed): a union-all inside a FROM
subquery PG would NOT flatten (e.g. `LIMIT` in the subquery) still gets
the arm here — the marker sees nesting, not flattenability. Fixing that
needs a real flattenability check; none of the corpus queries hit it.

New tests:
`TestAddPartialSetOpPathMixedArmSuppressedAtTopLevel` (same shape that
files mixed nested files nothing at top level) and
`TestAddPartialSetOpPathPureArmStillFilesAtTopLevel` (the pure arm is
not collateral).

### Test evidence

- Optimizer (`windowsetoppaths_test.go`): pure-arm cost/worker floor
  pinned with searched-marked fixtures; mixed-arm pricing; strict
  tie-to-nonpartial; all-claimed worker floor = 2; branch-with-neither
  rejection; boundary-chain admissibility (safe set admits, Limit/Agg/
  Memoize refuse); mixed `Rows` inheriting the pure arm's estimate;
  `RefusesWhenABranchHasNoPartialPath` updated — that shape is now the
  mixed arm's canonical input.
- Executor (`parallel_setop_claimset_test.go`): claimed-whole and
  all-claimed identity at workers 1/2/4 — exact multiset, no replay, no
  drop. Anti-drift `claimedWhole` case in
  `parallel_gather_merge_claimset_test.go`.
- End-to-end (`parallel_setop_gather_test.go`):
  `TestGatherOverSetOpPlannerWinnerIdentity` — a real planner-produced
  `Gather > SetOp` over comma-join branches, serial-vs-parallel row
  identity, one-column output intact (the splice witness).

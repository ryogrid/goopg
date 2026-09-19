# M0142-0005 — recon: the real blocker is Gather-eligibility refusing a Memoize-wrapped partial inner, not "no Memoize on the NL probe path"

Status: accepted (recon closed 2026-09-16, no code change; task closed 2026-09-19 after the B8 re-measurement — see `m0142-0005-b8-index-probe-mult-reverify.md`, which filed `M0142-0005b` for the streamed-probe executor fix that retires `indexProbeCostMultiplier`)

## Task

`.ralph/fix_plan.md`'s M0142-0005 line ("break the Memoize / probe-multiplier
interlock", B6+B8) claimed goopg's executor has **no Memoize on the NL probe
path at all**, so pricing an NL+IndexScan probe PG-faithfully sends other
queries (Q72) into 320s timeouts, and `indexProbeCostMultiplier=2.0`
compensates by keeping the DP away from NL-index plans generally. M0142-0010
(2026-09-15) restated this as the diagnosed cause of TPC-DS Q34/Q73 not
matching PG's `Nested Loop`+`Memoize`+`Index Scan using date_dim_pkey` shape.

Before spending a loop implementing an executor-side Memoize fix on that
premise, this recon re-verified it — per the standing instinct
`planner_verify_both_candidates_generated` ("instrument `addPath`, not the
cost functions") and the sibling memory
`goopg_parallelism_is_gather_stamping_not_partial_paths`, both of which warn
that a missing plan shape can be a generation gap, a domination loss, or (as
turned out here) neither.

## Finding 1 — Memoize already exists, both in the executor and as a costed DP-search path

Grep and `git log` show a full Memoize implementation has been live since
2026-07-21 (`894485ba7`, "csq(R2-7/S7): Memoize"):

- `internal/executor/operators_memoize.go` (`newMemoizeOp`) — a real executor
  operator, wired at `executor.go:266`.
- `internal/optimizer/joinpathsmemoize.go` (`getMemoizePath`,
  `memoizePathInfo`) — a genuine **cost-based path**, offered alongside the
  bare inner to `addPath` inside `addNLIPaths` (`joinpathsnli.go:313`) so
  `add_path`-style dominance decides whether the cache pays, exactly PG's own
  `try_nestloop_path` shape (joinpath.c:1965-1986).

So "no Memoize on the NL probe path" as a blanket claim is **stale** — it
predates neither B6 (R59, `plan_parity_fix_take2`) nor M0142-0010 correctly;
the mechanism simply landed since and the ledger row was never re-checked
against the current binary.

## Finding 2 — the NLI+Memoize candidate for Q34 IS generated and correctly costed

Cloned `bench/tpcds/runtime_goopg/data-sf025` to a private port (5533,
`tmp/m0142-0005/`, per `goopg_shared_bench_cluster_collisions`) and ran Q34
with `GOOPG_PGSHAPED_DP_TRACE=1` (an **existing**, no-code-change trace
channel — `internal/optimizer/joinsearchtrace.go`/`pathtrace.go` — DPTRACE for
pairs/costs/vetoes, DPPATH for every individual path `addPath`/`addPartialPath`
sees, `verdict=accepted|dominated`).

Q34's 4-relation join (`store_sales, date_dim, store, household_demographics`)
lets the DP reach relset `{date_dim+household_demographics+store_sales}` via
two different pairings: `{date_dim+store_sales}⋈{household_demographics}`
(cheap: `date_dim`'s own `d_dom`/`d_year` filter is very selective, 235/73049
rows) and `{household_demographics+store_sales}⋈{date_dim}` — the PG-matching
order (hdemo's own filter, ~500/12000 rows, joined to the full `store_sales`
first, THEN NLI+Memoize-probing the now-much-smaller intermediate into
`date_dim`'s PK).

The DPPATH trace shows the second pairing's **partial** NLI+Memoize candidate
(`producer=join.nestloop.partial`, `outer={store_sales+household_demographics}`
partial hash, `inner={date_dim}` Memoize-wrapped parameterised probe) filed at
**total=16463.24** (per-worker) and `verdict=accepted` — dominating every
other partial candidate for this joinrel down to a `PartialPathlist` of one
entry by the time `generateUsefulGatherPaths` runs
(`DPTRACE cpgather rel={date_dim+household_demographics+store_sales}
partials=1 verdict=admitted`). This is dramatically cheaper than the eventual
winning **serial/gathered** hash join (`total=19760.88`) — confirmed against
an isolated 2-relation `EXPLAIN` of `store_sales⋈household_demographics`
alone, whose `Parallel Hash Join (cost=182.10..16215.33 rows=15054)` is
near-identical to PG's own real plan fragment
(`Hash Join (cost=257.11..16125.21 rows=15076)`, `bench/tpcds/plans-pg/Q34.txt`)
— goopg's join-order/cardinality math for this join is **already PG-faithful**;
this is not a cardinality or `indexProbeCostMultiplier` bug.

## Finding 3 — the candidate never gets a chance to compete, because it can never be Gathered

Despite `cpgather verdict=admitted` (not blocked by parallel-mode/`ConsiderParallel`
gates) and a one-entry `PartialPathlist` holding exactly the winning candidate,
**no `producer=gather` DPPATH line exists at all for this joinrel** — not even
a `dominated` one. Every sibling relset at the same levels (`{0,1}`, `{0,2}`,
`{0,3}`, `{0,1,2}`, `{0,2,3}`) gets one; only `{0,1,3}` (the one whose sole
partial survivor is Memoize-wrapped) does not.

Root cause, `internal/optimizer/gatherpaths.go`:

- `generateUsefulGatherPaths` calls `makeGatherPath(rel, rel.PartialPathlist[0], cp)`.
- `makeGatherPath` calls `gatherSubpathIsRunnable(sub)` →
  `partialPathShapeIsGatherable(sub)` → `partialPathDrivingKind(sub)`.
- `partialPathDrivingKind`'s `PathNestLoop` case (`:456-490`) explicitly
  refuses a Memoize-wrapped inner in **both** its branches:
  - the whole-inner (`in.RequiredOuter == 0`) branch: `if in.Kind == PathMemoize
    { return PathPrebuilt }` — own comment: "must not be a Memoize cache
    (per-probe semantics no worker can supply)";
  - the lateral-probe (`in.RequiredOuter != 0`, our shape) branch:
    `if in.Kind != PathIndexScan || len(in.IndexClauses) == 0 { return
    PathPrebuilt }` — own comment: "a parameterized `PathIndexScan` from R60's
    producer (its Memoize loop output is `PathMemoize`, refused above)".
- `PathPrebuilt` makes `makeGatherPath` return `nil` before constructing
  anything, so no path is even built — matching the observed silence (no
  DPPATH line of any verdict).

This is a **deliberate, documented, already-reasoned restriction** — not an
oversight — because the *executor* side has no per-worker Memoize: `memoizeOp`
(`internal/executor/operators_memoize.go`) is a single cache instance wired to
one `indexScanOp`, with no claim-set partitioning story for splitting a
Memoize'd probe stream across parallel workers (`parallelClaimSet`/`attachAll`,
`gatherpaths.go:322-328`, only models `PathSeqScan`/`PathIndexScan`/
`PathBitmapHeapScan`/`PathHashJoin`/`PathMergeJoin`/plain `PathNestLoop`). The
planner's refusal is the *correct* conservative choice given that gap — the
alternative would cost a plan the executor cannot honour, the exact class of
bug rule #2 (`createplanindex.go`) exists to prevent elsewhere.

## Verdict — M0142-0005 is real, but mis-scoped; corrected

**B6's actual mechanism (2026-09-16 restatement):** goopg's DP search already
generates and correctly costs the PG-matching `Nested Loop`+`Memoize`+
`Index Scan` shape as a **partial** path — the join-order/cardinality layer is
not at fault — but `partialPathDrivingKind` unconditionally excludes any
Memoize-wrapped inner from ever becoming a **Gathered total** path, because
the executor has no mechanism to run a per-worker-partitioned Memoize cache.
Every TPC-DS query whose PG-parity shape needs a parallel Memoize-driven probe
(Q34, Q73, and likely others sharing the "wide dimension table PK, narrow FK
domain, filtered on the small side first" shape this recon's own M0142-0010
predecessor already flagged as common) is structurally blocked from reaching
it, regardless of any cost-model tuning.

**B8 (`indexProbeCostMultiplier=2.0`) is a separate, narrower question this
recon did not re-settle**: the calibration commit (`c61781d6`, 2026-09-05)
post-dates Memoize's own landing (`894485ba7`, 2026-07-21), so it was
calibrated against a binary that already had serial NLI+Memoize available —
its continued need is plausible (a *serial* NLI probe still materializes its
whole TID list eagerly per the multiplier's own comment) but wasn't
re-measured here; that remains open.

**This recon changes what the correct next step is.** The original framing
would have sent a loop hunting for "why doesn't the executor support Memoize"
(it does). The corrected framing is an executor+planner slice: either (a)
give the executor a per-worker Memoize so `partialPathDrivingKind`'s refusal
can be relaxed for the lateral-probe branch, or (b) determine PG's own
`parallel_worker_number`-keyed cache model (`nodeMemoize.c`) is not directly
portable and find a narrower relaxation. Both are executor-shaped work, sized
like a dedicated slice — not attempted this loop, per the recon-first
discipline M0142-0004a/0009/0010/0012a already established.

## Floor measurements (mandatory for M0137–M0143 recon tasks)

No production code changed — a temporary debug print was added to
`internal/optimizer/pathparamindex.go` during the investigation and fully
reverted (`git checkout --`) before this doc was written; `git status
--porcelain -- internal/` is clean. `GOOPG_PGSHAPED_DP_TRACE=1` is an existing,
already-shipped trace channel, not new instrumentation. TPC-H/TPC-DS
plan-parity floors and `make ea-ratchet`'s baseline are therefore unchanged by
construction (same precedent as M0142-0010/0013). Pre-commit pgbench smoke
gate run per policy. Throwaway server/clone (port 5533, `tmp/m0142-0005/`)
stopped and removed before commit.

## Follow-up

Filed **M0142-0005** itself, rewritten in `.ralph/fix_plan.md` with this
corrected mechanism and resume point (`gatherpaths.go:456-490`
`partialPathDrivingKind`'s `PathNestLoop` case; `internal/executor/
operators_memoize.go`'s `memoizeOp` for the executor-side per-worker cache
gap). No new task ID needed — M0142-0005 keeps its slot, only its diagnosis
and resume point change. Deferral ledger row added for the B8 multiplier
re-measurement, which stays open and unscoped.

## Update 2026-09-18 — corrected again: "no per-worker Memoize" premise is false; the executor already has one for free

Per this task's own reopened scoping instruction ("needs its own
scoping/floor-measurement pass before implementation, same discipline as
M0142-0012's `-verify`/`a` split" — `.ralph/fix_plan.md` banner item 6), this
update re-verified the 2026-09-16 verdict's central claim before sizing an
implementation, per the same `planner_verify_both_candidates_generated`
instinct the original recon cites. **The claim does not hold**, and the real
fix is far smaller than "give the executor a per-worker Memoize" implies.
Recon only — no `internal/`/`cmd/` file touched (C1); read-only code tracing.

**Finding 4 — every worker already gets its own, fully private operator tree,
including its own `memoizeOp`/`kvcache.Cache` instance; nothing shared exists to
partition.** `executor.go:354-371` (the `*optimizer.Gather` build-node arm)
states this outright, and the statement is load-bearing, not incidental prose:

> "Each worker builds its OWN operator tree over the shared, read-only partial
> plan — Build is a pure function of the plan node, so N calls give N
> independent trees."

`gatherOp.Open` (`operators_gather.go:191-260`) launches one goroutine per
worker via `runWorker`, and `runWorker` (`:325+`) calls
`o.buildChildForSlot(idx)` → `buildUnderFreshScope(..., o.buildChild)`, which
invokes the SAME closure `executor.go:369` built —
`func() (Operator, error) { return buildNode(p.Child, workerBound) }` — once
per worker. `buildNode` on an `*optimizer.Memoize` node
(`executor.go`'s own `*optimizer.Memoize` case, alongside `newMemoizeOp`)
constructs a brand-new `memoizeOp` with a brand-new `kvcache.Cache` on every
call — there is no global/shared cache map, no `sync.Map`, nothing keyed by
`parallel_worker_number`, because nothing needs to be: each worker's tree is
disjoint from every other worker's, the same as its `filterOp`/`projectOp`/
`sortOp` siblings one level up, none of which get a claim-set either.

This is not a gap relative to PG — it is PG's own model, cross-checked in
`postgres/src/backend/optimizer/path/joinpath.c`'s `get_memoize_path` /
`postgres/src/backend/optimizer/util/pathnode.c:1693` and
`postgres/src/backend/executor/nodeMemoize.c` directly: real PG's `Memoize`
under a `Gather` has **no DSM-shared cache at all** — `ExecMemoizeEstimate`/
`ExecMemoizeInitializeDSM`/`ExecMemoizeInitializeWorker`
(`nodeMemoize.c:1190-1260`) only shuttle the `show_memoize_info`
instrumentation counters (`sinstrument[ParallelWorkerNumber]`) through shared
memory; the cache data structure itself (`MemoizeState.hashtable`) is built
fresh per worker as an ordinary backend-local hash table, exactly goopg's
`kvcache.Cache` per `memoizeOp`. `cost_memoize_rescan`
(`costsize.c:2541-2620`) reads `mpath->calls`, sourced from `outer_path->rows`
at the `get_memoize_path` call site (`joinpath.c:819`) — for a PARTIAL
candidate this is already the per-worker-divided partial outer path's row
estimate (PG's own `parallel_divisor` convention), so PG's own cost formula
already assumes and prices a smaller, per-worker-private cache with a lower
hit ratio than the serial case — not a shared, larger one. goopg's
`joinpathsmemoize.go` costs the partial candidate the same way (Finding 2
above measured `total=16463.24` against PG's own real per-worker
`16125.21` — a match, not a divergence), so **the cost side is already
per-worker-faithful too**; only the *admission* side never lets that costed
candidate through.

**Finding 5 — the real refusal has nothing to do with claim-set
partitioning; it is two narrow, unrelated type switches that never learned a
`PathMemoize`/`*memoizeOp` case.**

1. Path level (already cited in Finding 3):
   `partialPathDrivingKind`'s `PathNestLoop` case,
   `gatherpaths.go:459` — `if in == nil || in.Kind == PathMemoize { return
   PathPrebuilt }` fires **before** the branch split (`in.RequiredOuter == 0`
   vs. the R95 lateral-probe `else`), so it blocks the lateral-probe branch
   Q34 actually needs even though that branch's own reasoning (":468-479",
   "the kinds that can only be probes are admitted") never asked for a bare
   `PathIndexScan` for any principled reason tied to Memoize specifically — it
   just never had a case for one.
2. Node level, `lateralProbeIsPartialProbe`
   (`internal/optimizer/parallel.go:965-985`): a `switch n.(type) { case
   *IndexScan, *IndexOnlyScan: ...; default: return false }` with a comment
   bundling Memoize in with "no wrappers — the BuildFast bridge implements
   `lateralBindable` unconditionally, so a wrapped probe would double-bind".
   That specific risk **does not apply to Memoize**: `lateralBindable`
   (`operators_join_agg.go:512`) requires `BindLateralOuter(SlotView)`, and
   grepping every implementer (`BindLateralOuter` defined only on the
   FROM-clause-SRF operators and `opNodeOperator`, the BuildFast bridge
   itself) shows `memoizeOp` is not one of them — it has an unrelated method,
   `BindOuter(slot SlotView, outerWidth int)`, that only forwards to its
   child. A Memoize-wrapped bare index probe therefore resolves correlation
   the same way a bare probe does — through `ctx.OuterRows`
   (`join_lateral_stream.go:60-65`'s "ALSO pushed onto ctx.OuterRows" path) —
   not through the double-bind-risking `lateralBindable` path the comment
   warns about. The comment conflated two different wrapper kinds.
3. Executor level, `lateralProbeJoinPartial`
   (`internal/executor/parallel_scan.go:55-78`): the matching twin, same
   `switch inner.(type) { case *indexScanOp, *indexOnlyScanOp: ...; default:
   return false }` — no `*memoizeOp` case, so a Memoize-wrapped probe hits
   `default` regardless of what its own child is.

**`PathMemoize`'s shape is a single, well-typed unwrap, not a search.**
`getMemoizePath` (`joinpathsmemoize.go:292-303`) always builds `Kind:
PathMemoize, RequiredOuter: innerPath.RequiredOuter, Children:
[]*Path{innerPath}` — `Children[0]` is always exactly the wrapped probe path
(the same `PathIndexScan` the lateral-probe branch already knows how to
validate), and `newMemoizeOp(p *optimizer.Memoize, child *indexScanOp)`
(`operators_memoize.go`) is typed to only ever wrap a plain `*indexScanOp` at
the executor level too — so "unwrap once, then run the existing IndexScan
check on the unwrapped shape" is exact, not an approximation, at both layers.

**Revised verdict: this is not an executor-shaped slice.** No new cache
mechanism, no claim-set model, no DSM-analogue is needed — the executor
already does the right thing by construction. The remaining work is: extend
three call sites (`gatherpaths.go`'s `partialPathDrivingKind` PathNestLoop
lateral-probe branch, `parallel.go`'s `lateralProbeIsPartialProbe`,
`parallel_scan.go`'s `lateralProbeJoinPartial`) to recognize "Memoize wrapping
a bare, unparameterized-shape-per-Finding-5's-existing-check IndexScan/
IndexOnlyScan" as an admissible driving kind, each citing the other two per
`pattern_sibling_paths_must_agree`, then re-run Finding 2's Q34 trace to
confirm a `producer=gather` DPPATH line now appears and the plan flips to
`Gather`+`Nested Loop`+`Memoize`+`Index Scan`. Filed as **M0142-0005a** below,
sized as one loop, gated per the standard TPC-DS SF0.25 sweep +
`tpch-spotcheck.sh` (the latter is blocked by the `:65433` catalog-loss hold
per the 2026-09-17 banner — not selectable until P0-E6 clears; this recon
does not change that).

B8 (`indexProbeCostMultiplier=2.0`) remains open and unscoped, unchanged by
this update.

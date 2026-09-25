# M0140-0004 — partial-Append producer (K43): recon and deferral

**Status:** accepted
**Milestone:** M0140 (TPC-DS parallelism), plan-parity group
**Harness:** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Task:** `.ralph/fix_plan.md` M0140-0004
**Outcome:** recon only, no production change. Deferred with a ledger row
(`.ralph/deferral_ledger.md`, `m0140-0004-partial-append-producer`) per the
milestone's own Definition of Done: *"a partial-Append producer exists, or
its absence is a filed ledger row with a resume point."*

## The task as filed

> M0140-0004 — partial-Append producer (K43) — there are currently zero
> partial paths on join rels via that route; PG uses Parallel Append in six
> TPC-DS queries and only Q5 and Q76 miss.

K43 is a named, pre-scoped finding from the prior phase, carried at
`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/02-open-problems.md:189-193`
(also N34, line 514) and `METHODOLOGY3/04-forward-plan.md:95,382-383`: *"no
partial-Append producer, so there are zero partial paths on join rels via
that route; PG uses Parallel Append in 6 queries and only Q5+Q76 miss."*
Confirmed independently this loop: `bench/tpcds/runtime_goopg/tpcds-data/queries/query5.sql`
and `query76.sql` both hinge on `UNION ALL` chains (grep hits at query5.sql
lines 15/46/77/107/114, query76.sql lines 7/13), and the live PG 18.3 oracle
capture from M0140-0003 (`analysis/m0140/m0140-0003-tpcds-pg.txt`) shows
`Parallel Append` wrapping exactly these `UNION ALL` branches (e.g. `Parallel
Seq Scan on catalog_sales` / `web_sales` under one `Parallel Append`). A
subagent recon cross-checked this against the originating prior-phase rounds
(`r32-targetlist-subplan-display/DESIGN.md:37-43,153-154`,
`r33-subquery-parallel-pass/DESIGN.md:179`, `TODO.md:1455-1458`) and found
they already reached the same "full round of its own" / "independent
producer+executor work" conclusion this doc reaches independently below —
two prior, differently-scoped attempts already declined to fold this into a
smaller task. **Correction to this task's "4 of 6 already fine" framing**:
that is not asserted anywhere in the record; goopg's Q2/Q14/Q71 (three of the
other four queries in the six-query list) also emit plain serial `Append`,
not `Parallel Append` — they simply carry a compensating `Gather`/`Gather
Merge` *elsewhere* in the same plan that is enough to satisfy whatever
match/category check scores them, which Q5/Q76 don't get. (The six-query
list itself — Q2/Q5/Q14/Q71/Q75/Q76 — traces to
`r32-targetlist-subplan-display/DESIGN.md:42`; the same recon flagged a
possible Q75 discrepancy, an older baseline capture shows PG using `Merge
Append` there rather than `Parallel Append`, unresolved and out of scope
here.)

## What this loop found: the gap is deeper than a producer function

The task description reads like K80 (`addPartialHashJoinPath`,
`joinpathsparallel.go:82`) — a serial mechanism already exists and just
needs a parallel-aware twin gated the same way. **That is not the shape of
this gap.** Three facts, chased in the code rather than assumed:

1. **There is no cost-based Append path producer at all**, serial or
   parallel, for UNION ALL. `windowsetoppaths.go`'s own header (written for
   the C-18/P4-09 slice that gave the SETOP upper rel its first price) says
   this explicitly: *"an APPEND path over an appendrel is its own item"*
   (`windowsetoppaths.go:44-50`). The real set-operation chain (what Q5/Q76
   use) is planned by `createSetOpPaths` (`windowsetoppaths.go:259-294`),
   which the same header states *"has exactly one form per node"*
   (`windowsetoppaths.go:21`) — one candidate, no alternatives, no partial
   variant.

2. **Each UNION ALL branch is a finished, opaque `Node` by the time the
   SetOp producer sees it — not a `RelOptInfo` with its own path list.**
   `planner.go:1114`'s `applySetOp` (the real fold over a `SELECT … UNION ALL
   SELECT …` chain) calls `planSelectWithSettings` on each branch to a
   **complete plan tree** before `createSetOpPaths` ever runs (`planner.go:
   1153`). `createSetOpPaths` then wraps each finished branch as a
   `*PathPrebuilt` via `seedPathForNode` (`windowsetoppaths.go:296-305`,
   called at `windowsetoppaths.go:276-277`) — there is no channel today for
   a branch's own partial path (which exists only transiently inside that
   nested `planSelectWithSettings` call's private search) to survive past
   that call and reach the SetOp rel's `PartialPathlist`
   (`RelOptInfo.PartialPathlist`, `path.go:516`).

3. **The other `*SetOp{All:true}` construction sites are a red herring for
   this task.** `planner.go:3844` and `planner.go:3895` also build
   `*SetOp{All:true}` chains (partition-leaf fan-out and inheritance
   fan-out), and these ARE PG's literal appendrels
   (`add_paths_to_append_rel`, `allpaths.c:1300ff`) — but they are built
   directly during FROM-clause/range-var resolution, entirely below and
   before the `RelOptInfo`/path-search machinery even runs (the function
   `return`s the finished chain as the range-var's scan, `planner.go:3852-
   3856`). `windowsetoppaths.go:41-50`'s DEVIATION note already records that
   these are deliberately excluded from the SETOP upper-rel's scope. Neither
   Q5 nor Q76 exercises this path (they are literal SQL `UNION ALL`, not
   partitioned/inherited tables), so it does not shrink this task, but it
   does mean a future partial-Append producer for *this* route (PG's actual
   appendrel case) is a **second**, separately-scoped item if TPC-DS ever
   needs it — not folded into K43's Q5/Q76 resume point below.

`costSetOp`'s streaming (`UNION ALL`) arm already prices the *serial* case as
`cost_append` (costsize.c:2250) per its own comment (`windowsetoppaths.go:
337-339`) — so the cost-model term PG uses exists in miniature, but as a
single-candidate display price, not as a producer that `add_path` compares
alternatives against.

## Why this is out of reach for a single loop

Landing a genuine partial-Append producer needs, at minimum:

1. Each UNION ALL branch built as a `RelOptInfo` (or something that exposes
   a `PartialPathlist`) rather than a finished `Node`, so a branch's own
   parallel scan/join search can leave BOTH a complete and a partial
   candidate behind for the SetOp rel to see. This is a structural change to
   how `applySetOp`/`planSelectWithSettings` hand branches to
   `createSetOpPaths` — not a new file beside an existing one.
2. A new partial-Append producer (the `addPartialHashJoinPath` counterpart)
   that seeds `setOpRel.PartialPathlist` from the branches' partial paths,
   priced on PG's parallel-Append rule (`cost_append`'s partial-path
   variant, `costsize.c` — divisor and worker-count arithmetic mirroring
   `considerparallel.go`'s convention).
3. `generateUsefulGatherPaths` (the existing K80 consumer,
   `considerparallel.go`) reading the new `PartialPathlist` — likely free by
   construction once (1)/(2) exist, but unverified.
4. **New executor worker-partitioning for the streaming `*SetOp` node — a
   correctness gap, not just a missing optimization.** A subagent recon this
   loop found `gatherOp` (`operators_gather.go`) is generic: each worker calls
   `buildChild()` and builds its **own full copy** of the operator subtree
   from the shared plan. That is safe for a base scan only because
   `parallel_scan.go`'s `ParallelGroup`/claim-set machinery (`claimed()`,
   `claimLeaf`, `parallelClaimSet`) hands each worker a distinct block range.
   `setOp` (`operators_setop.go:32`, `newSetOp`) has **no equivalent claim
   logic at all** — `Open` just opens both children and streams them to
   completion. Wrapping today's `setOp` in a `Gather` as-is would make every
   worker stream **both entire UNION ALL branches**, duplicating every row
   N-fold — a silent wrong-answer bug of exactly the kind
   `.ralph/PROMPT.md`'s Hard-won Rule #2 (sibling paths must change together)
   and Rule #1 (silent row-count regressions) warn about, not a performance
   shortfall. A partial-Append producer is unsafe to land without first
   giving `setOp` a claim scheme analogous to `parallel_scan.go`'s.

Each of (1)-(2) is comparable in size to an entire prior C-19-series slice
(`joinpathsparallel.go`'s own header cites its slice as "C-19f / P5-06"), and
(1) is a plan-shape change to the recursive SELECT-branch entry point used by
every SetOp query in the suite, not just Q5/Q76 — exactly the kind of
one-task-per-loop violation `.ralph/PROMPT.md` warns against bundling. There
is no reduced version that stays inside K43's two-query scope without also
touching this shared entry point.

## Disposition

Filed as `.ralph/deferral_ledger.md` row `m0140-0004-partial-append-producer`
per the milestone DoD's explicit "or its absence is a filed ledger row"
clause (the same disposition M0137-0012's four filing-only rows used).
`.ralph/fix_plan.md` M0140-0004 is checked off on that basis — the task was
to either land the producer or record why it can't be landed here, and this
recon does the latter with a concrete resume point. No production code
changed; no test pins moved; TPC-DS values/match floor is unaffected because
nothing was built.

## Resume point (for whoever picks this up)

Start at `planner.go:1106-1119` (`planSegment`) and `planner.go:1153`
(`applySetOp`'s call into `createSetOpPaths`) — the seam where a branch
becomes an opaque `Node` needs to instead retain (or re-derive) a
`RelOptInfo` with a `PartialPathlist`. `windowsetoppaths.go:259-305`
(`createSetOpPaths`, `seedPathForNode`) is where the new partial producer
would plug in, mirroring `joinpathsparallel.go:82`'s
`addPartialHashJoinPath` shape and gated the same way behind
`gatherPathsMode` (`gatherpaths.go`). Re-measure Q5/Q76 specifically
(`bench/tpcds` SF0.25, `scripts/tpcds-sf025-regression.sh`) once a candidate
lands — they are the only two queries K43 names as blocked on this route.

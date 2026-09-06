# C-19f (P5-06) — `try_partial_hashjoin_path`: the hash join as a partial path

Status: design, written before implementation (2026-09-06).
Item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md`, row `C-19f P5-06`.
Parent design: `docs/design/not_ralph/planner_refactor_take3/08-target-design.md` §8.
Gate protocol: same bundle, `09-…` §5 row **P5**.
Predecessor: `docs/design/planner-c19d-gather-paths/DESIGN.md` (read §5.1 first).
Executor contract: `docs/design/executor-e09a-shared-spilling-build/DESIGN.md`
and `…/DESIGN-E09b.md`.
Upstream oracle (read-only): `postgres/` @ PG 18.3.

---

## 1. What this slice is for, in one paragraph

C-19d landed `generate_useful_gather_paths` — the first reader
`RelOptInfo.PartialPathlist` ever had — and shipped it default-OFF for an
arithmetic reason, not a cautionary one. With partial paths on BASE rels only,
the whole relation crosses the Gather boundary: the charge is
`parallel_tuple_cost` × rows (0.1/row) and the saving is `cpu_tuple_cost`'s
worker share (≈0.0075/row at 4 workers). `add_path` correctly dominates a
base-rel Gather at every relation size, and no reduction in the subpath's price
rescues it (C-19d §5.1). PG escapes this by having far fewer rows cross the
boundary, because the join happens BELOW the Gather. **C-19f is what makes that
true in goopg**: it gives a JOINREL its own partial paths, so a Gather can float
above one or more joins and only the join's output is charged
`parallel_tuple_cost`.

It is `try_partial_hashjoin_path` (joinpath.c:1299) plus the parallel block of
`hash_inner_and_outer` (joinpath.c:2418-2477).

---

## 2. Which of PG's TWO parallel hash joins goopg has

Upstream offers two, and `try_partial_hashjoin_path`'s `parallel_hash` argument
selects between them (its own header, joinpath.c:1290-1297):

| variant | `parallel_hash` | outer | inner | hash table |
|---|---|---|---|---|
| **partial-outer, private inner** | `false` | partial | COMPLETE | "a copy of it is run in every process to create separate identical private hash tables" |
| **Parallel Hash** | `true` | partial | partial | one table built cooperatively in DSM behind a barrier protocol |

goopg has **neither, and something better than the first**. The executor's file
header says it plainly (`internal/executor/parallel_hash_build.go:5-16`):
workers are goroutines in one address space, so the table is built once and
shared by pointer, and PG's whole DSA+barrier apparatus is replaced by a struct
and a map lookup. Concretely, `gatherOp.Open` calls `prebuildSharedHashJoins`
(parallel_hash_build.go:202), which walks the PROBE chain
(`collectShareableJoins`, :265 — probe side only, because a join on another
join's build side is built as part of that build), runs each build phase in the
LEADER before fan-out, and publishes a `sharedHashBuild` every participant
adopts.

So goopg's shape is: **partial outer, complete inner, ONE shared build**. That
is upstream's `parallel_hash = false` variant with the N-fold build replication
removed. `parallel_hash = true` — a partial inner, built cooperatively — has no
goopg executor and is REFUSED by this slice (§4.4 item 4), because a path whose
executor does not exist is a wrong-answer bug waiting for a cost model to pick
it.

### 2.1 What E-09a and E-09b changed, and why the price changes with them

Both landed today, and they are the reason this item was sequenced after them.

- **E-09a (`67204579c`)** — `captureSharedBuild` now publishes a build that
  SPILLED. Before it, `sharedBuildWouldSpill` declined to share any build whose
  geometry said `NBatch > 1`, and every participant fell through to a full
  private build. Live witness on TPC-H Q9 with 4 workers + leader:
  `Seq Scan on orders rows=1500000.00 loops=1` in all five participants, five
  `Build Time`s. After: `rows=0.00 loops=0` in every worker, one `Build Time`,
  Q9 8.85 → 7.85 s.
- **E-09b (`d5ce1bb9b`, gate `b5647d6fa`)** — load-once-per-batch. The shared
  descriptor carries one `sharedBatchLoad` slot per batch, so one participant
  loads batch k and the rest wait on it. Measured on a 4-participant / 7-batch
  fixture: `loadCount` 7 vs 28, `maxLiveLoads` 1 vs 4, `maxLiveBytes` 140,775 vs
  ~563,100.

**Consequence for this slice's arithmetic, stated so it cannot be mis-applied
later:** the reverted D-05 experiment (`tmp/d05p4-buildcost.patch`) charged a
**5× participant multiplier** on a spilling build. That multiplier was DERIVED
FROM the sharing-decline rule E-09a deleted. It is now wrong. After E-09a/E-09b
a build — resident or spilling — is performed **once** and its batches are
loaded **once**. The multiplier is therefore **1×**, identical to a resident
build, which is also exactly what upstream charges
(`initial_cost_hashjoin`: `startup_cost += inner_path->total_cost`,
costsize.c:4187 — no multiplier, in either variant). Do not re-apply that patch;
re-derive at 1× (the D-05 correction row, `c94326cf6`, says the same).

---

## 3. Cost, transcribed with citations

Nothing new is invented. The partial hash join is priced by the SAME
`hashJoinCost` (cost_funcs.go:403 = `initial_cost_hashjoin` +
`final_cost_hashjoin`) the serial one uses, with three inputs changed and one
output changed. That is deliberate: two hash-join cost functions would drift,
and the drift would be invisible until a plan was costed by one and built by the
other (the discipline 03 §5.2 states for operator constructors).

### 3.1 The three changed inputs

| `hashJoinInputs` field | serial | partial |
|---|---|---|
| `outer` / `outerRows` | `outer.CheapestTotal` | `outer.PartialPathlist[0]` — a partial path's `Rows` is ALREADY the per-worker count (`costParallelSeqscan` divides it) |
| `inner` / `innerRows` | `inner.CheapestTotal` | `cheapestSafeInner` — the cheapest **parallel-safe, unparameterised** path on `inner.Pathlist` (§4.2); rows are the WHOLE inner, undivided |
| `outputRows` | `joinrel.Rows` | `clampRowEst(joinrel.Rows / divisor)` |

`divisor` is `getParallelDivisor(outer.ParallelWorkers, cp.parallelLeaderParticipation)`
— `get_parallel_divisor` (costsize.c:6474), yielding d(1)=1.7, d(2)=2.4,
d(3)=3.1, d(w≥4)=w.

The `outputRows` division is `final_cost_hashjoin` costsize.c:4307-4314:

```
/* For partial paths, scale row estimate. */
if (path->jpath.path.parallel_workers > 0)
{
    double parallel_divisor = get_parallel_divisor(&path->jpath.path);
    path->jpath.path.rows = clamp_row_est(path->jpath.path.rows / parallel_divisor);
}
```

Upstream computes `hashjointuples` independently via `approx_tuple_count`
(costsize.c:4504) over `outer_path_rows` — which is the partial outer's already
divided count — so its per-tuple charge comes out at ≈rows/divisor by a
different route. goopg uses the clamped divided figure for BOTH `Path.Rows` and
`outputRows`, because deriving the same quantity twice is exactly how
`Path.Rows`' own comment says a cost model acquires two fields that can
disagree.

The residual charge follows the output count for the same reason
(`addHashJoinPath` charges `qualEvalCost(cp, len(residual), joinRel.Rows)`; the
partial producer charges it on the per-worker count).

### 3.2 The build is charged ONCE — the E-09a/E-09b consequence

`hashJoinCost` charges the build as

```
build := (cpuOperatorCost*k + cpuTupleCost)*innerRows + inner.Total
```

once, at startup. For the partial path this stays **unchanged and undivided**,
and both halves of that need a reason:

- **Undivided** — the inner is a COMPLETE path (not partial), so `innerRows` is
  the whole inner relation. Upstream's `initial_cost_hashjoin` does the same,
  and for the `parallel_hash = true` variant it explicitly multiplies the count
  back up (`inner_path_rows_total *= get_parallel_divisor(inner_path)`,
  costsize.c:4209-4210) precisely because the sizing needs the total. goopg has
  no partial inner, so there is nothing to undo.
- **Once, not per-participant** — because after E-09a/E-09b the executor
  performs it once. §2.1. Charging it N× is the refuted `d05p4` multiplier.

`hashsize.Choose` is called on the same undivided `innerRows`, so the spill
geometry the planner prices is the geometry `joinOp.buildGeometry` will solve
for in the leader's prebuild — the invariant `internal/hashsize` exists to
protect. Note that goopg does NOT model PG's `try_combined_hash_mem`
(the parallel-hash budget widening, costsize.c:4225): it is a `parallel_hash =
true` feature and goopg has no such build.

### 3.3 `DisabledNodes`, `ParallelSafe`, `ParallelWorkers`, `ParallelAware`

Field-for-field with `create_hashjoin_path` (pathnode.c:2861-2866):

| PG | goopg |
|---|---|
| `parallel_aware = joinrel->consider_parallel && parallel_hash` | `ParallelAware = joinrel.ConsiderParallel` — see §5 for why the second conjunct differs |
| `parallel_safe = joinrel->consider_parallel && outer->parallel_safe && inner->parallel_safe` | `parallelSafeWith(joinrel, outer, inner)` — the existing helper, unchanged |
| `parallel_workers = outer_path->parallel_workers` | `ParallelWorkers = outer.ParallelWorkers` |
| `disabled_nodes` from `initial_cost_hashjoin` (costsize.c:4180-4182) | `disabledNodesFor(!cp.enableHashJoin, outer, inner)` — the same call the serial producer makes |
| `pathkeys = NIL` | `Pathkeys: nil` — "a hashjoin never has pathkeys" (pathnode.c:2879) |
| `required_outer = NULL` | `RequiredOuter: 0` — asserted, see §4.2 |

`parallel_workers` from the OUTER alone is upstream's, comment and all ("This is
a foolish way to estimate parallel_workers, but for now…"). It is kept because
it is the number `createGatherPlan` will plan and the number
`attachParallelScan` will partition the probe scan by; deriving a different one
here would put the planner's worker count and the executor's out of step.

---

## 4. The producer

`addPartialHashJoinPath` in a new `joinpathsparallel.go`, called from
`addPathsToJoinrel` (joinpaths.go) immediately after `addHashJoinPath`, which is
where upstream's parallel block sits relative to its serial one
(joinpath.c:2418, after the `try_hashjoin_path` loop closes at :2398).

### 4.1 Direction

`addPathsToJoinrel` is called once per input ORDER (`makeJoinRel`,
joinsearchlevel.go), so the producer sees each pair twice and offers the partial
path in whichever direction has a partial outer. That matches upstream, whose
parallel block reads `outerrel->partial_pathlist` only, and whose caller is
likewise invoked per direction.

### 4.2 Preconditions, each naming the wrong answer it prevents

Upstream's guard is joinpath.c:2418-2422 plus :1315-1318. goopg's, in order:

1. `s.parallelModeOK` and `joinrel.ConsiderParallel` — `joinrel->consider_parallel`,
   already propagated by `joinrelConsiderParallel` (joinsearchlevel.go:613,
   = `build_join_rel`, relnode.c:829-845). Without it the rel has no business
   producing a partial path at all, and `addPartialPath` refuses it anyway.
2. `len(outer.PartialPathlist) > 0`; take `[0]`, the cheapest
   (`addToPartialPathlist` keeps ascending total-cost order, as
   `add_partial_path`'s `insert_at` does).
3. `len(keys) > 0` — the producer sits inside `addPathsToJoinrel`'s
   `if len(keys) > 0` block, so a pair with no usable equality never reaches it,
   exactly as upstream's parallel block sits inside `if (hashclauses)`.
4. The inner is `cheapestSafeInner(inner.Pathlist)` =
   `get_cheapest_parallel_safe_total_inner` (pathkeys.c:699): the cheapest-total
   path that is `ParallelSafe` **and** unparameterised. Upstream first tries
   `cheapest_total_inner` and falls back to this scan; goopg folds the two,
   since `CheapestTotal` is on `Pathlist` and would be found by the same scan.
   *Divergence, recorded:* upstream returns the FIRST matching path and relies on
   its list order; goopg scans for the MINIMUM total, which is what the
   function's own name promises. On a list where the two differ, goopg picks the
   cheaper — never a wrong answer, and it removes a dependence on `addToPathlist`
   ordering that no test pins.
   **This is also the load-bearing guard against a nested Gather.**
   `makeGatherPath` sets `ParallelSafe: false` (C-19d §2 — "a Gather is the
   parallel/serial boundary"), and `parallelSafeWith` ANDs its children's flags,
   so any path with a Gather anywhere beneath it is `ParallelSafe == false` and
   is structurally excluded from the inner side here. Without that, a Gather
   could land on the build side of a join that the leader pre-builds inside
   `gatherOp.Open`, which is the shape
   `prebuildHashJoins`' "a Gather never appears inside another Gather's partial
   subtree" comment assumes away.
5. `RequiredOuter == 0` on the result — upstream `try_partial_hashjoin_path`
   returns early when the inner is parameterised (:1317) and asserts the outer
   is not (:1316). goopg computes `calcNonNestloopRequiredOuter(outer, inner)`
   and REFUSES a non-zero result rather than asserting, matching
   `addPartialPath`'s fail-closed convention (path.go:750: "a panic inside the
   planner would fail the statement").
6. **Executor-shape refusal** — `partialPathShapeIsGatherable(outer)` must hold
   for the partial outer, so a partial hash join is never built on top of a
   partial path whose driving scan the executor's attach walks do not model. The
   check is the path twin of `drivingScan`, and it is asked HERE as well as at
   the Gather so an unrunnable shape is never even costed.
7. **Jointype.** goopg's search is INNER-only pinned (leftdeep-joins 03 §4.4
   pins every outer/semi/anti construct outside the search as an opaque
   `PathPrebuilt` initial rel), and `createHashJoinPlan` hard-codes
   `Type: JoinTypeInner`. INNER is admitted by `hashJoinIsPartialCapable`
   (parallel.go:674), so the pin and the executor rule agree today. Upstream's
   two exclusions (`JOIN_UNIQUE_OUTER`, `JOIN_RIGHT_SEMI`) and the
   FULL/RIGHT/RIGHT_ANTI inner exclusion (:2458-2461) are unreachable while the
   pin holds; they are written down here rather than silently absent, and they
   become live code the same moment `join_is_legal` inference relaxes the pin.

### 4.3 What is filed, and what reads it

`addPartialPath(joinrel, path, "join.hash.partial")` — the joinrel's
`PartialPathlist`. Two readers, both pre-existing:

- `generateUsefulGatherPaths` (gatherpaths.go), called per joinrel immediately
  before `setCheapest` (joinsearchlevel.go:309) — this is the reader that turns
  a partial join into a Gather;
- `addPartialHashJoinPath` itself at the NEXT level, since a joinrel's partial
  path is the partial outer of the join above it. **This is the upward
  propagation C-19d §5 named as the thing it did not have**, and it is what lets
  one Gather sit above a whole join tree.

`partialPathDrivingKind` (gatherpaths.go) gains a `PathHashJoin` arm that
descends `Children[0]`. That is the probe side by goopg's child convention
(pathgen.go:74: "Children[0] is the probe (outer) side, Children[1] is the build
side"), and `createHashJoinPlan` leaves `BuildLeft` false, so `Children[0]`
becomes `Join.Left` and `joinProbeSideIsLeft` (= `!BuildLeft`) returns true —
the node-level walk `drivingScan` and `stampParallelScan` perform. The path walk
and the node walk therefore descend the same side; the arm is added explicitly,
not by relaxing the whitelist's default, because C-19a's review found four
fail-open holes in exactly that pattern.

### 4.4 What is NOT produced

1. **A partial MERGE join** (`try_partial_mergejoin_path`) — C-19e territory,
   and blocked on the same executor gap `gatherMergeSubpathIsRunnable` records.
2. **A partial NESTED LOOP** (`try_partial_nestloop_path`) — the plain-NL and
   NLI arms both need `attachParallelScan` to reach a driving scan through a
   `*NestedLoopIndexJoin`, which `terminatesPartial` explicitly stops at.
3. **A partial hash join on the BUILD side of another** — structurally
   impossible here: the producer only ever offers `PartialPathlist[0]` as the
   OUTER, and the inner must be `ParallelSafe`-and-serial. It matches
   `collectShareableJoins`' probe-only descent.
4. **`parallel_hash = true`** — §2. No goopg executor builds a hash table from a
   partial inner. Refused by construction (the producer never reads
   `inner.PartialPathlist`), and stated here rather than left as an absence.

---

## 5. `parallel_aware`: the flag, and its route to `createPlanNode`

`Path` gains `ParallelAware bool`, PG's `path->parallel_aware`
(pathnodes.h — "engage parallel-aware logic?").

In upstream it is `joinrel->consider_parallel && parallel_hash`, i.e. true only
for the cooperatively-built variant, because that is the only hash join whose
EXECUTOR node behaves differently. **In goopg the second conjunct is dropped**,
and that is a real statement about this executor rather than a shortcut: every
hash join inside a Gather's partial subtree IS treated specially by the executor
— `HasShareableHashJoin` selects it, `prebuildSharedHashJoins` runs its build in
the leader, `captureSharedBuild` publishes it, and `applySharedBuild` adopts it
in each participant. A serial hash join does none of that. So the flag means the
same thing it means upstream ("engage parallel-aware logic") over a different
mechanism, and it is set on exactly the paths this producer files.

The route to `createPlanNode` is an ASSERTION, not a new executor field, and the
reason is worth stating: the executor derives its parallel behaviour from the
BUILT TREE (`HasShareableHashJoin` and `attachParallelScan` both walk nodes), so
there is nothing for a flag to carry. What the flag buys is a fail-closed check
at the boundary. `createHashJoinPlan` gains:

```
if p.ParallelAware && !hashJoinIsPartialCapable(j) {
        panic(...)
}
```

— i.e. the planner refuses to BUILD a node it has just declared parallel-aware
if the executor's own predicate would decline to run it that way. That is the
same contract `createGatherPlan` already enforces with `drivingScan`
(createplangather.go:105), and it is the cheapest possible guard against the two
producers drifting: if a later slice relaxes the jointype pin and admits, say, a
RIGHT join here, this panics at plan-build time instead of returning a silently
partial join.

The probe scan's `Parallel: true` label needs NO new code: `createGatherPlan`
already calls `stampParallelScan` on the built child, and that walk descends a
`*Join` under `hashJoinIsPartialCapable` to the probe side (parallel.go:494-515).
A path-model Gather over a partial hash join therefore stamps exactly the scan a
post-pass Gather would have stamped — which is the sibling-agreement rule the
C-19d design set out to keep.

---

## 6. Admission: NO new flag

C-19f rides `GOOPG_GATHER_PATHS` (off / top / all, default **off**), C-19d's
knob, already registered in the flag-provenance table (flaglabels.go) and in
`scripts/planner-flags.env`. It does not get one of its own, for a structural
reason:

- the ONLY consumers of `PartialPathlist` are `generateUsefulGatherPaths` and
  this producer's own next level. The first is gated by the mode. So with the
  mode `off`, a partial join path is filed and never read — provably inert.
- a second flag would create a combinatorial arm nobody will measure. And the
  interesting A/B is already expressible: `off` vs `top` *is* the C-19f
  measurement, because `top` was inert-by-construction before this slice (the
  final rel is a joinrel and had no partial paths) and goes live with it. C-19d
  §5 said `top` "is the mode a later agent should measure FIRST"; this slice is
  what makes that mode mean anything.

The producer is nevertheless SKIPPED when the mode is `off`, and that is a
planner-time decision, not a semantic one: an extra `hashJoinCost` per pair per
direction per level, on a search whose planner time is measured by the
pre-commit pgbench smoke, buys nothing while nothing can read the result. Under
`top` the producer must still run at EVERY level — the final rel's partial path
only exists because the levels below propagated theirs upward.

**The default does not move in this slice.** §7 says why the measurement is
likely to say it should not move yet either.

---

## 7. The arithmetic: when can a Gather now win, and what still blocks it

This is the section C-19d §5.1 asked its successor to write.

### 7.1 The crossover

Take a partial hash join with a partial seq-scanned outer of N rows over P
pages, k hash clauses, a complete inner, and J join output rows, under d = 4
workers (so `1 − 1/d` = 0.75, `parallel_leader_participation` on ⇒ divisor
exactly 4). `cost_seqscan`'s parallel arm divides CPU only, never the page cost
(costsize.c) — so:

```
serial   = seqPage*P + cpuTuple*N + build + cpuOp*k*N + cpuTuple*J
partial  = seqPage*P + (cpuTuple*N)/d + build + (cpuOp*k*N)/d + (cpuTuple*J)/d
gather   = partial + parallelSetup + parallelTuple*J
```

Gather+partial beats serial iff

```
(1 − 1/d) * (cpuTuple + cpuOp*k) * N  >  parallelSetup + (parallelTuple − (1−1/d)*cpuTuple) * J
```

At PG 18 defaults (`cpu_tuple_cost` 0.01, `cpu_operator_cost` 0.0025,
`parallel_setup_cost` 1000, `parallel_tuple_cost` 0.1), k = 1, d = 4:

```
0.009375 * N  >  1000 + 0.0925 * J
N  >  106,667 + 9.87 * J
```

**The Gather pays when the partial subtree's input is roughly ten times its
output**, plus a fixed ~107 k rows to amortise the setup. Every term in that
inequality is a named `costParams` field, and the crossover test pins it that
way — never as a literal (the index-probe multiplier shipped for months at the
value its own comment called wrong, and C-19d hit a round literal that put a
crossover test inside `add_path`'s 1 % fuzz band).

Two things follow immediately, and they are the whole result:

- **A single FK join cannot pay.** A fact-to-dimension join emits about as many
  rows as it consumes: J ≈ N makes the inequality `N > 107k + 9.87N`, which has
  no solution. This is why C-19f is not, by itself, the win.
- **A join TREE can.** Every additional level below the Gather adds its own
  `(1 − 1/d)·(cpuTuple + cpuOp·k)·N_level` to the left side while the right side
  is still paid once, on the FINAL output. That is the mechanism, and it is
  exactly why PG's Gather ends up at the top of the join tree.

### 7.2 The blocker this slice does NOT remove, stated up front

Most TPC-H queries aggregate. Today `MaybeAddGather`'s `splitAggregate` builds
`Finalize Agg → Gather → Partial Agg → …`, so the row count charged
`parallel_tuple_cost` is the count AFTER partial aggregation — typically a
handful of groups per worker. A C-19f/C-19d Gather is placed inside the JOIN
SEARCH, below the upper planner's aggregate, so it is charged on the
pre-aggregation J. And by C-19d's coexistence rule (`subtreeHasGather` ⇒ the
post-pass stands down, parallel.go:126), a path-model Gather at the final
joinrel FORECLOSES the partial aggregation that would otherwise have been built
above it.

So on an aggregating query, `top` trades a Gather charged on group counts for a
Gather charged on join-output rows. That is a predicted REGRESSION, and it is
predicted for the same reason C-19d's base-rel Gather was: the boundary is in
the wrong place. **The item that moves it is C-19g** (partial aggregation as
paths, `create_partial_grouping_paths`), which is a separate row and explicitly
out of this slice.

The honest position this slice therefore takes:

- the mechanism lands, complete, priced and tested;
- the default stays `off`;
- the measurement to run is `off` vs `top` vs `all` on TPC-H SF=1 and it is
  expected to say "not yet, wait for C-19g" for every query with a GROUP BY
  above the join tree — and possibly to say "yes" on the non-aggregating,
  high-selectivity shapes. Either answer is a result; neither is a reason to
  flip a default. §9 lists exactly what must be measured.

The coexistence rule is NOT relaxed to work around this. It is a correctness
stop (`findPartialSubtree` descends through `*Gather` and would nest a second
one, N workers each launching N), and trading a correctness stop for a
benchmark number is the failure mode this bundle exists to avoid.

---

## 8. How we will know it worked (unit, no server)

1. **Cost identity.** A partial hash join path's cost equals `hashJoinCost` over
   the partial outer, the complete inner, and the per-worker output count —
   asserted term-by-term through the named `costParams` fields, and the build
   term asserted to be charged **once**, undivided (the E-09a/E-09b pin: a test
   that would fail if anyone reintroduced a participant multiplier).
2. **Rows round-trip.** `computeGatherRows(partialHashJoinPath)` returns
   `clampRowEst(joinrel.Rows)`: the divisor applied by `final_cost_hashjoin` and
   undone by `cost_gather` is ONE divisor, not two — the same property C-19d
   pinned for the scan.
3. **Both candidates exist before any cost is compared.** The producer's
   acceptance test asserts, via the path trace, that BOTH the serial hash path
   and the partial hash path were OFFERED for the pair before asserting which
   won. Five wrong hypotheses were burned on Q8 because an index producer
   emitted nothing at that parameterisation and the costs were never the
   question; a crossover test that does not first prove both candidates exist is
   the same trap.
4. **The crossover, both directions.** A fixture whose N/J ratio clears §7.1's
   inequality produces a Gather over a partial hash join as the final rel's
   cheapest path; the same fixture with J raised does not. Expressed through the
   constants.
5. **Upward propagation.** A three-relation fixture: the level-2 joinrel's
   `PartialPathlist` is non-empty AND the level-3 joinrel's partial path has the
   level-2 partial path as its `Children[0]`. This is the property C-19d lacked
   and the one thing that makes a top-of-tree Gather reachable.
6. **Fail-closed refusals**, one test per §4.2 clause: a parameterised inner, a
   non-parallel-safe inner (specifically: one carrying a Gather — the nested-
   Gather guard), an outer whose driving-scan shape is not modelled, and a rel
   that does not `ConsiderParallel`. Each asserts NO partial path was filed.
7. **`ParallelAware` route.** `createHashJoinPlan` panics on a `ParallelAware`
   path whose built `*Join` fails `hashJoinIsPartialCapable`; and a Gather built
   over a partial hash join has its PROBE-side scan stamped `Parallel` and its
   build-side scan NOT stamped.
8. **Mode `off` changes nothing** — the serial-control-arm argument: with the
   default mode, no joinrel gains a partial path and every fixture's chosen path
   is byte-identical to HEAD's.

### 8.1 The executor consumer check (the item's own requirement)

A planner-only test is explicitly not enough: "an unwinnable path is an untested
path" — when bitmap costs were fixed, four latent execution bugs surfaced in a
row, and a partial hash join path that has never been chosen has never been
executed. So, in `internal/executor`:

- build a fixture where the partial hash path WINS by cost (§7.1's inequality
  cleared), run it end to end, and diff the result multiset against the same
  query with `GOOPG_GATHER_PATHS=off`;
- assert the plan actually is `Gather → Hash Join → Parallel Seq Scan` (probe
  side) — i.e. the shape executed, not merely costed;
- assert the shared build fired: exactly ONE build for the join
  (`prebuildSharedHashJoins` published a `sharedHashBuild`), which is the
  E-09a/E-09b behaviour the price in §3.2 describes. A test that passes while
  five private builds run would mean the cost model is describing an executor
  that is not there.
- run it under `-race`, and include a spilling (`work_mem` small) variant, since
  the spilling shared build is the newest of the two mechanisms.

---

## 9. Beyond this slice — what needs the bench cluster

Not owned by this session: port 65433 is held by another agent
(`flock -n /tmp/goopg-65433.lock` reported BUSY at start and was re-checked at
the end). The following must be measured before ANY default moves, and no
number below may be inferred from a plan change alone — "a row-count gate cannot
catch a plan-shape regression" (21/21 result sets byte-identical while Q2 went
43× slower).

1. **TPC-H SF=1, three arms**: `GOOPG_GATHER_PATHS` = `off` / `top` / `all`,
   pinned regime `GOGC=100 GOMEMLIMIT=12GiB GOOPG_ANALYZE_SEED=20260905
   PGSHAPED=1 COLLAPSE=1`, fresh capped server per arm via
   `scripts/tpch-acceptance-arm.sh`, values via `tpch-runner -digest`/`-diff`
   (24 MATCH), plan capture + `scripts/pg-plan-parity-diff.py` + `make
   plan-gate`. **Time every query whose plan moved**, individually — the suite
   total hides a single 43× query behind 21 unchanged ones, and the per-query
   noise band is ±17 %.
2. **TPC-DS SF0.5 sweep**: `PASS=95 CKMISMATCH=0`.
3. **The specific hypothesis to test**, from §7.2: on aggregating queries, does
   `top` lose the `splitAggregate` shape? Read the EXPLAIN for a `Finalize
   HashAggregate` that became a plain aggregate over a Gather. If it does — the
   prediction — the finding is "C-19f is correct and cannot be turned on until
   C-19g", written up the way C-19d §5.1 was, and the default stays `off`.
4. **The D-05 re-run**, which is the acceptance argument for the whole C-19
   sequence and now has both prerequisites (C-19d's reader, C-19f's upward
   propagation) plus the E-09a executor its price describes: re-derive the
   build-cost correction at a **1× multiplier** (§2.1 — NOT by re-applying
   `tmp/d05p4-buildcost.patch`), with the mode on, and check whether Q5 / Q9 /
   Q10 still collapse. If they do, the remaining gap is C-19g, not the price of
   a hash join.

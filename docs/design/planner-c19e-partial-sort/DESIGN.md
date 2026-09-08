# C-19e (P5-05) — re-decide `Gather Merge → Sort → Parallel scan` by cost

*take3 08 §8; gate take3 09 §5 P5. Sibling designs:
`../planner-c19d-gather-paths/DESIGN.md`, `../planner-c19f-parallel-hashjoin/DESIGN.md`,
`../planner-c19g-partial-agg/DESIGN.md`.*

## 1. The rule this replaces

`MaybeAddGather` has three predicates that decide plan shape by reading
structure and sizes, never `PlanCost`:

| predicate | question | status |
|---|---|---|
| `splitAggregateIsProfitable` | split the aggregate? | replaced by a path tournament at C-19g |
| `sortPartialRootPays` | sort in the workers? | **this item** |
| `terminatesPartial` | where must the Gather sit? | structural; not a cost question |

`sortPartialRootPays` answers with a type switch over the driving scan: a
parallel seq scan says yes, an `*IndexScan` / `*IndexOnlyScan` says no. The
decline is measured, not preferred — enabling it took TPC-H q16 1.5 s → 2.3 s
and q13 4.2 s → 6.8 s (M0134-0189), with a CPU profile putting 34 % of q16 in
`sortOp.lessRows` / `sortTailWithCTIDs` and the scan nowhere in the top nodes.

The rule used to decline for a **second** reason: `gatherMergeOp` attached only
the seq-scan block allocator to its workers, so a per-worker Sort over an index
scan returned every row once per worker. **That reason is gone** — E-10
(`a22d995c8`) gave both gather operators a shared `parallelClaimSet` covering
all three claim kinds, with an anti-drift test, and `c92a9293d` admitted
index-driven subpaths under Gather Merge. Only the cost argument survives, and
a cost argument is exactly what a cost comparison should adjudicate.

## 2. What the two candidates are

Declining does **not** serialise the subtree. `findPartialSubtree` falls
through `terminatesPartial(*Sort)` and lands the Gather one level lower. So the
choice is between two parallel plans that differ only in which side of the
boundary the sort runs on:

```
worker-side (accept)     Gather Merge -> Sort -> <partial subtree>
leader-side (decline)    Sort -> Gather -> <partial subtree>
```

Both are plans the post-pass really builds; neither is a hypothetical. The
trade is the one the rule's own comment states: N sorts of R/N rows instead of
one of R rows saves exactly the `log N` factor and nothing else, and it has to
pay for a k-way merge stage on top plus a 5 % IPC surcharge
(`gatherMergeIPCFactor`, costsize.c:533).

## 3. Mechanism

`partialsortpaths.go`, modelled line-for-line on C-19g's
`partialaggpaths.go`:

1. `createPartialSortPaths` fetches an `UpperOrdered` rel and builds a **shared
   input seed** — one `newPrebuiltPath` over `srt.Child`, `Rows` divided by
   `get_parallel_divisor`, `Cost` from `legacyDisplayCostOf`.
2. The worker-side arm: `costSortRun` on **per-worker** rows, then
   `gatherMergeCost` on `compute_gather_rows` of that path.
3. The leader-side arm: `gatherCost` over the seed on **full** rows, then
   `costSortRun` on full rows in the leader.
4. Both are filed with `addPath`; `setCheapest` adjudicates;
   `workerSideWins()` asks whether the worker-side path is `CheapestTotal`.

**No new constant.** Every term is an existing PG-faithful function —
`cost_sort`, `cost_gather`, `cost_gather_merge`, `get_parallel_divisor`. The
rule it replaces had one hard-coded type switch; the replacement has none.

**Why a post-pass site may decide this honestly.** Both cost chains are
strictly additive in the input (`costSortRun` adds to the input's total;
`gatherCost` / `gatherMergeCost` add to `sub.Total`), so the input's absolute
price appears exactly once in each arm and cancels. This is C-19g DESIGN §3.1's
argument, unchanged.

`DefaultPlannerSettings().costParams()` supplies the GUCs, for C-19g's reason:
`MaybeAddGather` runs post-cache and no `PlannerSettings` is in scope. That is
strictly better than the constant it replaces, and the residual gap is the
existing ledger row M0127-P5.7-a.

## 4. Flag

`GOOPG_PARTIAL_SORT_PATHS`, `off` by default, resolved by
`partialSortModeFromEnv` (fail-closed: an unrecognised value is `off`), read
once at process start, rendered into the flag-provenance table so an artefact
names the arm it measured. `off` delegates to `sortPartialRootPays` unchanged,
so the default arm and the serial control arm are bit-identical to C-19g's
behaviour.

`partialSortRootPays` also falls back to the rule when the tournament cannot be
priced at all (`workers <= 0`, no child): a comparison that cannot be run is
not evidence for either side.

## 5. Measurement — the verdict

See §6 of this document (measurement addendum). The item permits recording a
**permitted divergence** (take3 09 §4.4 case 1) if goopg's costs still choose
leader-side sorting — which is the outcome the M0134-0189 timings predict, and
the outcome that would make the retired rule *correct* rather than merely
convenient.

## 6. Measurement addendum (2026-09-07)

Regime: SF=1 TPC-H, **private clone** `tmp/c19h-data` on port 5533 (the shared
65433 cluster was reserved by peers, and two agents sharing one data directory
damaged its WAL earlier the same day). One engine image across every arm —
`# engine-binary:` verified identical in each arm header. `GOMEMLIMIT=12GiB`,
`GOGC=100` (the bench default `GOGC=off` OOM-killed the scope on Q9),
`GOOPG_ANALYZE_SEED=20260905`, `GOOPG_PGSHAPED_DP=1`, `GOOPG_PGSHAPED_COLLAPSE=1`
(both are ON by default in the engine; the historical arm scripts pin them to
`0`, which is **not** today's default and silently measures the legacy
rewriter — an arm run that way shows the search producing no Gather at all).

### 6.1 What moves

Exactly **one** plan in 22, and it is q16 — the query the retired rule's own
measurement names:

```
off  GroupAggregate -> Sort -> Gather -> Hash Anti Join -> … -> Parallel Index Only Scan
on   GroupAggregate -> Gather Merge -> Sort -> Hash Anti Join -> … -> Parallel Index Only Scan
```

`make plan-gate` against `plan_snapshots/c05-c04b-20260907.txt`:

| arm | structural | costs |
|---|---|---|
| `PS=off` (serial control / default) | **22/22 MATCH** | **22/22 MATCH** |
| `PS=on` | 21/22, q16 diverged as above | — |

q13 — the rule's second cited regression — **does not move at all** under the
new verdict, so the 4.2 → 6.8 s number has no plan to attach to any more.

### 6.2 The verdict, and what it is worth

The cost model **disagrees with the retired rule**: it chooses the worker-side
sort. And the measurement supports the cost model, not the rule.

q16, five paired observations on one engine image (two full 22-query sweeps in
both orders, plus three interleaved q16/q17-only reps):

| | off | on |
|---|---|---|
| full sweep, off-then-on | 0.80 | 1.42 |
| full sweep, on-then-off | 0.83 | 0.68 |
| isolated rep 1 | 0.89 | 0.84 |
| isolated rep 2 | 0.77 | 0.70 |
| isolated rep 3 | 0.82 | 0.66 |
| **median** | **0.82** | **0.70** |

The single 1.42 s reading is the only one above the off arm's own spread and it
did not reproduce in four subsequent pairings, including the reversed-order
sweep in which q16 came out **faster** with the tournament on. Suite totals:
143.80 / 146.73 (off/on) in the first pair and 139.37 / 138.25 in the reversed
pair — both inside the suite's own ±spread, so **no suite claim is made**.

**The historical q16 1.5 → 2.3 s and q13 4.2 → 6.8 s regressions do not
reproduce.** They were measured at M0134-0189, before E-10 gave Gather Merge a
real claim set and before the index-driven subpath admission; the shape they
condemned is not the shape that runs today. This is the same pattern the
`GOOPG_INDEXKEY_HARVEST` row records — a catastrophe pinned in a comment that
stopped reproducing without the note being revisited.

**No permitted divergence is recorded**, because the case the item anticipated
(costs still choose leader-side) did not occur.

### 6.3 Value gates

`tpch-runner -digest` / `-diff`, both full pairs: **24 MATCH, 24/24, VERDICT
PASS** — compared on values, not on row counts.

### 6.4 A latent EXPLAIN bug the verdict exposed

`rebuildWithGather`'s merge arm called `stampParallelScan(root)` with `root`
= the `*Sort`. `stampParallelScan` has no `*Sort` arm — deliberately, since
`terminatesPartial` lists `*Sort` — so the call fell through every arm and
returned the tree unchanged, and the scan under a Gather Merge rendered with
**no `Parallel ` label** while the workers really were splitting it.

Label-only: the executor reads `SeqScan.Parallel` and its siblings in
`operators_explain.go` and nowhere else, so no rows were ever wrong. Latent
since P7 because the shape was unreachable — `sortPartialRootPays` declines
every index driver, and no TPC-H plan reached the arm with a seq-scan driver
either. C-19e's cost verdict makes it reachable on q16, which is "an unwinnable
path is an untested path" one more time.

Fixed by stamping the Sort's **child**, mirroring the asymmetry
`findPartialSubtree` already has (`drivingScan(srt.Child)`, not
`drivingScan(srt)`), with a shallow copy so the cross-session plan cache is not
written through. Pinned by `TestGatherMergeStampsDrivingScan`;
`TestGatherMergeIsNonMutating` updated, because the Sort below the boundary is
now a copy rather than the shared cached node — the property that matters
(nothing reachable from the original root is written) is asserted unchanged.

### 6.5 Default

Stays **off**, for C-19f/C-19g's reason: the flip moves a plan and the suite
move is inside its own spread, so it needs the shared `plan_snapshots/` re-pin
that is being held outside this item. The measurement above is what the flip
decision should be taken on; the recommendation is that q16 is not an obstacle
to it.

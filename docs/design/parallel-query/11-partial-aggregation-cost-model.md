# 11 — A Cost Model for the Partial-Aggregation Split

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) — revised after review |
| date | 2026-07-21 |
| depends on | [06](06-parallel-aggregation.md), [08](08-planner-integration.md) |
| premise | ANALYZE has been run on the relations being planned |

## 0. Why this chapter exists — and a correction to P9

P9 placed the Partial/Finalize split and measured it: TPC-H Q1 went from 7.15 s
to 4.72 s at four workers, because the Gather stopped carrying ~5.9 M rows to a
single leader-side aggregate that produced four groups.

It shipped with **no gate**. Every decomposable aggregate over a
partial-capable subtree splits.

**P9's commit message and TODO entry name the wrong query as the hazard, and
this chapter corrects that on the record.** Both say Q13 groups by `c_custkey`
into "150,000 groups for 150,000 rows". Q13's aggregate does not read
`customer`; it reads the output of `customer LEFT JOIN orders`, which at SF1 is
~1.5 M rows. Its split reduces 1.5 M rows to 150 k groups per worker — a **4×
reduction that the split should and does perform.** Q13 was never the failure
case. The error came from reading the probe side's row count as the aggregate's
input.

The real failure case is in the same reference set. **Q18's inner aggregate is
`select l_orderkey from lineitem group by l_orderkey`**: 1.5 M distinct keys
over 6 M rows. Each worker sees 6 M/d rows and emits ~6 M/d groups. Nothing is
reduced, every input row becomes a state, and each state is merged through a
single contended mutex.

Two earlier chapters called for this gate and neither was honoured:

- [06](06-parallel-aggregation.md) §4.2: with no hash-agg spill, N workers each
  holding a full group table is a memory-growth risk serial execution does not
  have.
- [08](08-planner-integration.md) §2.1: "estimated distinct group count is an
  input to the gate."

## 1. What PostgreSQL actually does

### 1.1 `estimate_num_groups` is called with different inputs per node

`estimate_num_groups()` (`selfuncs.c:3449`) takes `input_rows` as a
**caller-supplied parameter**. In the parallel path the planner calls it for the
FINAL node with `cheapest_path->rows` (`planner.c:4130-4134`) and for the
PARTIAL node with `cheapest_partial_path->rows` (`planner.c:7453-7458`) — the
per-worker count, already divided by `get_parallel_divisor()`. (A third call at
`planner.c:7448` uses `cheapest_total_path->rows` for the non-parallel partial
node used by partitionwise aggregation, which goopg does not have.)

The dominant difference between the two results is the final clamp
(`selfuncs.c:3779-3780`):

```c
	if (numdistinct > input_rows)
		numdistinct = input_rows;
```

**There is no dedicated partial-group model.** PG does not compute "how many of
the n groups will a random 1/d subset contain"; it re-runs the same estimator
with a smaller `input_rows` and lets the clamp saturate. When
`ndistinct ≪ rows_per_worker` both estimates equal `ndistinct` and the reduction
is real (Q1, Q13). When `ndistinct ≥ rows_per_worker` the partial estimate
saturates, correctly predicting one output state per input row (Q18).

Two other `input_rows`-dependent paths exist and matter for §5: a volatile
grouping expression returns `input_rows` outright (`selfuncs.c:3562-3565`), and
the all-constant/all-boolean early return carries its own clamp (`:3593`).

### 1.2 Group-count estimation proper

Per relation (`selfuncs.c:3660-3714`): boolean expressions contribute 2 and are
dropped (`:3515-3519`); expressions reduce to their component Vars, so
`GROUP BY a, a+b` becomes `GROUP BY a, b` (`:3570-3578`); the surviving
ndistincts are **multiplied**, i.e. assumed independent (`:3664`); the product
is clamped to **`rel->tuples`** for one Var or `rel->tuples * 0.1` for several,
never below the largest individual ndistinct (`:3697-3714`). Note that this
intermediate clamp is against the relation's total row count — *not* against
`input_rows`, which appears only in the final clamp.

`get_variable_numdistinct()` (`selfuncs.c:6258`) is the oracle: a **positive**
`stadistinct` is an absolute count, a **negative** one a fraction of
`rel->tuples`, and `0` unknown, falling back to
`ntuples < 200 ? ntuples : DEFAULT_NUM_DISTINCT` (`:6367-6377`).
`DEFAULT_NUM_DISTINCT` is 200, deliberately equal to `1/DEFAULT_EQ_SEL`
(`selfuncs.h:33-56`).

### 1.3 What the split costs

**`cost_agg()` has no `aggsplit` parameter** (`costsize.c:2682`). The
partial/final distinction is carried entirely by the `AggClauseCosts` that
`get_agg_clause_costs` builds (`prepagg.c:559-617`): a COMBINE split charges the
combine function instead of the transition function, and `:593-617` skips the
argument-expression and FILTER costs at the upper level because the initial
node already paid them.

The AGG_HASHED arm (`costsize.c:2751-2770`) charges, per **input** tuple,
`transCost.per_tuple + cpu_operator_cost * numGroupCols`; per **output group**,
`finalCost.per_tuple + cpu_tuple_cost`.

**The Gather formula** (`costsize.c:469-470`): `parallel_setup_cost` is a flat
startup addend multiplied by nothing; `parallel_tuple_cost` multiplies the rows
*emerging from the Gather*, which `compute_gather_rows()` (`costsize.c:6625`)
reconstructs as `partial_rows * divisor`.

**The parallel divisor** (`costsize.c:6486-6494`) adds the leader's
contribution **only when positive**:

```c
	leader_contribution = 1.0 - (0.3 * path->parallel_workers);
	if (leader_contribution > 0)
		parallel_divisor += leader_contribution;
```

So `d(1) = 1.7`, `d(2) = 2.4`, `d(3) = 3.1`, and `d(w) = w` for `w ≥ 4`. This
chapter uses those values throughout; earlier drafts quoted a formula without
the guard and are superseded.

### 1.4 The decision itself

No threshold, no heuristic. `can_partial_agg()` (`planner.c:7787`) is a pure
feasibility test; both candidate paths enter one pathlist and `set_cheapest()`
picks by total cost (`planner.c:3880`).

## 2. What goopg can and cannot reproduce

**goopg has no absolute cost model.** `cost=0.00..0.00` in EXPLAIN is a
hardcoded format-string literal (`operators_explain.go:371-378`); only `rows=`
is real. Every cost number in the planner is a relative quantity compared
within a single decision and then discarded. There is no `Path`, no
`add_path`, no startup/total split, and no node carries a cost.

So `set_cheapest` is unavailable. What *is* available: **the two alternatives
are identical below the aggregate** — same scan, same filter, same rows
arriving. That shared term cancels, and the remainder can be compared in
self-consistent relative units with no absolute scale.

### 2.1 goopg's split does not use the Gather to move state

This is the single most important divergence from PG, and an earlier draft of
this chapter got it wrong.

In PG, partial aggregate states are serialised and cross the Gather as tuples,
which is why `parallel_tuple_cost * (ndistinct * divisor)` is the term partial
aggregation attacks.

In goopg it does not happen. P9's Partial node emits **zero rows**
(`operators_join_agg.go:1457-1479`, `o.rows = nil`); each worker merges its
groups into a shared `aggPartialAccum` guarded by one mutex, and the Finalize
node reads that map after draining the Gather to EOF. Nothing aggregate-shaped
crosses the channel.

The cost consequence is not that the term disappears — it moves and gets
dearer. Instead of `Gw·d` tuples through a batched channel, there are `Gw·d`
**merges through a single contended mutex**, each a deep merge over
`aggRuntime`'s pointer fields ([06](06-parallel-aggregation.md) §2.3). That
serialisation is the split's real cost, and it is what the model must charge.

(This also supersedes [06](06-parallel-aggregation.md) §4.2's description of the
leader "re-grouping by key and combining" — P9 combines on insert in the
workers, not afterwards in the leader.)

## 3. The model

| symbol | meaning |
| --- | --- |
| `R` | rows arriving at the aggregate (after filters) |
| `A` | number of aggregate calls in the node |
| `k` | number of GROUP BY expressions |
| `d` | parallel divisor (§1.3) |
| `G` | `estimateNumGroups(exprs, R)` — final group count |
| `Gw` | `estimateNumGroups(exprs, R/d)` — **per-worker** group count |
| `ρ` | `Gw·d / R` — the reduction ratio. The clamp guarantees `ρ ∈ (0, 1]`. |

### 3.1 The two alternatives

**Without the split** (Gather below the aggregate — the pre-P9 shape, and the
fallback when the gate refuses):

```
serial:  R·c_xfer                        rows crossing the Gather
       + R·(A·c_trans + k·c_hash)        leader aggregates every row
       + G·c_out
```

**With the split:**

```
parallel: (R/d)·(A·c_trans + k·c_hash)   each worker aggregates its share
        + Gw·c_out                       each worker emits its groups
serial:   Gw·d·A·c_merge                 every state through ONE mutex
        + G·c_out
```

### 3.2 The threshold

Cancelling `G·c_out`, dividing through by `R`, and substituting `Gw = ρR/d`:

```
split wins  ⇔  c_xfer + (A·c_trans + k·c_hash)·(1 − 1/d)  >  ρ·A·c_merge + (ρ/d)·c_out
```

so

```
        c_xfer + (A·c_trans + k·c_hash)·(1 − 1/d)
ρ*  =  ───────────────────────────────────────────
                A·c_merge + c_out/d
```

Two corrections to an earlier draft, both of which an implementer transcribing
the printed inequality would have inherited: `(1 − 1/d)` must **not** multiply
`c_xfer` (the split removes that transfer entirely rather than dividing it
among workers), and `Gw·c_out` is **not** common to both sides — the split adds
a per-worker emit the serial plan does not have.

### 3.3 The constants decide, and `c_merge` decides most

An earlier draft claimed the constants barely matter because the real cases sit
orders of magnitude apart. **That is false and it was the most dangerous claim
in the chapter.** The clamp guarantees `ρ ≤ 1`, so the entire decision lives in
`(0, 1]` and `ρ*` lands squarely inside it. The constants are the gate.

Anchoring on `c_out = 1` (retrieving one group from a hash table, PG's
`cpu_tuple_cost`):

| constant | PG ratio | goopg | why |
| --- | ---: | ---: | --- |
| `c_xfer` | 10 | **2** | PG writes a `shm_mq` and the reader copies out. goopg batches into a channel, so per-tuple channel cost amortises away — but every transferred row still pays `MaterializeForTransfer`, a deep copy ([03](03-concurrency-substrate.md) §3.1). A row copy, not an IPC round trip. |
| `c_trans` | per aggregate | **1** | One transition call, charged `A` times. |
| `c_hash` | 0.25 | **0.25** | Per group column per input row; nothing suggests goopg differs. |
| `c_merge` | n/a | **4** | §2.1's mutex-serialised deep merge. Dearer than a transition because it is a lock acquisition plus a map probe plus a pointer-field merge, and because it does not parallelise. |
| `c_setup` (`parallel_setup_cost`) | 100 000 | **50** | Goroutine start, arena `Acquire`, context derivation. Real, but three orders below `fork()` plus DSM. Does not enter §3.2 — both alternatives pay it — and is stated only because [08](08-planner-integration.md) §3 promised a value. |

`c_trans` and `c_merge` scale with `A`; `c_hash` scales with `k`. This matters:
with the values above, `ρ*` is 0.96 for a single aggregate on two group columns
at `d = 4`, but 0.26 for Q1's eight aggregates. More aggregates means more
combine work per group, so the split must reduce more to pay.

Worked cases at SF1:

| query | `R` | `Gw` | `d` | `ρ` | `ρ*` | verdict |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| Q1 | 5.9 M | 6 | 4.0 | 4×10⁻⁶ | 0.26 | **split** |
| Q13 | 1.5 M | 150 k | 2.4 | 0.24 | 0.83 | **split** |
| Q18 inner | 6.0 M | 1.5 M | 4.0 | 1.00 | 0.96 | **refuse** |

Q18 is refused by a margin of 4 %. That is uncomfortably tight and is stated
rather than hidden: `c_merge` is the constant it turns on, it is calibrated
from one measurement, and the honest reading is that the cost arm discriminates
the clear cases while §3.4 is what makes the dangerous case safe.

**Calibration basis.** Fitting `T(n) = S + P/n` to the pre-P9 Q1 sweep
(7.84 / 7.15 / 7.35 s at 3/5/9 lanes) puts ~6.1 s in the serial tail; the
post-P9 sweep (4.72 / 4.01 s at 5/9) puts ~3.1 s there. The split removed ~3.0 s
of leader work over ~5.9 M rows — ~0.5 µs per row for transfer plus
aggregation. `c_merge` has **no** measurement behind it; it is reasoned from
the operation's shape. Recalibrating against a measured high-`ρ` query is
required follow-up, not optional polish.

### 3.4 The memory ceiling is a separate, hard refusal

[06](06-parallel-aggregation.md) §4.2: there is **no hash-agg spill**. Live
entries at peak are `d·Gw` across the worker tables plus `G` in the shared
accumulator.

Following PG's per-backend semantics (`hashentrysize` against `work_mem` inside
each worker), the check is per-worker, plus a goopg-specific one for the shared
map that PG has no equivalent of:

```
refuse if   Gw · entrySize  >  work_mem
       or   G  · entrySize  >  work_mem
```

`entrySize` is `unsafe.Sizeof(aggRuntime{})·A` plus the group-key string plus
map overhead — computed by a helper next to `aggRuntime` so it tracks the
struct rather than drifting from it.

This refuses outright and is **not** a cost term. Making it one would let a
large enough throughput win buy an OOM, and an OOM is not a slow query. Given
§3.3's thin margin on Q18, this ceiling is doing more of the real safety work
than the cost arm is.

### 3.5 No statistics means no split

When the group-key columns carry no usable statistics, refuse. Accepting
without evidence risks the bad direction — N group tables with no spill — while
declining restores the pre-P9 shape, which is slower on Q1 but never dangerous.
PG can afford `DEFAULT_NUM_DISTINCT` here because it has a spill path; goopg
cannot.

**This refusal is only safe if statistics are actually reachable**, which §5.1
shows they currently are not for most of TPC-H.

## 4. Two prerequisites the premise does not survive without

### 4.1 `n_distinct` is a sample count, not an estimate

`computeColumnStats` stores the raw distinct count **of the sample**
(`operators_analyze.go:474`, `stats.NDistinct = int64(len(freq))`). The sample
is `statsTarget * 300` rows — 30,000 by default — so on any larger table this
**saturates**: `l_orderkey`'s 1.5 M distinct values report as ~30,000.

For Q18's inner aggregate that turns `ρ = 1.00` into
`ρ = 30000·4/6M = 0.02` — a claimed 50× reduction where there is none. The gate
would split the one query it exists to refuse. A sample-saturated n_distinct
does not add noise; it systematically under-reports exactly the
high-cardinality columns the gate must catch.

(Q13 is unaffected either way: `ρ = 0.24` with correct statistics, `0.05` with
saturated ones — split under both. Fixing this changes Q18's verdict, not
Q13's.)

PG solves this in `compute_scalar_stats` with the Haas–Stokes estimator, and
emits a **negative** `stadistinct` when the count appears proportional to table
size.

**Storage convention.** `catalog.ColumnStats.NDistinct` is `int64` and every
consumer treats `<= 0` as "no estimate" — `cardinality.go:206,231`,
`subplan_cost.go:127`, `bushy.go:797,807`, `selectivity.go:117`,
`nl_index_join_selectivity.go:49`. **Seven sites, none checking for a
negative.** Introducing negatives in memory would silently break all seven. So:

- **in memory**: absolute, but table-scaled rather than sample-scaled;
- **on disk**: the signed PG convention, which `internal/catalog/codec.go:1424`
  already documents (`StaDistinct float32  // <0 → fraction; >0 → count`), so a
  real PG reading goopg's `pg_statistic` sees correct semantics.

Both write paths clamp negatives away
(`pg18_user_catalog_rows.go:1300-1304`, `catalog/pgstats.go:67-70`) and the
restore path discards them into 0. All three need the decode from
`get_variable_numdistinct` (`selfuncs.c:6342-6377`).
`columnNDistinctOverride` (`operators_analyze.go:389`) already implements that
decode for the `n_distinct` reloption and is the model to follow.

### 4.2 `RowCount` is never restored

`loadStatisticsFromHeap` (`internal/initdb/open.go:3429`) ends with
`stats := &catalog.TableStats{Columns: colStats}; cat.SetTableStats(tbl, stats)`.
`RowCount`, `Pages` and `AvgWidth` are left zero, and because `SetTableStats`
pointer-replaces the whole struct (`catalog.go:12167`) it actively clobbers
anything already there. This is ledger row `pq-P6`.

The effect is total: after a restart `tableRows()` returns 0, so `EstimateRows`
returns 0 for every relation, so `R` does not exist and the model has no input.
"ANALYZE has been run" stops being true the moment the server bounces.

goopg cannot follow `vac_update_relstats` directly — its `pg_class` is rendered
virtually from `catalog.Table` and the on-disk heap is only appended at CREATE
TABLE. But `persistStatsToPGStatistic` (`operators_analyze.go:184`) already
appends monotonically and the restore takes the last live tuple; appending a
fresh `pg_class` row carrying `reltuples`/`relpages`, with a matching reload, is
the same shape. Follow `UpdateRelStats` (`catalog.go:12184`), which merges
without discarding `Columns`, rather than `SetTableStats`.

#### 4.2.1 Two findings from a first implementation attempt

Recorded because both cost real time to find and neither is visible from
reading the code.

**The append must target the session's database, not `DefaultDBOid`.**
`persistStatsToPGStatistic` hardcodes `catalog.DefaultDBOid`, so copying its
`RelFileNode` for pg_class looks correct and silently does nothing:
`pg_class` rows are written per database by CREATE TABLE, so the appended row
lands in `base/<DefaultDBOid>/1259` while the reload scans
`base/<sessionDB>/1259`. The row is written, is durable, and is never read.
Use `catalogDBOids(ctx)`, which is what the DDL path uses.

With that fixed, one ANALYZE + restart round-trips correctly: the reload sees
both the CREATE TABLE row (`reltuples=0`) and the ANALYZE row
(`reltuples=500`), and the OID de-duplication keeps the later one.

**A SECOND ANALYZE + restart does not, and the cause is not yet established.**
On the third server start the pg_class reload does not observe the relation's
rows at all — the table is present but its relstats read zero, so something
other than `loadUserTablesFromHeapForDB` is supplying it on that path. This is
the steady-state case (ANALYZE runs repeatedly), so the work was reverted
rather than landed half-working. The next attempt should start by establishing
which code path reconstructs a relation on a start that follows a start which
already reconstructed it — the two catalog-DDL durability mechanisms
(pg_class heap-append versus goopg-private WAL record and startup replay) are
the obvious suspects.

**Autovacuum, correctly stated.** An earlier draft had this backwards.
`needsVacuum` (`autovacuum/launcher.go:233-236`) is:

```go
	if tbl.Stats == nil {
		return true
	}
	return tbl.Stats.RowCount > 0
```

Nil stats ⇒ vacuum. The bug is the opposite of what was claimed: the stats
reload makes `Stats` non-nil with `RowCount == 0`, so this returns **false** and
autovacuum is *suppressed* on restarted servers. Restoring `RowCount` fixes a
regression the reload itself introduced.

## 5. `estimateNumGroups`

Replaces the group-count logic in `estimateAggregate` (`cardinality.go:224-243`),
which handles a single bare `ColumnRef` and otherwise returns `child / 2`.

1. no grouping expressions → 1;
2. any grouping expression containing a **volatile** function → refuse
   (`reliable = false`). PG returns `input_rows` here (`selfuncs.c:3562-3565`);
   goopg refuses instead, because §3.5's fallback is refusal anyway and an
   unreliable estimate feeding a memory ceiling is worse than none;
3. boolean-typed expression → contributes 2, no statistics needed, and does
   **not** make the estimate unreliable (PG's `get_variable_numdistinct`
   returns 2 with `isdefault = false`);
4. otherwise decompose to component `ColumnRef`s, de-duplicate, take each
   `NDistinct`;
5. multiply the survivors (independence);
6. clamp to the **relation's row count** for one variable, `rowCount / 10` for
   several, never below the largest individual ndistinct. Note this is
   `rel->tuples`, **not** `inputRows` — an earlier draft substituted the latter,
   which biases `Gw` downward, i.e. toward splitting, the dangerous direction;
7. final clamp to `inputRows`; floor 1.

Return `(int64, bool)`, the bool following the existing
`selectivityEstimate.reliable` idiom (`selectivity.go:411`). **Composition
rule**: unreliable if *any* contributing column lacks statistics — the product
is only as good as its weakest factor, and a partial product silently
understates `Gw`.

Multi-relation grouping (§5.1) is estimated per relation and the per-relation
results multiplied, matching `selfuncs.c:3660-3714`; the `/10` clamp applies
within a relation, not across.

### 5.1 The statistics lookup must learn about joins — and is currently wrong

`columnStatsForChild` (`selectivity.go:385-403`) handles `*SeqScan`, `*Filter`,
`*Sort`, `*Project`. Two problems, both blocking:

**No `*Join` case.** `findPartialSubtree` reaches aggregates over joins —
`drivingSeqScan` (`parallel.go:320-333`) descends a partial-capable hash join's
probe side — so P9 today splits Q3, Q5, Q7–Q13, Q18 and Q21. With §3.5's
refusal and no `*Join` descent, **all of them would stop splitting**, leaving
Q1 and Q6. That is a large silent regression, and §7's acceptance criteria are
written to catch it.

Adding the case is not a one-liner: the aggregate's `ColumnRef.Index` indexes
the *join output* schema, so recursing into the right side requires subtracting
the left side's width, and which physical relation a column belongs to
determines which per-relation clamp it falls under in step 6.

**The `*Project` descent is a silent wrong answer.** It passes `idx` through
unremapped:

```go
	case *Project:
		return columnStatsForChild(idx, x.Child)
```

`Project.Targets[i]` is an arbitrary expression over the child's schema, so
indexing `Stats.Columns[idx]` with the Project's *output* ordinal returns
whichever table column happens to sit at that position. An earlier draft of this
chapter told the implementer to prefer this helper over
`columnNDistinctForChild` (`cardinality.go:253`) because the latter refuses to
descend `*Project`. **That is backwards**: refusing is a false negative,
descending unremapped is a wrong number, and for a gate that discriminates
purely on `NDistinct` a wrong number is far worse. Fix the remapping — resolve
`Targets[idx]` to a `ColumnRef` and recurse on *its* index, refusing when the
target is not a bare column reference — and make both helpers share it.

## 6. Where the gate runs, and what must be plumbed to it

Not mentioned by an earlier draft, and every item here is load-bearing.

**Call site.** Inside `findPartialSubtree`'s loop (`parallel.go:250`), at the
`splitAgg` branch. It must be there and not later: refusing has to let the walk
fall through `terminatesPartial(*Aggregate)` and place the Gather *below* the
aggregate, and that fallback is exactly the "without the split" alternative
§3.1 costs against.

**The worker-count ordering problem.** `MaybeAddGather` computes `workers`
*after* `findPartialSubtree` has chosen the target (`parallel.go:110-129`), but
the model needs `d` to compute `Gw`. There is no circularity — the sizing input
(`agg.Child`) is in scope at line 250 — so the fix is to pass
`ParallelSettings` into `findPartialSubtree` and let it call
`computeParallelWorkers` itself. Stated explicitly so nobody concludes the
model is unimplementable.

**`d` is not derivable from `ParallelSettings` today.** It needs
`parallel_leader_participation`, which lives on the executor context
(`operators_gather.go:172`), not in `ParallelSettings` (`parallel.go:57-72`).
So does `work_mem` for §3.4. Both must be added to the struct and populated at
the two injection sites (`dispatch.go`, `dispatch_extended.go`).

**Which relation sizes the workers.** `computeParallelWorkers` sizes from
`drivingSeqScan(subtree)` — for a GROUP BY over a join that is the **probe**
side, not the aggregate's input. For Q13 the probe is `customer`, so it yields
2 workers and `d = 2.4`, not the 4 a reader might assume. The worked table in
§3.3 uses the actual per-query `d` for this reason.

**Plan cache.** `MaybeAddGather` runs *after* the cache lookup on both protocol
paths (`dispatch.go:1197-1207`, `dispatch_extended.go:124`), so the split
decision is re-made per execution and picks up fresh statistics — load-bearing
and worth stating. But `pc.Invalidate()` fires only for `*planner.DDL`
(`dispatch.go:2974`), so **ANALYZE does not invalidate the cache**: the cached
serial subtree keeps its pre-ANALYZE join order and build-side choice while the
gate reads post-ANALYZE numbers. Not a correctness bug — both are valid plans —
but it means the first ANALYZE does not fully take effect until the entry ages
out, and a test that ANALYZEs and re-plans in one session may see a stale shape.

**HAVING** needs no handling. PG passes `quals` to `cost_agg`; goopg's Finalize
evaluates them after combining, identically on both sides of the comparison, so
the term cancels.

## 7. Deliberately not reproduced

| PG feature | Why not |
| --- | --- |
| Extended statistics (`estimate_multivariate_ndistinct`) | goopg stores and reloads `pg_statistic_ext` but **no planner code reads it**. The independence assumption of §1.2 stands unaided. |
| Yao/Dell'Era restriction correction (`selfuncs.c:3756`) | Needs `rel->tuples` and `rel->rows` as separate quantities, which goopg's estimator does not distinguish at the aggregate's input. Named so the omission is visible. |
| Grouping sets | Refused upstream too (`planner.c:7799-7802`); goopg refuses via `aggregateSplitIsSafe`. |
| AGG_SORTED costing | goopg has one grouped implementation and it is hash-based. No second strategy to cost against. |
| `add_path`/`set_cheapest` | Requires the absolute cost model goopg lacks (§2). The self-contained comparison substitutes, and is exact here because both alternatives share their entire subtree. |

## 8. Acceptance

On SF1 with ANALYZE run:

- **Q1 still splits**, and the P9 gain is intact (~4.7 s at four workers);
- **Q13 still splits** — it is a 4× reduction and refusing it would be the
  regression, not the fix;
- **Q18's inner aggregate stops splitting** — its plan contains no
  `Partial HashAggregate` over `lineitem`;
- **the queries over joins (Q3, Q5, Q7–Q12, Q21) do not silently stop
  splitting** — §5.1's `*Join` descent is what stands between the gate and a
  large regression, and only a plan-shape check across the whole reference set
  will notice.

A gate that refuses Q18 by also refusing everything else is a regression
wearing a fix's clothes.

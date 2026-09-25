# M0144-0011a — ordering-claim propagation at the ORDERED-step boundary

Status: LANDED 2026-09-20
Kind: impl
Parent: M0144-0011
Milestone: M0144 (plan-parity harness, measurement-first era)
Slice evidence: `analysis/m0144/m0144-0011-q8-slice-trace.md`,
`docs/design/0100-0149/m0144-0011-q8-vertical-slice.md`

## 1. The divergence this closes

M0144-0011's Q8 vertical slice traced the TPC-DS SF0.25 top first-divergence
cluster (`Limit -> {GroupAgg|IncrSort|Sort+CTE}` vs PG's `Limit -> GroupAgg`)
to layer 2, candidate generation. The finding, verbatim from the trace: the
winning `PathAgg` carries `pathkeys=1`, and **nothing transfers that claim to
the ordered-step seed** — the seed reaches `addOrderedPaths` with `keys=0`, so
the `pathkeys_contained_in` arm cannot fire and a redundant `Sort` is stacked
over a `GroupAggregate` that already emits in ORDER BY order.

PG never loses that claim. `create_agg_path` copies the subpath's pathkeys onto
an `AGG_SORTED` `AggPath`:

```c
/* postgres/src/backend/optimizer/util/pathnode.c:3412-3416 */
if (aggstrategy == AGG_SORTED)
    pathnode->path.pathkeys = subpath->pathkeys;  /* preserves order */
else
    pathnode->path.pathkeys = NIL;                /* output is unordered */
```

and `create_ordered_paths` reads exactly that field for **every** member of the
input rel's pathlist, taking a sorted-enough path as-is:

```c
/* postgres/src/backend/optimizer/plan/planner.c:5337, :5344-5348 */
foreach(lc, input_rel->pathlist)
{
    Path *input_path = (Path *) lfirst(lc);
    is_sorted = pathkeys_count_contained_in(root->sort_pathkeys,
                                            input_path->pathkeys, &presorted_keys);
    if (is_sorted)
        sorted_path = input_path;
    ...
}
```

goopg's ORDERED step consumes a finished **Node**, not a pathlist, through
`inputNodePathkeys` (`internal/optimizer/upperorderedinput.go`). That walk knew
two sources of an ordering claim — a `*Sort` at the top and a searched-subtree
root — and its `default: nil` arm swallowed `*Aggregate`. So an aggregate that
provably emits its group-key order contributed no claim at all.

## 2. What landed

`aggregateEmissionPathkeys(agg *Aggregate) []PathKey` — the third source, added
as an arm of `inputNodePathkeys`' walk. It is the **node-level twin** of
`groupingEmissionPathkeys` (`upperorderedgrouping.go`), which already performs
the identical job for an unbuilt `*Path` candidate inside
`electOrderedGrouping`'s loop. Sibling paths must agree
(`pattern_sibling_paths_must_agree`), so every decline of the new function
mirrors one of its twin's, and a unit test pins that correspondence explicitly
(`TestAggregateEmissionPathkeysDeclinesTheSameShapesAsItsPathTwin`).

Why a derivation and not a copy: the node's `GroupExprs` and its child Sort's
`Keys` are written in the aggregate's **input** coordinates, while
`createOrderedPaths`' `keys` are resolved against the aggregate's **output**
schema. That is the one coordinate boundary the file header's rule 2 forbids
the walk to cross by descending. The arm therefore translates positionally, and
only where the translation is a tautology: the group-prefix output layout
(`[groups|aggs|grouping|passthrough]`, `plan.go:1310-1359`) puts group key `j`
at output position `j`, and that is VERIFIED per key
(`out[j].Name == groupExprName(groups[j])`), never assumed. Nothing is
translated in the sense the take3 C-07 header rules out — a failed check
returns nil, which is the pre-change answer exactly.

Declines (all mirroring the `*Path` twin):

- non-`AggStrategySorted` strategy — a hashed aggregate emits no order;
- non-`AggModeSimple`, grouping sets, or no group keys — mirrors the executor's
  sorted-agg guard (`internal/executor/operators_join_agg.go`);
- `GroupKeyOrder != nil` — the EXPLAIN-only index-remapped permutation, whose
  key positions are not the written ones this translation reads;
- a child that is neither `*Sort` nor `*GatherMerge` — nothing else at that
  position states an order (a Gather Merge emits the merged order of its sorted
  inputs, the same delivery contract PG relies on when it feeds Gather Merge
  paths into `create_ordered_paths`);
- leading child sort keys that are not the group keys positionally, a
  non-`*ColumnRef` group expression, or an output column that does not carry
  the group key's name.

Direction is carried, not defaulted: `SortAsc`/`NullsFirst` come from the child
sort key at that position, so a DESC ORDER BY over an ASC-emitting aggregate
still gets its Sort.

### Files

| file | why |
|---|---|
| `internal/optimizer/upperorderedinput.go` | the `*Aggregate` walk arm + `aggregateEmissionPathkeys` |
| `internal/optimizer/upperorderedgrouping.go` | comment only: its header named the now-closed gap |
| `internal/optimizer/upperorderedinput_test.go` | three tests: the claim, the decline table, the GatherMerge child |
| `internal/executor/explain_indent_test.go` | fixture repair, see §3 |

## 3. The fixture the change broke, and why repairing it is not weakening it

`TestExplainIndentDeepNesting` / `TestExplainAnalyzeIndentDeepNesting` pin
EXPLAIN's cumulative-indent arithmetic (raw columns 0, 2, 8, 14, 20) and need a
4-level `Sort / GroupAggregate / Sort / Index Scan` shape to exercise depth > 1.
Its fixture query used an **ascending** `ORDER BY 2` over `GROUP BY b` — which
is precisely the redundant Sort this slice removes, and the fixture's own
comment already flagged it as a shape goopg keeps where PG elides it.

The fixture now uses `ORDER BY 2 DESC`. That is a genuine re-ordering the
aggregate cannot deliver, so the 4-level shape is restored **without**
re-introducing a redundant node, and the resulting root line `Sort Key: b DESC`
doubles as evidence that the new arm carries direction rather than eliding any
ORDER BY over a grouped input. Every indent assertion is unchanged.

## 4. Measurement (AGENT.md D2)

Clean A/B: both arms captured in the same session with
`GOOPG_ANALYZE_SEED=20260905` pinned, against the same PG reference. Distinct
binaries, distinct plan files, **identical stats epoch** — the confound that
made the first (unpinned) attempt read `match 1 -> 2` purely from sampling.

| arm | binary sha256 | plans sha256 | stats epoch |
|---|---|---|---|
| base (HEAD `f23c3c249`) | `bdb4d6bade614b25` | `9e2ef18eae65371a` | `e4a554b2a4cfb710` |
| on (this change) | `163ba219e018a91a` | `1727126e8ed71456` | `e4a554b2a4cfb710` |

### 4.1 TPC-H SF1, parallel canonical mode

`estimate-audit -plan-only -serial=false` on a private `:5582` clone; PG half is
the committed `analysis/m0144/m0144-0001-pg-parallel.plans.txt`.

base:

```
PLAN-PARITY: queries=22 match=2 shapediff=20 unparsed=0 missingnode=0 error=0 timeout=0
CATEGORIES: join-order=15 join-method=11 scan-type=10 parameterisation=7 aggregation-strategy=7 sort-strategy=12 parallelism=15 qual-placement=3 rendering=1
CATEGORIES-EXCL-MATCH: join-order=15 join-method=11 scan-type=10 parameterisation=7 aggregation-strategy=7 sort-strategy=12 parallelism=15 qual-placement=3 rendering=1
```

on:

```
PLAN-PARITY: queries=22 match=2 shapediff=20 unparsed=0 missingnode=0 error=0 timeout=0
CATEGORIES: join-order=16 join-method=10 scan-type=10 parameterisation=7 aggregation-strategy=7 sort-strategy=11 parallelism=15 qual-placement=4 rendering=2
CATEGORIES-EXCL-MATCH: join-order=16 join-method=10 scan-type=10 parameterisation=7 aggregation-strategy=7 sort-strategy=11 parallelism=15 qual-placement=4 rendering=2
```

Match 2 -> 2: the floor holds, nothing lost. Every category delta is inside the
±3 noise band (sort-strategy −1, join-order +1, join-method −1,
qual-placement +1, rendering +1), so TPC-H registers **no movement** — expected,
since the TPC-H corpus is overwhelmingly aggregate-rooted with a single output
group, where a grouped emission order has nothing to deliver.

### 4.2 TPC-DS SF0.25 — the corpus the task named

Both arms are `scripts/tpcds-sf025-regression.sh sweep` plan captures (the same
channel `m0144-0002`'s census reads), diffed against the committed PG reference
`analysis/m0142/m0142-0012verify-tpcds-pg.txt` after normalising the sweep's
`===== Qn =====` headers to the census's `=== Qn` (content untouched).

base (`plans-20260920-074115.txt`):

```
PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
CATEGORIES-EXCL-MATCH: join-order=91 join-method=69 scan-type=59 parameterisation=58 aggregation-strategy=44 sort-strategy=67 parallelism=86 qual-placement=21 rendering=26
```

on (`plans-20260920-144915.txt`):

```
PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
CATEGORIES-EXCL-MATCH: join-order=91 join-method=70 scan-type=61 parameterisation=55 aggregation-strategy=44 sort-strategy=60 parallelism=85 qual-placement=24 rendering=26
```

**`sort-strategy` 67 -> 60 (−7)** — outside the ±3 band, and it is exactly the
category the task's `Expected movement` named. Match 2 -> 2 holds the SF0.25
floor (Q9, Q41). `missingnode` is unchanged at 25, so nothing was matched by
losing a node. Second-order: `parameterisation` −3 (at the band),
`qual-placement` +3 (at the band), `scan-type` +2, `join-method` +1 — a plan
that stops being Sort-rooted is re-adjudicated at the depths below it.

### 4.3 shape-delta

The sweep's own non-blocking plan channel: `queries=99 same=83 changed=16
added=0 removed=0`, changed = Q7 Q8 Q10 Q15 Q17 Q25 Q26 Q29 Q35 Q37 Q40 Q45
Q50 Q66 Q69 Q82. Seven of the task's nine named queries (Q8, Q17, Q25, Q26,
Q29, Q45, Q50) are in that set; Q21, Q62 and Q99 did not move.

Q8, the slice's representative, before and after:

```
 Limit                                    Limit
   ->  Sort                     ==>         ->  GroupAggregate
         Sort Key: s_store_name                   Group Key: s_store_name
         ->  GroupAggregate                       ->  Sort
               Group Key: s_store_name                  Sort Key: s_store_name
               ->  Sort ...                             ->  Hash Join ...
```

which is PG's own depth-1/-2 shape for Q8.

### 4.4 stats epoch of both arms

TPC-H: `e4a554b2a4cfb710` on both (pinned seed, §4). TPC-DS: consecutive
same-day sweeps on the gate-owned `:65437` dataset with no reload between them;
83/99 byte-identical plans bounds the sampling noise.

### 4.5 planning route

PG-shaped search (`GOOPG_PGSHAPED_DP=1`, the default) on both arms. The change
sits in the upper-rel pipeline above the search seam, so the route is not what
it touches.

### 4.6 seam-decline census

`N/A — this change adds a claim at the ORDERED seam; it files no path and
declines no join-search seam, so the census channel has no input to report.`

### 4.7 wall time of every query whose plan changed

The sweep's non-blocking status-delta channel reports `verdict-changes=none
runtime-moves=0 total-delta=+4.8%` — no single query crossed the 2.0x / 5s
move floor, and the aggregate move is inside normal host variance for a 125s
sweep. No ledger row for a slower plan is therefore owed.

### 4.8 Movement

`Movement: yes — TPC-DS SF0.25 pg-plan-parity-diff.py CATEGORIES-EXCL-MATCH
sort-strategy 67 -> 60 (−7)`

## 5. Gates

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | PASS (after the §3 fixture repair) |
| `scripts/tpch-spotcheck.sh` | PASS — Q12 rows=2, Q13 rows=33 (canonical) |
| `scripts/tpcds-sf025-regression.sh sweep` | PASS — `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` (PASS=96, SKIP=3) |
| `scripts/tpch-acceptance-arm.sh` | PASS — 24/24 labels MATCH on VALUES vs the HEAD baseline `tmp/arm-p0h11-on.txt` |
| floor capture + `pg-plan-parity-diff.py` | PASS — TPC-H match 2 (>= re-pinned 1), TPC-DS SF0.25 match 2 (>= floor 2) |
| `make ea-ratchet` | `N/A — no estimate, selectivity or statistics code is touched; the change adds an ordering claim at a plan seam` |

## 6. What is deliberately NOT in this slice

The task also asked for a review of `electOrderedGrouping`'s `len(cands) < 2`
gate against PG's `foreach(lc, input_rel->pathlist)` (`planner.c:5337`), which
has no minimum. The review was done and the divergence is real: a lone
translatable `PathAgg` is declined by goopg and offered by PG.

It is **not** changed here, for a reason the measurement above makes concrete:
with this slice landed, a single-candidate grouping rel now reaches the ORDERED
step's `pathkeys_contained_in` arm anyway — `electOrderedGrouping` declines,
`createOrderedPaths` runs on the pristine rel, and `inputNodePathkeys` derives
the very same claim from the finished `*Aggregate`. Relaxing the gate in the
same commit would therefore have been an untestable no-op on this corpus while
widening the loop's blast radius across every single-candidate grouping query
at once. It is filed as its own task with its own expected movement
(**M0144-0011a-2**) and a deferral-ledger row, so it is measured on its own.

The other `default: nil` tops the task named for survey — MergeJoin,
GatherMerge and IncrementalSort as the walk's TOP node (a `*GatherMerge` is
handled here only as an aggregate's CHILD) — are likewise left to that
follow-up: each needs its own output-coordinate argument, and bundling them
would make a category move unattributable.

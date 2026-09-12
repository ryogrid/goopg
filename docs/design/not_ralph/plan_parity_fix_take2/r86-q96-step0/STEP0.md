# R86 Step-0 — Q96 partial-row provenance and L3-prefix election

**Outcome: F — the PG-shaped partial `{ss,hd}->store` prefix loses within the
common `{ss,hd,store}` partial-path list.** A parameterised partial NLI is
also generated and costed but is not a runnable Gather subpath; that is real,
downstream D evidence, not the earliest failure. No program code changed.

The apparent row discrepancy which selected Q96 is real in EXPLAIN, but it is
not the input to the partial-path tournament. The normal configuration retains
a serial `PathPrebuilt` join tree and the legacy post-pass stamps its scan as
parallel. The partial path has PG's row unit. The controlled `top` arm then
shows that the PG-shaped partial prefix first loses to a same-relset competitor;
its later NLI Gather refusal cannot be the primary branch.

## 1. Reproducible input and oracle

The measured binary was built from `10312c92b` and ran against a private
`cp -a /tmp/pp2/clone-ds025-r82 /tmp/pp2/clone-ds025-r86` on `:5558` through
`scripts/goopg-test-run.sh`. `/proc/<listener>/exe` resolved to
`/tmp/pp2/r86/goopg-r86`. The retained clone and peer `:5533` were untouched.
PG was the read-only `:65438/tpcds025` oracle. The goopg server was stopped
after the controls.

Every Q96 plan command set `work_mem=64MB`,
`max_parallel_workers_per_gather=4`, and
`parallel_leader_participation=on` in its session. The pre-SET catalog/GUC
probe (which explains the `4MB` line in the raw PG artifact) and equivalent
table inputs are:

| source | `count(*)` | `reltuples` | `relpages` | default work_mem | workers / leader |
| --- | ---: | ---: | ---: | --- | --- |
| PG 18.3 `store_sales` | 719876 | 719876 | 12936 | 4MB | 4 / on |
| goopg private clone | 719876 | 719876 | 12934 | 512MB | 4 / on |

Thus A is false. The row count and planner base statistic agree, and the page
counts differ by only two. No source clone was ANALYZEd.

PG's pinned live plan is:

```
Finalize Aggregate
  Gather (Workers Planned: 3)
    Partial Aggregate
      Nested Loop (rows=35, total=16672.62)
        Hash Join: (store_sales ⋈ household_demographics) ⋈ store
        Index Scan using time_dim_pkey on time_dim
```

Its `Parallel Seq Scan on store_sales` has `rows=232218`, exactly
`clamp_row_est(719876 / 3.1)`. This is the oracle partition and row unit.

## 2. Row provenance: the partial tournament uses the right unit

The Q96 relation-bit map is `{0}=store_sales`,
`{1}=household_demographics`, `{2}=time_dim`, `{3}=store`.

| stage | evidence | rows | conclusion |
| --- | --- | ---: | --- |
| base serial path | `joinsearch.prebuilt {0}` | 719876 | correct total base cardinality |
| partial base path | `scan.seq.partial {0}` | 232218 | correct 3-worker/leader divisor result |
| partial `{0,1}` hash | `join.hash.partial` | 23222 | per-worker downstream cardinality |
| partial PG prefix `{0,1,3}` hash | `join.hash.partial` | 1935 | offered, then evicted from the final partial list |
| final partial NLI | `join.nestloop.partial {0,1,2,3}` | 27 | consumes the non-PG prefix (shown below) |
| rendered normal-plan child | serial merge tree under legacy Gather | 719876 at scan | not the partial-path carrier |

The normal plan prints `Parallel Seq Scan ... rows=719876` under a
three-worker Gather and uses the serial tree
`store_sales -> time_dim -> household_demographics -> store`. That display is
misleading as a description of the partial tournament, but C is not primary:
a serial `PathPrebuilt` tree, not an otherwise-winning partial path with a bad
renderer, supplies the visible subtree. The trace shows the partial base scan
already has 232218 rows, so B is false.

In default `GOOPG_GATHER_PATHS=off`, base partial-scan producers are reported,
but partial join producers file nothing and every joinrel `cpgather` record is
`partials=0 verdict=no-partials`. They therefore do not enter a join relation's
pathlist. The later gather/post-pass can still label the serial driving scan
`Parallel`. That is why a full base total survives in the printed normal tree;
it does not license a row-division change.

## 3. DP election evidence

The visible first-tree difference is not an L2 contest. `{ss,hd}` and
`{ss,time}` are different `RelOptInfo`s, so both exist independently.

| L2 relset | serial cheapest | total | next NLI | total |
| --- | --- | ---: | --- | ---: |
| `{store_sales, household_demographics}` | Hash Join | 23701.175 | index NLI | 40033.563 |
| `{store_sales, time_dim}` | Hash Join | 25290.815 | index NLI | 45855.738 |
| `{store_sales, store}` | Hash Join | 23433.358 | NL | 30932.053 |

The same-relset test is PG's prefix `{ss,hd,store}`, not either L2 row. Its
natural serial cheapest path is a hash join at `23867.310`, with an index-NLI
alternative at `26836.982`. The `top` diagnostic offers these two otherwise
comparable partial hash paths for the same L3 relset:

| L3 partial origin | L2 input total / rows | L3 added cost | L3 total / rows | pathkeys / disabled |
| --- | ---: | ---: | ---: | --- |
| PG-shaped `(ss ⋈ hd) ⋈ store` | 16508.22 / 23222 | 107.59 | 16615.81 / 1935 | 0 / 0 |
| competing `(ss ⋈ store) ⋈ hd` | 16321.68 / 19352 | 240.92 | 16562.60 / 1935 | 0 / 0 |

The first input difference is at L2: the `ss⋈store` estimate is 59990 total
rows (19352 partial, selectivity about 1/12), while `ss⋈hd` is 71988 total
rows (23222 partial, selectivity 1/10). Its lower input total exceeds the
extra L3 join work: the competitor is cheaper by `53.21` while output rows,
pathkeys, and disabled-node count are equal. `addToPartialPathlist` therefore
uses its lower total to evict the earlier `{ss,hd}`-origin path. The trace's
per-offer `verdict=accepted` at 16615.81 does not mean it remains in the final
list; `cpgather ... partials=1` proves there is one survivor.

The final partial NLI's `inputtotal=16562.60` independently identifies that
survivor as `(ss⋈store)⋈hd`, not PG's `(ss⋈hd)⋈store`. F is therefore true:
the PG prefix is not the applicable partial input for the final edge. This
precedes any final NLI admission/election under the scope's A -> B -> F -> D
-> E -> G order.

The required parameterised probe is explicit:

```
DPPATH path producer=index.parameterised relids={2} reqouter={0}
       rows=1 startup=0.25 total=0.28 verdict=accepted
```

With diagnostic `GOOPG_GATHER_PATHS=top` (only after natural classification),
a final edge with PG's same final relset and `time_dim_pkey` direction is
produced:

```
DPPATH partial producer=join.nestloop.partial relids={0,1,2,3}
  rows=27 startup=150.41 total=16647.46 verdict=accepted
  outer={0,1,3} inner={2}
```

This has the same relset and final `time_dim_pkey` direction as PG, but its
`inputtotal=16562.60` proves it begins with `(ss⋈store)⋈hd`, not PG's prefix.
It is consequential downstream evidence, not a usable PG-shaped candidate.
E is not reached until F is resolved. The diagnostic is not an
oracle-equivalence claim and does not alter the rendered Q96 plan.

## 4. Secondary Gather/NLI veto (D), after F

The same `top` trace records:

```
DPTRACE cpgather rel={household_demographics+store+store_sales+time_dim}
        partials=1 verdict=admitted
```

but no `DPPATH producer=gather` is added, and the Q96 plan remains
byte-identical to normal. The fail-closed boundary is
`gatherSubpathIsRunnable` in `internal/optimizer/gatherpaths.go`. It calls
`partialPathShapeIsGatherable`, whose `partialPathDrivingKind` whitelist
accepts seq/index/bitmap scans and partial hash/merge joins but intentionally
has no `PathNestLoop` arm. The NLI consequently returns the `PathPrebuilt`
refusal marker and `makeGatherPath` returns nil.

The built-node mirrors deliberately agree with that refusal:

- `terminatesPartial` lists `*NestedLoopIndexJoin`
  (`internal/optimizer/parallel.go`), so a legacy parallel walk cannot pass
  its outer side.
- `drivingScan`, `stampParallelScan`, `unstampParallelScan`,
  `drivingScanCrossesSort`, `findPartialSubtree`, and executor
  `attachParallelScan` have no matching NLI-outer traversal.

This is not a harmless planner omission. The admission comment records that an
unmodelled subtree can make every worker read the whole relation and emit
duplicates. The refusal is correct until all mirrors can claim only the NLI
**outer** scan while keeping the parameterised inner probe per outer row.
This would satisfy D's permitted form if the PG prefix were still applicable:
the probe and a partial-NLI producer exist, but its Gather consumer is vetoed
as not runnable. Here it is secondary because the final NLI consumes the
non-PG L3 survivor. It must be revisited only after the F investigation. G is
not selected: the immediate issue is a local, measurable L3 election.

## 5. Repeatability, controls, and values

All program execution was foreground. Evidence remains in `/tmp/pp2/r86/`.

| check | result |
| --- | --- |
| `go test ./internal/optimizer ./internal/executor ./internal/testutil/estimateaudit` | PASS |
| `go vet ./internal/optimizer ./internal/executor ./internal/testutil/estimateaudit` | PASS |
| Q96 trace A/B | byte-identical |
| Q96 trace-on/off | byte-identical |
| Q9, Q41, Q91 trace-on/off controls | each byte-identical |
| Q96 result versus PG | byte-identical (44 bytes) |
| temporary source instrumentation | none added; code tree remains clean |

The `top` arm was a diagnostic-only EXPLAIN. It also produced the default plan,
as expected when `makeGatherPath` declines the NLI subpath. No corpus sweep is
due for this measurement-only commit: no planner or runtime behavior changed.

## 6. Bounded R87 seed — localise the partial L2 estimate/cost input

This report authorizes no implementation. R87 must first create and review a
separate scope, commit and push it, then investigate only the first F input:
the partial L2 `ss⋈hd` versus `ss⋈store` cardinality/selectivity and the
resulting hash-join cost terms. It must decompose 16508.22 versus 16321.68
against PG's corresponding statistics and cost implementation, establish why
PG retains the `{ss,hd}`-origin prefix, and identify one mistranscribed input
before proposing any correction. It must not adjust a tie-break or a constant
to force a tree, alter Gather/NLI executability, or change
`GOOPG_GATHER_PATHS` policy. Re-evaluate D only if that round makes PG's L3
prefix applicable. The scope must retain Q96 PG values and normal trace
controls; an SF0.25 sweep becomes mandatory only if executable behavior changes.

# C-19h census re-run and retirement probe — 2026-09-07

Re-run of `docs/design/planner-c19h-gather-postpass/DESIGN.md` §3 after C-19g's
upper-rel-resident half landed (`cb4556791`, rebased `cd9218645`), and a full
retirement probe built on top of it.

Setup: private SF=1 clone `/tmp/c19h-d` off the TPC-H bench cluster, private
port 5541, cgroup-capped server (`GOMEMLIMIT=12GiB GOGC=off`,
`GOOPG_ANALYZE_SEED=20260905`), engine defaults `GOOPG_PGSHAPED_DP=1
GOOPG_PGSHAPED_COLLAPSE=1`. One engine image per arm, built from
`origin/plan-narrowing-and-etc` @ `8f5a3c6e6` in a detached worktree.

`evidence/retirement-probe.patch` is the probe build: `MaybeAddGather`'s ADD
half deleted (`findPartialSubtree`, `partialTarget`, `rebuildWithGather`,
`terminatesPartial` with it), the enforcement half kept and renamed
`EnforceParallelPermission`, both `dispatch.go` call sites updated. It builds
and `go build ./...` is clean; it is a probe, NOT a landed change.

## 1. The census — blockers 1 and 3 are gone

| arm | queries carrying a Gather |
|---|---|
| A — post-pass live, engine defaults (`evidence/armA-base.txt`) | **12/22** |
| B — post-pass ADD half deleted, engine defaults (`evidence/armB-retired.txt`) | **12/22** |
| C — post-pass deleted + `GOOPG_GATHER_PATHS=all` (`evidence/armC-ret-gpall.txt`) | 13/22 (adds Q21) |

The twelve are the same twelve in A and B — Q1 Q3 Q5 Q6 Q7 Q8 Q9 Q10 Q14 Q15a
Q16 Q19 — and the check is stronger than a count: **`diff armA-base.txt
armB-retired.txt` is EMPTY.** All 22 plans are byte-identical, costs included.
At the engine defaults the post-pass's ADD half contributes nothing to any
TPC-H plan; every one of the twelve Gathers is produced by C-19g's
`partialaggupper.go` path producer.

So DESIGN §2's blocker 1 ("at the default the post-pass is the ONLY producer,
12/22 with it and 0/22 without") and §3.1's blocker 3 ("Q1's win is delivered
THROUGH the post-pass") are both discharged, by construction rather than by
argument.

## 2. The new blocker: the non-aggregate cohort, which TPC-H cannot show

Every one of the 22 TPC-H queries has an aggregate at its top, and C-19g's
producer is aggregate-only. A 22-query census therefore cannot see the plan
class the post-pass still owns. Probed directly, same clone, same arms:

```
select * from lineitem where l_extendedprice > 90000
  A (post-pass live)   Gather (Workers Planned: 4) -> Parallel Seq Scan on lineitem
  B (post-pass gone)   Seq Scan on lineitem                      <- parallelism LOST
  C (gone, GP=all)     Seq Scan on lineitem                      <- still lost

select l_orderkey from lineitem where l_shipdate < '1992-02-01'
  A  Gather (Workers Planned: 4) -> Parallel Seq Scan
  B  Seq Scan
  C  Seq Scan
```

Vanilla **PG 18.3 on the reference cluster (:65432, db `tpch`) emits
`Gather / Workers Planned: 4 / Parallel Seq Scan` for BOTH queries.** So arm A
matches PG and arms B and C do not: retiring the post-pass is a PG-parity
regression, not a cleanup.

Arm C is the decisive part. `GOOPG_GATHER_PATHS=all` is the most aggressive
setting C-19d offers, and the path model still declines — for the reason
C-19d's own DESIGN §5.1 states: with only BASE-rel partial paths the whole
relation crosses the boundary, so `cost_gather` charges
`parallel_tuple_cost` = 0.1/row against a 4-worker saving of
`cpu_tuple_cost`'s share ≈ 0.0075/row, and `add_path` correctly dominates
every plain Gather at any relation size. **C-19h's remaining prerequisite is
therefore NOT C-19d's default flip** — flipping it does not discharge this —
**it is C-19d's crossover arithmetic itself.**

## 3. Two further things the deletion takes with it

- **`debug_parallel_query` becomes a fully unconsumed GUC.** Its only planner
  consumer is `ParallelSettings.DebugParallelQuery` -> `computeParallelWorkers`
  (parallel.go), reached only from the deleted `findPartialSubtree`;
  `considerparallel.go` sizes with `computeParallelWorkerForRel`, which does
  not read it. PG implements the same GUC as a post-planning Gather wrap in
  `standard_planner` (`postgres/src/backend/optimizer/plan/planner.c:465-495`),
  so a `debug_parallel_query`-gated post-pass is PG-FAITHFUL IN KIND and is the
  shape any conditional retirement should take.
- **68 call sites across 14 test files** use `MaybeAddGather` as the
  forced-parallel plan constructor for the executor's parallel operators
  (hash join, spill, index scan, gather merge, agg split). They are the only
  construction path for those shapes; deleting it deletes that coverage with
  no replacement.

## 4. Double-Gather verification (the item's own requirement), re-done

Per-query Gather counts are exactly 1 in every arm above (see the awk counts
in each capture) — no plan carries more than one Gather on any root-to-leaf
path, in arm A (post-pass live over a path-model Gather, i.e. the case
C-19d's `subtreeHasGather` stand-down exists to prevent), in arm B, or in arm
C at `GOOPG_GATHER_PATHS=all`. `subtreeHasGather` is untouched by this probe
and keeps its second, live caller in `partialaggupper.go:92`.

## 5. D-05 re-test, on the same clone

D-05's blocker (`analysis/minimize-datum/d05-buildcost-20260906/README.md` §6)
is that a hash-join cost rise flips the plan to a merge join, `drivingScan`
returns nil for a merge join, and the whole plan goes serial — priced by a
model with no term for it. C-19g moved the aggregate Gather's PRODUCER into
the search but not the ELIGIBILITY PREDICATE, which is the same `drivingScan`
with the same `hashJoinIsPartialCapable` / `JoinAlgoHash` requirement.
Measured at the engine defaults on arm A:

```
select l_returnflag, sum(l_extendedprice) from lineitem
  join orders on l_orderkey=o_orderkey
  where o_orderdate < '1995-01-01' group by l_returnflag

HashAggregate
  ->  Merge Join                       <- no Gather anywhere, C-19g on
        ->  Index Scan ... lineitem
        ->  Index Scan ... orders
```

An aggregate over a merge join gets no Gather even with `partialaggupper`
live. And the hash-vs-merge choice is made at the JOIN rel, where the default
still has no partial path at all (`GOOPG_GATHER_PATHS` off), so the comparison
that makes the choice still cannot see the parallelism it destroys. **D-05
stays blocked, with its mechanism narrowed**: not "no parallel dimension
anywhere" but "the parallel dimension reaches only the aggregate upper rel,
one level above where the decision is made".

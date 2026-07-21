# 12 — Parallel Multi-Way Hash Join

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) — revised after review |
| date | 2026-07-22 |
| depends on | [04](04-parallel-scan.md), [07](07-parallel-hash-join.md) |

## 0. Why, and a correction to chapter 07

Chapter [07](07-parallel-hash-join.md) §6 excluded the multi-way hash join from
v1 with two reasons. The first — no PG oracle plan — is true and is handled in
§4. **The second is wrong, and P8 shipping proved the first sentence of it false
in practice.** It said:

> Parallelising the two-way `Join` first gives the TPC-H benefit (the reference
> set uses `Parallel Hash Join`, i.e. the two-way form).

P8 landed and changed exactly one TPC-H plan (Q13), with no measurable gain. The
sentence reasoned from **PostgreSQL's** plan shapes. goopg does not keep
multi-table joins as chains of two-way `Join`s: `rewriteMultiWayChain`
(`internal/planner/planner.go:988`) collapses every chain of ≥3 inner hash joins
into one `MultiHashJoin`, which P8 never touches. That operator appears across
eight TPC-H queries; several drive their probe off `lineitem` at ~6 M rows
(Q3, Q10, Q18, Q21 clearly; Q7 off `orders` at 1.5 M). That is where the
parallelism P8 was meant to deliver actually lives.

Chapter 07 §6's second reason — MHJ "interacts with the planner's join-order DP
in ways that would widen the plan-gate surface" — is also inaccurate and is
corrected here. There is **no such interaction**. `rewriteMultiWayChain`
consumes an already-finished binary tree two passes after `tryBushyDP`, and
`MaybeAddGather` runs later still, outside the planner, after the plan-cache
lookup, and non-mutating by contract (`internal/planner/parallel.go:83`). There
is no feedback edge, and the DP has no notion of parallel cost to be perturbed
by.

### 0.1 Measured motivation

Q3 was profiled serially on SF1 (bench server, 95.99 % on-CPU, no GC-throttle
band). `seqScanOp.Next` — the probe-side scan of `lineitem` — dominated:
`DecodeRowIntoMctxPGTuple` 34.8 %, `cloneRowOwned` 9.1 %, so **~44 % of CPU is
the probe scan alone**, before the per-probe-row chain lookup
(`initStepHelper`, 5 %) and residual filter evaluation (`evalExprSlot`, 6.9 %)
that also parallelise. This is measured, not argued from plan shape — the
discipline the two earlier misdirections in this work (Q13 named as the split's
hazard; the chapter-07 sentence above) were missing.

Q9 was profiled too and is GC-dominated (`gcDrain` 76 %) because its per-row
arithmetic allocates heavily and buries the scan, so the acceptance measurement
(§5) uses **Q3**, where the scan is cleanly dominant.

## 1. The design, in full

**The parallel MHJ needs no shared-build machinery. Each worker builds its own
copy of the dimension tables; only the probe scan is partitioned.**

This is the whole design, and it is correct for a specific structural reason:

- `attachParallelScan` partitions **only the probe-side scan** (§3). The build
  children are never partitioned.
- So a worker that runs the ordinary `multiHashJoinOp.Open` — building its own
  N−1 hash tables from the *full* dimension scans, then probing its *partition*
  of the fact table — returns exactly correct results. The union of the workers'
  probe partitions is the fact relation, once; every worker holds identical,
  complete dimension tables.
- The dimension tables are the **small** side by construction: `ProbeTable` is
  chosen as the largest relation (`bushy.go:1070-1078`), so the builds are of
  the small relations. Rebuilding them per worker is N× a cheap scan, dwarfed by
  the fact-table scan being parallelised.

This is why MHJ is *easier* than the two-way join, not harder. P8 built the
`sharedHashBuild` publish-once machinery ([07](07-parallel-hash-join.md) §3)
because its build side could be large (`orders`, 1.5 M rows in Q13). Here the
build side is small by the planner's own probe-selection rule, so the machinery
buys little and is deliberately **not** built (§6).

### 1.1 How a worker gets its tree

No new construction path. The Gather already gives each worker its own operator
tree: `gatherOp.Open` calls `o.buildChild()` — `Build(p.Child)` — once per
worker (`operators_gather.go:183`), producing an independent `multiHashJoinOp`,
then `attachParallelScan` wires that tree's probe scan to the shared block
allocator. Each `Open` builds its own dimension tables exactly as it does today.
`prebuildSharedHashJoins` finds no shareable two-way join and returns nil, so
`ctx.SharedHashBuilds` stays nil and nothing changes on that path.

## 2. Why it is safe to parallelise at all

Verified by reading `internal/executor/multi_hash_join.go` in full:

- **INNER only.** `collectMultiHashTables` refuses anything else
  (`bushy.go:988`) and `planner.MultiHashJoin` has no `Type` field
  (`plan.go:1101-1108`). No unmatched-build tracking, no LEFT null-padding, no
  `antiBuildRows`/`antiBuildHasNull` analogue — the refusal set P8 had is empty
  here.
- **Nothing accumulates across probe rows.** `lazyMatches` / `lazyCursors`
  (`:51-52`) are an odometer, fully re-initialised per probe row by
  `initStepHelper(0)`. No bitmap, dedup set or counter anywhere. That absence is
  the property whose presence would defeat a per-worker split.
- **The probe is named, not derived.** `plan.ProbeTable` is an explicit index,
  so P8's three-way-duplicated `probeSideIsLeft` coupling — where a disagreement
  silently drops matches — has no counterpart.

## 3. The three changes

All in the planner and the scan-attach walk; the executor operator itself is
unchanged.

**(a) `attachParallelScan`** (`internal/executor/parallel_scan.go`) — one
unambiguous field access:

```go
case *multiHashJoinOp:
    return attachParallelScan(x.children[x.plan.ProbeTable], st)
```

`children` is in exact index correspondence with `plan.Tables`, and `ProbeTable`
indexes the same array. The build children are structurally unreachable from
that expression, so "each worker gets a partition of the build input" — the
failure the two-way path guards against with a rule in three places — is
impossible here by construction.

**(b) `drivingSeqScan`** (`internal/planner/parallel.go`) —

```go
case *MultiHashJoin:
    if !multiHashJoinIsPartialCapable(x) {
        return nil
    }
    return drivingSeqScan(x.Tables[x.ProbeTable])
```

`multiHashJoinIsPartialCapable` bounds-checks `ProbeTable` and confirms the node
is well-formed. There is no `Type` to gate on (INNER by construction), and the
residual `Filters` are per-probe-row evaluable (they are WHERE conjuncts over
the concatenated output row), so the predicate is nearly trivial — it exists as
the explicit approval point the walk's "refuse toward serial" discipline
requires.

**(c) `parallelChildren`** (`internal/planner/parallel.go`) — `case
*MultiHashJoin: return x.Tables`. This is the safety fix and it is the one item
that is genuinely dangerous (§7). It must land **before or with** change (b).

## 4. PG-comparability — the reference the missing oracle would have supplied

With no MHJ in PostgreSQL, the identity gate (§5) is the primary correctness
check, backed by a stated invariant: a parallel MHJ must return exactly what the
equivalent left-deep tree of two-way parallel hash joins would — probe side
partial, every build side serial, INNER only. Every MHJ is by construction a
collapsed chain of inner hash joins, so it already satisfies this; the parallel
form changes only *which rows each worker's probe sees*, never the join
semantics. Therefore serial-MHJ ≡ parallel-MHJ is the same statement and needs
no PG process to check. Shapes PG could not express — per-table probe
partitioning, parallel builds — are deliberately out of scope.

## 5. Verification

- **Serial-vs-parallel identity over an MHJ corpus** — 3- and 4-table chains,
  with and without residual `Filters`, compared as sets. Substitutes for the
  missing oracle (§4) and is the gate that caught P6's duplicate-rows bug.
- **The duplicate-rows shape specifically**: N workers return the relation once,
  not N times. This failure appears only when `attachParallelScan` fails to
  reach the scan under a new node kind, and it has now been caught twice (P6's
  plain scan, P7's sort). MHJ is a third such node, which is why change (a) has
  its own test.
- **Safety**: a temp / virtual relation beneath an MHJ probe side must refuse
  (§7).
- **Probe sizing**: an MHJ whose probe is a small relation must yield 0 workers.
  This needs no new gate — change (b) routes `drivingSeqScan` to the probe, so
  `computeParallelWorkers` already sizes off the relation actually being
  scanned and returns 0 below `min_parallel_table_scan_size` (see §6).
- **race-gate** under a probe-heavy MHJ workload.
- **Acceptance is a measurement.** Re-run Q3 at 0/2/4/8 workers, warm and
  alternating, against the §0.1 serial baseline, and report the speedup plainly
  — small if it is small.

## 6. Deliberately not done

**No shared build.** §1 establishes it is unnecessary for correctness and of low
value here, because the build side is small by the probe-selection rule. Adding
it would mean widening `ctx.SharedHashBuilds` (typed `map[*planner.Join]…`), a
`MultiHashJoin`-keyed lookup, an `applySharedBuild` analogue, worker-context
propagation and Gather-Close reset — the most complex machinery in the P8 line,
for a re-read of small dimension tables. Reopen only if a measured MHJ shows the
per-worker dimension rebuild dominating, which the §0.1 profile (44 % probe
scan) says it does not.

**No probe-selection floor.** An earlier draft proposed refusing when the
selected probe is small. It is redundant: once change (b) makes
`drivingSeqScan(MHJ)` return the probe's scan, `computeParallelWorkers` sizes
off that scan, so a small probe yields 0 workers automatically — the two inputs
do not "disagree by construction" as that draft claimed, because sizing never
measures a different relation than the one being split. The residual — a
*mis-selected* probe when statistics are absent losing parallelism it could have
had — is a performance miss, not a correctness or safety issue, and no cheap gate
fixes it.

## 7. The safety hole, stated on its own

`parallelChildren` does **not** list `*MultiHashJoin`, so `subtreeHasUnsafeNode`
— the walk that refuses temp tables, virtual catalog relations and `LockRows` —
stops dead at an MHJ and sees nothing beneath it.

Today this is harmless only by accident: `drivingSeqScan` also refuses MHJ, so
no Gather is placed at or below one and the blindness is unreachable. **The
moment change (b) lands, the walk would approve a probe-side temp table it never
visited.** Hence change (c) must land before or with (b).

The asymmetry that makes it a trap: the file's own comment says an unlisted node
kind "reports no children, which makes the enclosing walks refuse rather than
descend." That is true of the *placement* walks and false of the *safety* walk,
which reads "no children" as "nothing unsafe below" — the opposite of
conservative.

Returning `x.Tables` (N children) does **not** break the placement walks despite
their `len(kids) != 1` bail, and this is worth stating so nobody "fixes" a
non-problem: `findPartialSubtree` returns the MHJ as target via the
`drivingSeqScan(cur) != nil` short-circuit *before* the multi-child check, and
`rebuildWithGather` matches `root == tgt.node` *before* walking children. So the
N-child return is seen only by the safety walk, which is exactly what needs it.

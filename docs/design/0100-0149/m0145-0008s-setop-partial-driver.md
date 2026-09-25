# M0145-0008s: only a UNION ALL SetOp may drive a partial subtree

Status: **LANDED 2026-09-25** (`031e8452c`). Task: `.ralph/fix_plan.md`
M0145-0008s (Kind: impl, Parent: none). This was a wrong-results defect,
owner-placed second in banner item 2a. Evidence: `analysis/m0145/m0145-0008s/`.

## Defect

The must-pass regress case `union` returned a varying, too-small count (1660,
then 1721) for
`select count(*) from (select unique1 from tenk1 intersect select fivethous from tenk1) ss`.
PG returns 5000. goopg's plan was:

```
Finalize Aggregate
  -> Gather
       -> Partial Aggregate
            -> HashSetOp Intersect
                 -> Parallel Seq Scan on tenk1
                 -> Parallel Seq Scan on tenk1_1
```

Each worker intersected only its own share of each input, so every match
whose two rows landed in different workers was lost. It showed only in the
full suite, where earlier cases leave `tenk1` large enough for the parallel
split to win.

Minimal repro: a 10,000-row table with a 900-byte pad column under default
settings gives 3672, then 3968, instead of 5000.

## Cause

The parallel post-pass (`findPartialSubtree` → `splitAggregate`,
`internal/optimizer/parallel.go`) splits an aggregate when `drivingScan` finds
a partial driver below it. `drivingScan`'s `*SetOp` arm resolved a driver for
**every** set operation.

That is only sound for a streaming SetOp, UNION ALL (`setOpStreams`), where
each worker forwarding its share of both branches is row-exact. UNION,
INTERSECT and EXCEPT, with or without ALL, compare rows across the **whole**
of both inputs.

The path-level producer `addPartialSetOpPath` already refused non-streaming
set operations. The post-pass walk was the unguarded sibling.

## What PG does

PG never builds a SetOp over partial paths. `generate_nonunion_paths`
(`postgres/src/backend/optimizer/prep/prepunion.c`) plans both children as
complete paths, and a SetOp path is not partial, so a Gather can sit only
**below** it, on each child. Parallel Append exists only for UNION ALL.

## Change

- The three sibling walks' `*SetOp` arms now require `setOpStreams(x)`:
  - `drivingScan` returns nil for a non-streaming SetOp;
  - `stampParallelScan` stamps nothing below it;
  - `drivingScanCrossesSort` answers false.
- `TestNonStreamingSetOpIsNeverAPartialDriver` drives all three for UNION ALL
  and for the other five operator forms: UNION, INTERSECT [ALL] and EXCEPT
  [ALL].

## Verified

- **Repro:**
  - INTERSECT returns 5000 on repeated runs.
  - EXCEPT and UNION return the right counts.
  - UNION ALL keeps its parallel `Gather > Partial Aggregate > Append` plan
    and returns the right count.
- **Full `TestPort_RegressSuite`:** PASS, including `union`, `portals_p2` and
  `select`. That closes the nightly item AI-20260925-002342-005.
- **Gates:**
  - units: PASS.
  - `tpch-spotcheck`: PASS.
  - acceptance arm: 24 MATCH.
  - `tpcds-sf025`: 96 PASS, 99/99 shapes the same.
  - fire-set (SF0.25 + SF1): PASS, fires none; no TPC-DS INTERSECT/EXCEPT
    query was taking the defective shape.
  - pgbench smoke: PASS.

Movement: none. This is a wrong-results fix with no corpus plan change.

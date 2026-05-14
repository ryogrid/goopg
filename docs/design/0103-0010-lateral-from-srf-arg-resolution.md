# 0103-0010: LATERAL FROM-clause SRF arg resolution (planner side)

Status: draft
Milestone: M0103-0008
Date: 2026-05-14

## Background

Closing the libpqrcv probe-survival ladder for `TestPort_PgoutputInteropGoopgToPG`
exposed a sixth, distinct gap on the goopg publisher: the column-list probe
that the apply launcher ships after the relation-list probe is
structurally different.

The relation-list probe (rungs 1–5) is

```sql
SELECT … FROM (
  SELECT (pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*
    FROM pg_publication WHERE pubname IN (…)
) gpt(…)
```

— a single FROM item whose SRF args are aggregates over the same FROM
item. M0103-0009 (loop 5) closed it with a `ProjectSet` lowering.

The column-list probe (rung 6), shipped from
`postgres/src/backend/replication/logical/tablesync.c::fetch_remote_table_info`,
is

```sql
SELECT DISTINCT
  (CASE WHEN (array_length(gpt.attrs, 1) = c.relnatts)
   THEN NULL ELSE gpt.attrs END)
FROM   pg_publication p,
       LATERAL pg_get_publication_tables(p.pubname) gpt,
       pg_class c
WHERE  gpt.relid = <oid> AND c.oid = gpt.relid AND p.pubname IN ( … )
```

— a three-item FROM list where the middle item is a LATERAL SRF whose
argument is an outer column reference (`p.pubname`) from the left
sibling.

Before this slice, the planner rejected the SRF arg with
`42703: column "pubname" does not exist`: `planTableFuncRangeVar`
created a fresh empty `resolveContext` for the SRF's argument
resolution, so cross-FROM-item bindings were invisible.

## Change

This slice closes the planner side of rung 6. The executor side
(outer-row-driven SRF evaluation for the cross-FROM Join) is the
next rung and stays deferred inside M0103-0008.

### Code

`internal/planner/planner.go`

* `planScanRangeVar`, `planTableFuncRangeVar`, and
  `planPgGetPublicationTables` gain a `lateralCtx *resolveContext`
  parameter. `nil` from non-FROM call sites (the INSERT source path
  and the MERGE source path) preserves prior semantics.

* `planPgGetPublicationTables` resolves each SRF argument against
  `lateralCtx` (when non-nil) instead of `&resolveContext{}`. The
  static three-column output schema (`relid oid, attrs text, qual
  text`) is unchanged.

* `planFromRangeVars` (legacy comma-FROM path) and `planFromClause`
  (FromExpr/Join path) build the accumulated `*resolveContext`
  per FROM iteration and thread it in. The first item gets `nil`;
  every subsequent item sees the previously-planned items as
  LATERAL siblings.

* `planFromItem` accepts a `lateralCtx` and forwards it to the
  base item. For JOIN right-hand sides it merges the outer
  lateralCtx with the same item's left-side context via the new
  `mergeResolveContexts(outer, inner)` helper so the right child
  can reference both the FROM-list siblings and the same item's
  left side of the JOIN.

### Test

`internal/planner/planner_test.go::TestPlanLateralSrfArgResolvesAgainstLeftFromItem`
parses and plans the canonical libpqrcv shape against an in-memory
catalog with a `pg_publication(pubname text)` stand-in. Pins the
output schema as a single column named `attrs` — the SRF's static
column projected through `gpt.attrs` at the top-level target list.

## What is still missing for full rung-6 survival

The executor's cross-FROM Join opens its right child once, with no
outer slot — so `PgGetPublicationTables`'s `ColumnRef("pubname")`
hits a nil tuple at `Next` time and raises
`XX000: column ref pubname/0 on nil slot`. Two viable paths:

1. **NestedLoop-with-parameter-binding for FROM-clause SRFs.** Generalise
   the `nestedLoopIndexJoinOp::BindOuter` slot-binding pattern so the
   inner SRF is rebound per outer row. Cleanest, but touches plan-node
   selection (when to pick NLJ-with-param vs the existing cross-Join).
2. **Inline materialisation at plan time.** When the SRF arg references
   exactly one outer column with a finite source, expand the SRF into
   a Values-style materialisation per outer row before the Join is
   built. Less invasive for the executor, but only handles bounded
   outer sides.

Path 1 is the principled fix and matches upstream's `Param`-based
LATERAL execution. Path 2 is a stopgap. Decision deferred to the
rung-7 loop alongside the actual executor work; the planner change
in this slice is forward-compatible with either.

## Verification

* `go test -count=1 -timeout 180s ./internal/parser/ ./internal/planner/
  ./internal/analyzer/ ./internal/executor/ ./internal/server/
  ./internal/wal/ ./internal/catalog/` — all green.
* `TestPort_PgoutputInteropGoopgToPG` — still `t.Skip`, message
  updated to point at the executor-side LATERAL gap.

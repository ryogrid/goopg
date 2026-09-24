# M0145-0008h: ordered-grouping loop vs a passthrough added after the grouping snapshot

Status: landed 2026-09-24 `39751edc9` (comment correction `71b5b84d6`).
Movement: none (no corpus plan changed). Found: M0145-0008m, a pre-existing
wrong result with the same root.
Task: `.ralph/fix_plan.md` M0145-0008h (Kind: impl, Parent: M0145-0008d).
Evidence: `analysis/m0145/m0145-0008h/probe.txt`.

## The defect

The upstream regress query `aggregates.out:3158`:

```sql
SELECT array_agg(c1 ORDER BY c2), c2 FROM agg_sort_order
 WHERE c2 < 100 GROUP BY c1 ORDER BY 2;   -- c1 PRIMARY KEY, unique index on c2
```

It panicked in `assertSortInputTargetCoversKeys`: "Sort input target []
drops sort-key column c2 of a 2-column input row".

- The task was filed believing the server process exited. A re-check on a
  HEAD build showed `serveConn` recovering the panic: only the session is
  dropped (`connection closed reason=panic`), and the server keeps serving.
- PG returns the rows.

## Root cause

- `c2` is functionally dependent on the GROUP BY primary key, so goopg adds
  it lazily as an aggregate passthrough column when a reference to it is
  resolved (`resolveExprAfterAggregate`). Here that happens while the ORDER BY
  keys are resolved.
- `electOrderedGrouping` then rebuilds its winner from the grouping-path
  spec, which was snapshotted before that append. The Sort's key names
  output column 2, which the rebuilt Aggregate does not emit.
- The copy-back (`*agg.node = *ba`) also erased the passthrough from
  `agg.node`.

## Fix (`internal/optimizer/upperorderedgrouping.go`)

The loop declines (`agg-surface-grew-after-snapshot`) when any candidate
spec has fewer passthrough or output columns than `agg.node`. The normal
ordered arm then sorts over `agg.node` itself, which carries the column.
Grafting the column onto the winner is not safe, because its child may be a
narrowed or index-ordered input with remapped positions (M0145-0008m).

goopg's plan now matches PG's expected output line for line:

```
Sort  (c2)
  -> GroupAggregate  (Group Key: c1)
       -> Sort  (c1, c2)
            -> Index Scan using agg_sort_order_c2_idx
```

`TestAggSortOrderPassthroughOrderBy` pins the plan and PG's rows; it panics
at HEAD.

## Found: M0145-0008m (wrong result, same root)

`SELECT sum(c1), c2 FROM agg_sort_order GROUP BY c1` returns NULL for c2 on
goopg (PG returns c2). It reproduces at HEAD without this change, and needs
no ORDER BY.

- The grouping election picks the index-ordered input
  (`indexOrderedAggInput` → `buildIndexOrderedScan`), an Index Only Scan on
  the primary key, whose coverage check reads the Passthrough list. At
  election time that list is still empty.
- The passthrough appended afterwards reads a column the narrowed child does
  not produce.
- Filed with an S2 escalation.

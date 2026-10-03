# M0146-0009m — a nested loop over a LATERAL Append is sized outer × one call

Status: done (2026-10-04, `c79dc3a42`). Parent: M0146-0009. Found by
M0146-0049a.

## The divergence

```sql
SELECT li.id, x.amt FROM li,
  LATERAL (SELECT amt FROM cs1 WHERE item = li.id
           UNION ALL SELECT amt FROM ws1 WHERE item = li.id) x
WHERE li.cat = 3
```

| | Nested Loop rows | Append rows | members |
|---|---|---|---|
| PG 18.3 | 3000 | 75 | Bitmap Heap Scan 50 + 25 |
| goopg before | 1 | 1 | 50 + 25 (displayed) |

The plan shape and the values already matched PG.

## Cause

This plan is not sized by the join search. The nested loop's
`estimateJoin` and the set operation's `estimateSetOp` call `EstimateRows`
on their inputs. Two gaps in `EstimateRows` (cardinality.go) produced the
wrong count:

1. **No `*BitmapHeapScan` arm.** Every bitmap heap scan fell through to
   0. The members' displayed 50 and 25 come from the paths' stamped
   costs, which `EstimateRows` never read.
2. **The probe's qual is charged twice.** The one-relation scope keeps the
   correlated qual `item = li.id` in a `Filter` over the probe as well as
   in the probe's index key. PG keeps such a clause only in the indexqual:
   `create_indexscan_plan` and `create_bitmap_scan_plan` (createplan.c)
   drop it from the qpqual. `filterSelectivity` charged it again, so 50
   rows came out as 1.

## Change

- **`case *BitmapHeapScan`** takes the bitmap path's stamped rows: the
  restricted size, or for a parameterised probe the rows of one call
  (`ppi_rows`). An unstamped scan falls back to `bitmapHeapScanRows`: the
  bitmap index scan's bound keys through `indexScanRows`, narrowed by the
  residual `Cond`. A BitmapAnd or BitmapOr input falls back to the whole
  relation.
- **`indexKeyEnforced`** marks a Filter conjunct `col = key` when the index
  or bitmap probe directly below binds that index column to the same key
  (`probeIndexKeys`). `filterSelectivity` skips it, as it skips a conjunct
  pushed below (`PushedBelow`).

## Effect

- **The query:** Nested Loop rows=3000 over Append rows=75, as in PG.
- **Fire set:** flat at both scales. The TPC-DS plans are byte-identical;
  these plans are sized by the join search.
- **Other gates:**
  - ea-ratchet PASS; SF0.25 sweep 96/96; TPC-H arm 24/24;
  - 20 planner-heavy regress cases identical to HEAD.

Test: `TestLateralAppendNestedLoopRows` fails before the change, with
rows=1.

## Not done (ledgered)

- **The nested loop's cost.** goopg displays outer + one inner call
  (292.66). PG charges each outer row's rescan of the inner
  (`final_cost_nestloop`): 9430.27.
- **Rendering.** goopg's bitmap heap scans still show the leftover
  `Filter: (item = …)` where PG shows `Recheck Cond:`, even for plain
  `WHERE item = 5`.

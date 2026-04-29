# 0020-0003 - Window Function Executor (Stage A)

**Status:** accepted (Stage A row_number/rank)
**Milestone:** [0020 - Window Function Support](../milestones/0020-window-functions-over-row-number-rank-lag-lead.md)
**Spans seam:** planner WindowAgg node, executor WindowAgg operator
**Cross-links:**
[root-0011](root-0011-planner.md),
[root-0012](root-0012-executor.md),
[0020-0002](0020-0002-window-analyzer-and-planner.md).

## Scope

This slice implements runtime behavior for Stage A window functions:

- `row_number()`
- `rank()`

Both run on top of planner's `WindowAgg` node with one shared
`OVER (...)` specification (Stage-A simplification from 0020-0002).

## Execution model

`windowOp` (new executor operator) is built from `*planner.WindowAgg`.

Open lifecycle:

1. Open child operator.
2. Drain all child rows into memory.
3. Stable-sort rows by:
   - `PARTITION BY` keys (ascending, nulls first)
   - then `ORDER BY` keys (per-key direction, same null handling)
4. Append one output cell per window function.
5. Evaluate window functions over the sorted stream.

Next lifecycle:

- Return buffered rows sequentially.

Stage A accepts full buffering to keep semantics deterministic and small.
Streaming/ring-buffer execution is deferred.

## Function semantics

### row_number()

- Resets to 1 at each partition boundary.
- Increments by 1 per row inside the partition's order.

### rank()

- Resets to 1 at each partition boundary.
- Peer rows (equal across all `ORDER BY` expressions) share the same rank.
- Non-peer rows get rank = current row position in partition
  (gapped rank semantics).
- With no `ORDER BY`, every row in the partition is a peer (rank 1).

## Null and peer behavior

- Sort behavior follows existing executor sort semantics:
  - nulls first for ascending
  - nulls last for descending
- Peer detection for `rank()` treats NULL = NULL for peer grouping and
  NULL != non-NULL.

## Limitations (deferred)

- `lag()` / `lead()`
- Frame clauses (`ROWS`, `RANGE`, `GROUPS`)
- Multiple independent window specs in one SELECT (planner guard exists)
- Memory-bounded or streaming WindowAgg execution

## Tests

Unit-level executor tests:

- `TestWindowOpRowNumberByPartitionAndOrder`
- `TestWindowOpRankWithoutOrderAllPeers`
- `TestWindowOpRankWithPeersAndPartitions`

Compatibility/end-to-end tests via parser->planner->executor:

- `TestCompatWindowRowNumberPartitionOrder`
- `TestCompatWindowRankPeerGroups`
- `TestCompatWindowRankNullPeersAsc`

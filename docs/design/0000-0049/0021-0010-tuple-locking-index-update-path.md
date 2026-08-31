# 0021-0010 — Tuple-Level Locking: UPDATE/DELETE via IndexScan Path

**Status:** accepted (step 2d — planner emits IndexScan for
UPDATE/DELETE when `WHERE indexed_col = key`; executor handles
both shapes; foreign-lock detection continues to fire)
**Milestone:** [0021 — Pessimistic Row Locking](../../milestones/0021-pessimistic-lock-select-for-update.md)
(deferred follow-up: tuple-level locking on top of M0012).
**Spans seam:** Planner planUpdate / planDelete, executor
extractScanAndPredicate, indexScanPredicate synthesis.
**Cross-links:**
[0021-0007](0021-0007-tuple-locking-producer-wiring.md) (seqScan
producer wiring),
[0021-0008](0021-0008-tuple-locking-blocking-enforcement.md)
(scanMatching foreign-lock detection),
[0021-0009](0021-0009-tuple-locking-indexscan-leaf.md) (IndexScan
leaf for SELECT FOR UPDATE).

## Context

Step 2c gave SELECT FOR UPDATE the IndexScan leaf path. The
matching gap on the write side: `planUpdate` / `planDelete`
always emitted `Filter(SeqScan)` for `WHERE indexed_col = N`,
even when an index existed. `extractScanAndPredicate` would have
errored on any IndexScan-shaped child plan because of its
explicit `Filter child is not SeqScan` guard.

This slice promotes the planner so UPDATE/DELETE can pick the
index-driven plan shape and extends the executor to consume it
without losing the per-tuple foreign-lock detection from step 2b.

## Planner promotion

`planUpdate` and `planDelete` now mirror `planSelect`'s
`planIndexScanFromWhere` arm:

```go
ctx := singleBindingContext(tbl, s.Target.Alias)
ctx.cat = cat   // ← needed so subquery resolution inside index-key works
var node Node = &SeqScan{...}
if s.Where != nil {
    if idxNode, ok, err := planIndexScanFromWhere(s.Where, ctx, cat); err != nil {
        return nil, err
    } else if ok {
        node = idxNode
    } else {
        pred, err := resolveExpr(s.Where, ctx)
        if err != nil { return nil, err }
        node = &Filter{pos: s.Where.Pos(), Child: node, Predicate: pred}
    }
}
```

The IndexScan path is taken only for the `WHERE col = key`
shape `planIndexScanFromWhere` recognises (single-column equality
on an indexed column where the rhs is a constant / param).
Otherwise we fall through to the historic Filter(SeqScan).

## Executor consumption

`extractScanAndPredicate` learns the new shapes:

```go
func extractScanAndPredicate(child planner.Node) (*planner.SeqScan, planner.Expr, error) {
    switch c := child.(type) {
    case *planner.SeqScan:                    return c, nil, nil
    case *planner.IndexScan:                  return synth(c), indexScanPredicate(c), nil
    case *planner.Filter:
        switch inner := c.Child.(type) {
        case *planner.SeqScan:                return inner, c.Predicate, nil
        case *planner.IndexScan:              return synth(inner), AND(c.Predicate, indexScanPredicate(inner)), nil
        }
        return error
    }
}
```

`indexScanPredicate(ix)` synthesises a `<indexed_col> = key`
equality predicate from the IndexScan node's resolved fields.
The lhs is a fresh `*planner.ColumnRef` pointing at the table's
output ordinal for the indexed column; the rhs is the IndexScan's
already-resolved `Key` expression.

The runtime's `scanMatching` is inherently sequential — it walks
every block of the relation. Treating IndexScan as
"SeqScan with a synthesised key predicate" is correct (the
predicate filters the same tuples the index would have probed)
but does not exploit the index for fast access. That index-driven
update optimisation is a follow-up. The point of this slice is
to lift the previous executor restriction so the planner is
free to emit IndexScan plans for UPDATE/DELETE without breaking
the runtime.

## Foreign-lock detection continues to work

Crucially, `scanMatching`'s per-tuple foreign-lock detection
from step 2b is unchanged. Every tuple the seq-scan visits
(including those that pass the synthesised `=` predicate) goes
through the same `lockedByForeign` check + `acquireTupleLock`
acquire. So `UPDATE WHERE indexed_col = N` against a row a
SELECT FOR UPDATE holds still blocks at the lockmgr — the
new test pins this.

## Tests

`internal/executor/storage_dml_test.go`:

- `TestUpdateViaIndexScanPath` — NEW. Creates a unique index on
  items.id, runs `UPDATE items SET label = 'updated' WHERE id =
  2`, verifies the rewrite produces the same observable
  outcome (id=2 → 'updated'; id=1, 3 unchanged) as the
  pre-IndexScan SeqScan-only path.

`internal/executor/operators_lockrows_test.go`:

- `TestUpdateViaIndexScanBlocksOnForeignTupleLock` — NEW. Once
  the IndexScan path is taken, session 2's UPDATE on a row
  session 1 holds via SELECT FOR UPDATE still registers as a
  tuple-tag waiter. Confirms the IndexScan promotion didn't
  bypass step 2b's blocking.

Full `go test ./...` green; race-mode targeted runs across
executor + planner all green.

## Out of scope

- Index-driven UPDATE/DELETE optimisation: seq-scan is still
  the runtime, just with a key predicate. Promoting
  scanMatching to actually probe the index requires a deeper
  refactor of the scanForMatches kernel.
- Tuple-level NOWAIT/SKIP LOCKED through UPDATE/DELETE — the
  per-row dispatch loop currently always blocks; promoting to
  fail-fast / skip-locked needs threading the wait policy
  from the user-facing statement (UPDATE/DELETE in upstream
  also always block, so this gap is mostly theoretical).
- MultiXact-aware multi-holder support for FOR SHARE.
- Streaming per-row stamping (eliminate the two-pass buffer
  in lockRowsOp).
- pg_locks-style introspection of tuple-level lock holders.

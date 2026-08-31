# 0021-0009 — Tuple-Level Locking: IndexScan Leaf Support

**Status:** accepted (step 2c — IndexScan currentTID + lockRowsOp
traversal; UPDATE/DELETE-via-index path stays out of scope)
**Milestone:** [0021 — Pessimistic Row Locking](../../milestones/0021-pessimistic-lock-select-for-update.md)
(deferred follow-up: tuple-level locking on top of M0012).
**Spans seam:** indexScanOp TID retention, lockRowsOp scan-leaf
interface dispatch.
**Cross-links:**
[0021-0007](0021-0007-tuple-locking-producer-wiring.md)
(seqScan-leaf producer wiring),
[0021-0008](0021-0008-tuple-locking-blocking-enforcement.md)
(blocking enforcement).

## Context

Step 2a wired `lockRowsOp` to find a `*seqScanOp` leaf via
`findSeqScan` and stamp per-row lock-only xmax. The planner can
also pick `IndexScan` when `WHERE indexed_col = expr` is in
play (`planIndexScanFromWhere`); SELECT FOR UPDATE WHERE
indexed_col routed through that path was effectively a relation-
level-only lock — `findSeqScan` returned nil and lockRowsOp fell
through to pass-through Next without per-tuple stamping.

This slice closes that gap: IndexScan now retains the heap
ItemPointer alongside each emitted row, exposes it via the same
`currentTID` shape seqScanOp uses, and lockRowsOp's traversal
helper picks up either leaf type.

## indexScanOp TID retention

`indexScanOp` already iterates its single-key range via
`btree.RangeScan`'s callback, which receives `(key, ItemPointer)`
per match. The callback decoded the heap tuple but discarded the
ItemPointer. New parallel slice:

```go
type indexScanOp struct {
    ...
    rows []Row
    tids []storage.ItemPointer  // ← new
    idx  int
}
```

`tids` is appended in lockstep with `rows` inside the RangeScan
callback. Reset to nil on Open (matching the `rows` reset) and
on Close.

```go
func (o *indexScanOp) currentTID() (storage.RelFileNode, storage.ItemPointer, bool) {
    if o.idx == 0 || o.idx > len(o.tids) {
        return storage.RelFileNode{}, storage.ItemPointer{}, false
    }
    rel := o.ctx.Catalog.RelFileNode(o.plan.Table)
    return rel, o.tids[o.idx-1], true
}
```

`idx` points one past the last-returned row (Next increments
post-fetch), so the just-returned row's TID is at idx-1 —
mirrors seqScanOp's `curSlot - 1` convention.

## currentTIDProvider interface

`findSeqScan` is replaced with `findScanLeaf` returning the
new interface:

```go
type currentTIDProvider interface {
    currentTID() (storage.RelFileNode, storage.ItemPointer, bool)
}

func findScanLeaf(op Operator) currentTIDProvider {
    for {
        switch v := op.(type) {
        case *seqScanOp:   return v
        case *indexScanOp: return v
        case *projectOp:   op = v.child
        case *filterOp:    op = v.child
        default:           return nil
        }
    }
}
```

Both leaf types implement the interface unchanged (seqScanOp's
existing currentTID method and the new indexScanOp method
match). lockRowsOp's `scan` field changes type from
`*seqScanOp` to `currentTIDProvider`; the rest of the
two-pass drain-then-stamp flow is untouched.

## Out of scope

- UPDATE / DELETE through IndexScan: `extractScanAndPredicate`
  in operators_storage.go still requires a Filter(SeqScan) or
  bare SeqScan; an IndexScan-based UPDATE plan would error
  before the lock-detection logic runs. Promoting that
  requires building an index-driven UPDATE/DELETE path,
  which is a wider slice.
- Tuple-level NOWAIT/SKIP LOCKED through IndexScan — same
  caveat as the seqScan path; the wait policy is not yet
  threaded into the per-row stamp acquire.
- MultiXact-aware multi-holder support for FOR SHARE.
- Streaming per-row stamping (eliminate the two-pass buffer).

## Tests

`internal/executor/operators_lockrows_test.go`:

- `TestLockRowsStampsLockOnlyXmaxIndexScan` — NEW.
  Creates a unique index on items.id, runs `SELECT id FROM
  items WHERE id = 2 FOR UPDATE`. The planner picks
  IndexScan; lockRowsOp must traverse past Project →
  IndexScan via findScanLeaf and stamp lock-only xmax on
  exactly one row (id=2). Reads the heap page back and
  verifies precisely one tuple has Xmax == ctx.Tx.XID +
  HeapXmaxLockOnly bit set.

Full `go test ./...` green; race-mode targeted runs across
executor green.

# 0103-0034 — M0103-0007 rung 11: preserve subscriber-only columns across replicated UPDATEs

Status: accepted (2026-05-14)
Owner: goopg apply worker
Implements: `internal/executor/applyworker.go`

## Problem

Rung 10 (`docs/design/0103-0033-m0103-0007-rung-10-pg-to-goopg-column-order.md`)
taught `decodePgoutputTupleAsRow` to resolve remote→local attribute positions
by name and explicitly listed subscriber-extra columns as out of scope:

> "Subscriber columns missing on the publisher remain `NullDatum` (existing
> init behaviour); DEFAULT-value support for that asymmetric case stays out
> of scope for this rung."

The follow-on rung 11 closes the half of that gap that is actually a
correctness bug rather than a DEFAULT-evaluation gap.

Concrete failure mode pre-rung-11:

- Publisher: `public.t (id int PRIMARY KEY, v text)`
- Subscriber: `public.t (id int PRIMARY KEY, v text, note text)`
- Subscriber writes `UPDATE t SET note='kept' WHERE id=1` directly.
- Publisher then `UPDATE t SET v='updated' WHERE id=1`.

The apply worker's `decodePgoutputTupleAsRow` filled `note`'s position
with `NullDatum` (no remote attribute mentioned it). `applyUpdateByKey`'s
"fill from matched existing row" path only triggered when the publisher's
`'u'` (unchanged-TOAST) status appeared in the new tuple — for an UPDATE
that touches only `v`, `unchanged[]` is all-false. So the path skipped
the read-side scan, ran `applyDeleteByKey` + `writeHeapRowReturning` with
`note=NullDatum`, and the subscriber-only value vanished on every
replicated UPDATE.

Upstream PG's `slot_modify_data` (`worker.c`) preserves the existing tuple's
value for any local column the remote tuple didn't carry — same shape we
need here. INSERT semantics differ (upstream evaluates the column DEFAULT
expression for the missing slot); rung 11 explicitly leaves INSERT at
NullDatum and defers DEFAULT-expression evaluation to a later rung,
because it requires plumbing CREATE-TABLE-time expression text through
the catalog and a constrained expression evaluator that's safe to run
inside the apply worker.

## Decision

Extend `decodePgoutputTupleAsRow` with a third return mask:

```go
func decodePgoutputTupleAsRow(
    remoteCols []wal.DecodedAttr,
    localCols []catalog.Column,
    tup []wal.DecodedColumn,
) (Row, /*unchanged*/ []bool, /*missing*/ []bool, error)
```

`missing[j]` is true when the local column at position `j` was not
claimed by any remote attribute (subscriber-extra). The row sized to
`len(localCols)` starts at NullDatum; missing positions stay there. The
existing `unchanged[j]` slice keeps its rung-5 meaning — only `'u'`
status cells flip it.

`applyUpdateByKey` accepts a new `newMissing []bool` parameter and merges
it with `newUnchanged` into a single "needs fill from heap" mask:

```go
func applyUpdateByKey(..., newUnchanged, newMissing []bool) error {
    needFill := anyTrue(newUnchanged) || anyTrue(newMissing)
    if needFill {
        matched, _ := applyScanFirstMatch(ctx, rel, cols, oldKeyRow)
        if matched != nil {
            for i := range newRow {
                if (i < len(newUnchanged) && newUnchanged[i]) ||
                   (i < len(newMissing)  && newMissing[i])  {
                    newRow[i] = matched[i]
                }
            }
        }
    }
    return delete+insert
}
```

`applyInsert` ignores `missing[]`: subscriber-extra positions stay at
NullDatum (existing behaviour), and the defensive `'u'`-on-INSERT check
keeps firing only on `unchanged[i]`. `applyDelete` decodes the DELETE
old-tuple; subscriber-extra positions in the resulting key row are
NullDatum, which `rowMatchesKey` already treats as wildcards — those
columns never exist on the publisher and can't participate in row-locator
matching anyway.

## Why not merge `unchanged` and `missing` into one mask

Two distinct concepts share a remedy on UPDATE but diverge on INSERT:

| Status        | meaning                                | INSERT remedy | UPDATE remedy |
|---------------|----------------------------------------|---------------|---------------|
| `unchanged[i]`| Publisher emitted `'u'` (TOAST unchanged) | INVALID (`'u'` never legal on INSERT) — refuse | preserve from heap |
| `missing[i]`  | Local column has no remote attribute   | NullDatum (DEFAULT eval is later rung) | preserve from heap |

Conflating them would either silently install NULL for malformed `'u'`-on-
INSERT streams (regressing rung 5's defensive check) or refuse legitimate
subscriber-extra INSERTs that should just produce NULL. Two slices keep
the two policies independently testable.

## Why INSERT stays NullDatum

Full DEFAULT-expression evaluation requires:

1. Capturing CREATE-TABLE-time DEFAULT expression text on
   `catalog.Column` (currently dropped by `parseColumnDef`'s
   skip-until-comma branch).
2. A constrained expression evaluator that can run inside the apply
   worker context — `now()` and `current_timestamp` are easy, but
   `nextval('seq')` requires plumbing sequence state, and arbitrary
   subqueries would invite re-entrant planner calls from inside the
   apply path.

Both are larger lifts than this rung is willing to take. NULL is the
right answer for columns that already declared no DEFAULT, and for
columns with a NOT NULL + DEFAULT pair the test surface today rejects
the INSERT at heap write time — the apply worker will surface that
loudly enough for an operator to diagnose.

## Tests

Unit (in `internal/executor/applyworker_test.go`):

- `TestApplyWorkerDecodeMarksSubscriberExtraAsMissing` — decoder returns
  `missing=[false,false,true]` for a 2-col remote, 3-col local pair.
  Pins the contract that `unchanged` and `missing` are independent.
- `TestApplyUpdateByKeyPreservesSubscriberExtraColumn` — seeds a 3-col
  heap row, drives `applyUpdateByKey` with `newMissing=[false,false,true]`
  and asserts the post-update heap state preserves `note='kept'`. Without
  the rung-11 fill loop the post-update row would have `note=NULL`.
- `TestApplyWorkerDecodeReturnsUnchangedMask` — updated to assert
  `missing[]` is all-false when every local column is claimed.
- `TestApplyWorkerDecodeRemapsReorderedColumns` /
  `TestApplyWorkerDecodeRejectsUnmatchedRemoteCol` — touched only to
  consume the third return value.

Live (in `internal/testport/pgoutput_interop_test.go`):

- `TestPort_PgoutputInteropPGToGoopgSubscriberExtraColumn` — PG
  publisher with `(id, v)` + goopg subscriber with `(id, v, note)`;
  workload INSERT → subscriber-side UPDATE setting `note='kept'` →
  publisher UPDATE touching only `v`. Asserts the final state has
  `v='updated' AND note='kept'`. Without the fix the third assertion
  fails because `note` is NULL'd; a negative pin (`note IS NULL` returns
  0 rows) catches a regression that overwrites instead of preserves.

## Verification

- `go test -count=1 -timeout 60s -run "TestApplyWorker|TestPrimaryKeyOnlyRow|TestApplyUpdateByKey" ./internal/executor/` → PASS.
- `go test -count=1 -timeout 180s -run TestPort_PgoutputInteropPGToGoopgSubscriberExtraColumn ./internal/testport/` → PASS (~2.2 s).

## Out of scope (next rungs)

- DEFAULT-expression evaluation for subscriber-extra columns on INSERT.
- Publisher-extra columns (currently rejected with a hard error — already
  the right behaviour).
- proto_version=2 streaming subxacts + `kill -9` failover plumbing —
  parallel rungs.

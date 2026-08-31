# 0103-0033 — M0103-0007 rung 10: PG → goopg column-order remap

Status: accepted (2026-05-14)

Milestone: M0103-0007 (Scenario A: PG publisher → goopg subscriber E2E
replication).

Rungs 1–9 closed pgoutput-shape gaps assuming schema parity between
publisher and subscriber: identical column names, types, AND physical
ordinal positions. Rung 10 closes the column-order parity assumption.

## Problem

`internal/executor/applyworker.go::decodePgoutputTupleAsRow` was
indexing the subscriber's column list with the same ordinal the
publisher emitted on the wire:

```
for i, col := range tup {       // i = REMOTE ordinal
    local := localCols[i]       // BUG: i used as LOCAL ordinal too
    ...
    row[i] = d                  // BUG: row[i] follows remote ordering
}
```

A publisher table declared `(id int PRIMARY KEY, v text)` and a
subscriber table declared `(v text, id int PRIMARY KEY)` then produced
one of two failure modes:

- **Loud**: wire `[id=1, v='alice']` ingests `'alice'` against
  `localCols[0]={v text}` for col 0 (OK) and `'1'` against
  `localCols[1]={id int}` for col 1 (OK because both parse), but the
  resulting `row` is in REMOTE order. Downstream code (`writeHeapRow`,
  `maintainUniqueIndexesForInsert`) consumes `row` aligned with
  `r.local.Columns`, so the values land in the wrong heap slots:
  `(v=1, id='alice')`. The PK constraint on the int column then
  rejects the INSERT (or stores corruption silently if no PK
  validator).

- **Silent corruption**: if both columns happen to parse in either
  type and constraints don't fire, the heap accumulates rows with
  swapped values forever.

PG's apply worker (upstream `apply_handle_insert_internal` →
`logicalrep_rel_open`) resolves attribute mapping by NAME, populating
`remoterel->attmap[]`. Goopg now matches.

## Change

`decodePgoutputTupleAsRow(remoteCols, localCols, tup)` builds a per-
call `localIdx[i] = j` map where `j` is the position of
`remoteCols[i].Name` inside `localCols`. The output `row` is sized to
`len(localCols)` and indexed by LOCAL position. When a remote column
name has no local match, the helper returns an explicit error rather
than silently dropping the value — symmetric with PG's behaviour and
load-bearing for catching subscriber DDL drift early. When the
subscriber's table has additional columns not on the publisher, those
positions remain `NullDatum` (existing init behaviour); DEFAULT-value
support for that asymmetric case stays out of scope and a future rung
can add it when needed.

The `unchanged` mask is also indexed by local position, keeping it in
lockstep with `row` so `applyUpdateByKey`'s `'u'` fill loop
(`newRow[i] = matched[i]` for unchanged cells) stays valid.

All other apply paths (`applyInsert`, `applyDelete`, `applyUpdate`,
`primaryKeyOnlyRow`, `applyScanFirstMatch`, `applyDeleteByKey`,
`writeHeapRowReturning`) consume Rows aligned with the subscriber's
`catalog.Table.Columns`. They were already correct for that shape;
only the decoder bridged the wire side to the local side, so the fix
is isolated to one function.

When publisher and subscriber declare the same columns in the same
order — the rungs 1–9 case — `localIdx[i] == i` for every `i` and the
behaviour is identical to before. No regressions in the existing
suite.

## Pinning tests

- Unit: `internal/executor/applyworker_test.go`
  - `TestApplyWorkerDecodeRemapsReorderedColumns` — swapped-order
    publisher/subscriber columns; asserts the decoded `Row` places `v`
    at local position 0 and `id` at local position 1 even though the
    wire tuple emitted them in reverse.
  - `TestApplyWorkerDecodeRejectsUnmatchedRemoteCol` — publisher
    carries `extra_on_publisher` that the subscriber's table doesn't
    declare; asserts the helper returns an error whose message
    mentions the offending column.

- Live E2E: `internal/testport/pgoutput_interop_test.go`
  - `TestPort_PgoutputInteropPGToGoopgColumnOrderMismatch` — full PG
    publisher + goopg subscriber pair with swapped column order on
    each side. Workload: 2 INSERTs, 1 no-key-touched UPDATE
    (exercises `primaryKeyOnlyRow` + `applyUpdateByKey`), 1 DELETE.
    Asserts via fresh `database/sql` sessions through the goopg PK
    IndexScan path that `count(*) = 1`, `WHERE id = 1 AND v =
    'alice-updated'` returns 1, `WHERE id = 2` returns 0.

## Verification

- `go test -count=1 -timeout 60s -run "TestApplyWorker|TestPrimaryKeyOnlyRow" ./internal/executor/` → PASS (0.02 s)
- `go test -count=1 -timeout 120s -run TestPort_PgoutputInteropPGToGoopgColumnOrderMismatch ./internal/testport/` → PASS (~2.0 s)
- `go test -count=1 -timeout 180s -run "TestPort_PgoutputInteropPGToGoopg" ./internal/testport/` → PASS for all 10 rungs (~17 s)
- `go test -race -count=1 -timeout 300s ./internal/executor/ ./internal/wal/ ./internal/catalog/ ./internal/testutil/pubsubcluster/` → all green

## Out of scope (future rungs)

- DEFAULT-value handling for subscriber columns absent from the
  publisher. Currently those land as `NullDatum`, which violates
  `NOT NULL` constraints if any.
- Case-sensitive vs case-insensitive identifier matching for
  quoted-identifier DDL asymmetries. Today both sides emit catalog-
  normalised lowercase names; quoted-identifier DDL is rare in real
  pipelines but would need explicit handling.
- Column-type incompatibility detection at subscription create time
  (PG validates types up front via the protocol; goopg discovers
  mismatches lazily through `parsePgoutputText` errors).

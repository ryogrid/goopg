# 0094-0002 — Logical Replication Apply Completeness (DELETE + UPDATE)

**Status:** accepted (2026-05-11 — Delete+Update apply landed; TestE2E_LogicalReplication passes)
**Date:** 2026-05-11
**Milestone:** M0094-0002

## Problem

`TestE2E_LogicalReplication` is hard-skipped. The logical apply worker
(`internal/executor/applyworker.go`) handles `B` (Begin), `R` (Relation),
`I` (Insert), and `C` (Commit) messages, but:

- `applyDelete()` is a no-op (logs only). pgoutput `D` messages are received
  but the corresponding row is never removed from the subscriber table.
- `applyUpdate()` does not exist. The executor emits UPDATE as a pair of
  `HeapDelete` + `HeapInsert` records. The pgoutput encoder emits them as
  separate `D` + `I` messages. The apply worker therefore sees two separate
  messages and applies them as a delete-no-op followed by an insert, which
  is incorrect when `applyDelete()` is a no-op (leaves both the old and new
  row).

## Root Cause Analysis

### DELETE

pgoutput `D` (Delete) message format (v1):
```
'D'  — message type byte
RelationID (uint32)
TupleType ('K' = key-only, 'O' = full old tuple)
TupleData — encoded columns (key columns only for DEFAULT identity;
            all columns for FULL identity)
```

`applyDelete()` receives a decoded `DecodedMessage` containing the relation OID
and a pre-image tuple. The current implementation logs and returns nil.

The fix must:
1. Decode the key/old tuple from the message.
2. Identify the local table for the relation.
3. Find the target row via a heap scan with equality predicates on the key columns.
4. Delete the row.

### UPDATE

The executor stores UPDATE as:
1. `HeapDelete` on the old tuple (marks xmax).
2. `HeapInsert` of the new tuple (new CTID).

The WAL classifier (`internal/wal/classifier.go`) emits these as separate
`Change{Kind: ChangeDelete}` and `Change{Kind: ChangeInsert}` events in the
reorder buffer.

The pgoutput encoder (`internal/wal/pgoutput.go`) currently only handles
`ChangeInsert` → `I` and `ChangeDelete` → `D`. It emits no `U` message.

Two approaches are possible:

**Option A — Reorder buffer folding (chosen):** Detect consecutive `(xid, rel,
Delete)` + `(xid, rel, Insert)` pairs in `reorder.go::Close()` and fold them
into a single `Change{Kind: ChangeUpdate, OldTuple, NewTuple}`. The pgoutput
encoder then emits `U` with both old and new tuples. The apply worker handles
`U` messages.

**Option B — HOT-Update WAL record:** Add a new `RecordKindHeapUpdate` that
carries old and new tuple in one WAL record. Higher WAL volume; requires format
change.

Option A is chosen because it requires no WAL format change and the reorder
buffer already tracks all changes for a transaction before emitting.

## Chosen Design

### 1. Reorder Buffer Update Folding (reorder.go)

In `reorder.go::Close()` (the point where the reorder buffer emits committed
changes), after collecting the ordered `[]Change` for a transaction, run a
folding pass:

```
for each consecutive pair (changes[i], changes[i+1]):
    if changes[i].Kind == ChangeDelete &&
       changes[i+1].Kind == ChangeInsert &&
       changes[i].RelOID == changes[i+1].RelOID:
        fold into Change{Kind: ChangeUpdate,
                         OldTuple: changes[i].OldTuple,
                         NewTuple: changes[i+1].NewTuple}
        consume changes[i+1]
```

This relies on the invariant that the executor always emits HeapDelete before
HeapInsert for the same relation within the same transaction when doing an
UPDATE. This is verified in unit tests.

### 2. pgoutput U Message Encoding (pgoutput.go)

Extend `Encode()` to handle `ChangeUpdate`:

```
'U' (byte)
RelationID (uint32)
'O' (old tuple type — key or full depending on replica identity)
OldTupleData
'N' (new tuple type)
NewTupleData
```

For `DEFAULT` replica identity (primary key only), old tuple contains only PK
columns. For `FULL`, all columns. The replica identity is stored in the
`RelationFilter` / catalog and looked up at encoding time.

### 3. applyDelete() Implementation (applyworker.go)

```go
func (w *ApplyWorker) applyDelete(msg DecodedMessage) error {
    rel := w.relations[msg.RelationID]
    if rel == nil {
        return fmt.Errorf("unknown relation %d", msg.RelationID)
    }
    // Build equality predicates from key columns.
    preds := buildKeyPredicates(rel, msg.OldTuple)
    // Heap scan with predicates; delete matching row.
    return w.execContext.DeleteWhere(rel.TableName, preds)
}
```

`DeleteWhere` performs a sequential heap scan, evaluates the predicates on each
tuple, and calls `markHeapDelete` on matches. For tables with a primary-key index
(replica identity DEFAULT), the planner can use the B-tree index to avoid a full
table scan — but correctness is achieved with a seq scan; optimisation is
deferred.

### 4. applyUpdate() Implementation (applyworker.go)

```go
func (w *ApplyWorker) applyUpdate(msg DecodedMessage) error {
    rel := w.relations[msg.RelationID]
    preds := buildKeyPredicates(rel, msg.OldTuple)
    newRow := decodeRow(rel, msg.NewTuple)
    return w.execContext.UpdateWhere(rel.TableName, preds, newRow)
}
```

`UpdateWhere` is logically equivalent to `DeleteWhere` + `InsertRow` in the same
transaction.

### 5. Un-Skip TestE2E_LogicalReplication

The test:
1. Starts publisher + subscriber.
2. Creates `pub_t (id int primary key, val text)` on publisher.
3. Creates publication + subscription.
4. INSERTs a row → verifies on subscriber.
5. DELETEs the row → verifies it disappears on subscriber.
6. INSERTs another row → UPDATEs it → verifies new value on subscriber.

## Key Files

| File | Change |
|------|--------|
| `internal/wal/reorder.go` | Fold Delete+Insert → Update in `Close()` |
| `internal/wal/pgoutput.go` | Emit `U` message for `ChangeUpdate` |
| `internal/executor/applyworker.go` | Implement `applyDelete()`, `applyUpdate()` |
| `internal/testport/e2e_replication_test.go` | Un-skip `TestE2E_LogicalReplication` |

## Tests

- `TestE2E_LogicalReplication` (un-skipped) — INSERT + DELETE + UPDATE end-to-end.
- Unit test for reorder buffer folding: `TestReorderFoldDeleteInsertToUpdate`.
- Unit test for pgoutput U encoding: `TestPgoutputUpdateMessageEncoding`.
- Existing tests must not regress: `go test ./internal/wal/... ./internal/executor/...`

## PostgreSQL Reference

- `postgres/src/backend/replication/logical/reorderbuffer.c` — `ReorderBufferCommit`,
  change accumulation and emission.
- `postgres/src/backend/replication/pgoutput/pgoutput.c` — `OutputPluginApplyChange`,
  U message encoding.
- `postgres/src/backend/replication/logical/worker.c` — `apply_handle_update`,
  `apply_handle_delete`.

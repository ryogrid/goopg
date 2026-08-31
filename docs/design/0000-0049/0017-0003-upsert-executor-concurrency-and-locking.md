# 0017-0003 — UPSERT Executor: Conflict Detection and DO UPDATE Apply

**Status:** accepted (Stage A executor slice)
**Milestone:** [0017 — UPSERT Support](../../milestones/0017-upsert-on-conflict-do-update.md)
**Spans seam:** Executor Insert build path, B-tree arbiter probe,
DO UPDATE apply, arbiter index maintenance.
**Cross-links:**
[0017-0001](0017-0001-on-conflict-parser-ast-and-analysis.md)
(parser + analyzer),
[0017-0002](0017-0002-upsert-planner-and-arbiter-selection.md)
(planner conflict-arbiter resolution),
[root-0012](../../root/root-0012-executor.md) (executor scaffolding),
[0002-0001](../0002-0001-storage-architecture.md) (heap +
buffer pool semantics).

## Context

M0017-0001 / M0017-0002 produced an executable plan node:
`Insert.OnConflict` carries a resolved arbiter unique index, the
inserted-row column ordinals that participate in the conflict key,
and a pre-bound `UpdateSet`/`UpdateWhere` whose ColumnRef indices
address a 2N-wide merged tuple (existing 0..N-1, inserted N..2N-1).

This slice is the runtime: detect the conflict, apply the DO UPDATE
or DO NOTHING action, and keep the arbiter index live across the
statement so multi-row VALUES inside the same UPSERT see prior
rows' inserts.

## Build-time dispatch

`executor.Build` routes any `*planner.Insert` carrying a non-nil
`OnConflict` to a new `upsertOp` instead of the plain `insertOp`:

```go
case *planner.Insert:
    child, err := Build(p.Source)
    if err != nil { return nil, err }
    if p.OnConflict != nil {
        return maybeInstrument(p, newUpsertOp(p, child)), nil
    }
    return maybeInstrument(p, newInsertOp(p, child)), nil
```

The `M0017-0002` rejection is removed; existing INSERT-without-ON-
CONFLICT paths are unchanged (zero-OnConflict → plain `insertOp`).

## upsertOp lifecycle

**Open:**

1. Validate Context (Pool / Catalog non-nil, plan.OnConflict
   non-nil).
2. Stage A guard — reject any UpdateSet that targets a
   conflict-key column. Without this, the arbiter index entry for
   the original key would point at a tuple whose actual key bytes
   differ; future probes would land on the wrong row. Surfaces
   `0A000` "ON CONFLICT DO UPDATE may not modify conflict-key
   column X in v0 (Stage A)".
3. Acquire `RowExclusiveLock` on the heap relation (mirrors
   plain INSERT).
4. Open the child operator (Values).
5. If `OnConflict.ArbiterIndex != nil`, open the arbiter btree
   once and cache it on the op for reuse across rows.

**Next:** one-shot side-effect loop. For each child row:

```
1. Reorder source row → target column order via plan.ColumnIndex.
2. probeArbiter(rel, cols, inserted) → (ptr, existingRow, conflicted, err)
3. if !conflicted:
       applyInsert  // writeHeapRowReturning + arbiter Insert
       rowsAffected++
   else if Action == OnConflictActionNothing:
       skip      // no rowsAffected bump (matches upstream)
   else:  // OnConflictActionUpdate
       evalUpdate(existingRow, inserted) → (updatedRow, skip, err)
       if skip: continue  // UpdateWhere evaluated false
       applyUpdate(rel, cols, ptr, updatedRow)
       rowsAffected++
```

**Close:** delegates to child.Close.

## Arbiter probe (probeArbiter)

`encodeArbiterKey(plan, tbl, row, pos)` extracts the conflict-key
columns from the inserted row (via `OnConflict.ArbiterColumns`)
and reuses `encodeBTreeKeyForColumn` so the probe encoding
matches what backfill stored. Returns `(nil, nil)` when any
conflict-key column is NULL — upstream's NULL-never-matches rule
for unique-constraint inference; the row falls through to the
no-conflict path with no probe and no maintenance.

`btree.RangeScan(key, key, callback)` walks every index entry
whose key matches. The callback fetches the heap tuple at each
returned `ItemPointer` and applies `mvcc.TupleVisible` against
the current snapshot+xid. **Invisible tuples are skipped** — this
is essential because UPSERT writes new tuples and inserts duplicate
index entries, so historical dead versions are still reachable
via the same key. The first visible match becomes the conflict
tuple; the callback returns `(false, nil)` to short-circuit
further iteration.

`probeArbiter` returns `(_, nil, false, nil)` when no visible
tuple exists for the key — including the v0 case where
`ArbiterIndex == nil` (bare `ON CONFLICT DO NOTHING`).

## applyInsert (no-conflict path)

```go
ptr, err := writeHeapRowReturning(ctx, rel, cols, inserted)
return o.maintainArbiter(inserted, ptr)
```

`writeHeapRow` was refactored to expose its sister
`writeHeapRowReturning` that surfaces the freshly-inserted
tuple's `(block, slot)` ItemPointer; the existing INSERT and
UPDATE callers still use the void-returning wrapper unchanged.

`maintainArbiter` inserts `(key → ptr)` into the arbiter btree so
subsequent rows in the same statement (multi-row VALUES, CTE-fed
INSERT) see the new entry. NULL keys are skipped per upstream
semantics.

## applyUpdate (DO UPDATE path)

```
1. Pin the conflicting tuple's page, stamp xmax via
   PageSetHeapTupleXmax with the current xact's xid, and dirty
   the page through markHeapDeleteDirty so the redo path emits
   either a fixed-size logical record (when LogHeapDelete is
   wired) or marks the page dirty for FPI fallback.
2. writeHeapRowReturning(updatedRow) — appends the new tuple.
3. maintainArbiter(updatedRow, newPtr) — adds (key → newPtr)
   to the arbiter. The old (key → oldPtr) entry stays in place;
   the next probe walks both, the visibility check skips the
   dead one, and the live one is returned.
```

Note: the old index entry is NOT deleted. The btree's
`tree.Insert` allows duplicate keys (uniqueness was enforced
once at backfill time + at probe-and-insert time by upsertOp
itself). Live `RangeScan` iteration tolerates duplicates because
the visibility filter rejects dead tuples.

## evalUpdate (merged 2N row)

```go
merged := append(append(Row{}, existing...), inserted...)
```

The planner emitted ColumnRef indices 0..N-1 for the existing
tuple and N..2N-1 for the inserted (`excluded.col`) tuple. The
runtime evaluator looks up each ColumnRef.Index in `merged`
unchanged — no special-cased "excluded" path is needed.

`UpdateWhere`, when present, is evaluated first; non-true
(false / NULL / non-bool) skips the row silently per upstream's
"WHERE-failed UPDATE doesn't fall back to DO NOTHING" rule. Then
each non-nil `UpdateSet[i]` is evaluated and copied into
`updated[i]`; nil slots inherit `existing[i]`.

## Stage A scope and limitations

What lands in this slice:

- Single-column arbiter (the v0 btree's
  `encodeBTreeKeyForColumn` only handles one column — multi-
  column arbiters surface `0A000` at runtime).
- DO NOTHING and DO UPDATE.
- `excluded.col` references in SET / WHERE.
- Per-statement arbiter maintenance so multi-row VALUES is
  internally consistent.
- Visibility-filtered conflict detection.

What stays deferred:

- **Concurrent writers**: the slice doesn't add the upstream
  `speculative-insert + cleanup-on-conflict` dance. Concurrent
  UPSERTs on the same key may both believe they're winning the
  insert race — under contention, behavior degrades to "two
  rows with the same key" until the next CREATE INDEX rebuild
  surfaces the duplicate. M0012 + M0017's concurrency follow-up
  will tighten this.
- **Multi-column arbiters**: needs btree composite-key
  encoding.
- **Indexes other than the arbiter** are not maintained on
  INSERT — this is a pre-existing v0 limitation, not new to
  M0017-0003. Tables touched by UPSERT must rebuild their
  non-arbiter indexes via CREATE INDEX after a workload run.
- **RETURNING** with ON CONFLICT — analyzer already rejects
  RETURNING for v0 INSERTs.
- **Conflict-key column modifications by UpdateSet** — Stage A
  guard rejects with `0A000`; lifting requires both index
  delete-and-reinsert + a re-probe loop to detect that the
  updated row now conflicts with a different existing row.

## Tests

`internal/executor/operators_upsert_test.go` — six end-to-end
parser→analyzer→planner→executor scenarios:

- `TestUpsertNoConflictInsertsRow` — new key inserts cleanly,
  arbiter index gets the new entry, RowsAffected=1.
- `TestUpsertConflictDoUpdate` — existing key triggers DO UPDATE
  with `excluded.label`; result row carries the inserted value;
  RowsAffected=1.
- `TestUpsertConflictDoNothing` — existing key + DO NOTHING
  leaves the heap untouched; RowsAffected=0 (matches upstream
  silent-skip rule).
- `TestUpsertDoUpdateMixingExistingAndExcluded` —
  `SET label = label || '/' || excluded.label`. Pins the merged
  2N-row layout: bare `label` pulls from existing[N=1],
  `excluded.label` pulls from inserted[N+1].
- `TestUpsertDoUpdateWithWhereSkipsRow` — UpdateWhere predicate
  evaluates false → row left unchanged, RowsAffected=0.
- `TestUpsertConflictKeyModificationRejected` — `0A000` Stage A
  guard against `SET id = excluded.id + 100` (would otherwise
  desync the index).

The test fixture seeds rows then creates the unique index AFTER
seeding so backfill picks up the existing tuples — required
because v0 doesn't maintain non-arbiter indexes on plain INSERT.
The arbiter (which lands here) is the only index UPSERT itself
maintains.

Full `go test ./...` green.

## Out of scope

- Concurrency hardening (speculative insert + cleanup on
  conflict) — M0017-0004 or follow-on.
- Multi-column arbiter btree encoding — extension of
  M0011 / M0017-0004.
- Observability counters (conflict-hit / conflict-update
  totals) — M0017-0004.

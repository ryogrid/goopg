# 0100-0005o — Partition-child trigger firing for UPDATE / DELETE on partitioned parents

Status: accepted
Milestone: M0100-0005 (RC isolation suite 21-spec pass)

## Problem

`partition-key-update-1.spec` defines a BEFORE UPDATE trigger on the
partition *child* `footrg1`:

```sql
CREATE TRIGGER footrg_mod_a BEFORE UPDATE ON footrg1
  FOR EACH ROW EXECUTE PROCEDURE func_footrg_mod_a();
```

`func_footrg_mod_a` sets `NEW.a = 2`, which rewrites the partition key and
forces a cross-partition move on the next UPDATE. The session statement,
however, targets the partitioned *parent*:

```sql
UPDATE footrg SET b='EFG' WHERE a=1;
```

The trigger never fired. goopg's `updateOp.Next` SeqScan path was deciding
the trigger source unconditionally:

```go
scanTblForTrig := tbl
if pu.rel != rel && pu.rel != (storage.RelFileNode{}) {
    scanTblForTrig = tbl // parent triggers apply on writes
}
```

`tbl` is the parent (`footrg`), which has no triggers — so the lookup
silently produced an empty trigger list and the row was written as-is.
`deleteOp.Next` had the symmetric bug: `fireTriggers(o.ctx, tbl, ...)`
without consulting the source-relation's trigger list. Both bugs blocked
the `footrg` permutations of `partition-key-update-1.spec`.

This mirrors upstream PostgreSQL's behaviour: each leaf partition fires
its own row-level triggers when a row routes through it, regardless of
the statement's nominal target table.

## Decision

Track the source table for every collected pending UPDATE / DELETE row,
and use that table's trigger list at the firing site. The source table is
already in scope when `scanMatching` collects pending mutations — the
fix is purely a plumbing change.

## Implementation

`internal/executor/operators_storage.go`:

1. `pendingUpdate` (local struct inside `updateOp.Next`) gains a
   `scanTbl *catalog.Table` field. The scan-collection loop captures
   the current `scanTbl` into a closure variable (matching the existing
   `captureRel` / `captureCols` pattern) and threads it into the appended
   `pendingUpdate`.

2. The SeqScan trigger firing site replaces the dead `if pu.rel != rel`
   branch with:

   ```go
   scanTblForTrig := pu.scanTbl
   if scanTblForTrig == nil {
       scanTblForTrig = tbl
   }
   ```

   `pu.scanTbl == nil` cannot occur for rows collected by the new path,
   but the fallback keeps the IndexScan code path (which does not yet
   populate `scanTbl` because it scans a single relation) behaving
   identically to before.

3. `deleteOp.Next`'s `victim` struct gains `scanTbl *catalog.Table`
   with the same population pattern, and the BEFORE DELETE firing site
   prefers `v.scanTbl` over `tbl`. FK enforcement
   (`enforceFKOnDelete(ctx, tbl, v.row)`) still uses the parent table,
   because partitioned FK enforcement is anchored to the parent's
   `ForeignKeys` list — that remains correct.

The IndexScan path (`updateViaIndex`) is untouched: it only operates on
a single relation today, so its existing `idxTbl := o.plan.Table` is the
right trigger lookup target. If parent-indexed partitioned UPDATEs land
later, the same `scanTbl`-on-pending pattern can be reused there.

## Verification

Two regression tests in `internal/server/notice_test.go`:

- `TestPartitionChildTriggerFiresOnParentUpdate` — partition `footrg_part`
  with LIST partitions `footrg_part1`/`footrg_part2`, BEFORE UPDATE
  trigger on `footrg_part1` only, `UPDATE footrg_part SET b='EFG'
  WHERE a=1` must surface the child's RAISE NOTICE.

- `TestPartitionChildTriggerFiresOnParentDelete` — symmetric for the
  DELETE path.

Both fail without the fix (no NOTICE captured) and pass with it. Full
suite verification:

```
go test -race -count=1 ./internal/executor/ ./internal/server/
```

Both packages pass.

## Cross-references

- Spec: `postgres/src/test/isolation/specs/partition-key-update-1.spec`
- Companion: `docs/design/0100-0005n-cross-partition-update-moved-tuple-error.md`
  (handles the cross-partition move that the trigger's NEW.a rewrite
  triggers on subsequent statements).
- Trigger infrastructure: `docs/design/0096-0012-triggers.md`.

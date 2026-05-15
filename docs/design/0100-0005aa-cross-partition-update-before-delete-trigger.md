# M0100-0005aa — Cross-Partition UPDATE fires BEFORE DELETE on source partition

Status: accepted (2026-05-15 loop 42)

## Context

`partition-key-update-4.spec` permutation 2 (`s1b s2b s2ut1 s1ut s2c s1c
s1st s1stl`) was the last permutation of that spec still deferring.  The
spec defines

```sql
CREATE TRIGGER footrg_ondel BEFORE DELETE ON footrg1
   FOR EACH ROW EXECUTE PROCEDURE func_footrg();
```

on the source leaf partition.  The trigger body mutates `OLD.b` and
records the mutated `OLD.*` row into a `triglog` audit table:

```sql
BEGIN
  OLD.b = OLD.b || ' trigger';
  INSERT INTO triglog select OLD.*;
  RETURN OLD;
END
```

Permutation 2 issues a concurrent `UPDATE footrg SET b = b || ' update2'
WHERE a = 1` from s2, commits it, then s1's `UPDATE footrg SET a = a + 1,
b = b || ' update1'` runs.  The UPDATE moves the row from `footrg1` to
`footrg2` (cross-partition).  Upstream PostgreSQL implements cross-
partition UPDATE as DELETE-on-source + INSERT-on-destination
(`ExecCrossPartitionUpdate` in `nodeModifyTable.c`), so the BEFORE DELETE
trigger fires on `footrg1` with `OLD` reflecting s2's committed update.
Goopg was firing only the BEFORE UPDATE trigger family for cross-
partition UPDATEs; the source partition's BEFORE DELETE trigger was
never invoked, leaving `triglog` empty.

A secondary defect compounded the problem: M0100-0005p deliberately made
`OLD.<col> := expr` a no-op in `parseDottedExprStmt` (only NEW writes
fired).  Even if the BEFORE DELETE trigger fired, the trigger body's
`OLD.b = OLD.b || ' trigger'` would have been discarded and the embedded
`INSERT INTO triglog select OLD.*` would have logged the unmutated
`OLD`.

## Change

Three coupled edits.

### 1. `parseDottedExprStmt` accepts OLD.<field> assignment

`internal/plpgsql/parser.go::parseDottedExprStmt` previously treated
every `OLD.<field>` write as `_plpgsql_noop`, on the grounds that `OLD`
in BEFORE UPDATE / AFTER triggers is conceptually immutable.  But within
the trigger function `OLD` is mutable — the mutation does not change
what is being deleted, it only changes what subsequent expressions and
embedded SQL inside the trigger body observe via `OLD`.  partition-key-
update-4.spec depends on this.

The parser now emits `AssignStmt{Target: "_old_<field>"}` for
`OLD.<field>` assignments — mirroring the existing `_new_<field>`
target.  Both `:=` and bare `=` operator forms are accepted (the SQL
lexer emits `=` as `TokenOperator`).  Unrecognised dotted writes (e.g.
record variables we do not yet support) continue to fall through to the
`_plpgsql_noop` sentinel.

### 2. AssignStmt propagates `_old_<col>` / `_new_<col>` writes back to the trigger row

`internal/executor/plpgsql_runtime.go::executePLpgSQLStmt` AssignStmt
case now intercepts writes whose `Target` starts with `_old_` or
`_new_`.  After the frame slot is updated, the runtime writes the new
value back into `frame.trig.OldRow[i]` / `frame.trig.NewRow[i]` (the
slice the trigger context exposes).

The reason: `execPLpgSQLEmbeddedSQL` calls `substituteTriggerRefs`,
which substitutes literal values for `OLD.<col>` / `OLD.*` / `NEW.<col>`
/ `NEW.*` references in embedded SQL **by reading `trig.OldRow` and
`trig.NewRow` directly** — not by reading the plpgsql frame.  Without
the propagation, `OLD.b = OLD.b || ' trigger'` updates the frame slot
but leaves `trig.OldRow[b_idx]` at its scan-time value, and the
subsequent `INSERT INTO triglog select OLD.*` would record the
unmutated row.

This propagation is also applied to `_new_` writes (M0100-0005p's
`rebuildNewRowFromFrame` already re-reads from the frame at end-of-
trigger, so the extra propagation has no observable effect on the NEW
side, but keeps the slots consistent throughout the trigger body for
any embedded SQL that references `NEW.*`).

### 3. SeqScan UPDATE path fires BEFORE DELETE on cross-partition move

`internal/executor/operators_storage.go::updateOp.Next` SeqScan branch
gains, right after `routeToPartition` has computed
`isCrossPartitionMove` and before any xmax stamp on the old slot:

```go
if isCrossPartitionMove && pu.scanTbl != nil && len(pu.scanTbl.Triggers) > 0 {
    _, ok := fireTriggers(o.ctx, pu.scanTbl, "before", "delete", pu.oldRow, nil)
    if !ok {
        s.Unlock(); o.ctx.Pool.Unpin(s)
        epqSkipSeq = true
        break
    }
}
```

The trigger fires on the **source partition** (`pu.scanTbl`, e.g.
`footrg1`), not on the partitioned parent — partition-key-update-4.spec
attaches the trigger to the leaf.  RETURN NULL semantics (`!ok`) are
honoured: the row is skipped, matching upstream's "suppress the
delete" semantics.  Trigger errors propagate through `fireTriggers`'s
current best-effort path.

The trigger fires **after the EPQ refetch loop** completes (this branch
is reached only after EPQ has either succeeded or determined no
concurrent update is in flight), so `pu.oldRow` reflects the
EvalPlanQual-refetched row, not the row as it looked at scan-time.  This
matches the spec comment: "This will verify that the trigger is not run
*before* the row is refetched by EvalPlanQual."

The EPQ retry path additionally now refreshes `pu.oldRow = cloneRow(baseRow)`
when it re-binds the SET expressions, so the trigger sees the concurrent
updater's committed changes.

The IndexScan UPDATE path (`updateViaIndex`) is unchanged: it does not
support partition routing today (it scans a single index on a single
relation), so the cross-partition codepath is not reachable through it.
When partition-aware IndexScan UPDATE lands, the same trigger fire-site
will need to be replicated there.

## Regression pins

- `internal/plpgsql/parser_test.go::TestParseTriggerOldFieldAssign`
  (renamed from `TestParseTriggerOldFieldAssignStaysNoop`) — asserts
  `OLD.a = 99` now produces `Target = "_old_a"`.  The prior assertion
  enshrined the M0100-0005p no-op semantics that this change reverses
  for OLD.
- `internal/server/notice_test.go::TestCrossPartitionUpdateFiresBeforeDeleteOnSourcePartition`
  — end-to-end pin: BEFORE DELETE on the source leaf fires during a
  cross-partition UPDATE, the trigger body mutates OLD, and an embedded
  `INSERT INTO triglog select OLD.*` records the mutated row.  Asserts
  `(1, 'ABC trigger')` in the log table and the source partition empty,
  destination holding the moved row.
- `internal/testport/TestPort_IsolationPartitionKeyUpdate4` — flips
  from `defer` to PASS; previously failed only on perm-2 lines L34-L36
  (`(0 rows)` vs `1|ABC update2 trigger`/`(1 row)`).

## Verification

`go test -count=1 -race -timeout 280s ./internal/executor/
./internal/plpgsql/ ./internal/server/ ./internal/storage/
./internal/mvcc/ ./internal/wal/ ./internal/parser/ ./internal/planner/
./internal/analyzer/` PASS.

Adjacent isolation tests `LockCommittedUpdate`, `InsertConflictDoUpdate`,
`InsertConflictDoNothing`, `FkSnapshot`,
`PartitionKeyUpdate{1,2,3,4}` all PASS after the change (no regressions
in the 21-spec target).

## Known follow-ups

- `updateViaIndex` cross-partition routing (when it grows partition
  awareness, it must also fire BEFORE DELETE on the source leaf).
- AFTER DELETE trigger firing for cross-partition UPDATE — analogous
  pattern, separate scope.
- Statement-level triggers — only ROW-level handled here.

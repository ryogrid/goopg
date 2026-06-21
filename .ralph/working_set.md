Task: M0118-0003 (row locking) — COMPLETE this loop: promoted `tuplelock-partition`
(slice 13). The M0118-0003 spec group continues (more specs below).

DONE this loop (committed):
- internal/executor/operators_upsert.go: two fixes in the ON CONFLICT DO UPDATE
  `applyUpdate` path.
  (1) new helper `onConflictUpdateTouchesKeyColumn` — true when any
      `OnConflict.UpdateSet[i]` is non-nil AND `Table.Columns[i].Name` is a key
      column of a unique/primary index (`idx.Unique || idx.Primary`, via
      `Catalog.IndexesOnTable`). When true, applyUpdate stamps HEAP_KEYS_UPDATED
      on the conflicting (old) tuple alongside xmax (PageSetHeapTupleKeysUpdated).
      Mirrors PG ExecUpdateLockMode/ExecGetAllUpdatedCols (set-list based, NOT
      value-based) so `SET key=1` (value unchanged) still makes a concurrent
      FOR KEY SHARE wait; no-key `SET col1/col2` does not. The plain-UPDATE path
      stays value-based (!hotEligible) — legitimately different.
  (2) applyUpdate now links old->new via `stampOldCtid` (was missing) so a
      waiting FOR KEY SHARE follows the t_ctid chain to the live successor
      instead of `short read at block` on a self-pointing ctid. ON CONFLICT never
      moves partitions so old/new share rel.
- internal/testport/isolation_port_test.go: +TestPort_IsolationTuplelockPartition.
- CSV target-inventory line 610: failed->pass (comma-free rationale); regenerated
  postgres-oracle-target-inventory.md + upstream-isolation-coverage.md
  (isolation pass 73->74).
- docs/design/0118-0002-*: slice 13 section + status checklist (✅ tuplelock-partition)
  + README index slice-13 sentence. Ledger row appended.

ROOT CAUSE: two perms differ only in DO UPDATE SET. PG's ON CONFLICT locks the
conflicting tuple FOR UPDATE (key-reserving) when the SET list names a key column
regardless of value; goopg's applyUpdate stamped xmax but never set
HEAP_KEYS_UPDATED off the set list, and never linked old->new t_ctid.

GATES (all PASS): go build ./...; go vet executor+testport; gofmt clean on my
added lines (pre-existing go1.25/1.26 version-mismatch noise elsewhere — do NOT
gofmt -w); full internal/executor suite PASS incl. -race; regression batch
TestPort_IsolationInsertConflict*/Tuplelock*/LockCommitted*/UpdateLockedTuple/
EvalPlanQual* all PASS no silent skips; ralph-state-guard OK; pgbench smoke via
pre-commit hook. DO NOT stage: postgres, weekly_loc.*, requirements.txt,
weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0003 row-locking group, one spec per loop):
    RESUME at `propagate-lock-delete` (FK-INSERT lock propagation onto the updated
    tuple + RI trigger; 8 perms incl. SAVEPOINT-rollback variant — a preexisting
    FK-induced lock on `parent` must NOT be ignored after an UPDATE propagates it)
    OR `lock-nowait` (LOCK TABLE — needs a NEW txn-scoped heavyweight lock
    lifecycle, [[lockmgr_locks_are_statement_scoped]]). Both distinct higher-blast
    subsystems, each its own loop.
    Per-spec workflow: add TestPort_Isolation<Name> for the live diff -> fix ->
    green -> CSV failed->pass (rationale=Go func, COMMA-FREE) -> regen
    gen-isolation-coverage + gen-oracle-inventory -> design doc slice + README +
    ledger.

GOTCHAS: CSV rationale MUST be comma-free — [[serena_replace_content_dotall_eats_file]];
prefer built-in Edit for Go code. tpch-spotcheck INFRA-BLOCKED (SLRU backfill >60s);
ON CONFLICT/row-lock path never fires in pgbench TPC-B/TPC-H so TPS blast radius nil.

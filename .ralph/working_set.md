# Working Set (carried from loop 2, 2026-06-13)

## Completed this loop

**M0100-0006b parts (a)/(b) — speculative locks surface via pg_locks ⋈ pg_stat_activity** — DONE
Both `controller_print_speculative_locks` steps now match PG (4-row then 3-row).
Three root causes fixed + row model completed:
- `internal/activity/registry.go`: new `PIDForProcNum(procNum)` (live backend PID
  from the cold slot; lock-free, mirrors GetBackendType).
- `internal/executor/context.go`: new `(*Context).backendPID()` — resolves PID via
  Activity+ProcNum (deprecated `ActivityPID` was always ""); falls back to ActivityPID.
- `internal/executor/operators_upsert.go`: 3 spec-registry call sites use `backendPID()`.
- `internal/executor/spec_insert_registry.go`: `LockRows` now emits each waiter's
  OWN-XID `transactionid ExclusiveLock t` (waiter not in active map), deduped.
- `internal/server/dispatch.go`: `ectx.Activity = s.cfg.Activity` (was never wired;
  that's why backendPID saw Activity==nil).
- `internal/initdb/pg_stat_activity_view.go`: `pid`/`leader_pid` `text`→`int4` +
  `numericPIDOrNull` (bg-worker pids like "cp-0" → NULL). text↔int4 USING(pid) join
  silently returned 0 rows — THE join blocker.
- Design doc updated: docs/design/0100-0006b-*.md (parts a/b section).

## Next task (M0100-0006b — the SKIP itself, SEPARATE executor bug)

Test still SKIPs on a +2-line offset cascading to EOF. After `s2_commit`, s1's
`ON CONFLICT DO UPDATE` retry re-evaluates the NON-UNIQUE index expression
`blurt_and_lock_4(key)`, emitting 2 extra NOTICEs PG does not emit at completion.
- Resume: `internal/executor/operators_upsert.go` post-XID-wait path (~L387-410:
  re-probe → applyInsert/evalUpdate). Stop the redundant non-unique-index expr
  re-eval on the UPDATE branch (PG evals blurt_and_lock_4 only once, at spec insert).
- THIS IS EXECUTOR/ROW-COUNT RISK: run `scripts/tpch-spotcheck.sh` (Q12=2/Q13=35)
  + InsertConflict*/Merge* before committing.

## Gates run this loop

- go build ./... : PASS ; go vet (initdb/activity/executor/server) : PASS
- internal/activity, internal/catalog, internal/executor, internal/initdb : PASS
- Dedicated isolation: EvalPlanQual(+Trigger), InsertConflictDoNothing,
  InsertConflictDoUpdate[/2/3/4], LockCommitted(Update/Keyupdate), ReadWriteUnique,
  FkSnapshot : PASS. IsolationSuite: 0 FAIL.
- TestPort_PgStatActivity(+WaitEventsNull), TestSyntax_Catalog_PgStatActivity,
  plpgsql pid scan : PASS.
- TPC-H spotcheck: SKIPPED (no data dir) — no row-count semantics changed anyway.
- make ralph-state-guard : (run immediately before status block)

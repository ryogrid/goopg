Loop #55: M0118-0008 — `pg_cancel_backend(pid)` scalar built-in LANDED (enabler,
NOT a promotion; design 0118-0056). Finished the in-flight work loop #54 left
uncommitted (cancel.go/dispatch.go/context.go/expr.go), added the sibling
extended-path wiring + a unit test, and the design doc.

Done: `pg_cancel_backend(pid int4)→bool` was seeded in pg_proc (OID 2171, bool,
int4 arg) but had NO executor handler → SQL call fell through evalFuncCall→
evalStoredRoutineFuncCall → hard `function pg_cancel_backend does not exist`
(step s1cancel of detach-partition-concurrently-3/-4). goopg is one OS process,
so a cancel calls the process-wide cancel registry that already backs the wire
CancelRequest path. New cancelEntry.cancelNoSecret() + registry.cancelByPID(pid)
fire the target's active query cancel func WITHOUT a secret check (caller is an
authenticated backend). New strict evalFuncCall case (NULL→NULL) → ctx.CancelBackend.

Files: internal/server/cancel.go (cancelNoSecret + cancelByPID), dispatch.go +
dispatch_extended.go (BOTH wire ectx.CancelBackend → s.cancelReg.cancelByPID —
sibling-path), internal/executor/context.go (+CancelBackend field), expr.go
(+case), internal/server/cancel_by_pid_test.go (NEW TestCancelByPID), docs/design/
0118-0056 + README index, deferral_ledger.

Gates: TestCancelByPID PASS (unknown→false / idle→true no-fire / busy→true fires
once / secret irrelevant); full internal/server + internal/executor PASS;
go build ./... + go vet clean. tpch-spotcheck + pgbench smoke (pre-commit hook).

Next step (M0118-0008 hard tail — one new subsystem each, one slice per loop):
- detach-partition-concurrently-3/4: next blocker = DETACH … CONCURRENTLY
  two-phase `<waiting>` (wait out old snapshots before relpartbound→NULL flips
  at commit) COUPLED to cross-session catalog visibility. goopg's single shared
  in-memory catalog makes DETACH/ATTACH/INHERIT immediately+globally visible; PG
  defers per-snapshot. Milestone-sized — probe with zz_probe_test.go first.
- partition-drop-index-locking: real pg_locks + pg_stat_activity view population.
- reindex-concurrently-toast: real auto-created TOAST relations (reltoastrelid=0
  for plain text cols today) + allow_system_table_mods GUC.
Both pg_backend_pid (0118-0055) + pg_cancel_backend (0118-0056) enablers now
landed; the remaining tail is the `<waiting>`/visibility subsystem.

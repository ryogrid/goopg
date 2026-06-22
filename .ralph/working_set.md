Loop #45: M0118-0008 fifteenth promotion (design 0118-0047) — `alter-table-1`
PROMOTED to pass (strict). All 170 permutations byte-for-byte vs PG 18.3.

The spec adds `ALTER TABLE b VALIDATE CONSTRAINT bfk` on top of `alter-table-2`'s
`ADD CONSTRAINT … FOREIGN KEY … NOT VALID`. The ADD-FK half already parsed +
locked (0118-0046); the only new piece was VALIDATE CONSTRAINT:
1. Parser: new `AlterTableValidateConstraint` action kind (ast.go); branch in
   `parseAlterTableAction` (ddl.go) matching `VALIDATE CONSTRAINT name` —
   `VALIDATE` is an identifier-keyword (acceptIdentKeyword), not reserved.
2. Executor (operators_ddl.go AlterTable dispatch): new case takes a
   transaction-scoped `ShareUpdateExclusiveLock` via acquireDDLLockTxn
   (AlterTableGetLockLevel → AT_ValidateConstraint, minimum lock) and flips the
   named FK's `NotValid`→false (convalidated 'f'→'t'); unknown name ⇒ 42704.
SUE doesn't conflict with AccessShare/RowShare/RowExclusive, so VALIDATE never
blocks the reader session — the only wait is the INSERT behind the uncommitted
ADD CONSTRAINT's ShareRowExclusiveLock (same as alter-table-2).

Files: internal/parser/ast.go (action kind), internal/parser/ddl.go (parse
branch), internal/executor/operators_ddl.go (dispatch case),
internal/testport/isolation_port_test.go (TestPort_IsolationAlterTable1),
docs/design/0118-0047-* + README index; fix_plan; D-002 CSV rationale + regen md.

Gates run: TestPort_IsolationAlterTable1 strict PASS (170 perms); sibling
AlterTable2/AlterTable3 strict PASS; parser+executor units PASS; go vet clean;
ralph-state-guard OK (auto-repaired stale completed marker); pgbench smoke =
pre-commit hook.

Next step (M0118-0008 tail): alter-table-4 (INHERITS + transactional-DDL
cross-session visibility — likely needs cross-session DDL visibility work), then
reindex-concurrently-toast (allow_system_table_mods GUC + TOAST reindex),
plpgsql-toast (COMMIT in DO), partition ATTACH/DETACH (transactional-DDL
visibility + DETACH CONCURRENTLY parse).

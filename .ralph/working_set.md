Loop #44: M0118-0008 fourteenth promotion (design 0118-0046) — `alter-table-2`
PROMOTED to pass (strict). All 48 permutations byte-for-byte vs PG 18.3.

The spec mixes `ALTER TABLE b ADD CONSTRAINT bfk FOREIGN KEY … NOT VALID` with
concurrent reads / FOR UPDATE / INSERT. Two changes:
1. Parser (`ddl.go` ADD FOREIGN KEY arm): accept the `NOT VALID` trailer in any
   order with `[NOT] DEFERRABLE [INITIALLY …]` (loop, not single if/else). New
   AST `AlterTableAction.NotValid`; new `catalog.ForeignKey.NotValid` →
   `pg_constraint.convalidated='f'`.
2. Executor (`operators_ddl.go` AlterTableAddForeignKey case): take
   `acquireDDLLockTxn(rel, ShareRowExclusiveLock)` on the altered table b
   (AlterTableGetLockLevel → AT_AddConstraint). Lock matrix then drives all
   perms: concurrent INSERT (RowExclusiveLock) conflicts → one of s1b/s2d waits;
   FOR UPDATE (RowShareLock) + plain reads proceed. No-op in autocommit /
   system catalogs (pg_dump-restore + pgbench untouched). Only table b locked
   (spec can't distinguish referenced-table a lock; s2e INSERT a always follows
   s2d INSERT b).

Files: internal/parser/ast.go (NotValid field), internal/parser/ddl.go (trailer
loop), internal/catalog/catalog.go (ForeignKey.NotValid + convalidated),
internal/executor/operators_ddl.go (lock + NotValid wiring),
internal/testport/isolation_port_test.go (TestPort_IsolationAlterTable2),
docs/design/0118-0046-* + README index; fix_plan; D-002 CSV rationale + regen md.

Gates run: TestPort_IsolationAlterTable2 strict PASS (48 perms); sibling
AlterTable3/CreateTrigger/SequenceDdl strict PASS; parser/catalog/executor units
PASS; go vet clean; ralph-state-guard OK; pgbench smoke = pre-commit hook.

Next step (M0118-0008 tail): alter-table-1 (closest — same FK ADD NOT VALID
now parses, adds `ALTER TABLE … VALIDATE CONSTRAINT name` parse +
ShareUpdateExclusiveLock level; ~140 perms). Then alter-table-4 (INHERITS +
transactional-DDL cross-session visibility), reindex-concurrently-toast
(allow_system_table_mods GUC + TOAST reindex), plpgsql-toast (COMMIT in DO),
partition ATTACH/DETACH (transactional-DDL visibility + DETACH CONCURRENTLY parse).

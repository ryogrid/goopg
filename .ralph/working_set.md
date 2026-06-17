(idle — nothing in flight)

Last landed: DU-002 slice 128 (loop #92) — anonymous table-level CHECK with
NO INHERIT (`CREATE TABLE t (..., CHECK (expr) NO INHERIT)`) now re-emits the
` NO INHERIT` suffix on dump. **Real production fix:** slice 127's auto-naming
kept only the aggregate `CreateTableStmt.TableHasNoInheritCheck` bool and
DISCARDED the per-check flag, so the constraint stored NoInherit=false and the
dump produced a plain *inheritable* CHECK (pg_get_constraintdef dropped
` NO INHERIT`; pg_constraint.connoinherit reported 'f') — a re-loaded dump would
wrongly propagate the constraint to children. The deparse path already appended
the suffix when the flag was set (slice 127); gap was purely the lost flag.
Fix threads it end-to-end:
  - parser/ast.go: TableCheckNoInherit []bool parallel to TableChecks
  - parser/ddl.go: both anonymous-CHECK parse sites append one flag each
  - catalog/catalog.go: AddCheckWithNoInherit (AddCheck delegates false);
    pg_constraint CHECK VirtualRow connoinherit from nc.NoInherit (was 'f')
  - executor/operators_ddl.go: TableChecks loop passes TableCheckNoInherit[i]
  chk2 (y integer, CHECK (y > 0) NO INHERIT) → CONSTRAINT chk2_y_check
  CHECK ((y > 0)) NO INHERIT. Verified byte-identical vs real pg_dump 18.3
  (reference /tmp/du128_pgdata).
Committed + pushed (see git log).

Files: internal/parser/ast.go, internal/parser/ddl.go,
internal/catalog/catalog.go, internal/executor/operators_ddl.go,
internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/fix_plan.md.

Next direction (slice 129): a NAMED table-level NO-INHERIT check
(`CONSTRAINT c CHECK (...) NO INHERIT`) — the analog gap: PartitionCheckConstraint
(TableNamedChecks) lacks a NoInherit field, so named NO-INHERIT checks still drop
the suffix. OR a UNIQUE constraint with an INCLUDE column, OR a table+VIEW
dependency-ordering case (verify topological emission ORDER).

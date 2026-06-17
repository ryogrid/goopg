(idle — nothing in flight)

Last landed: DU-002 slice 129 (loop #93) — a NAMED table-level CHECK with
NO INHERIT (`CONSTRAINT c CHECK (expr) NO INHERIT`) now re-emits the
` NO INHERIT` suffix on dump. The named analog of slice 128's anonymous fix:
`PartitionCheckConstraint` (CreateTableStmt.TableNamedChecks) had NO NoInherit
field, and the executor stored named checks via AddCheck (NoInherit=false), so
the named NO-INHERIT check dumped as a plain *inheritable* CHECK
(pg_get_constraintdef dropped the suffix; pg_constraint.connoinherit='f') — the
identical silent inheritance divergence as slice 128, for the named form.
The deparse path needed no change (both pg_get_constraintdef's CHECK branch and
the pg_constraint VirtualRow already key off NamedCheckConstraint.NoInherit,
shared by anon-auto-named and named checks). Fix:
  - parser/ast.go: PartitionCheckConstraint.NoInherit bool
  - parser/ddl.go: named-CHECK parse back-fills the flag on the just-appended
    TableNamedChecks entry once ` NO INHERIT` is consumed (append precedes the
    suffix parse, so back-fill the last element)
  - executor/operators_ddl.go: TableNamedChecks loop calls
    AddCheckWithNoInherit(nc.Name, nc.Expr, oid, nc.NoInherit) (was AddCheck)
  CREATE TABLE chk3 (z integer, CONSTRAINT chk3_pos CHECK (z > 0) NO INHERIT)
  → CONSTRAINT chk3_pos CHECK ((z > 0)) NO INHERIT.
Committed + pushed (see git log).

Next direction (slice 130): a table+VIEW dependency-ordering case (verify
topological emission ORDER — view depends on a table, must be dumped after), OR
a UNIQUE constraint with an INCLUDE column.

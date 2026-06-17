(idle — nothing in flight)

Last landed: DU-002 slice 135 (loop #100) — table-level `UNIQUE NULLS NOT
DISTINCT` *constraint* dump-fidelity round-trip (the CONSTRAINT sibling of
slice 134's CREATE INDEX surface). Parser accepted-and-DISCARDED the clause on a
table-level UNIQUE so the backing index's NullsNotDistinct stayed false → the
constraint dumped as a plain `UNIQUE (a)` (silent NULL-dedup loss). Threaded:
`CreateTableStmt.TableUniqueNullsNotDistinct []bool` (parallel to TableUniques) →
executor table-UNIQUE loop sets `catalog.Index.NullsNotDistinct` →
`buildConstraintDefString` emits ` NULLS NOT DISTINCT` BETWEEN keyword and column
list (ruleutils.c pg_get_constraintdef order — UNIQUE only, never PK; DIFFERS
from pg_get_indexdef which trails the columns). Added
`TableConstraintDef.NullsNotDistinct` for the named-constraint AST too.
Enforcement at INSERT/UPDATE DEFERRED (ledger; shares slice-134
encodeIndexKeyFromCols path).
Files: internal/parser/ast.go, internal/parser/ddl.go,
internal/executor/operators_ddl.go, internal/executor/expr.go,
internal/parser/ddl_test.go, internal/executor/constraintdef_nnd_test.go (NEW),
internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/deferral_ledger.md.
Verified: TestParseTableUniqueNullsNotDistinct PASS;
TestBuildConstraintDefNullsNotDistinct PASS; TestPort_PgDumpConnectionSetup PASS
(2.50s); parser+catalog+executor pkgs PASS. Committed + pushed.

Next direction (slice 136): enforcement of the slice-134/135 NULLS-equal
semantics at INSERT/UPDATE (encodeIndexKeyFromCols NULL-sentinel encoding, gated
on idx.NullsNotDistinct, consistent across insert-maintain / unique-check /
index-scan probe), OR a `UNIQUE NULLS NOT DISTINCT` inline-on-column constraint
dump surface, OR an exclusion-constraint (`EXCLUDE USING gist`) dump surface.

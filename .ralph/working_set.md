(idle — nothing in flight)

Loop #96 COMPLETE: M0119-0004 DU-002 slice 365 — a view's `WITH [CASCADED|LOCAL]
CHECK OPTION` clause now round-trips through real pg_dump 18.3 as the
`\n  WITH <MODE> CHECK OPTION;` suffix after the view body.

How: PG stores the clause as the `check_option=<mode>` pg_class.reloption
(view.c). pg_dump's getTables (pg_dump.c:7158) strips the marker from the
reloptions array via array_remove (already handled, slice 5) AND derives
CASCADED/LOCAL from a `'check_option=cascaded' = ANY(c.reloptions)` CASE column;
dumpTableSchema (pg_dump.c:16982) appends the suffix. So **no pg_dump-query
change** was needed — only catalog plumbing to put `check_option=<mode>` into the
view's reloptions array.

Files:
- internal/parser/ast.go (+CreateViewStmt.CheckOption)
- internal/parser/ddl.go (parseCreateViewTail: capture cascaded/local; bare → cascaded)
- internal/catalog/catalog.go (+Table.CheckOption field; reloptions builder appends check_option=<mode>)
- internal/executor/operators_ddl.go (execCreateView: vt.CheckOption = s.CheckOption)
- tests: internal/parser/view_test.go (TestParseCreateViewCheckOption),
  internal/executor/operators_fillfactor_reloptions_test.go
  (TestViewCheckOptionSurfacesInPgClassReloptions),
  internal/testport/pgdump_connsetup_test.go (slice-365 vchk/vchk_local fixture+asserts)
- docs/design/0110-0001-pg-dump-tap-port.md (Slice 365)
- .ralph/deferral_ledger.md (slice-365 row), .ralph/fix_plan.md (loop #96 note)

Gates run: parser+catalog+executor unit suites PASS; TestPort_PgDumpConnectionSetup
PASS (5.96s, byte-identical vs real PG 18.3); go vet clean; pgbench smoke=pre-commit.

Deferred (ledger): CHECK OPTION enforcement on INSERT/UPDATE (PG rewriteHandler.c);
the `WITH (check_option=...)` reloption FORM before AS + security_barrier/
security_invoker still parsed-and-ignored; restart persistence (in-memory only).

Next loop: pick a fresh M0119-0004 pg_dump slice. Candidates: view CHECK OPTION
ENFORCEMENT (query-rewrite, larger); the `WITH (check_option=...)`/security_barrier
view-reloption capture form; the long-open typed STRING-literal cast in an operator
arg (`name || '_x'` → `'_x'::text`, needs operator-arg type resolution); or
action-command CREATE RULE (milestone-sized reverse-compiler).

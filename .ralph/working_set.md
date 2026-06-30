(idle — nothing in flight)

Loop #97 COMPLETE: M0119-0004 DU-002 slice 366 — a view's pre-AS
`WITH (security_barrier=<bool>)` storage option now round-trips through real
pg_dump 18.3 as the `WITH (security_barrier='true')` clause after the view name.

How: PG stores it as the `security_barrier=<bool>` pg_class.reloption. Unlike
check_option, pg_dump's getTables KEEPS it in the reloptions array (array_remove
strips only check_option=*); dumpTableSchema (pg_dump.c:16971-16976) re-emits it
via appendReloptionsArray (value single-quoted because fmtId('true')!='true').
So **no pg_dump-query change** — only catalog plumbing to put
`security_barrier=<bool>` into the view's reloptions array.

Files:
- internal/parser/ast.go (+CreateViewStmt.SecurityBarrier *bool)
- internal/parser/ddl.go (parseCreateViewTail pre-AS WITH loop captures
  security_barrier; +parseBoolReloptionValue helper)
- internal/catalog/catalog.go (+Table.SecurityBarrier/SecurityBarrierSet;
  reloptions builder appends security_barrier=<bool> before check_option)
- internal/executor/operators_ddl.go (execCreateView: vt.SecurityBarrier=*s.SecurityBarrier)
- tests: internal/parser/view_test.go (TestParseCreateViewSecurityBarrier),
  internal/executor/operators_fillfactor_reloptions_test.go
  (TestViewSecurityBarrierSurfacesInPgClassReloptions),
  internal/testport/pgdump_connsetup_test.go (slice-366 vsecbar fixture+assert)
- docs/design/0110-0001-pg-dump-tap-port.md (Slice 366)
- .ralph/deferral_ledger.md (slice-366 row), .ralph/fix_plan.md (loop #97 note)

Gates run: parser+catalog+executor unit suites PASS; TestPort_PgDumpConnectionSetup
PASS (5.88s, byte-identical vs real PG 18.3); go vet clean; pgbench smoke=pre-commit hook.

Deferred (ledger): security_barrier has NO runtime effect (planner qual-fencing
against leaky operators not implemented); security_invoker + the
`WITH (check_option=...)` reloption FORM stay parsed-and-ignored; restart
persistence (in-memory only).

Next loop: pick a fresh M0119-0004 pg_dump slice. Candidates: view
`security_invoker` reloption (sibling of this slice); view CHECK OPTION
ENFORCEMENT (query-rewrite, larger); the long-open typed STRING-literal cast in an
operator arg (`name || '_x'` → `'_x'::text`, needs operator-arg type resolution);
or action-command CREATE RULE (milestone-sized reverse-compiler).

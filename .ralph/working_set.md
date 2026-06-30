(idle — nothing in flight)

Loop #98 COMPLETE: M0119-0004 DU-002 slice 367 — a view's pre-AS
`WITH (security_invoker=<bool>)` storage option now round-trips through real
pg_dump 18.3 as the `WITH (security_invoker='true')` clause after the view name.
The sibling of slice 366 (security_barrier).

How: PG stores it as the `security_invoker=<bool>` pg_class.reloption. Like
security_barrier, pg_dump's getTables KEEPS it in the reloptions array
(array_remove strips only check_option=*); dumpTableSchema re-emits it via
appendReloptionsArray (value single-quoted because fmtId('true')!='true').
So **no pg_dump-query change** — only catalog plumbing.

Files:
- internal/parser/ast.go (+CreateViewStmt.SecurityInvoker *bool)
- internal/parser/ddl.go (parseCreateViewTail WITH loop captures security_invoker)
- internal/catalog/catalog.go (+Table.SecurityInvoker/SecurityInvokerSet;
  reloptions builder appends security_invoker after security_barrier, before check_option)
- internal/executor/operators_ddl.go (execCreateView: vt.SecurityInvoker=*s.SecurityInvoker)
- tests: internal/parser/view_test.go (TestParseCreateViewSecurityInvoker),
  internal/executor/operators_fillfactor_reloptions_test.go
  (TestViewSecurityInvokerSurfacesInPgClassReloptions),
  internal/testport/pgdump_connsetup_test.go (slice-367 vsecinv fixture+assert)
- docs/design/0110-0001-pg-dump-tap-port.md (Slice 367)
- .ralph/deferral_ledger.md (slice-367 row), .ralph/fix_plan.md (loop #98 note)

Gates run: parser+executor unit suites PASS; TestPort_PgDumpConnectionSetup
PASS (5.65s, byte-identical vs real PG 18.3); go build clean; pgbench smoke=pre-commit hook.

Deferred (ledger): security_invoker has NO runtime effect (invoking-vs-owner
permission model not implemented); the `WITH (check_option=...)` reloption FORM
stays parsed-and-ignored; restart persistence (in-memory only).

Next loop: pick a fresh M0119-0004 pg_dump slice. Candidates: view CHECK OPTION
ENFORCEMENT (query-rewrite, larger); the long-open typed STRING-literal cast in an
operator arg (`name || '_x'` → `'_x'::text`, needs operator-arg type resolution);
or action-command CREATE RULE (milestone-sized reverse-compiler).

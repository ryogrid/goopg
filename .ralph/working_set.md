Task: M0110-0001 — Port pg_dump TAP tests. Landed the CLI-only tier
(001_basic); 002–010 deferred. (idle — nothing in flight on this task)

Files (committed this loop):
- internal/testport/pgdump_port_test.go (NEW) — TestPort_PgDump001Basic +
  helpers programHelpOk/programVersionOk/programOptionsHandlingOk/
  commandFailsContaining (mirror PostgreSQL::Test::Utils).
- docs/test-port/postgres-oracle-port-status.csv — DU-001 (port) added,
  E-002 rationale narrowed to the deferred remainder.
- docs/test-port/postgres-oracle-port-status.md — regenerated.
- docs/design/0110-0001-pg-dump-tap-port.md (NEW) + README index row.
- .ralph/fix_plan.md (M0110-0001 progress), .ralph/deferral_ledger.md.

Key facts learned this loop:
- 001_basic.pl is PURE CLI option-handling ("Doesn't require a PG instance");
  it drives upstream pg_dump/pg_restore/pg_dumpall binaries in
  postgres/local_install/bin (reused unchanged), no goopg server involved.
- HAVE_LIBZ branch reproduced by probing `pg_dump -Z 15` behaviour, not config.
- The ~771 lines of OTHER uncommitted work in the tree (gen_override, lockrows,
  catalog, planner — M0100-0010/M0100-0005/M0103-0008 tags) are SEPARATE
  in-flight features deliberately left for their owning task (fix_plan lines
  326–332, confirmed loops #5/#6/#15). DO NOT touch/commit them. Build PASS.
- The other active `claude` session (pid ~2177381) is in /home/ryo/work/
  tetetetennis — a DIFFERENT project, not goopg. No concurrent goopg editing.

Next step: M0110-0001 resume = port 002_pg_dump (schema dump) once catalog-view
parity lands; then 003 (dump+restore round-trip). Or pick next topmost
fix_plan item (M0110-0002 pg_waldump / M0110-0004 pg_resetwal are also
isolated to testport).

Gates run: go vet ./internal/testport PASS; gofmt clean;
go test -run TestPort_PgDump001Basic ./internal/testport PASS;
gen-oracle-port-status regenerated OK; go build ./... PASS.

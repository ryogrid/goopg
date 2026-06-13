Task: M0110-0002 — Port pg_waldump TAP tests. Landed the CLI-only tier of
001_basic.pl; server tier + 002_save_fullpage.pl deferred. (idle — nothing in
flight on this task.)

Files (this loop):
- internal/testport/pgwaldump_port_test.go (NEW) — TestPort_PgWaldump001Basic +
  commandLikeMatching helper (mirror command_like). Reuses programHelpOk/
  programVersionOk/programOptionsHandlingOk/commandFailsContaining from
  pgdump_port_test.go (same package).
- docs/test-port/postgres-oracle-port-status.csv — WD-001 (port) + WD-002
  (defer) added below W-001.
- docs/test-port/postgres-oracle-port-status.md — regenerated.
- docs/design/0110-0002-pg-waldump-tap-port.md (NEW) + README index row.
- .ralph/fix_plan.md (M0110-0002 progress), .ralph/deferral_ledger.md.

Key facts learned this loop:
- 001_basic.pl has TWO tiers: (1) pure CLI option-handling (lines 10-77, no
  server) — PORTED; (2) server tier (lines 80-323) running DDL over
  heap/btree/hash/gin/gist/spgist/brin + tablespaces + logical messages then
  asserting per-rmgr/-relation/-block filtering — DEFERRED (goopg lacks
  hash/gin/gist/spgist/brin AMs).
- Upstream binaries in postgres/local_install/bin have rpath baked in — run
  fine WITHOUT LD_LIBRARY_PATH (runTool needs no env tweak).
- W-001 (TestPort_WALPgWaldumpCompat) already covers goopg WAL-format
  readability by upstream pg_waldump for supported record types — so deferring
  the server tier leaves no compat gap for implemented features.
- PG 18.3 --rmgr=list output pinned in test (22 rmgrs, XLOG..LogicalMessage).
- This is a test-only port (internal/testport, drives upstream binary); no
  executor/planner/catalog code touched → TPC-H spotcheck gate not applicable.

Next step: pick next topmost fix_plan item. Candidates still isolated to
testport: M0110-0003 (pg_amcheck — all UNIMPLEMENTED, blocked on verify_heapam
SRF + opclass catalog), M0110-0004 (pg_resetwal 001_basic — control-file
parse, depends on M0106 pg_control byte-compat). Or M0110-0001 resume
(pg_dump 002_pg_dump) once catalog-view parity lands.

Gates run: gofmt clean; go vet ./internal/testport PASS;
go test -run TestPort_PgWaldump001Basic ./internal/testport PASS;
gen-oracle-port-status regenerated OK; go build ./... PASS.

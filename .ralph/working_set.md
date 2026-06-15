Task: M0110-0003 (AC-002 amcheck) — loop #3. LANDED gap #7b (non-existent role
rejection at connection handshake). Remaining: gap #7 (a) + (c), then clog
XidStatusFunc wiring, then AC-002…AC-005 TAP port + CSV flip.

=== WHAT LANDED (this loop) — committed on align-data-structure-with-pg ===
gap #7b: a connection whose role is absent from goopg's runtime role authority
is now rejected after authentication with FATAL 28000 `role "%s" does not exist`,
mirroring PG InitializeSessionUserId (utils/init/miscinit.c).
- Authority = Server.roles (in-memory, seeded `postgres`, maintained by
  CREATE/DROP ROLE) PLUS any UserStore (pg_auth) account.
- The check is in the connection handshake right AFTER checkAuth (PG establishes
  role before database), gated on `s.cfg.Catalog.(databaseRegistry)` + non-
  replication — IDENTICAL gate to the gap #3 database-existence check, so
  catalog-less wire-protocol unit tests and physical walsenders are unaffected.
- The trust-auth path previously admitted any role (exit 0); password/SCRAM
  already rejected unknown users in checkAuth. This closes the trust hole.
- One test helper had to change: query_test.go dialAndComplete connected as
  user "u" against real-catalog servers; now "postgres" (always seeded). No
  caller asserts the username. (server_test.go's "u"/"alice" tests use
  catalog-less servers → unaffected.)

Files: internal/server/server.go (handshake role check),
internal/server/role_exists_test.go (NEW: TestConnectNonexistentRoleRejected +
TestConnectSeededRoleAccepted), internal/server/query_test.go (helper user),
internal/testport/pgamcheck002_port_test.go (gap #7b now a regression guard;
self-skip re-keyed to gap #7a), docs/design/0110-0008-amcheck-sql-surface-plan.md.

Key symbols: Server.serveConn handshake (server.go ~line 705), roleExists,
databaseRegistry gate, writeFatal, sqlstate.InvalidAuthorizationSpecification.

Gates run: go test ./internal/server PASS (full suite); cmd/goopg PASS;
TestPort_PgAmcheck002Nonesuch SKIPs cleanly on gap #7 (a)/(c) (was FAIL after
un-skip); gofmt+vet clean; make ralph-state-guard OK. TPC-H spotcheck NOT run —
change is connection-admission only, touches no executor/planner/codec path, so
row counts cannot change.

=== NEXT STEP (resume point) — AC-002 gap #7 (a) then (c), each its own loop ===
(a) database-name pattern resolution: pg_amcheck resolves each --database arg as
    a connectable-name PATTERN and errors `no connectable databases to check
    matching "<pat>"` when it resolves to nothing (multi-pattern / substring /
    superstring). goopg silently accepts the pattern and exits 0. START HERE.
    This is pg_amcheck CLIENT behavior driven by goopg's database-list bootstrap
    query result — confirm whether the fix is goopg-side (query output shape) or
    just that goopg returns the wrong db set. Inspect pg_amcheck.c
    compile_database_list + the VALUES-CTE pattern query against goopg.
(c) per-database amcheck-installed detection + template1/template0 amcheck
    skip: `skipping database "template1": amcheck is not installed`.
After gap #7: clog XidStatusFunc tier wiring, then AC-002…AC-005 TAP port + CSV
flip (docs/test-port/postgres-oracle-port-status.csv).

=== CONTEXT ===
Main tree is clean — commit engine work directly on align-data-structure-with-pg.
.ralph/fix_plan.md is churned by the driver — progress recorded here + ledger.

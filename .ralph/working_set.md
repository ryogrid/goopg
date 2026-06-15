Task: M0110-0001 — pg_dump connection-setup compatibility (enabler for DU-002+).
COMPLETE + committed (loop #22). Next loop starts clean unless picking up the
resume candidate below.

=== WHAT LANDED (loop #22) ===
An empirical probe (real `pg_dump --no-sync postgres` vs a live goopg server)
showed pg_dump aborting in setup_connection() BEFORE any catalog query. Closed
two gap classes so it now completes the handshake and reaches its first catalog
query:
- 3 unregistered GUCs added as accepted no-ops: synchronize_seqscans,
  transaction_timeout (PG17+), row_security (config/defaults.go + sample; boot
  on/0/on per guc_tables.c).
- `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY`: the server
  simple-query string fast-path mis-routed `SET TRANSACTION …` to handleSet
  (`unrecognized configuration parameter "TRANSACTION"`). New query.go case
  routes `SET [LOCAL|SESSION] TRANSACTION …` / `SET SESSION CHARACTERISTICS …`
  to the executor (existing SetTransactionStmt, M0096-0002). Parser's
  transaction-mode loop now consumes the comma (it stopped at it).

Files:
- internal/config/defaults.go (3 GUCs), internal/config/postgresql.conf.sample
- internal/server/query.go (SET TRANSACTION routing case)
- internal/parser/parser.go (parseSet transaction-mode comma)
- tests: internal/testport/pgdump_connsetup_test.go
  (TestPort_PgDumpConnectionSetup), internal/config/pgdump_gucs_test.go,
  internal/parser/set_transaction_test.go
- docs/design/0110-0001-pg-dump-tap-port.md (Connection-setup section)
- .ralph/fix_plan.md (loop #22 PROGRESS), .ralph/deferral_ledger.md

Key symbols: BuildDefaultRegistry GUC registrations; handleQuery SET-dispatch
switch (query.go); parseSet KwTransaction loop; SetTransactionStmt/setTransactionOp.

Gates: build/gofmt/vet clean; config + parser + server suites PASS; pg_dump 001
+ new connection-setup test PASS. TPC-H spotcheck N/A (no query-path change;
additive GUCs + SET-dispatch routing only).

=== NEXT STEP (resume candidate) ===
DU-002+ catalog-view parity. PRECISE first blocker: getRoles
`SELECT oid, rolname FROM pg_catalog.pg_roles ORDER BY 1` fails — goopg's
pg_roles view lacks an `oid` column. Add it, then walk setup_connection's query
order (getTablespaces/getNamespaces/getTypes/getTables…). Each catalog query is
a small fix but there are many — bound to one logical group per loop.
Other open (all larger): M0110-0003 AC-003 003_check feature tiers (index AMs /
box/int4range/int4[] / EXTERNAL TOAST / multi-DB), 005_opclass_damage (needs
runtime pg_amproc write goopg lacks); M0110-0002 002_save_fullpage (PG-format
heap WAL FPI, high blast radius); M0095-0003 recvlogical (logical decoding);
M0117-0006/7/8 (Effort-L CLOG rewrites, dedicated session).

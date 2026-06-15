Task: M0110-0001 / DU-002 — pg_dump catalog-view parity, slice 1: pg_roles.oid.
COMPLETE + committed (loop #23). Next loop resumes at the acldefault() blocker.

=== WHAT LANDED (loop #23) ===
pg_dump's collectRoleNames issues
  SELECT oid, rolname FROM pg_catalog.pg_roles ORDER BY 1   (pg_dump.c:10548)
to build its role-oid → name map. goopg's pg_roles virtual view lacked an oid
column, aborting the dump right after connection setup. Added oid at ordinal 0
carrying OID 10 (BOOTSTRAP_SUPERUSERID, the postgres superuser; pg_authid.dat),
shifting rolname/rolsuper/rolcanlogin to ordinals 1-3.

Empirical probe (real pg_dump --no-sync postgres vs live goopg) confirms pg_dump
now passes collectRoleNames and advances to getNamespaces.

Files:
- internal/catalog/catalog.go (pgRoles Columns + VirtualRows)
- internal/catalog/catalog_test.go (TestPgCatalogBootstrapViews: oid assertions)
- internal/testport/pgdump_connsetup_test.go (next-blocker marker comment)
- docs/design/0110-0001-pg-dump-tap-port.md (catalog-parity slice list)

Key symbols: BuildDefaultRegistry pgRoles (catalog.go); collectRoleNames
(pg_dump.c); TestPort_PgDumpConnectionSetup (logs remaining gap).

Gates: build/gofmt/vet clean; catalog + testport (TestPgCatalog*, TestSyntax_
Catalog_PgRoles, TestPort_PgDump*) PASS. No query-path change → TPC-H N/A.

=== NEXT STEP (resume candidate) ===
DU-002 slice 2: getNamespaces. pg_dump runs
  SELECT n.tableoid, n.oid, n.nspname, n.nspowner, n.nspacl,
         acldefault('n', n.nspowner) AS acldefault FROM pg_namespace n
→ fails: "function acldefault does not exist". Need: acldefault() builtin
(2-arg: type-char, owner-oid → aclitem[]) + pg_namespace columns tableoid,
nspowner, nspacl. Then walk getTypes/getTables/etc. one group per loop.
Other open (larger): M0110-0003 AC-003 003_check feature tiers; M0110-0002
002_save_fullpage (PG-format heap WAL FPI); M0095-0003 recvlogical; M0117-0006/7/8
(Effort-L CLOG rewrites, dedicated session).

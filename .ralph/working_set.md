(idle — nothing in flight)

Loop #14 COMPLETE: M0119-0004 DU-002 slice 375 — `CREATE FOREIGN DATA WRAPPER
<name>` now round-trips through pg_dump (PRODUCTION fix).

Bug: goopg parsed CREATE FDW as a CompatNoopStmt tracked only in the bare-name
compat set, and pg_foreign_data_wrapper.VirtualRows was hard-wired to 0 rows, so
a created FDW silently vanished from the dump. Real pg_dump's
getForeignDataWrappers reads ALL pg_foreign_data_wrapper rows and
dumpForeignDataWrapper emits `CREATE FOREIGN DATA WRAPPER <name>;` +
`ALTER FOREIGN DATA WRAPPER <name> OWNER TO postgres;`.

Fix (3 parts):
- catalog: dedicated FDW registry (ForeignDataWrapper{Name,OID,Owner} keyed by
  name; stable OID via new allocOIDLocked); pg_foreign_data_wrapper.VirtualRows
  surfaces each (fdwhandler/fdwvalidator=0, acl/options NULL, owner=10).
- executor: RegisterForeignDataWrapper / DropForeignDataWrapper replace the
  bare compat-object register/drop.
- executor cast: `<oid>::regproc` now renders InvalidOid(0) as '-' (regprocout)
  so dumpForeignDataWrapper suppresses the HANDLER/VALIDATOR clause (a bare "0"
  would have emitted ` HANDLER 0`). General PG-correct, not FDW-specific.

Files: internal/catalog/catalog.go (registry + VirtualRows + allocOIDLocked),
internal/catalog/fdw_registry_test.go (new),
internal/executor/operators_ddl.go (register/drop wiring),
internal/executor/expr.go (regproc 0→'-'),
internal/testport/pgdump_connsetup_test.go (goopg_fdw fixture + asserts),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 375), ledger.

Gates: TestForeignDataWrapperRegistry + TestPort_PgDumpConnectionSetup PASS
(byte-identical vs real pg_dump 18.3, ref /tmp/du_fdw_ref); catalog/executor/
parser suites PASS; build/vet/gofmt(my lines) clean. pgbench smoke = pre-commit
hook. No TPC-H (metadata-only virtual-catalog change; FDWs absent from TPC-H).

Deferred (ledger): HANDLER/VALIDATOR + OPTIONS discarded by parser; FDWs
in-memory only; CREATE SERVER / USER MAPPING still not dumped.

Next loop: pick a fresh M0119-0004 pg_dump slice via empirical probe. Candidates
surfaced this loop but NOT yet done (silently created, not dumped): range types
(CREATE TYPE AS RANGE — needs pg_range+pg_opclass+multirange, large), aggregates,
operators, text-search configs, and CREATE COLLATION (parser doesn't even accept).

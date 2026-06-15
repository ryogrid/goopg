Task: M0110-0003 (AC-003 pg_amcheck) — loop #7. LANDED the page-structural heap
tier of 004_verify_heapam.pl as TestPort_PgAmcheck004VerifyHeapam, the first
end-to-end on-disk-corruption → pg_amcheck reproduction against a live goopg
cluster. Committed on align-data-structure-with-pg.

=== WHAT LANDED (this loop) ===
internal/testport/pgamcheck004_port_test.go (NEW): drives real pg_amcheck vs a
live goopg cluster, mirroring upstream stop→seek/overwrite→restart:
- init with --no-data-checksums (upstream no_data_checksums=>1). goopg now
  DEFAULTS data checksums ON (cmd/goopg/main.go:183) — with them on, overwriting
  page bytes trips the storage-manager checksum verify ("invalid page in block 0
  … checksum verification failed") before verify_heapam sees the damage.
- locate heap file by globbing base/*/<reloid>: goopg's storage dbOid (5 for
  postgres) ≠ pg_database.oid (16384). reloid filename = pg_class.oid (no
  separate relfilenode in v0; catalog RelFileNode uses table.OID).
- corrupt: first line pointer (slot 1) is the 4-byte LE ItemId at offset 24,
  packed offset(15)|flags<<15|length<<17; set length=0x7FFF so lp_off+lp_len >
  BLCKSZ → engine emits verbatim "line pointer to page offset N with length
  32767 ends beyond maximum page offset 8192".
- re-CREATE EXTENSION amcheck AFTER restart: install is runtime-only (gap #7c
  per-db pg_extension scoping) and does NOT survive a server restart.
- assert pg_amcheck exit 2 + report on stdout (pg_amcheck prints corruption to
  STDOUT, not stderr).

Files: internal/testport/pgamcheck004_port_test.go (new), CSV AC-003 rationale,
docs/design/0110-0003-pg-amcheck-tap-port.md (004 section + AC-002 done),
docs/design/README.md (0110-0003 row), .ralph/fix_plan.md, deferral_ledger.md.

Gates: gofmt clean; go vet ./internal/testport clean; TestPort_PgAmcheck001/002/
004 all PASS; internal/amcheck + internal/mvcc PASS. gen-oracle-port-status
regenerated the .md. No TPC-H spotcheck (test-only, new file; no planner/codec/
executor row path touched).

=== NEXT STEP (resume point) — AC-003 remainder ===
Two tests remain to promote AC-003 → port:
- 003_check.pl: whole-database pg_amcheck orchestration. Blocker = goopg's
  SYSTEM-CATALOG heap pages must verify cleanly through verify_heapam (upstream
  asserts an empty `pg_amcheck <db>` run before corruption). Today the 004 port
  restricts to the user table to dodge catalog-page false positives — quantify
  which catalog pages trip the engine first.
- 005_opclass_damage.pl: needs CREATE OPERATOR CLASS + pg_amproc catalog parity
  to inject a breaking sort order via UPDATE pg_amproc, then --checkunique. Large
  new catalog/DDL surface.
NOT portable faithfully: 004's MVCC/attribute + TOAST tiers (PG varatt_external
vs goopg chunk-relation TOAST).

=== CONTEXT ===
Main tree clean (foreign gen-column WIP that blocked loops #51–65 is GONE).
Commit engine work directly on align-data-structure-with-pg.

Task: M0110-0003 AC-003 enabler (loop #20) — CREATE SCHEMA durability across
restart. COMPLETE + committed. (idle-ish — see "Next step" for candidates.)

=== WHAT LANDED (loop #20) ===
`CREATE/DROP SCHEMA` now survives a server restart, closing the recurring
003_check blocker (a `--schema s1` run clean pre-restart reported "no relations
to check" post-restart because schema registration was in-memory-only). Chose
the CREATE/DROP DATABASE WAL-record mechanism (M0054-0001) over the pg_class
heap-append — schemas have no per-schema on-disk file namespace, exactly like
databases. ZERO change to the high-risk catalog heap-write/load subsystem.

Files:
- internal/wal/recovery.go (+ RecordKindCreateSchema=34/DropSchema=35,
  Encode/Decode{Create,Drop}Schema, applyRecord + nativeApplyRecordKindKnown)
- internal/initdb/schema_ddl_recovery.go (new; replaySchemaDDLRecords)
- internal/initdb/open.go (wire replaySchemaDDLRecords after database-DDL replay)
- internal/catalog/catalog.go (Register/UnregisterSchemaDuringRecovery)
- internal/executor/operators_ddl.go (emit at execCompatNoop "schema" + DROP SCHEMA)
- internal/server/dispatch.go (emit at parser-rejected CREATE SCHEMA branch)
- tests: internal/wal/schema_ddl_test.go,
  internal/initdb/schema_ddl_recovery_test.go,
  internal/testport/create_schema_durability_test.go (TestPort_CreateSchemaSurvivesRestart)
- docs/design/0110-0012-create-schema-wal-durability.md + README index
- .ralph/fix_plan.md (loop #20 PROGRESS)

Key symbols: RecordKindCreateSchema/DropSchema, EncodeCreateSchema(name,oid),
replaySchemaDDLRecords, RegisterSchemaDuringRecovery, execCompatNoop case "schema".

Gates: build + vet clean; gofmt clean on all edited files. PASS: wal, catalog,
initdb, executor, server suites; testport amcheck alltables/003 (no regression);
new wal/initdb/e2e schema tests. TPC-H spotcheck N/A (no query path touched;
only new DDL WAL records + catalog registry hooks).

=== OUT OF SCOPE (documented) ===
- Non-transactional (matches CREATE DATABASE precedent).
- PG-standby visibility: NO pg_namespace heap row / 2684/2685 index maintenance.
  goopg resolves schemas via the in-memory registry, not the index. A follow-up
  could mirror syncTableToCatalogHeap if PG-standby user-schema visibility is needed.
- User-schema tables: syncTableToCatalogHeap's namespaceOIDForSchema still maps
  only public/pg_catalog (user-schema RelNamespace on disk is a separate gap).

=== NEXT STEP (resume) ===
Commit done. Candidate next tasks (all larger): remaining AC-003 003_check tiers
need feature work (hash/gist/gin/brin/spgist AMs, box/int4range/int4[] types,
STORAGE EXTERNAL TOAST, multi-DB orchestration); 005_opclass_damage (CREATE
OPERATOR CLASS + pg_amproc parity); M0095-0003 recvlogical (030, logical
decoding); M0110-0001 pg_dump 002 / M0110-0002 pg_waldump 002 (catalog parity).

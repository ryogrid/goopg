(idle — nothing in flight)

Last landed: DU-002 slice 121 (loop #85) — SERIAL/BIGSERIAL columns dump
byte-identically. The AUTO ('a') counterpart to slice 120's IDENTITY ('i'), and
the first object whose default pg_dump forces into a SEPARATE `ALTER TABLE …
SET DEFAULT nextval(…)` via repairTableAttrDefMultiLoop (the owned-sequence ↔
table dependency loop). Five coupled changes:
  1. createSeqCatalogTable now runs for serial too (was identity-only) → sequence
     discoverable in pg_class relkind='S'; `AS integer` flows from slice-117 path.
  2. buildUserPGAttributeRow remaps atttypid serial→int4/bigserial→int8/
     smallserial→int2 (catalog type-name stays the serial spelling; INSERT
     auto-gen keys on it).
  3. atthasdef=true for serial (catalog.IsSerialTypeName).
  4. NEW catalog.attrDefRowsLocked — shared deterministic (sorted-key) builder
     feeding BOTH pg_attrdef view AND dependVirtualRows; emits adbin=
     nextval('<schema>.<tbl>_<col>_seq'::regclass) for serial cols.
  5. dependVirtualRows emits pg_depend NORMAL ('n') attrdef→sequence row using
     the SAME attrdef oid the view uses (sibling-path: pg_dump matches scanned
     pg_attrdef.oid against pg_depend.objid) → closes the loop.
Test-only fix: slice-90 empty-default guard tightened to newline-anchored
(`DEFAULT;\n`/`DEFAULT \n`) — pg_dump's new `Type: DEFAULT;` section comment was
a false positive.
Verified byte-identical vs real pg_dump 18.3 (reference captured /tmp/serialref_pgdata).
Files: internal/catalog/catalog.go (IsSerialTypeName + attrDefRowsLocked +
attrdef→seq depend), internal/executor/operators_ddl.go (createSeqCatalogTable
for serial), internal/executor/pg18_user_catalog_rows.go (atttypid remap +
atthasdef), internal/testport/pgdump_connsetup_test.go (ser_tbl/bigser_tbl
fixtures + assertions + slice-90 guard), docs/design/0110-0001-pg-dump-tap-port.md.

Next direction (slice 122): a table+sequence+VIEW dependency-ordering case (view
depends on table; pg_dump must emit CREATE TABLE before CREATE VIEW), OR a
multi-column / explicit-START serial, OR `GENERATED ALWAYS AS IDENTITY` combined
with a serial in the same table to stress the mixed deptype graph.

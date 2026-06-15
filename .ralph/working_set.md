Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 24
(pg_attribute.attstattarget) COMPLETE this loop. NOTHING in flight; next loop
starts on slice 25 (empty pg_partitioned_table virtual view).

=== DONE (loop #48) — DU-002 slice 24 ===
getTableAttrs reads `a.attstattarget`. goopg's pg_attribute already exposed
every OTHER getTableAttrs column (attstorage/attcompression/attidentity/
atthasmissing/attmissingval/attgenerated/attfdwoptions/attcollation/attislocal/
atthasdef) — so only attstattarget was missing (single-column slice, NOT the
broad-column slice prior notes predicted). PG18 = NULLABLE int2 (CATALOG_VARLEN
BKI_FORCE_NULL). Added in lockstep to 4 sibling layouts:
- internal/catalog/codec.go PGAttributeColumns (queryable schema, name resolve)
- internal/initdb/initdb.go pgAttrColDefs + pgAttributeRow (nailed heap write)
- internal/executor/pg18_user_catalog_rows.go pgAttributeColumnsPG18 +
  buildUserPGAttributeRow (user-table heap write)
APPENDED LAST (not PG18-canonical #4): goopg heap is already non-canonical and
DecodePGAttributePhysicalRow (codec.go) reads attrelid/attname/atttypid/attnum/
attnotnull/attisdropped by HARDCODED BYTE OFFSET. A trailing always-NULL column
(like existing attacl/attoptions/attfdwoptions/attmissingval) keeps every offset
valid; null bitmap 3→4 bytes stays within MAXALIGN(8) → t_hoff=32 unchanged.
SELECT resolves by name → pg_dump reads NULL → default stats target -1.
LEFT UNTOUCHED: initdb.pgAttributeAttrs (relcache-init tupdesc PG standby reads)
— already a separately-divergent 24-col layout (lists attstattarget#4+attcacheoff#8,
omits attfdwoptions/attmissingval); fully-canonical on-disk pg_attribute is a
larger PG-standby task, out of scope.
Tests: count assertions bumped 24→25 in catalog.TestPGAttributeColumnsCount,
initdb.TestBootstrappedPGAttributeRowsReadable,
initdb.TestPgAttributeRowEmitsNullForOptionalArrayColumns (added attstattarget
to NULL set). Design doc 0110-0001 slice-24 block; pgdump_connsetup_test.go
header updated (next blocker → pg_partitioned_table); fix_plan loop #48 entry.
Gates: build/gofmt/vet clean; catalog+initdb+executor suites PASS;
TestPort_PgDumpConnectionSetup PASS. tpch-spotcheck N/A (additive trailing NULL
column on a catalog only; zero existing query row-count risk — column resolved
by name, physical offsets/t_hoff unchanged).

=== NEXT STEP — DU-002 slice 25 (pg_partitioned_table empty view) ===
pg_dump now fails: `relation "pg_partitioned_table" does not exist`. Query:
`SELECT partrelid FROM pg_partitioned_table WHERE (SELECT c.oid FROM pg_opclass
c JOIN pg_am a ON c.opcmethod = a.oid WHERE opcname = 'enum_ops' AND
opcnamespace = 'pg_catalog'::regnamespace AND amname = 'hash') = ANY(partclass)`.
Add empty pg_partitioned_table virtual view (pg_partitioned_table.h, OID 3350:
partrelid oid, partstrat "char", partnatts int2, partdefid oid, partattrs
int2vector, partclass oidvector, partcollation oidvector, partexprs pg_node_tree)
in internal/catalog/catalog.go beside pg_event_trigger/pg_range. goopg surfaces
partition metadata via pg_class.relkind='p', not a separate heap, so empty view
is correct for the dump (RUN test to confirm + find next blocker empirically).

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString raw bytes), not the text
repr expandArrayDatum parses. Irrelevant to empty pg_dump views.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).

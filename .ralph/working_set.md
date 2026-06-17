(idle — nothing in flight)

Last landed: DU-002 slice 167 (loop #134) — a RANGE-partitioned table + its partition
now round-trip through pg_dump. REAL DIVERGENCE FIXED.

Root cause: every partition moving part already existed (relkind='p', relispartition,
pg_get_partkeydef, pg_inherits, pg_get_expr pass-through) EXCEPT the bound:
buildUserPGClassRow (the heap-backed pg_class row pg_dump reads) HARDCODED relpartbound
to "". So a partition child attached with an empty (invalid) FOR VALUES bound — silent
loss of the value range on restore.

Fix (catalog-metadata only, zero storage-path risk): buildUserPGClassRow derives
relpartbound from catalog.FormatPartitionBound(tbl.PartitionBounds[0]) for a partition
child (PartitionParentOID != 0); a parent keeps "" (no bound, matching PG). This is a
sibling-paths-must-agree fix — catalog.go VirtualRows already computed the same string.
FormatPartitionBound covers RANGE/LIST/HASH/DEFAULT, so all kinds are handled.

Files: internal/executor/pg18_user_catalog_rows.go (relpartbound derive),
internal/testport/pgdump_connsetup_test.go (fixture public.part PARTITION BY RANGE +
public.part_p0 partition, asserts parent PARTITION BY clause + child ATTACH-with-bound),
docs/design/0110-0001-pg-dump-tap-port.md (slice 167 section), .ralph/fix_plan.md (#134).
Verified: gofmt OK; go build ./internal/... OK; go vet ./internal/testport/ clean;
TestPort_PgDumpConnectionSetup PASS (2.43s, not skipped); executor+catalog PASS;
pgbench pre-commit smoke on commit.

Next direction (slice 168): table inheritance (INHERITS — check pg_inherits + INHERITS
clause emit), LIST/HASH partition bounds (FormatPartitionBound already covers them —
just add fixtures to lock them), multi-level partition trees, or column-level
STORAGE/COMPRESSION (needs new parser keywords KwStorage/KwCompress). Probe goopg
support before picking.

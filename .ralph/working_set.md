(idle — nothing in flight)

Last landed: DU-002 slice 166 (loop #133) — UNLOGGED tables now round-trip through
pg_dump as `CREATE UNLOGGED TABLE`. REAL DIVERGENCE FIXED.

Root cause: parser captured CreateTableStmt.Unlogged and executor stored it on
catalog.Table.Unlogged (both predate this slice), but the pg_class emitter
buildUserPGClassRow HARDCODED relpersistence to "p". So an UNLOGGED table surfaced as
permanent and dumped as plain CREATE TABLE — pg_dump (dumpTableSchema) keys the UNLOGGED
keyword solely off pg_class.relpersistence=='u'.

Fix (catalog-metadata only, zero storage-path risk): buildUserPGClassRow derives
relpersistence from tbl.Unlogged ('u'/'p'); new indexPersistence(idx) helper makes
buildUserPGClassRowForIndex inherit the table's persistence. goopg does NOT change
WAL/storage behaviour for unlogged tables (separate capability) — only the dumped DDL.
TEMP ('t') tables never reach the on-disk catalog, so only the 'u' branch is reachable.

Probed alternatives first (all heavier): column COLLATE needs pg_collation populated
(empty VirtualRows→nil stub); SET STORAGE/COMPRESSION lack parser keywords; GRANT/REVOKE
lack statement support; triggers store no body.

Files: internal/executor/pg18_user_catalog_rows.go (relpersistence + indexPersistence
helper), internal/testport/pgdump_connsetup_test.go (fixture public.ulog UNLOGGED w/ PK +
positive assertion + negative guard on foo/opt),
docs/design/0110-0001-pg-dump-tap-port.md (slice 166 section), .ralph/fix_plan.md (#133).
Verified: gofmt OK; go build ./internal/... OK; go vet clean;
TestPort_PgDumpConnectionSetup PASS (2.34s, not skipped); executor+catalog+parser PASS;
pgbench pre-commit smoke on commit.

Next direction (slice 167): remaining untested table-level attributes — table
inheritance (INHERITS), partitioning (PARTITION BY / PARTITION OF — catalog has
PartitionMethod/PartitionParentOID fields already), or column-level STORAGE/COMPRESSION
(needs new parser keywords KwStorage/KwCompress in token.go). Probe goopg support before
picking; partitioning infra is partly present (relkind='p', relispartition).

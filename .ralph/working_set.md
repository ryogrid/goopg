(idle — nothing in flight)

Last loop (#42): M0119-0004 **clustered-index round-trip in pg_dump** (DU-002
slice 320) — LANDED, committed. Real feature gap: `CLUSTER <t> USING <idx>` was
a pure no-op so the clustering selection was silently dropped on dump/restore.

Fix (mirrors REPLICA IDENTITY USING INDEX, slice 306):
- new `catalog.Index.IsClustered` (catalog.go ~1452), projected `indisclustered`
  in BOTH pg_index builders: virtual `pgIndexCatalog` (catalog.go ~5411, the one
  pg_dump's getIndexes reads) + heap `buildUserPGIndexRow`
  (pg18_user_catalog_rows.go ~925).
- `clusterOp.Next()` (operators_cluster.go): when `stmt.IndexName != ""`, resolve
  the named index in `IndexesOnTable(tbl)` (42704 if absent), set IsClustered on
  it + clear the table's other indexes (mark_index_clustered), re-sync each
  changed pg_index heap row.
- `resyncIndexReplicaIdentHeap` → renamed `resyncIndexHeapRow` (full-row rewrite
  from buildUserPGIndexRow, now shared by replica-ident + cluster paths;
  operators_ddl.go ~10555).

Dump-fidelity only (no physical heap reorder); IsClustered defaults false → zero
blast radius. pg_dump emits `ALTER TABLE <t> CLUSTER ON <idx>;` after CREATE INDEX
(dumpIndex, pg_dump.c:18141) / ADD CONSTRAINT (dumpConstraint, :18483).

Files: internal/catalog/catalog.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/operators_cluster.go, internal/executor/operators_ddl.go
(rename), internal/testport/pgdump_connsetup_test.go (slice 320 fixture+assert),
docs/design/0119-0004-cluster-roundtrip.md (+README index 0119-0004w).

Gates: DU-002 slice 320 in TestPort_PgDumpConnectionSetup (plain index=dumpIndex
path + PK index=dumpConstraint path) PASS vs real pg_dump 18.3 (~4.5s);
internal/catalog + internal/executor suites PASS; go build clean; pgbench smoke =
pre-commit hook; ralph-state-guard OK.

NEXT loop — next pg_dump getter-battery gap (uncovered, real feature gaps):
GRANT/ACL (relacl + dumpACL), CREATE RULE (pg_rewrite/pg_get_ruledef),
CREATE POLICY / ROW LEVEL SECURITY (pg_policy). Richer CLUSTER forms
(ALTER TABLE CLUSTER ON / SET WITHOUT CLUSTER) need parser support first.

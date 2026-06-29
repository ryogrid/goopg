(idle — nothing in flight)

Last loop (#43): M0119-0004 **`ALTER TABLE … CLUSTER ON` / `SET WITHOUT CLUSTER`
restore form** (DU-002 slice 321) — LANDED, committed. Closes the CLUSTER
round-trip: slice 320 made it dump *out* (`ALTER TABLE <t> CLUSTER ON <idx>;`) but
goopg couldn't parse/execute that emitted clause → couldn't restore its own dump.

Fix:
- parser ast.go: new AlterTableActionKind `AlterTableClusterOn` (+ field
  `ClusterIndexName`) and `AlterTableSetWithoutCluster`.
- parser ddl.go parseAlterTableAction: `CLUSTER ON ident` arm + `SET WITHOUT
  CLUSTER` arm (gated on post-SET token == ident "without" so it doesn't shadow
  the `SET (reloptions)` form).
- executor operators_cluster.go: extracted `markTableClusterIndex` /
  `clearTableClusterIndex` helpers from clusterOp; clusterOp now calls the former.
- executor operators_ddl.go ALTER loop: AlterTableClusterOn→markTableClusterIndex,
  AlterTableSetWithoutCluster→clearTableClusterIndex.

Dump-fidelity only; same IsClustered/indisclustered state as slice 320 → output
unchanged, zero blast radius.

Files: internal/parser/ast.go, internal/parser/ddl.go,
internal/executor/operators_cluster.go, internal/executor/operators_ddl.go,
internal/parser/alter_test.go (TestParseAlterTableClusterOn),
internal/executor/storage_ddl_test.go (TestDDLAlterTableClusterOnRoundTrip),
docs/design/0119-0004-cluster-on-restore.md (+README index 0119-0004x).

Gates: parser/executor/catalog suites PASS; slice-320 TestPort_PgDumpConnectionSetup
PASS (refactored clusterOp); go build clean; pgbench smoke = pre-commit hook.

NEXT loop — next pg_dump getter-battery gap (real feature gaps): GRANT/ACL
(relacl + dumpACL — needs GRANT parser, big), CREATE RULE (pg_rewrite +
pg_get_ruledef, big), CREATE POLICY / RLS (pg_policy). All three are multi-
component; pick one and scope a contained first slice.

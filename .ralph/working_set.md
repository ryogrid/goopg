Loop #48 COMPLETE: M0118-0009 — `intra-grant-inplace` enabler (design 0118-0109).
Committing + pushing. NOT a promotion (perm 1 only; spec stays defer).

What landed (low blast radius, parser + executor xmax-wait replay):
- Permutation 1 of intra-grant-inplace.spec now byte-identical; first divergence
  advanced L17→L62. `ALTER TABLE … ADD PRIMARY KEY` (in-place relhasindex=true on
  the pg_class tuple) must `<waiting ...>` behind a concurrent uncommitted
  `GRANT SELECT ON <table>` — PG's lock for an ACL change IS the catalog tuple
  xmax. Replayed the wait (pg_class sibling of 0118-0098's pg_database case):
  - internal/parser/{ast.go,parser.go}: GRANT/REVOKE scan now resolves a table
    target into CompatNoopStmt.TableACL (helpers grantObjectName / grantNonTableClass;
    default object class is TABLE).
  - internal/catalog/catalog.go: tableACLChangeXID map[oid]xid (mutex-guarded) +
    SetTableACLChangeXID / TableACLChangeXID.
  - internal/executor/operators_ddl.go: execCompatNoop records the writer XID keyed
    by table OID for a TableACL grant; execAlterTableAddPrimaryKey calls new
    waitForTableACLChange → mvcc.WaitForXID before building the PK index.
  - internal/parser/op_compat_test.go: TestParseGrantTableACL.
  - docs/design/0118-0109-*.md + README row; fix_plan M0118-0009 entry; ledger row.

Gates (PASS): TestParseGrantTableACL; internal/parser + internal/catalog units;
non-regression TestPort_IsolationIntraGrantInplaceDb (shared GRANT/DatabaseACL
path) + TestPort_IsolationTruncateConflict (GRANT-on-table) strict; go build +
go vet + gofmt clean. pgbench smoke = pre-commit hook.

NEXT (all remaining M0118 are Effort-L distinct unbuilt subsystems):
- intra-grant-inplace EFFORT-L CORE: perms 3,4,7–11 need real pg_class ROWMARK
  locking — `SELECT relhasindex FROM pg_class … FOR NO KEY UPDATE`/`FOR UPDATE`/
  `FOR KEY SHARE` + `DELETE FROM pg_class` taking a genuine MVCC tuple lock on a
  VIRTUAL catalog row, cross-session FOR-KEY-SHARE-vs-FOR-NO-KEY-UPDATE multixact
  conflict, plus LockTuple-vs-xmax deadlock detection (perm 8 is an intentional
  deadlock). The runtime shared-catalog MVCC-tuple-lock subsystem.
- stats (pg_stat_force_next_flush + cumulative function-stats + 2PC interaction),
  prepared-transactions{,-cic} (2PC PREPARE/COMMIT PREPARED), index-only-bitmapscan
  (BitmapOr plan + EXPLAIN DECLARE CURSOR), predicate-gin/gist (GIN/GiST AM +
  int4[]/point types), deadlock-parallel (parallel workers), fk-partitioned-1/2.
Probe helper: throwaway zz_probe_test.go in internal/testport using
framework.IsolationRunner.RunAndCompare → log Status/Diff (import
internal/testutil/cluster).

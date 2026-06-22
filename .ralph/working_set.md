(idle — nothing in flight)

Loop #36: PROMOTED `truncate-conflict` (M0118-0008 tenth promotion, design 0118-0039,
first of the `*-conflict` family) — all 8 permutations byte-for-byte vs PG 18.3.

Three problems solved:
1. CREATE-ROLE batch-swallow (the working-set bug): setup `CREATE ROLE x; CREATE TABLE y`
   is one batch the parser can't parse; the recovery path handed the whole batch to
   tryHandleRoleDDL which dropped the CREATE TABLE. New splitLeadingRoleDDL +
   firstTopLevelSemicolon (role_ddl.go) peel the leading role stmt off and recurse on the
   remainder (dispatch.go); standalone role DDL untouched (DROP ROLE already parsed).
2. TRUNCATE privilege model: catalog ACL store tableACLs +
   Catalog.GrantTablePrivilege/HasTablePrivilege/DropTableACL (catalog.go); SET/RESET ROLE
   now set connTx.NonSuperuserRole (query.go); autocommit table-level GRANT recorder
   (server/grant_ddl.go, new); pre-lock check in execTruncate (operators_ddl.go):
   NonSuperuserRole!="" && !HasTablePrivilege(oid,role,"TRUNCATE") ⇒ 42501 immediately.
3. Granted autocommit TRUNCATE must WAIT for a conflicting lock: execTruncate switched
   acquireDDLLockTxn (no-op in autocommit) → acquireRelLockMaybeTransient. Preserves
   inherit-temp (its TRUNCATE is in an explicit txn → identical hold-to-commit).

Gates green: TestPort_IsolationTruncateConflict strict + -race; all sibling M0118-0008
specs (inherit-temp/create-trigger/alter-table-3/sequence-ddl/vacuum-*/reindex-*/
multiple-cic); createuser/dropuser/amcheck; catalog/parser/server/executor units;
pgbench smoke 0-failed ~15.2k TPS. Design 0118-0039 + README + CSV/MD port-status +
fix_plan updated.

Next step: `vacuum-conflict` / `cluster-conflict` need OWNERSHIP-based checks (VACUUM/
CLUSTER require relation ownership or MAINTAIN, not a grantable privilege) — extend the
0118-0039 ACL store with per-relation owner tracking (relowner is currently hardcoded
"10"). Then the rest of the deferred M0118-0008 tail (alter-table-{1,2,4}, partition
ATTACH/DETACH, reindex-toast, vacuum-no-cleanup-lock, plpgsql-toast).

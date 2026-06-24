Loop #28: **M0118-0009 temp-schema-cleanup permutation 1 PASSES byte-for-byte**
(design 0118-0091). Per-session temporary-namespace model + pg_my_temp_schema()
+ real DISCARD TEMP cleanup. COMMITTED. Spec NOT yet promoted (perm 2 deferred).

Landed:
- catalog.go: InMemory.tempNamespaces (owner "s<id>"→nsOID) + EnsureTempNamespace
  (lazy/idempotent, persists for session), TempNamespaceOID, DropTempNamespace,
  DropSessionTempObjects (owner-scoped relation drop, keeps namespace),
  tempNamespaceOIDLocked. allSchemasLocked emits pg_temp_<id> rows. pg_class
  VirtualRows renders temp rels (t.Temp && t.TempOwner!="") in pg_temp_<id> ns.
- expr.go: pg_my_temp_schema() evaluator. operators_ddl.go: both CREATE TEMP
  paths call EnsureTempNamespace. operators_utility_settings.go: DISCARD TEMP/ALL
  → DropSessionTempObjects (was a silent no-op). planner.go exprType→oid.
- Tests: catalog TestTempNamespaceLifecycle/TestDropSessionTempObjects;
  testport TestSyntax_TempSchema_MyTempSchemaAndDiscard (cluster, hard guard);
  anchor TestPort_IsolationTempSchemaCleanup (runIsoSpec, skips until full pass).

Gates run: catalog+executor+planner unit PASS; TestSyntax_TempSchema PASS;
TestPort_IsolationInheritTemp PASS (cross-session temp regression guard);
go build ./... clean; gofmt clean (the operators_utility_settings.go EOF-newline
flag is PRE-EXISTING, verified via git stash — do NOT gofmt -w, version mismatch).

Next step (perm 2, ledger 2026-06-25): (1) pg_terminate_backend evaluator
(sibling of pg_cancel_backend in expr.go ~5857); (2) IsolationRunner connection-
death rendering — self-terminating step emits "FATAL: terminating connection due
to administrator command" + "server closed the connection unexpectedly\n\tThis
probably means…", blocked peer step then "<... completed>"; (3) on-disconnect:
DropSessionTempObjects THEN DropTempNamespace THEN release session advisory locks
(ordering: s2_advisory must see clean catalog); (4) temp-type dependency cascade
— drop public uses_a_temp_type(just_give_me_a_type) when its temp rowtype drops
(else perm 2's re-run of s1_create_temp_objects fails "function already exists").
Probe first: throwaway test RunAndCompare on temp-schema-cleanup.spec → diff
currently starts at L80 (perm 2). Other M0118-0009 specs still open: horizons
(JSON -> ops + EXPLAIN json ANALYZE Heap Fetches/IOS), intra-grant-inplace{,-db}
(catalog tuple-xmax lock on virtual pg_class/pg_database), stats, prepared-
transactions{,-cic} (2PC).

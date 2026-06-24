Loop #29: **M0118-0009 temp-schema-cleanup perm-2 enabler — temp-type
dependency cascade on DISCARD TEMP** (design 0118-0092). Enabler, NOT a
promotion. COMMITTED.

Landed:
- catalog/catalog.go: `(*InMemory).SessionTempTableNames(owner) []string`
  (read-only temp-relation name list for a session; call BEFORE
  DropSessionTempObjects).
- catalog/routines.go: `(*Routines).DropRoutinesReferencingTypes(typeNames)
  []*Routine` — drops every routine whose arg/return Type.Name matches one of
  typeNames (case-insensitive; maintains byKey + byName). The cascade goopg uses
  in lieu of an OID-level pg_depend graph (temp table's implicit composite
  rowtype shares the table name).
- executor/operators_utility_settings.go: DISCARD TEMP now captures
  SessionTempTableNames(owner) → DropSessionTempObjects(owner) →
  Routines().DropRoutinesReferencingTypes(names). So perm-1's DISCARD TEMP
  cascade-drops the public function uses_a_temp_type(just_give_me_a_type), and
  perm-2's re-create of s1_create_temp_objects no longer fails "already exists".
- Test: catalog TestSessionTempTableNamesAndTypeCascade.
- docs/design/0118-0092 + README index; deferral ledger 2026-06-25.

Effect: temp-schema-cleanup.spec perm-2 first divergence advanced L80 → L88
(everything through L87 now byte-matches PG 18.3).

Gates run: internal/catalog + internal/executor units PASS;
TestSessionTempTableNamesAndTypeCascade PASS; TestDropSessionTempObjects/
TestTempNamespaceLifecycle PASS; TestSyntax_TempSchema_MyTempSchemaAndDiscard
PASS (perm-1 DISCARD guard, exercises the cascade); TestPort_IsolationInheritTemp
PASS (cross-session temp regression guard); go build ./... clean; pgbench smoke =
pre-commit hook.

Next step (perm 2 process-exit path, ledger 2026-06-25): the remaining 22 diff
lines from L88 are ALL the self-termination block. (1) pg_terminate_backend
evaluator in expr.go (~5856, sibling of pg_cancel_backend; needs a
ctx.TerminateBackend callback reaching the server's connection registry to close
the self connection); (2) IsolationRunner connection-death rendering — the
self-terminating step emits "FATAL:  terminating connection due to administrator
command" + "server closed the connection unexpectedly\n\tThis probably means the
server terminated abnormally\n\tbefore or while processing the request." then the
blocked peer step (s2_advisory) renders "<... completed>"; (3) on-disconnect
cleanup ordering — SessionTempTableNames→DropSessionTempObjects+
DropRoutinesReferencingTypes→DropTempNamespace→release session-level advisory
locks (the cascade helpers from THIS loop are the catalog half; s2_advisory must
observe a clean catalog only AFTER temp cleanup). Probe: re-run
`go test -run TestPort_IsolationTempSchemaCleanup ./internal/testport/ -v` →
diff now starts at L88. Other M0118-0009 specs still open: horizons (EXPLAIN
FORMAT json ANALYZE Heap Fetches + IOS + temp-prune horizon), intra-grant-inplace
{,-db} (FOR UPDATE on virtual pg_class/pg_database rows + GRANT tuple-xmax lock +
VACUUM FREEZE inplace wait), stats (pg_stat_* infra), prepared-transactions{,-cic}
(2PC).

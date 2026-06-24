(idle — nothing in flight)

Loop #36 COMPLETE + committed: M0118-0009 `intra-grant-inplace-db.spec` PROMOTED
`failed`→`pass` (single permutation byte-identical, strict
TestPort_IsolationIntraGrantInplaceDb). Design 0118-0098.

What landed: replays PG's pg_database-tuple-xmax serialization between an ACL
change and a concurrent in-place datfrozenxid update, WITHOUT a real heap tuple:
- internal/parser/{parser.go,ast.go}: GRANT/REVOKE no-op arm flags ON DATABASE →
  CompatNoopStmt.DatabaseACL.
- internal/catalog/catalog.go: InMemory.dbACLChangeXID (atomic.Uint32) +
  Set/Get DatabaseACLChangeXID — the in-memory stand-in for the tuple xmax.
- internal/executor/operators_ddl.go (execCompatNoop): DatabaseACL stmt
  materializes the writer XID + records it as the ACL-change xmax.
- internal/executor/operators_vacuum.go (vacuumOp.Next + waitForDatabaseACLChange):
  a database-wide VACUUM (len(vs.Targets)==0) WaitForXIDs on the marker first.
- docs/design/0118-0098 + README; CSV+coverage md regen; fix_plan + ledger.

Gates: TestPort_IsolationIntraGrantInplaceDb strict PASS; sibling
VacuumConflict/VacuumNoCleanupLock/ClusterConflict/TruncateConflict/CreateTrigger
PASS (VacuumConcurrentDrop fails identically on clean HEAD = PRE-EXISTING timing
flake on this WSL2 host, NOT a regression — it uses only targeted VACUUM/ANALYZE);
parser/catalog units PASS; executor Vacuum|Grant|CompatNoop|Freeze units PASS;
build+vet+gofmt clean. pgbench smoke = pre-commit hook.

Remaining M0118-0009 failed specs (4): intra-grant-inplace (pg_class sibling —
same xmax-wait but for ALTER TABLE ADD PRIMARY KEY behind FOR KEY SHARE on
pg_class), horizons (JSON `->` operator + EXPLAIN FORMAT json heap-fetch parity),
stats (pg_stat_force_next_flush + cumulative-stats infra),
prepared-transactions{,-cic} (2PC). Other M0118 remainders span
M0118-0002/0004/0005/0007 (distinct unbuilt subsystems). Isolation tally 107
pass / 14 failed.

NEXT candidate: intra-grant-inplace (the pg_class sibling) likely reuses this
loop's xmax-wait pattern — but the blocker is ALTER TABLE ADD PRIMARY KEY waiting
behind a FOR KEY SHARE row lock on the pg_class tuple (relhasindex inplace
update), a different lock shape. Probe-rank first.

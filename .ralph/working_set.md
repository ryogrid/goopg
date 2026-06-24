(idle — nothing in flight)

Loop #34 COMPLETE + ready to commit: M0118-0005 `ri-trigger.spec` PROMOTED
`failed`→`pass` (all 10 perms byte-identical, strict TestPort_IsolationRiTrigger).
Design 0118-0097.

What landed (three plpgsql/trigger fixes — high-blast-radius, all gated):
- internal/executor/operators_trigger.go: fireTriggers now returns
  (Row,bool,error) and PROPAGATES a trigger body RAISE (was silently swallowed —
  real correctness bug: user-trigger constraints ignored, DML committed anyway).
- Propagated at ALL ~21 call sites in operators_storage.go / operators_merge.go /
  operators_upsert.go (page-locked sites release buffer first; AFTER-trigger
  discard sites too).
- internal/plpgsql/{ast.go,parser.go}: PerformStmt.Query (raw source); parsePerform
  captures it + keeps Expr for scalar fast path. PERFORM query form runs as
  SELECT <query>.
- internal/executor/plpgsql_runtime.go: plpgsqlFrame.found; lowerPLpgSQLExpr
  resolves bare FOUND (declared vars win); execPLpgSQLEmbeddedSQL returns
  (int,error); PERFORM + SQLStmt set FOUND from last stmt row count.
- docs/design/0118-0097 + README; target-inventory.csv/md + coverage md regen;
  fix_plan M0118-0005 + deferral_ledger updated.

Gates: TestPort_IsolationRiTrigger strict PASS; trigger/DML batch
(EvalPlanQualTrigger/CreateTrigger/PartitionKeyUpdate4/Merge*/InsertConflict*/
InheritTemp/ReferentialIntegrity/FkDeadlock) PASS no regression; plpgsql/executor/
planner/server units + TestPort_PLpgSQL* PASS; regress-port DML/trigger cases no
new failure (delete PASS); build+vet clean. pgbench smoke = pre-commit hook
(pending commit). ralph-state-guard: pending.

Remaining M0118 failed specs (14, distinct subsystems): deadlock-parallel,
index-only-bitmapscan, eval-plan-qual, fk-partitioned-1/2, horizons, stats,
intra-grant-inplace{,-db}, predicate-gin/gist/hash, prepared-transactions{,-cic}.
fk-partitioned-1/2 now the only M0118-0005 remainder (ATTACH PARTITION + part-FK).

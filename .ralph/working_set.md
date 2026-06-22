Task: M0118-0006 — MERGE & INSERT ON CONFLICT output parity. COMPLETE this loop.
Whole group CLOSED (design 0118-0022). Committing.

WHAT LANDED (NO engine change — promotion only):
- All ten M0118-0006 specs already matched PG 18.3 byte-for-byte (verified via a
  throwaway zz_probe_test.go that logged RunAndCompare .Status — every one
  status=pass). The MERGE executor + EvalPlanQual recheck + ON CONFLICT arbiter +
  SSI/REPEATABLE-READ DO NOTHING semantics all landed in prior milestones.
- Promotion mechanism: new test helper `runIsoSpecStrict` (isolation_port_test.go)
  — identical to `runIsoSpec` except a non-`pass` status is a `t.Errorf` (red
  test) instead of `t.Skip`. Switched the ten dedicated test funcs to it:
  TestPort_IsolationMerge{Update,Delete,InsertUpdate,MatchRecheck,Join},
  TestPort_IsolationInsertConflict{DoUpdate2,DoUpdate3,DoUpdate4,Specconflict,
  DoNothing2}.
- Files: internal/testport/isolation_port_test.go (helper + 10 one-line switches),
  docs/test-port/postgres-oracle-port-status.{csv,md} (D-002 rationale + regen),
  docs/design/0118-0022-*.md + README row, .ralph/fix_plan.md ([x] M0118-0006).
- Removed the throwaway zz_probe_test.go after use.

Gates (green): all 10 promoted tests PASS under strict gate (~55s);
go vet ./internal/testport/ clean; make ralph-state-guard OK; pgbench smoke via
pre-commit hook at commit.

NEXT loop candidates (probed this loop, ranked by first-divergence cost):
- intra-grant-inplace: output matches EXCEPT one `<waiting>` divergence on
  `ALTER TABLE … ADD PRIMARY KEY` (addk2 should wait on GRANT's pg_class
  catalog-tuple xmax). Closest by output, but needs pg_class catalog-tuple row
  locks across 9 perms — large gap vs virtual pg_class. NOT cheap.
- M0118-0005 FK: referential-integrity + fk-snapshot already PASS (soft anchors
  exist) but ri-trigger DEFERS — group can't fully close; could promote the 2
  passing ones strictly + document ri-trigger/fk-contention/fk-deadlock/
  fk-partitioned/temporal-range-integrity as deferred. PARTIAL win available.
- M0118-0008 DDL/VACUUM: sequence-ddl, vacuum-skip-locked, truncate-conflict,
  vacuum-conflict, alter-table-1, create-trigger, inherit-temp ALL still defer
  (probed) — real engine work.
- M0118-0009 misc remaining (intra-grant-inplace-db needs pg_database.datfrozenxid
  col; temp-schema-cleanup needs pg_my_temp_schema(); horizons needs $$-dollar-quote
  isolation lexer + EXPLAIN FORMAT json + json `->`; async-notify LISTEN/NOTIFY;
  prepared-transactions 2PC) — all NEW subsystems.

GOTCHAS: isolation specs run goopg as a SUBPROCESS. CSV rationale must be
comma-free inside a field (it's a CSV). tpch-spotcheck INFRA-BLOCKED; pgbench
smoke is the live guard. never gofmt -w (go1.25 repo vs local 1.26). Untracked
postgres/ + weekly_loc.* + requirements.txt are stray — leave them.

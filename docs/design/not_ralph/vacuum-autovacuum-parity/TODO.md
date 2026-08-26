# TODO — vacuum/autovacuum parity (branch `waitevent-impl`)

## Phase A — docs
- [x] A1 Parity audit matrix (02) with dual citations
- [x] A2 Bundle: README / 01 / 02 / 03 / 04 / TODO
- [x] A3 Sub-agent review + resolve findings
- [ ] A4 Commit `-n` + push

## Phase B — accounting layer
- [x] B1 pgstat relation store: insSinceVacuum / modSinceAnalyze atomics +
      increments at insert/update/delete folds
- [x] B2 SQL: pg_stat_get_ins_since_vacuum / _mod_since_analyze; de-zero
      pg_stat_user_tables columns
- [x] B3 Reset hooks: VACUUM→dead+ins, ANALYZE→mod; unit tests

## Phase C — scan semantics
- [x] C1 Two-bit VM (ALL_VISIBLE+ALL_FROZEN) + vacuumCore skip
      (non-aggressive, never last block) + split skip stats + relfrozenxid
      guard in executor & launcher callers
- [x] C2 Aggressive determination (invalid relfrozenxid = infinite age;
      0.95*max_age cap); VACUUM (FREEZE) executes; DISABLE_PAGE_SKIPPING consumed
- [x] C3 computeFreezeCutoffs helper (min(min_age,max_age/2), OldestXmin
      clamp); session GUCs; reloption overrides; launcher un-hardcoded

## Phase D — autovacuum engine
- [x] D1 Register missing GUCs incl autovacuum_vacuum_max_threshold (03 §F8)
      + postgresql.conf.sample sync (TestSampleConfigCoversRegistry green)
- [x] D2 Launcher actually started in bootstrap (gated on `autovacuum`),
      naptime from GUC, activity-registry backend registration
- [x] D3 Trigger formula w/ freeze-min-age reloption override;
      anti-wraparound ordering + aggressive marking (SKIP_LOCKED suppression
      N/A: launcher path never used SKIP_LOCKED)
- [x] D4 autoanalyze → sampled analyzer core (catalog sidecar persisted;
      pg_statistic heap rows still need manual ANALYZE — documented)

## Phase E — extras
- [x] E1 Cost model in vacuumCore (proportional sleep ≤4×delay; inert when
      vacuum_cost_delay=0 / autovacuum default 2ms active for launcher)
- [ ] E2 Tail truncation — DEFERRED (see below)
- [ ] E3 relallvisible publish — DEFERRED (see below)

## Phase F — verification & wrap-up
- [x] F1 Unit tests: VM skip counts/aggressive/all-frozen bits
      (TestVacuumVMSkipAndAllFrozenBits); trigger formula + enabled-off
      caller-gate (launcher tests); zero-alloc untouched paths vetted
- [x] F2 Regression: executor/postmaster/transam/storage/catalog/misc/
      xlog suites PASS (testport vacuum isolation set left for CI lane)
- [x] F3 Live (:5533, conf-driven naptime=3s/thresholds lowered):
      auto pass fired from dead-tuple math (643 > 245) in one naptime;
      second pass logged skipped_frozen=51 pages=1 (two-bit VM working);
      autoanalyze produced columns=2 sampled stats
- [ ] F4 Results appended to this bundle; final commit `-n` + push

## Deferred discovered during implementation
- Tail truncation (E2): capability exists (smgr.TruncateRelationTo +
  InvalidateBlock + RecordKindSmgrTruncate replay) but no runtime WAL
  emitter — shipping truncation without its WAL record risks crash-safety;
  resume = wire emitter then vacuumCore tail pass.
- relallvisible publish (E3): pg_class view column exists hardcoded "0";
  needs a catalog↔executor hook (pattern: UserTableTriggerStatsFunc).

## Deferred (documented, not this task)
- VACUUM FULL physical rewrite; CLUSTER reorder
- Partitioned-parent inherited stats
- failsafe/eager-scan machinery; parallel workers
- multixact freeze bookkeeping (relminmxid)

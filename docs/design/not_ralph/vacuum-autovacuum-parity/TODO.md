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
- [x] E2 Tail truncation: Pool.TruncateRelationTail (WAL-first via new
      RecordKindSmgrTruncateTo=18 encode/decode/replay + invalidation +
      smgr shrink), honored vacuum_truncate/NoTruncate;
      TestVacuumTailTruncation
- [x] E3 relallvisible publish: catalog.RelAllVisibleFunc hook wired from
      bootstrap VM; table rows render real counts (index/composite rows stay 0)

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

## Follow-up round (same task, review of deferrals)
- [x] Failsafe escalation: vacuum_failsafe_age reached => aggressive +
      cost-delay off for that pass (03 F-failsafe)
- [x] Partitioned-parent rollup: ANALYZE aggregates children RowCount/Pages
      into the parent sidecar (column stats still per-child)

## Deferred (documented, NOT safely implementable without new machinery)
- VACUUM FULL physical rewrite; CLUSTER reorder — both need transactional
  relfilenode-swap with dedicated WAL (new-record + replay + catalog swap),
  i.e. a dedicated milestone. Rushing them without the WAL story would be
  crash-unsafe, which violates the project's durability bar.
- Multixact freeze bookkeeping (relminmxid); failsafe EAGER scanning;
  parallel workers; partitioned-parent COLUMN stats merge.
- Partitioned-parent inherited stats
- failsafe/eager-scan machinery; parallel workers
- multixact freeze bookkeeping (relminmxid)

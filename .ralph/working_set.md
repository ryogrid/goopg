Last landed (loop #2 / this session): `stats` rung 7 (M0118-0009, design 0118-0131)
— transactional relation-counter staging + abort/TRUNCATE/2PC reconciliation.
First divergence advanced **L2704 → L3072**. Every abort/`ROLLBACK PREPARED`,
TRUNCATE-in-2PC, and cross-backend `COMMIT`/`ROLLBACK PREPARED` table-stats
permutation now matches PG 18.3 byte-for-byte.

Files:
- internal/executor/pgstat_relations.go — NEW staging tier (`relXactCounters` +
  `staging`/`prepared` maps); recordInsert/Update/Delete now STAGE; new
  recordTruncate/commitXact/abortXact/prepareXact/finalizePrepared/
  applyXactToPending/saveTruncDropCounters; exported CommitRelStats/AbortRelStats/
  PrepareRelStats/FinalizePreparedRelStats + recordRel{Insert,Update,Delete,
  Truncate} autocommit-aware helpers; newRelationStatsManager().
- internal/executor/operators_storage.go — insert/update/delete Close use helpers.
- internal/executor/operators_ddl.go — execTruncate records truncate post-truncate.
- internal/executor/operators_tx.go — execCommit folds (commit), execRollback (abort).
- internal/server/twophase.go — detached PREPARE→PrepareRelStats; detached
  finalize→FinalizePreparedRelStats; PREPARE-SSI-failure rollback→AbortRelStats.
- internal/executor/pgstat_relations_test.go — staging/abort/truncate/2PC tests.
- docs/design/0118-0131-*.md + README index row + fix_plan rung-7 note.

Model: scans NON-transactional → pending immediately. tuples_ins/upd/del +
live/dead deltas TRANSACTIONAL → staging, folded to pending at commit/abort math
(AtEOXact_PgStat_Relations). TRUNCATE = truncDropped flag riding to flush (forgets
live/dead). 2PC = staging→per-gid prepared at PREPARE, applied to FINALISING
backend's pending at COMMIT/ROLLBACK PREPARED (sessionStatsID stays the issuing
conn since only ctx.Session is retargeted, not ctx.AdvisorySessionIdentity).

Gates run: go test ./internal/executor/ ./internal/mvcc/ ./internal/server/ PASS;
new pgstat_relations_test.go PASS; TestPort_TwoPhaseCommitSameBackend +
TestPort_IsolationPreparedTransactions{,CIC} strict PASS; DML+commit/rollback
strict isolation regression PASS; TestPort_IsolationStats soft probe L2704→L3072;
build clean. pgbench smoke = pre-commit hook (commit pending).

NEXT rung for `stats` (Effort-L; spec stays `defer`): **L3072 — SLRU statistics
(`pg_stat_slru`).** New first divergence is `s1_slru_check_stats` /
`SELECT blks_zeroed FROM pg_stat_slru WHERE name='notify'` — goopg has no
`pg_stat_slru` view / per-SLRU `blks_zeroed`/`blks_hit`/… counters. Needs: a
process-global SLRU-stats store (per-SLRU named bucket: notify/clog/subtrans/…),
counting hooks in the SLRU page paths (zeroed/read/written/flushes), and the
`pg_stat_slru` SRF. After SLRU lands, `stats` may finally promote to `pass`.
Also still pending (later): sub-transaction (savepoint) staging tier for rel
stats; index-scan `tuples_fetched`; VACUUM `vacuum_count`/live-dead recompute.

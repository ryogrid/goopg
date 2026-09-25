# 0118-0128 — `stats` enabler rung 6: relation tuple statistics (M0118-0009)

Status: accepted — **enabler, NOT a promotion** (`stats.spec` stays `defer`).

## Summary

Advances `postgres/src/test/isolation/specs/stats.spec`'s first divergence
**L2180 → L2704** by implementing the **cumulative per-relation (table)
statistics** subsystem: the `pg_stat_get_numscans`, `_tuples_returned`,
`_tuples_fetched`, `_tuples_inserted`, `_tuples_updated`, `_tuples_deleted`,
`_live_tuples`, `_dead_tuples`, and `_vacuum_count` getters, fed by sequential
scan and INSERT/UPDATE/DELETE counting, gated by `track_counts`, with stats
removed when the relation is dropped and flushed by `pg_stat_force_next_flush()`.

All seven **non-2PC** table-stats permutations (the drop-removes-stats pair, the
`track_counts off/on` access cases, the cumulative seq-scan/DML count cases) and
the **2PC `COMMIT PREPARED`** permutations now match PG 18.3 byte-for-byte. The
new first divergence (L2704) is the first **`ROLLBACK PREPARED`** permutation —
the transactional-counter abort/2PC reconciliation rung.

## Problem

The `stats` spec's "Table stats tests" section exercises the relation half of
PostgreSQL's cumulative statistics. Before this rung the very first table-stats
step failed with `function pg_stat_get_numscans does not exist`; goopg tracked
only function stats (designs 0118-0123…0127).

PostgreSQL splits per-relation counters into two classes (pgstat_relation.c):

- **Non-transactional** — `numscans` / `tuples_returned` / `tuples_fetched`.
  Accumulated in the backend's `PgStat_TableStatus.t_counts` as scans run and
  flushed to shared memory regardless of whether the surrounding transaction
  commits or aborts.
- **Transactional** — `tuples_inserted` / `_updated` / `_deleted` and the
  live/dead-tuple deltas. Staged in `PgStat_TableXactStatus` and folded into
  `t_counts` at `AtEOXact_PgStat`, with the live/dead deltas reconciled
  differently on commit vs abort (an aborted insert/update becomes a *dead*
  tuple, not a live one).

## Design

`internal/executor/pgstat_relations.go` mirrors the two-tier shape already used
for function stats (`pgstat_functions.go`):

- `relationStatsManager` holds a process-global `shared[oid]` store and a
  per-session `pending[sessionID][oid]` store. `relStats` is the singleton.
- `recordScan / recordInsert / recordUpdate / recordDelete` add to the calling
  session's pending counters. INSERT adds `+1` live per row; DELETE adds `+1`
  dead and `-1` live per row; UPDATE adds `+1` dead per row and leaves live
  unchanged (goopg has no HOT update — see [[goopg_no_hot_update_index_reeval]]).
- `flush(sessionID)` merges a session's pending counters into `shared` and
  clears the pending set — driven by `pg_stat_force_next_flush()`.
- `get(oid)` returns the shared counters; the getters report **0** (not SQL
  NULL) for an absent OID, matching PG's relation-stat getters on a
  dropped/never-touched relation.
- `dropTable(oid)` removes the shared entry and every session's pending entry,
  so a dropped relation reads 0 and a peer's stale pending counts are not
  revived on its next flush (pgstat_drop_relation).

### Counting hook points (all gated by `track_counts`)

| Counter | Site (`internal/executor/`) |
|---|---|
| numscans + tuples_returned (SELECT) | `seqScanOp`: `statReturned` incremented per yielded tuple, recorded once in `Close()` |
| numscans + tuples_returned (UPDATE/DELETE base scan) | `scanMatching` gains a `statOID` param; records one scan reading every visible tuple at clean completion |
| tuples_inserted (+live) | `insertOp.Close()` using final `rowsAffected` |
| tuples_updated (+dead) | `updateOp.Close()` |
| tuples_deleted (+dead, −live) | `deleteOp.Close()` |
| drop removes stats | `ddlOp.dropTableByRefImmediate` (autocommit + deferred-drop both funnel here) |
| flush / reset | `pg_stat_force_next_flush`, `pg_stat_reset` in `expr.go` |

`scanMatching`'s two FK-maintenance call sites pass `statOID = 0` (no-op), since
PG does not attribute those internal scans to the user table.

### Why per-statement `Close()` recording is correct here

goopg's simple-query path commits each autocommit statement immediately, so
recording transactional counts at statement `Close()` is equivalent to applying
them at commit for a statement that succeeds — which is exactly what the seven
non-2PC permutations need. The non-transactional scan counters are likewise
correct because a successful autocommit statement is its own committed
transaction. This deliberately does **not** stage transactional counters in a
per-transaction structure; abort/2PC reconciliation is the next rung (below).

## Verification

- New first divergence: `stats.spec` L2180 → **L2704** (the
  `s1_…_rollback_prepared_a` permutation). All non-2PC and 2PC-commit-prepared
  table-stats permutations match PG 18.3.
- `go test ./internal/executor/` green; new `pgstat_relations_test.go` covers
  accumulate/flush/get, update dead-delta, and drop-without-revival.
- `TestPort_IsolationStats` still soft-`SKIP`s with the new first-divergence
  diff (spec remains `defer`).
- pgbench CI-parity smoke via the pre-commit hook (scan/DML hot-path change).

Compared against `postgres/src/backend/utils/activity/pgstat_relation.c` and the
`pgstat_count_heap_*` macros in `src/include/pgstat.h`.

## Next rung (still `defer`)

Transactional-counter staging + abort/2PC reconciliation for relation stats,
matching `stats.spec` from L2704:

- Stage `tuples_inserted/_updated/_deleted` and live/dead deltas per
  transaction; on **abort / `ROLLBACK PREPARED`**, inserted+updated tuples
  become dead (no live increment) and the transactional ins/upd/del counters
  follow PG's `AtEOXact_PgStat_Relations` rules (incl. the `truncdropped` path
  for in-transaction `TRUNCATE` / `DROP`).
- 2PC handoff of the staged relation counters to the prepared transaction so a
  cross-backend `COMMIT/ROLLBACK PREPARED` applies them (mirrors the function-
  stats 2PC rung, design 0118-0127).
- Later: index-scan `tuples_fetched`, VACUUM-driven `vacuum_count` /
  live/dead recompute, and SLRU stats (`pg_stat_slru`).

## Update 2026-09-19 (M-NIGHTLY AI-…-004/-007/-011): getter-source repair — `n_dead_tup` reads the tiered entry, not the trigger store

The 2PC-abort permutations of `stats.spec` (`s1_rollback_prepared_a` and
`s2_rollback_prepared_a`, both plain and truncate variants) regressed at
L2704/L2771/L2897/L2943: `n_dead_tup` read 6 where PG reports 8, and 1 where PG
reports 2. The tiered counters were never wrong — `applyXactToPending`'s abort
arm (`deltaDead += restored ins + upd`, mirroring `AtEOXact_PgStat_Relations` /
`pgstat_twophase_postabort`) already computed exactly 8 and 2, as the unit
tests pinned. The divergence lived in `expr.go`'s getter arm: the
`pg_stat_get_dead_tuples` / `_ins_since_vacuum` / `_mod_since_analyze` triple
read `triggerSnapshot` — the **non-transactional** autovacuum-trigger store —
which bumps `dead` per update/delete at DML time and is never reconciled at
abort (so it accumulates `upd + del`, the commit formula, and loses the
truncdrop restore). Upstream has exactly one `PgStat_StatTabEntry.dead_tuples`,
fed only by `delta_dead_tuples` at `pgstat_relation_flush_cb`.

Fix — one PG tabentry, one goopg source:

- `pg_stat_get_dead_tuples` now reads `c.deltaDead` (clamped ≥0) from the
  flushed shared entry, the same source `pg_stat_get_live_tuples` already used.
  The abort-fold math this rung's successor (0118-0131) built is finally what
  the SQL surface reports: spec byte-identical again.
- `pg_stat_get_ins_since_vacuum` reads a new `insSinceVacuum` shared field fed
  at flush by the pending entry's *attempted* `tuples_inserted` (aborted
  inserts count — `pgstat_relation_flush_cb`'s own comment calls the extra
  autovacuum triggers not worth a separate field), reset to zero by a
  committed truncdrop or by `reportVacuum`.
- `pg_stat_get_mod_since_analyze` reads a new `changedTuples` field fed by the
  commit fold (`ins + upd + del`; an abort generates no change events —
  `changed_tuples` is not reset by truncdrop) and zeroed by `reportAnalyze`.
- `pg_stat_get_vacuum_count` reads a real `vacuumCount` bumped by
  `reportVacuum` (was hardwired 0).
- `reportVacuum(oid, live, dead)` mirrors `pgstat_report_vacuum`: writes the
  shared entry directly — measured live survivors, ~0 remaining dead,
  `ins_since_vacuum = 0`, `vacuum_count++` — wired next to the existing
  `resetVacuumTriggers` call in `operators_vacuum.go`.
  `reportAnalyze(oid)` mirrors the `mod_since_analyze = 0` half of
  `pgstat_report_analyze`, wired at both `resetAnalyzeTriggers` sites in
  `operators_analyze.go`. (The analyze-time live/dead measured-overwrite stays
  deferred — goopg's analyze scan does not count dead tuples.)
- `UserTableTriggerStatsFunc` (the `pg_stat_user_tables` /
  `pg_stat_all_tables` view columns `n_dead_tup` / `n_mod_since_analyze` /
  `n_ins_since_vacuum`) is repointed to the same flushed shared entry, so view
  and function getters cannot disagree — upstream they are one tabentry.

The `relTriggerCounters` store remains as designed (F4 in
`vacuum-autovacuum-parity/03-design.md`): it exists solely so the autovacuum
launcher sees fresh DML counts without a periodic flush, and is still the
documented approximation — aborted-xact corrections still do not reach it
(ledgered).

Verified: `TestPort_IsolationStats` PASS (all permutations byte-identical);
`pgstat_relations_test.go` extended — commit/abort/truncate-commit/2PC wants
now pin `changedTuples` + `insSinceVacuum`, plus new
`TestRelStatsTruncateAbortRestores` (the L2897 row `3|9|4|2|0|4|2|0`) and
`TestRelStatsReportVacuumAnalyze`; sibling isolation tests (prepared-transactions
{,-cic}, vacuum-{skip-locked,concurrent-drop,conflict,no-cleanup-lock},
TwoPhaseCommitSameBackend) all PASS; `go test -race ./internal/executor/` clean.

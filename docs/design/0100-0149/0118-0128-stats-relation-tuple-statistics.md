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

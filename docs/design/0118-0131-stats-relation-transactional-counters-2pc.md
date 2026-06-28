# 0118-0131 — `stats` rung 7: transactional relation-counter staging, abort reconciliation, TRUNCATE, and 2PC handoff

Status: landed
Milestone: M0118-0009 (`stats` isolation spec — cumulative pgstat subsystem)
Spec: `postgres/src/test/isolation/specs/stats.spec`
Oracle: `postgres/src/backend/utils/activity/pgstat_relation.c`
Predecessors: 0118-0128 (rung 6 — relation tuple stats), 0118-0127 (rung 5 —
cross-backend 2PC), 0118-0124/0125 (function stats), 0118-0123 (GUCs / flush
no-ops)

## Problem

Rung 6 (0118-0128) introduced per-relation cumulative tuple statistics
(`pg_stat_get_tuples_inserted/_updated/_deleted`, `_live_tuples`,
`_dead_tuples`) but recorded them **immediately at each DML op's `Close`** into
the backend-local `pending` counters — i.e. "applied at statement commit" for
the autocommit simple-query path only. The first divergence sat at **L2704** of
`expected/stats.out`, the `s1_…_rollback_prepared_a` permutation:

```
s1_begin s1_table_insert s1_table_update_k1×2 s1_table_update_k2×3
s1_table_delete_k1 … s1_prepare_a … s1_rollback_prepared_a … s1_ff s1_table_stats
```

PostgreSQL keeps the insert/update/delete counters and the live/dead deltas
**transactional** (`PgStat_TableXactStatus`), folding them into the backend's
counts only at `AtEOXact_PgStat_Relations`, with different math on commit vs
abort:

- **commit:** `tuples_*` += staged; `delta_live += ins − del`;
  `delta_dead += upd + del`.
- **abort:** `tuples_*` still += staged (attempted actions count), but
  `delta_dead += ins + upd` (aborted inserts/updates become dead tuples) and the
  delete is a no-op on live/dead (the row was never removed).

rung 6's immediate-apply model produced `live=3 dead=6` (commit math) where PG
expects `live=1 dead=8` (abort math). Three further mechanisms were also
missing: TRUNCATE's "forget live/dead" reset (`pgstat_count_truncate`), and the
two-phase-commit hand-off of staged counters (`AtPrepare_PgStat_Relations` +
`pgstat_twophase_post{commit,abort}`).

## Design

A third counter tier — **staging** — is inserted in front of `pending`, mirroring
`PgStat_TableXactStatus`:

```
DML op.Close ──► staging[sessionID][oid]      (transactional, per (conn,txn))
seq scan     ──► pending[sessionID][oid]       (NON-transactional, immediate)
commit/abort ──► fold staging → pending        (commit/abort math)
flush (_ff)  ──► merge pending → shared         (cluster-global)
getter       ──► read shared
```

`relXactCounters` holds `tuples_inserted/_updated/_deleted` plus a
`truncDropped` flag and the saved pre-truncate counts. `relStatCounters` (used
for both `pending` and `shared`) gains a `truncDropped` flag that rides from a
committed TRUNCATE through to the flush.

Key invariant: scan counters (`numScans`, `tuplesReturned`) remain
non-transactional and continue to land directly in `pending` at scan time —
PostgreSQL reports them regardless of commit/abort and even across PREPARE
(`PostPrepare_PgStat_Relations` leaves them with the originating backend).

### Fold math (`applyXactToPending`)

Direct port of `AtEOXact_PgStat_Relations` + `pgstat_twophase_post{commit,abort}`:

```
if !isCommit && x.truncDropped:           # restore_truncdrop_counters
    x.tuples_* = x.*_pre_truncdrop
pending.tuples_* += x.tuples_*            # attempted actions count either way
if isCommit:
    if x.truncDropped:                    # forget prior live/dead
        pending.truncDropped = true
        pending.deltaLive = 0; pending.deltaDead = 0
    pending.deltaLive += x.ins - x.del
    pending.deltaDead += x.upd + x.del
else:
    pending.deltaDead += x.ins + x.upd    # aborted ins/upd are dead; del no-op
```

### TRUNCATE (`recordTruncate` → `pgstat_count_truncate`)

Saves the current staged counts as `*_pre_truncdrop`, sets `truncDropped`, then
resets the staged `tuples_*` to 0 so post-truncate inserts/updates count afresh.
On commit, `truncDropped` rides into `pending` (resetting its live/dead to 0) and
then into `shared` at flush — `flush()` resets `shared.deltaLive/deltaDead` to 0
when the pending entry is truncdropped, forgetting already-flushed counts
(`pgstat_relation_flush_cb`). On abort, the pre-truncate counts are restored.

### Two-phase commit hand-off

At PREPARE (RC/RR detached path in `internal/server/twophase.go`), the
originating backend's staging is moved into a per-gid `prepared` record
(`prepareXact` ≈ `AtPrepare_PgStat_Relations` + `PostPrepare`). The backend's
already-pending scan counters stay and report via its own `_ff`.

At COMMIT/ROLLBACK PREPARED, `finalizePrepared(gid, finalisingSessionID,
isCommit)` folds the prepared record into the **finalising backend's** pending
counters (`pgstat_twophase_post{commit,abort}` apply to the local backend), so a
cross-backend `s2_commit_prepared_a` + `s2_ff` applies them. The finalising
context still carries the issuing connection's session identity
(`ctx.AdvisorySessionIdentity`; only `ctx.Session` is retargeted), so
`sessionStatsID(ctx)` resolves to the backend that flushes next.

The SERIALIZABLE keep-open 2PC path is unchanged: PREPARE does not extract
staging, and the finalising COMMIT/ROLLBACK runs on the originating connection,
so the normal `execCommit`/`execRollback` fold applies it.

### Autocommit

Outside an explicit transaction block, a statement commits immediately, so the
`recordRel{Insert,Update,Delete,Truncate}` helpers fold the session's staging
into pending right after staging (equivalent to rung 6's immediate-apply for the
non-truncate case, but now flowing through the commit math). Inside a `BEGIN …`
block the staging accumulates across statements and folds at `COMMIT` /
`ROLLBACK` / PREPARE.

## Integration points

- `internal/executor/pgstat_relations.go` — new staging + prepared tiers;
  `recordInsert/Update/Delete` now stage; new `recordTruncate`, `commitXact`,
  `abortXact`, `prepareXact`, `finalizePrepared`, `applyXactToPending`,
  `saveTruncDropCounters`; exported `CommitRelStats`/`AbortRelStats`/
  `PrepareRelStats`/`FinalizePreparedRelStats` and the
  `recordRel{Insert,Update,Delete,Truncate}` autocommit-aware helpers.
- `internal/executor/operators_storage.go` — insert/update/delete `Close` call
  the new helpers.
- `internal/executor/operators_ddl.go` — `execTruncate` records the truncate
  after the physical truncate succeeds.
- `internal/executor/operators_tx.go` — `execCommit` folds with commit math
  after `TxnMgr.Commit`; `execRollback` folds with abort math after
  `TxnMgr.Rollback`.
- `internal/server/twophase.go` — `execPrepareTransaction` (detached path) calls
  `PrepareRelStats`; `execFinalizePrepared` (detached path) calls
  `FinalizePreparedRelStats`; the PREPARE-time SSI-failure rollback calls
  `AbortRelStats`.

## Result

First divergence advanced **L2704 → L3072** (`go test -run
TestPort_IsolationStats` soft probe). All transactional-counter permutations now
match PG 18.3 byte-for-byte: the abort/rollback-prepared rows (`1/8` live/dead),
the TRUNCATE-in-2PC rows (`5/1/0/1/1`), and the cross-backend COMMIT/ROLLBACK
PREPARED rows. The new first divergence (L3072) is the **`pg_stat_slru`** SLRU
statistics block (`blks_zeroed` for the `notify` SLRU) — a distinct unbuilt
subsystem and the final `stats` rung. Spec stays `defer`.

## Limitations / later rungs

- **Sub-transaction (savepoint) staging.** Staging is top-level only; goopg has
  no `AtEOSubXact_PgStat_Relations` tier, so a caught-exception INSERT inside a
  plpgsql `EXCEPTION` block commits as live rather than converting to dead. Not
  exercised by the failing permutations.
- **SLRU statistics** (`pg_stat_slru`) — the remaining divergence (L3072+).
- **Index-scan `tuples_fetched`** and **VACUUM `vacuum_count`** /
  live-dead recompute remain stubbed (return 0), unchanged from rung 6.

## Tests / gates

- `internal/executor/pgstat_relations_test.go` — staging+commit+flush;
  abort-dead-tuples (`1/8`); TRUNCATE commit (`5/1/0/1/1`); 2PC commit
  (cross-backend); 2PC abort; drop clears staging/prepared.
- `go test ./internal/executor/ ./internal/mvcc/ ./internal/server/` PASS.
- `TestPort_TwoPhaseCommitSameBackend`, `TestPort_IsolationPreparedTransactions`,
  `TestPort_IsolationPreparedTransactionsCIC` strict PASS (no 2PC regression).
- DML+commit/rollback strict isolation regression (`delete-abort-savept`,
  `fk-snapshot`, `merge-update`, `insert-conflict-do-nothing-2`, `alter-table-3`)
  PASS.
- pgbench CI-parity smoke (pre-commit hook).

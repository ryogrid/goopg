# 08-04 — Sub-transaction CLOG lanes + pg_subtrans parent durability

status: design · date: 2026-07-14 · base: `a640d2b0` · gates: G-race, G-crash,
G-tpch, D-002 isolation → [README](README.md)

## 1. Problem and numbers

C2 removed the commit-path CLOG fsync for top-level transactions, but its
sub-transaction handling is incomplete (06-02 #4; project memory
`perf-optimize3-dash-c2-c3-landed`): sub-xid CLOG lanes are never stamped
(`TransactionIdCommitTree` parity gap), so **subxact-deleted tuples are
unreclaimable** (bloat), and `pg_subtrans` `SetParent` still does a per-write
fsync. This is invisible to pgbench `-N` (no savepoints) but matters for
SAVEPOINT-heavy and PL/pgSQL-exception-heavy workloads. It is the last C2
leftover.

## 2. Current-code map (verified at `a640d2b0`)

- **`SubxactMap`** — `internal/mvcc/subxact_visibility.go`: `NewSubxactMap`
  (:37), `Register(subxid, parentXid)` (:143), `MarkAborted` (:172),
  `EnablePersistence`/`RestoreFromSLRU` (:53/:81), `Truncate` (:119). This is the
  pg_subtrans analog (parent-xid map, SLRU-backed).
- Top-level commit sets the CLOG status bits; the **sub-xid lanes** are not
  stamped on commit (the `TransactionIdCommitTree` parity gap), so a reader
  resolving a subxact's status falls back to the parent map rather than a
  committed-tree stamp — correct for visibility but leaves subxact-deleted
  tuples looking in-doubt to VACUUM's reclaim test.
- `pg_subtrans` parent recording (`Register` → SLRU) performs durability
  per-write rather than batching at commit.

## 3. PostgreSQL reference

- `src/backend/access/transam/transam.c` — `TransactionIdCommitTree(xid,
  nsubxids, subxids)` stamps the top xid **and every sub-xid** committed in one
  CLOG update, so a subxact reads as committed directly.
- `src/backend/access/transam/subtrans.c` — `SubTransSetParent` writes the
  parent link to the `pg_subtrans` SLRU; it is **not** fsynced per write
  (pg_subtrans is rebuilt from WAL on crash, like pg_xact under C2's model).

## 4. Target design

Two parts, mirroring PG:

1. **Stamp sub-xid CLOG lanes at commit** — extend the commit path to call the
   `TransactionIdCommitTree` analog: stamp the top xid and all its committed
   sub-xids' CLOG status in one update, so VACUUM's reclaim test sees
   subxact-deleted tuples as committed-dead and can reclaim them.
2. **Drop the per-write pg_subtrans fsync** — `SubxactMap.Register` writes to the
   SLRU without a synchronous fsync; durability comes from WAL replay on crash
   (the same redo-anchored reconstruction C2-S3 established for pg_xact). The
   checkpoint's `FlushCLOGFn`-equivalent must cover pg_subtrans pages too.

### Decision log

- **D1 — reuse C2's redo-anchored reconstruction.** pg_subtrans, like pg_xact
  under C2, is reconstructable from post-redo WAL; the per-write fsync is
  therefore removable under the same checkpoint-ordering invariant (README X6 in
  the 05 bundle). Do not invent a separate durability scheme.
- **D2 — stamp the whole tree in one CLOG update.** Per-subxid stamping would
  reintroduce write amplification; the batched `CommitTree` is both correct and
  cheaper.

## 5. Invariants and failure modes

- **I1 — subxact status is durable-or-reconstructable.** After the stamp, a
  committed subxact reads committed from CLOG; after a crash, WAL replay
  re-stamps it. The checkpoint must flush pg_subtrans + CLOG pages before the
  checkpoint record (X6 invariant), or a post-checkpoint reader could see an
  unstamped subxact.
- **F1 — abort within a subxact tree.** `MarkAborted` must stamp aborted
  sub-xids so their tuples are reclaimable as aborted-dead; the commit-tree stamp
  covers only committed sub-xids.
- **F2 — SLRU truncation race.** `Truncate` (:119) must not remove a lane still
  needed by an in-flight reader; the oldest-xact horizon gates it (existing).

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S1 | commit-tree CLOG stamp | stamp top + committed sub-xids in one CLOG update at commit; VACUUM reclaim test now sees subxact-dead tuples. | G-race, D-002, G-tpch |
| S2 | drop pg_subtrans per-write fsync | SLRU write without fsync; checkpoint flushes pg_subtrans pages; crash replay re-stamps. | G-crash, G-race |
| S3 | perf/bloat acceptance | SAVEPOINT-heavy workload: subxact-deleted tuples reclaimed by VACUUM; no bloat growth. | G-perf (savepoint microbench) |

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| subxact map restore/truncate | `internal/mvcc/subxact_restore_test.go`, `subxact_truncate_test.go` | S1, S2 |
| savepoint visibility/rollback | savepoint isolation specs (project memory family) | S1 |
| crash recovery (pg_subtrans rebuild) | `internal/initdb/`, `TestKillKillRecovery` | S2 |
| VACUUM reclaim of subxact-dead tuples (new) | `internal/mvcc/` or vacuum test | S1, S3 |

## 8. Performance verification

A SAVEPOINT-heavy microbenchmark (not pgbench `-N`, which has none): confirm
subxact-deleted tuples are reclaimed by VACUUM (no unbounded bloat) and the
per-write pg_subtrans fsync is gone (fsync count flat under savepoint churn).

## 9. Open questions

- **O-SX-1** — Does goopg's commit path have a single choke point to add the
  tree stamp, or is it split across the simple/extended COMMIT paths (relates to
  doc 09)? Enumerate the commit sites.
- **O-SX-2** — Interaction with doc 09: extended-protocol savepoints are
  deferred there (O-XP-2); the subxact durability here should not assume
  extended-path savepoints exist yet.

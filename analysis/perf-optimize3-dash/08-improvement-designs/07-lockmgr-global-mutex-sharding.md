# 08-07 — Shard the lockmgr global mutex (fast-path relation locks)

status: design · date: 2026-07-14 · base: `a640d2b0` · gates: G-race, G-tpch,
G-perf, D-002 isolation suite → [README](README.md)

## 1. Problem and numbers

The lockmgr global mutex is the single largest read-path (`-S`) block-delay
sink, at both scales:

| run | share of `-S` block delay | detail |
|---|---:|---|
| 06 scale-100 | **53.1 %** of 1,213.8 s | `acquire` 408.9 s + `Release` 233.3 s ≈ 68 µs/query ≈ 12 % of the 0.555 ms latency |
| 07 scale-500 | **43.4 %** of 1,328.3 s | `acquire` 366.06 s + `Release` 210.69 s ≈ 56 µs/query ≈ 9.6 % of latency |

Every point-SELECT takes a per-statement relation read lock, and all of them
serialize on **one** `LockManager.mu`. This is pure serialization overhead — a
point read never actually conflicts with another point read on the same
relation; they all want `AccessShareLock` and coexist.

## 2. Current-code map (verified at `a640d2b0`)

- **`LockManager`** — `internal/lockmgr/lockmgr.go:310`: `mu sync.Mutex`
  (line 311), `states map[LockTag]*lockState`. One mutex guards the whole table.
- **`LockManager.acquire(...)`** — `lockmgr.go:403`: takes `lm.mu`, looks up/
  creates the `lockState`, grants or FIFO-parks. **`AcquireWithTimeout`**
  (:395) / **`Acquire`** (:385) wrap it.
- **`LockManager.Release(b, t, m)`** — `lockmgr.go:538`: takes `lm.mu`, runs the
  wake-pass.
- **Caller (read path):** `Context.acquireRelLockMaybeTransient(rel, mode)` —
  `internal/executor/context.go:1098`, reached via
  `acquireScanReadLockTxn` (:969) / `acquireScanIndexReadLocksTxn` (:990) at
  `indexScanOp.openPrep` — i.e. once per statement per relation/index.

## 3. PostgreSQL reference

- `src/backend/storage/lmgr/lock.c` + `lwlock.c` — the heavyweight lock table is
  sharded into **`NUM_LOCK_PARTITIONS` (16)** partitions
  (`LockHashPartitionLock`, keyed by `LOCKTAG` hash). Each partition has its own
  LWLock, so unrelated relations' locks never contend.
- `src/backend/storage/lmgr/lock.c` — the **fast-path** for weak relation locks
  (`AccessShareLock`/`RowShareLock`/`RowExclusiveLock`): each backend records up
  to `FP_LOCK_SLOTS_PER_BACKEND` such locks in its **own PGPROC**, taking *no*
  shared lock-table partition at all unless a strong locker forces a fallback
  (`FastPathStrongRelationLocks`). This is why PG's point-SELECT relation lock
  is nearly free.

goopg should adopt **both**: partition the table (removes cross-relation
contention) and add a fast-path for weak relation locks (removes the shared
acquisition entirely for the common case).

## 4. Target design

### 4.1 Partition the lock table

`LockManager` gets `partitions [16]struct{ mu sync.Mutex; states
map[LockTag]*lockState }`, routed by `hash(tag) % 16`. `acquire`/`Release` take
only the target partition's mutex. Deadlock detection (`deadlockTimeout`) must
scan across partitions — the detector acquires partitions in a fixed order
(index ascending) to build the wait-for graph.

### 4.2 Per-backend fast-path for weak relation locks

Each backend (its `BackendID` / proc slot) carries a small fixed array of
fast-path relation-lock slots. Acquiring `AccessShareLock` on a relation:

```
if no strong locker is registered for this relation (a global generation
   counter / small strong-lock table):
    record (rel, mode) in the backend's own fast-path slot   // no shared lock
else:
    fall back to the partitioned table
```

A strong locker (e.g. `AccessExclusiveLock` from DDL/VACUUM FULL) first bumps
the strong-lock registration, then sweeps existing backends' fast-path slots
into the shared table before proceeding — PG's `FastPathTransferRelationLocks`.
Release clears the fast-path slot without touching shared state.

### 4.3 Decision log

- **D1 — 16 partitions (PG parity).** Enough to decorrelate the ~few relations
  a pgbench mix touches; a point-SELECT storm on ONE relation still needs the
  fast path (D2), because all its locks hash to one partition.
- **D2 — fast-path is the real win for `-S`.** pgbench `-S` hammers one relation
  (`pgbench_accounts`) — partitioning alone routes every one of those locks to
  the *same* partition, so it barely helps. The per-backend fast-path is what
  removes the shared acquisition. Partitioning is the correctness-simple first
  slice; the fast path is the performance slice.
- **D3 — reuse the existing proc-slot machinery.** goopg already has per-backend
  proc slots (`mvcc.ConnSlotCount`); the fast-path slots live there, avoiding a
  new registry.

## 5. Invariants and failure modes

- **I1 — a strong lock always sees weak holders.** The strong-lock registration
  + fast-path sweep must be atomic w.r.t. new fast-path acquisitions: a backend
  taking a weak lock must re-check the strong-lock generation *after* recording
  its slot (PG's double-check), or a DDL could miss a concurrent reader.
- **I2 — deadlock detection completeness.** With the table partitioned and some
  locks in fast-path slots, the detector must consult both the partitions and
  the fast-path slots to build a complete wait-for graph.
- **F1 — fast-path slot exhaustion.** A backend holding more weak relation locks
  than fast-path slots falls back to the shared table for the overflow — never
  an error, just slower. Bounded by `FP_LOCK_SLOTS_PER_BACKEND`.
- **F2 — partition-order deadlock in the detector.** The detector taking
  multiple partition locks must use a fixed order; `-race` + the isolation
  suite (D-002) is the gate.

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S1 | partition the table | `[16]` partitions + hash routing; deadlock detector scans partitions in order. Behavior-identical, finer locking. | G-race, D-002 isolation |
| S2 | strong-lock registration | the generation counter + strong-lock table + the fast-path-sweep primitive (no fast-path readers yet — sweep is a no-op). | G-race |
| S3 | weak-lock fast path | per-backend fast-path slots for `AccessShareLock`/`RowShareLock`/`RowExclusiveLock`; the double-check against the strong-lock generation; release clears slot. **The performance slice.** | G-race, D-002, G-tpch |
| S4 | perf acceptance | re-measure `-S`; the lockmgr block-delay share (43–53 %) should collapse toward zero for the point-read workload. | G-perf |

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| lockmgr unit + FIFO fairness | `internal/lockmgr/lockmgr_test.go` | S1, S3 |
| isolation specs (lock waits, deadlocks) | `internal/testport/isolation_port_test.go` + D-002 suite | S1, S3 |
| DDL vs. concurrent reader (strong-lock sweep) | new isolation spec | S2, S3 |
| TPC-H spotcheck | `scripts/tpch-spotcheck.sh` | S3 |

## 8. Performance verification

`run_rw50.sh` `-S` at scale 100 (`-M prepared`), `GOOPG_BLOCK_PROFILE_RATE=1`.
Success: the `acquireRelLockMaybeTransient` → `LockManager.acquire`/`Release`
block-delay share drops from ~43–53 % toward negligible; `-S` TPS rises toward
the CPU-bound ceiling (the wait was ~10–12 % of latency, so ~10 % TPS headroom
on this item alone).

## 9. Open questions

- **O-LM-1** — Do goopg's tuple/row locks (which today ride `WaitForXID`, not
  the lockmgr, per project memory) interact with the fast-path relation-lock
  sweep? Confirm the sweep scope is relation locks only.
- **O-LM-2** — Is the isolation-runner's timing-only blocking detection
  (300 ms, no `pg_locks` probe — project memory) sensitive to the finer lock
  granularity? Re-baseline the isolation suite's `<waiting>` annotations.
- **O-LM-3** — `FP_LOCK_SLOTS_PER_BACKEND` sizing for goopg's workloads (PG uses
  16); a partition-heavy TPC-H query touches many relations.

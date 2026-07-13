# 06-02: Where the bottleneck moved

profiles: `runs/postdash_6e3b7a37/profiles/` (CPU = 90 s in-run window;
block/allocs/mutex are cumulative since the pre-workload restart, ~105 s).
`-N` block profile total: 4,170.9 s of delay.

## Write path (`-N`): the bottleneck is now PG-shaped

Baseline block profile: 80.5 % of all block delay under
`CommitTransaction`, split between the WAL flush select (43.2 %) and the CLOG
group-commit `flushMu` (32.8 %).

Now:

| block-delay sink | share (cum) | meaning |
|---|---:|---|
| `CommitTransaction` → `wal.FlushUpTo` → `walWriteLock.acquireOrWait` | **59.2 %** | waiting for the WAL flush cycle — the same thing PG waits for |
| `updateOp.updateViaIndex` (whole statement) | 17.7 % | of which most bottoms out in `Pool.Pin` waits |
| — `Pool.Pin` → `pinSlow` (total across ALL callers, incl. the RangeScan probe inside updateViaIndex) | 15.6 % | waits ultimately on the **per-file `relFile.readBlock` mutex** (647.8 s of the 863 s Mutex flat total) — page *reload* serialization, not leaf latches |
| btree `Insert` | 3.3 % | index insert latching |

(Nesting note, per review: `RangeScan`'s 14 % cum is ~89 % the same
`Pool.Pin` wait counted in the 15.6 % row — the UPDATE probe's leaf pins —
so these rows are NOT additive; the independent quantities are the 59.2 %
commit wait, the 17.7 % updateViaIndex statement, and inside it the
readBlock-mutex-dominated Pin waits.)

The CLOG `flushMu` entry is **gone** (the machinery was deleted in C2-S4).
The commit path now has exactly one durable-wait: the WAL flush. That is the
canonical group-commit-bound profile — PG's own wait sampling in the same run
is 86.8 % `LWLock:WALWrite` + 2.1 % `IO:WalSync` (21,606 + 527 of 24,891
samples).

**Decomposing the remaining 1.47× (per-statement arithmetic).** Per-txn
latency excess over PG = 5.05 − 3.44 = 1.61 ms, of which:

| component | excess vs PG | share of the excess |
|---|---:|---:|
| `UPDATE` (1.022 vs 0.217 ms) | 0.805 ms | **50 %** |
| `END` (3.263 vs 2.828 ms) | 0.435 ms | 27 % |
| `BEGIN`+`SELECT`+`INSERT` | 0.37 ms | 23 % |

So the largest single component is now the **UPDATE statement**, not the
commit. Converging END fully to PG's 2.83 ms (the C5 ceiling) yields
~10.8 k TPS — a residual of **1.34×**, not parity; the UPDATE excess (whose
measured wait is the readBlock-mutex Pin serialization above, plus the C3
probe work) is what stands between 1.34× and ~1.1×.

**On commit-group width**: the AUX probe's 5.7-vs-23.2 comparison is
regime-skewed (goopg measured at 1,783 TPS under strace; PG at 13,996 —
width scales with arrival rate, and the headline goopg run must have
operated at effective width ~20+ to reach 9,898 TPS). What remains true:
PG's serialized fdatasync cycle is ≤1.66 ms (36,164 in 60 s) while goopg's
END excess says its cycle amortization still trails; goopg's `walWriteLock`
lets followers arriving during an in-flight flush start the next cycle
rather than sweeping the full queue the way PG's `LWLockAcquireOrWait` +
`WaitXLogInsertionsToFinish` does (xlog.c). C5
(`05-improvement-designs/05-c5-pipelined-commit-groups.md`) remains the
designed fix — but per the arithmetic above it is now CO-RANKED with the
UPDATE-statement work, not above it.

CPU on `-N` is not the constraint: 24.3 % of samples are raw syscalls and
7.8 % `futex` (wait machinery); the largest engine entries are
`VacuumHeapPageBySlots` 5.6 % (opportunistic prune — busier now that
throughput is 4.6×) and `captureSnapshot` 5.4 %.

## Ranked next fixes (write path)

1. **UPDATE statement cost (1.022 ms, 4.7×; 50 % of the per-txn excess)** —
   the dominant measured wait is `Pool.Pin` bottoming in the per-file
   `relFile.readBlock` mutex (647.8 s — page reload serialization; shard it
   or issue reads outside the mutex), plus the C3 probe-path work. Migrating
   the 9 remaining `RangeScan` callers to kill collection (ledgered) also
   removes the residual pkey growth and its split WAL.
2. **C5 — pipelined commit groups** (design 05/05; 27 % of the excess):
   sweep-while-syncing to close the amortization gap; ceiling = PG-equal
   END (~2.8 ms), i.e. 1.47× → ~1.34× by itself.
3. **BEGIN 0.229 ms (2.7×)** — per-txn setup (snapshot capture 5.4 % CPU);
   candidate: snapshot reuse for read-committed single-statement txns
   (PG takes the snapshot lazily at first statement).
4. Subxact CLOG lanes + pg_subtrans fsync (C2 leftovers, ledgered) — matter
   for SAVEPOINT-heavy workloads, invisible to pgbench.

## Read path (`-S`): unchanged, CPU-bound, and now the larger absolute gap

goopg `-S` burns ~10.1 cores (907.9 s of samples in a 90 s window) at 89,955
TPS while PG reaches 182,384. Contrary to a first read, the `-S` block
profile is NOT contention-free (review finding): **53.1 % of its 1,213.8 s
of delay sits under `opOpen` → `indexScanOp.openPrep` →
`acquireRelLockMaybeTransient` — the lockmgr GLOBAL mutex** (`acquire`
408.9 s + `Release` 233.3 s), i.e. per-statement relation read locks
serialized on one mutex ≈ 68 µs/query ≈ 12 % of the 0.555 ms latency.
Beyond that wait, the path is a per-query CPU-cost race. The cost buckets
and the Go-vs-C attribution are the subject of 03 — with the write path now
at 1.47×, the ~2× read gap is where the next factor of overall parity lives.

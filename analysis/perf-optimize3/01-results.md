# perf-optimize3 — 01: Results

run: `runs/20260713_004324` · goopg `e453e3f2` · PG 18.3 · scale 100, c=50/j=50,
T=120 s per workload, fresh restart per workload, both engines uncapped.

## Headline: the read/write asymmetry

| Workload | goopg | PostgreSQL | gap |
|---|---:|---:|---:|
| **read** `-S` (select-only) | 91,783 TPS / 0.544 ms | 180,257 TPS / 0.276 ms | **1.96×** |
| **write** `-N` (simple-update) | 2,141 TPS / 23.35 ms | 15,806 TPS / 3.16 ms | **7.38×** |

The write path is **3.8× worse relative to the read path** (7.38 ÷ 1.96). The
read gap is a uniform ~2× "engine tax"; everything beyond that is
write-machinery-specific and is what this bundle attributes.

## Per-statement latency (`pgbench -r`, the decisive decomposition)

`-N` transaction = `BEGIN; UPDATE accounts; SELECT; INSERT history; END`:

| statement | goopg (ms) | PG (ms) | ratio |
|---|---:|---:|---:|
| `BEGIN` | 0.251 | 0.083 | 3.0× |
| `UPDATE pgbench_accounts` | 0.797 | 0.155 | 5.1× |
| `SELECT abalance` | 0.227 | 0.181 | 1.25× |
| `INSERT pgbench_history` | 0.899 | 0.132 | 6.8× |
| **`END` (commit + WAL flush)** | **21.200** | **2.619** | **8.1×** |
| total | 23.35 | 3.16 | 7.4× |

Two facts jump out:

1. **`END` is 91 % of goopg's transaction latency** (21.2 of 23.35 ms). Both
   engines are commit-flush-bound at c=50 (PG's `END` is 83 % of its total),
   but goopg's flush cycle is ~8× slower.
2. The **write statements** (`UPDATE` 5.1×, `INSERT` 6.8×) carry a much larger
   gap than the read statement executed inside the very same transaction
   (`SELECT` 1.25×) — the extra statement-time cost is specific to the write
   machinery, not to general per-statement overhead.

## Wait events (5 Hz `pg_stat_activity` sampling during `-N`)

- **PG**: 87.4 % `LWLock:WALWrite`, 5.5 % on-CPU, 4.7 % `Client:ClientRead`,
  2.1 % `IO:WalSync` (21,482 / 1,353 / 1,143 / 526 of 24,566 samples).
  PG backends overwhelmingly wait for the WAL write lock — the canonical
  group-commit-bound profile.
- **goopg**: all 28,425 samples returned **empty** wait-event columns — goopg's
  `pg_stat_activity` wait-event instrumentation does not cover the
  backend-flush wait (observability gap, noted in 04).

## HOT updates and relation growth during `-N`

| metric | goopg | PG |
|---|---:|---:|
| transactions in run | 256,927 | 1,896,847 |
| `n_tup_hot_upd` / `n_tup_upd` | (counters are a zero stub) | 1,729,445 / 1,896,847 = **91.2 % HOT** |
| `pgbench_accounts` heap growth | +2.5 MB (~10 B/txn) | +22.9 MB (~12 B/txn) |
| **`pgbench_accounts_pkey` growth** | **+166.8 MB (649 B/txn) — file doubles in 2 min** | **+0 bytes** |
| `pgbench_history` growth | +13.4 MB (52 B/row) | +99.2 MB (52 B/row) |

goopg's heap growth (~10 B/txn) shows its HOT-update + opportunistic-prune path
works about as well as PG's on the heap. The **primary-key index is the
anomaly**: the updated column (`abalance`) is not indexed, yet the pkey file
doubles during the run, while PG's stays byte-identical in size. Attribution in
02/03: dead index entries are never reclaimed on access (no
LP_DEAD/kill-prior-tuple "simple deletion"), so non-HOT updates keep splitting
leaves.

## AUX attribution probe (60 s `-N` re-runs on the post-headline data; disclosed non-headline)

| metric | goopg (under `strace -c`) | PG |
|---|---:|---:|
| TPS in probe | 1,243 (strace overhead + index bloat) | 8,852 (aux1: 15,586) |
| **WAL bytes / txn** | 2,460,934,744 B / 74,566 txns = **33,004 B/txn** | 1,517,055,392 / 531,649 = **2,853 B/txn** (aux1: 1,801) |
| WAL write rate | ~41 MB/s at 1.2 k TPS | ~25 MB/s at 8.9 k TPS |
| WAL fsync calls | 12,269 `fdatasync`, **avg 3.81 ms** | 28,190 (`pg_stat_io` object='wal') |
| group-commit width (txns/fsync) | ≈ **6.1** | ≈ **18.9** |
| **non-WAL `fsync` calls** | **6,734, avg 6.29 ms — 42.4 s of fsync time** | (commit path issues none) |

Measurement notes: goopg WAL bytes = `pg_stat_wal_io`
`wal_buffers_{flush,overflow}_drain_bytes` delta (goopg's `pg_stat_wal` view is
a zero stub and `pg_current_wal_lsn()` is not wired at runtime — both noted in
04); PG WAL bytes = `pg_current_wal_lsn()` delta; goopg fsync counts =
`strace -f -c -e trace=fdatasync,fsync` with the server as strace's child
(ptrace_scope=1 forbids attach); PG fsync counts = `pg_stat_io` fsyncs delta.
The PG aux2 TPS (8,852) is well below both its headline (15,806) and aux1
(15,586): the aux2 PG baseline is bloat-degraded by the preceding aux1 run on
the same data dir. Only the TPS-independent ratios (bytes/txn, width) are
meaningful outputs of the aux runs; a future probe should restart from fresh
data.

**Headline numbers per mechanism:**

- goopg writes **~12–30× more WAL per transaction** than PG: 33.0 KB vs
  1.8–2.9 KB by LSN delta (11.6× against the same-run aux2 PG figure, 18.3×
  vs aux1), or vs 1.06 KB by PG's headline `pg_stat_wal` (~31×). The meters
  also differ — goopg's is physical drain bytes (can re-drain partial pages),
  PG's is logical LSN advance — so the multiplier is soft; the *mechanism*
  (an 8 KB image in every heap record) is not.
- goopg's commit group is **~3× narrower** (≈6.1 vs 18.9 txns per WAL fsync).
- goopg's commit path additionally serializes a **~6.3 ms plain `fsync` about
  once per 11 commits (≈ every other WAL group)** — pg_xact CLOG segment
  write-back that PG does not perform at commit time at all; 6,734 of them in
  one 60 s run. (Attribution of the plain-fsync count to CLOG is inferred from
  code — `strace -c` reports no file paths — but the WAL path uses only
  `fdatasync` and expected non-CLOG fsyncs are ~2 orders of magnitude fewer.)

## CPU profiles (90 s in-run windows)

- **goopg `-N`**: only **1.93 cores** busy — the write path is wait-bound, not
  CPU-bound. Of the CPU it does use: 21 % raw syscalls, 11.3 % `memmove`
  (≈75 % of which is WAL record assembly: `emitWithPageHeaders`,
  `buildCanonicalSingleFPIBody`, `encodeRecordXLog`, `buildCanonicalPayload` —
  the 8 KB page-image copies), `updateViaIndex` 21.4 % cum
  (`tryApplyHOTUpdate` 12.3 %, canonical-WAL emits ~8.4 %,
  `maintainUniqueIndexesForInsert`+`BTree.Insert` ~3.4 %), protocol flush 14.5 %.
- **goopg `-N` block profile** (the substitute for the empty wait-event
  columns): **80.5 % of all block delay sits under
  `executor.(*Context).CommitTransaction`** — `runtime.selectgo` 43.2 % (WAL
  flush waiters parked in `walWriteLock.acquireOrWait`'s select) +
  `sync.(*Mutex).Lock` 32.8 % (the CLOG group-commit `flushMu`;
  `clog_groupcommit.go`: "goopg serialises ALL durable writes on a single
  flushMu"). This directly demonstrates "commit-bound, on exactly the two
  durable-write serialization points" — not some other global lock.
- **goopg `-S`**: **10.8 cores** busy — the read path is CPU-bound. Per-query
  costs dominate: `executeOneSimpleStmt` 24.9 % cum, operator-tree
  construction (`opOpen`) 16.5 %, `WriteReadyForQuery` protocol flush 18 %,
  socket write syscalls 17 %. This is the generic ~2× engine tax; it applies to
  the write path too but is dwarfed there by the WAL/commit costs.

## Consistency check (order-of-magnitude; mixed regimes)

END = 21.2 ms at 2,141 TPS ⇒ ~45 of 50 clients are parked in commit at any
instant. Reconstructing the cycle from the AUX numbers: WAL `fdatasync`
3.8 ms + drain `pwrite` + CLOG `fsync` amortized ~0.55 × 6.3 ≈ 3.5 ms ⇒
~8–9 ms per group cycle; a committing backend waits for the in-flight cycle
plus its own ⇒ ~13–18 ms, the right magnitude for the measured 21.2 ms.
Caveat: the per-call latencies come from the strace-perturbed aux2 run at
1,243 TPS while END is from the unperturbed headline run at 2,141 TPS, so
this is a magnitude check, not a validation — with an unperturbed fdatasync
of ~2–3 ms the arithmetic still lands in the mid-teens.
PG: width 18.9 × cycle ≈ 2.1 ms ⇒ END ≈ 2.6 ms — matching its 2.619 ms.

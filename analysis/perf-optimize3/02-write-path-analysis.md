# perf-optimize3 — 02: Why the write path trails PG so much more than the read path

The read gap (1.96×) and the write gap (7.38×) share one component and differ
by three. Ranked by measured impact:

## Mechanism 1 — 12–18× WAL volume: a full page image inside every WAL record

**Measured**: 33,004 WAL bytes/txn (goopg) vs 1,801–2,853 by LSN delta (PG) —
~12–18× (and ~31× vs PG's headline `pg_stat_wal` count; the multiplier is
meter-soft, the mechanism is not); ~41 MB/s of WAL at only 1.2–2.1 k TPS;
`memmove` alone is 11.3 % of goopg's `-N` CPU, ~75 % of it in WAL record
assembly.

What the volume does **not** explain: the per-call `fdatasync` latency. The
strace data shows a ~200 KB WAL fdatasync averages 3.8 ms while an ~8 KB CLOG
fsync averages 6.3 ms — per-sync cost on this host is dominated by a multi-ms
device floor, not by bytes. The volume's costs are the **CPU** to assemble and
copy it, the **drain** work per cycle, and — through slower statements and
cycles — the **group width**; attacking volume (C1) does not shrink the fsync
floor itself.

goopg's canonical (PG-format) **heap** WAL records are built by
`buildCanonicalSingleFPIBody` (`internal/catalog/canonical.go`): every heap
record — insert, HOT update (logged as inplace), delete, opportunistic prune —
embeds a **full 8 KB page image**, by design ("a standby simply restores the
page rather than re-deriving the tuple bytes"). On top of that, each of these
heap writes is **logged twice**: the executor emits the canonical FPI record
*and* the native logical record (`markHeapHotUpdateDirty` →
`MarkDirtyChangeRecord`, which is FPI-gated), back-to-back. User-index btree
*leaf inserts* are the exception on this hot path — they go through the
FPI-gated `MarkDirtyChangeRecord` with item-level `EncodeBtreeInsert` payloads
(the unconditional-FPI canonical btree builder serves only system-catalog
indexes) — but btree *splits* log 2–3 full page images each
(`EncodeBtreeSplit` carries left+right+sibling pages). A pgbench `-N`
transaction therefore logs roughly:

| record | goopg | PG (steady state) |
|---|---:|---:|
| UPDATE accounts (HOT) | 8 KB FPI (+ inline 8 KB page copy at emit) + native record | `xl_heap_update`, ~60–90 B |
| INSERT history | 8 KB FPI + native record | `xl_heap_insert`, ~80–110 B |
| prune (when it fires) | 8 KB FPI each | tens of bytes, amortized |
| btree leaf insert (non-HOT) | item bytes (+ first-touch FPI); a split logs 2–3 page images | tens of bytes; splits rare (see M3) |
| commit | ~34 B | ~34 B |
| **total** | **~16–33 KB** | **~0.2–0.5 KB** (+ rare post-checkpoint FPIs) |

PostgreSQL attaches an FPI to a record **only when the page is dirtied for the
first time after a checkpoint** (`XLogRecordAssemble`'s `page LSN ≤ RedoRecPtr`
test); with `checkpoint_timeout=24h` the steady-state FPI rate in this
workload is near zero. goopg has the same once-per-checkpoint machinery at the
buffer-pool level (`Pool.maybeEmitFPI` / `MarkDirtyChangeRecord`,
`fpiSinceCheckpoint`) — the btree leaf-insert path uses it — but the canonical
**heap** record emitters bypass it by embedding their own unconditional FPI.

Consequences compound down the whole write path:

- statement time: each write statement allocates and copies 8 KB+ per record
  (`UPDATE` 0.797 ms vs PG 0.155 ms; `INSERT` 0.899 vs 0.132);
- ring/drain: 33 KB/txn must transit the WAL buffer and be `pwrite`-drained
  under the flush cycle;
- fsync: each group cycle drains and syncs ~width × 33 KB; the byte volume
  adds to, but does not dominate, the per-call fsync floor (see above).

## Mechanism 2 — a synchronous CLOG fsync on the commit path

**Measured**: 6,734 plain `fsync` calls in a 60 s run — ~1 per 11 commits,
i.e. roughly every other WAL group — averaging **6.29 ms** (42.4 s cumulative)
competing with WAL `fdatasync` on the same device queue. The block profile
confirms this is one of exactly two serialization points backends wait on:
32.8 % of all block delay is the CLOG `flushMu` (vs 43.2 % parked on the WAL
write lock).

goopg's CLOG group-commit leader (`applyGroupBatchLocked`,
`internal/mvcc/clog_groupcommit.go`) performs an **eager durable write-back**
of dirty CLOG pages: `clogBufferPool.flushDirty` writes each dirty page and
**fsyncs the pg_xact segment** — on the commit path, once per CLOG group
(measured: ~once per 11 commits ≈ every other WAL group).

PostgreSQL never does this. A commit sets two bits in an in-memory SLRU buffer
(`TransactionIdSetPageStatus`, `clog.c`); the page reaches disk only at
checkpoint or SLRU-eviction (`SlruPhysicalWritePage`), and crash-safety comes
from replaying the WAL commit record. The commit path's only durable write is
the WAL flush itself.

Effect: goopg's commit path carries **two serialized durable-write waits**
(WAL fdatasync 3.8 ms every cycle + CLOG fsync 6.3 ms roughly every other
cycle, amortized ~3.5 ms) where PG carries one (~2.1 ms).

**Storage dependence**: this mechanism's weight is proportional to the
device's fsync floor. On this WSL2/ext4 host the floor is multi-ms, making M2
comparable to M1; on low-latency storage (NVMe with power-loss-protected
cache, ~50 µs fsync) M2 largely vanishes while M1's byte/CPU costs remain —
the ranking, and the C2-first sequencing in 04, should be re-checked on the
target hardware before committing to it.

## Mechanism 3 — B-tree dead-entry accumulation (no on-access cleanup)

**Measured**: `pgbench_accounts_pkey` grows **+166.8 MB in 2 minutes**
(649 B/txn) on goopg; **+0 bytes** on PG. goopg heap growth (~10 B/txn) shows
HOT+prune works on the heap — the index is the anomaly.

The updated column is not indexed, so HOT-eligible updates insert no index
entry. But whenever a page lacks room and the update goes non-HOT,
`maintainUniqueIndexesForInsert` inserts a new pkey entry for the same key,
and the **old entry is never removed until VACUUM**: goopg's btree
(`internal/access/btree/`) has no LP_DEAD hinting, no kill-prior-tuple, and no
split-time "simple deletion" pass. PostgreSQL's nbtree marks dead entries
LP_DEAD during scans (`kill_prior_tuple`) and purges them **before splitting**
(`_bt_simpledel_pass`, nbtinsert.c) — which is precisely why its pkey does not
grow at all in this workload.

Dead entries accumulate → leaves split (new pages ≈ 8 % of txns — an upper
bound on the split rate, since a cascading split allocates more than one page)
→ each split logs 2–3 full page images (`EncodeBtreeSplit`, compounding
Mechanism 1) → descents and range scans lengthen over time. This mechanism
makes goopg's write throughput **degrade with runtime**, not just run slow.

## Mechanism 4 — the shared ~2× engine tax (what the read path measures)

goopg `-S` is CPU-bound at 10.8 cores / 91.8 k TPS vs PG's 180 k. Profile:
operator-tree construction per query (`opOpen` 16.5 %), per-message protocol
flush (`WriteReadyForQuery` 18 %, socket writes 17 %), snapshot capture. No
lock contention. This tax applies to every statement of the write path too
(visible as `BEGIN` 3.0×, `SELECT`-in-txn 1.25×) but explains only ~2× of the
7.4× write gap.

## Why the group-commit width difference is a symptom, not a cause

goopg batches ~6 commits per WAL fsync (strace-regime measurement; the
unperturbed width is somewhat higher but bounded by the same equilibrium); PG
batches 18.9. Width is emergent:
arrivals during one flush cycle join the next group. goopg's cycle is longer
(33 KB/txn drain + 3.8 ms fdatasync + an amortized ~3.5 ms CLOG fsync) **and** its arrival
rate is lower (statement-time costs above), so fewer commits accumulate per
cycle even though each cycle is slower. Fixing Mechanisms 1–3 shortens the
cycle and raises the arrival rate — width then grows on its own, exactly as
PG's does. The wal-backend-flush rewrite (docs/design/wal-backend-flush/)
already gave goopg PG's lock discipline; what it flushes per cycle is the
remaining difference.

## Sanity decomposition of the 7.4× (illustrative only)

Roughly: ~2× engine tax (M4) × ~2–3× commit cycle (M2 + the device fsync
floor) × ~1.5–2× statement-time WAL assembly and index-maintenance overheads
(M1 + M3) ≈ 6–12×, bracketing the observed 7.4×. The factors overlap (M1
inflates both statement time and cycle time), so this is an illustration, not
an independent-factor proof. The evidence that no hidden mechanism lurks is
the **block profile**: 80.5 % of all block delay sits under
`CommitTransaction`, split across exactly the two serialization points that
M1/M2 describe (WAL write lock 43.2 %, CLOG flushMu 32.8 %).

# 08-02 — Shard the buffer-pool miss path

status: design · date: 2026-07-14 · base: `a640d2b0` · gates: G-race, G-crash,
G-tpch, G-perf → [README](README.md)

## 1. Problem and numbers

At scale 500 (data > `shared_buffers`), the miss path is the new write-path
sink after the commit wait. From 07-02 (`scale500b_2159d329`, `-N` block total
4,650.2 s):

| sink | delay | note |
|---|---:|---|
| `Pool.pinSlow` (all callers) | 400.2 s = 8.6 % | serialized on the **global `pinMu`** |
| — `pinLoad` → `evictVictim` | 172.0 s (+10.7 s via `PinNew`) | slot-mutex 150.1 + `flushSlot` 32.6 |
| — `pinLoad` mutex + `ReadBlock` | 92.1 + 17.1 s | |
| — pinSlow-level slot mutex | 118.8 s | |

Total pool-internal mutex delay ≈ **361 s** across `evictVictim`/`pinLoad`/
`pinSlow`. On the read path (`-S`), the same miss machinery adds a fresh 186.9 s
= 14.1 % of block delay (07-02), ~18 µs/query, that scale 100 never paid.

06's #1 write finding — the per-file `relFile.readBlock` mutex, 647.8 s at scale
100 — **dissolved** at scale 500 (17.1 s) because misses spread across many
segment files instead of concentrating on the freshly-doubled pkey file
(07-02). So the durable target is not the per-file mutex but the **global
`pinMu`** that serializes every cache miss regardless of which relation missed.

## 2. Current-code map (verified at `a640d2b0`)

- **`Pool.pinMu sync.Mutex`** — `internal/storage/bufpool.go:209`. One global
  mutex for the entire slow (miss) path. Comment at :202: "Slow path (cache
  miss / IO-inflight wait): must hold pinMu."
- **`Pool.Pin(tag)`** — `bufpool.go:1659`: fast path (hit) is lock-light; on
  miss calls `pinSlow`.
- **`Pool.pinSlow(tag)`** — `bufpool.go:1700`: `p.pinMu.Lock(); defer
  Unlock()` (lines 1701–1702) held across the **entire** miss resolution: the
  `Lookup` retry loop, the IO-inflight wait (drops/re-takes `pinMu` around a
  per-slot semaphore, :1714/:1720), and the load.
- **`Pool.pinLoad(tag)`** — `bufpool.go:1745`: "Called under pinMu." Picks a
  victim, calls `evictVictim`, then `ReadBlock`.
- **`Pool.evictVictim(idx, wasDirty, oldTag)`** — `bufpool.go:1373`: **already
  releases `pinMu` around the dirty-victim flush** — `p.pinMu.Unlock()`
  (`:1400`), `s.contentMu.Lock()` (`:1401`), `p.flushSlot(...)` (`:1406`),
  `s.contentMu.Unlock()` (`:1410`), `p.pinMu.Lock()` (`:1411`); same-victim
  pinners park on the per-slot semaphore (`slotWaiters`/`slotSema`, `:1424`–1427).
  So the flush runs under the **per-slot `contentMu`**, not `pinMu`. The 07-02
  profile's 150.1 s "slot mutex" is therefore the `pinMu` lookup-loop +
  post-flush **re-acquire** contention (the global lock), and the 32.6 s is the
  `flushSlot` device write itself under `contentMu` — *not* a flush held under
  `pinMu`.
- **`relFile.readBlock(blk, buf)`** — `internal/storage/smgr.go:826`: per-file
  `r.mu.Lock()` + `lockBlock(blk)` around `f.ReadAt` (the "keeps Extend/Close
  coherent" note is at `smgr.go:821`). The 06-era concentration point; still
  present but no longer the bottleneck at scale.

## 3. PostgreSQL reference

- `src/backend/storage/buffer/bufmgr.c` — `BufferAlloc`, `StartBufferIO`,
  `GetVictimBuffer`. PG shards buffer lookup with **`NUM_BUFFER_PARTITIONS`
  (128) buffer-mapping partition locks** (`BufMappingPartitionLock`, keyed by
  the buffer tag hash), not a single lock. IO is coordinated per-buffer via the
  `BM_IO_IN_PROGRESS` flag + a per-buffer `io_in_progress` condition, never a
  global mutex held across the read.
- `src/backend/storage/buffer/freelist.c` — the clock-sweep victim selection
  runs under a lightweight `buffer_strategy_lock` for the sweep hand, not a lock
  held across the victim's write-back.

The two PG lessons: (a) partition the mapping lock by tag hash; (b) never hold
an allocation lock across the victim's fsync/write.

## 4. Target design

### 4.1 Partition `pinMu` by tag hash

Replace the single `pinMu` with `pinShards [N]struct{ mu sync.Mutex }` (N =
128, PG parity), sharded by `hash(tag) % N`. `pinSlow`/`pinLoad` take only the
shard for their tag. The buffer-map (`states`/lookup) must likewise be
partition-safe: either a sharded map per partition or a concurrent map keyed by
tag, with the invariant that a tag's slot transitions are always done under its
own shard lock.

### 4.2 The victim write-back is already off the allocation lock

Unlike PG's global buffer-mapping problem, goopg **already** drops `pinMu`
around the dirty-victim flush (`evictVictim` §2: `pinMu.Unlock()` `:1400` →
`flushSlot` under `contentMu` → `pinMu.Lock()` `:1411`), and same-victim pinners
already park on the per-slot semaphore. So the "move the fsync off the
allocation lock" work is done; the residual cost the profile shows is that every
miss still **re-acquires the single global `pinMu`** after its flush (and holds
it for the whole `Lookup`/allocation loop). That global re-acquire is the 150.1 s
"slot mutex" — partitioning `pinMu` (§4.1) is what removes it. This slice
therefore has one job (partition the global lock), not two.

The one remaining per-slot refinement: `contentMu` is per-slot already, but a
victim's `flushSlot` still blocks a *different* backend that wants to load into
that same physical slot; with partitioning, such collisions drop to within-shard
only. No new IO-in-progress handshake is needed — it exists.

### 4.3 Decision log

- **D1 — 128 partitions (PG parity).** Rejected: a per-P shard (GOMAXPROCS) —
  simpler but couples partition count to CPU and can under-partition on
  many-core hosts; PG's fixed 128 is well-validated. A GUC is a follow-up.
- **D2 — keep the per-file `readBlock` mutex.** It is not the scale-500
  bottleneck (07-02) and it guards `Extend`/`Close` coherence
  (`smgr.go:821` comment). Sharding it is a separate, lower-priority change; do
  not bundle. Recorded as O-BP-1.
- **D3 — do not re-solve the off-lock flush; it exists.** goopg already runs
  `flushSlot` with `pinMu` released (§2/§4.2), so the 32.6 s `flushSlot` in the
  profile is genuine device-write time under the per-slot `contentMu`, not a
  fsync serialized under the allocation lock. The lever is the **global `pinMu`
  itself** (the 150.1 s), which partitioning (§4.1) addresses. Correcting this
  is why the slice count is 1 (partition) + 1 (perf), not a redundant off-lock
  slice.

## 5. Invariants and failure modes

- **I1 — one loader per tag.** A tag is loaded by exactly one backend; others
  wait on the existing per-slot semaphore (`slotWaiters`/`slotSema`).
  Partitioning must not let two backends both miss the same tag and both load it
  (double-buffering / dirty-write race). The shard lock + the existing
  IO-inflight slot bit enforces this per tag.
- **I2 — no cross-shard lock nesting.** A backend holds at most one pin shard
  at a time; victim selection that would touch another shard's slot must be
  ordered (lowest shard index first) or avoided. F-BP-1 guards deadlock.
- **F1 — deadlock from multi-shard hold.** If eviction ever needs two shard
  locks (victim in a different shard than the target tag), acquire in a fixed
  global order; `-race` + the isolation suite is the gate.
- **F2 — WAL-before-data preserved under partitioning.** `flushSlot` flushes WAL
  to the page LSN before writing the page (already off-lock under `contentMu`);
  partitioning `pinMu` must not disturb that ordering (it doesn't — the flush
  path is unchanged, only the surrounding allocation lock is split). G-crash gate.

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S1 | partition `pinMu` | `pinShards[128]` + tag-hash routing; buffer-map made partition-safe; `pinSlow`/`pinLoad`/`evictVictim` (incl. its post-flush re-acquire at `:1411`) take one shard. The dirty-victim flush stays off-lock exactly as today (`contentMu` + per-slot semaphore) — only the global lock is partitioned. | G-race, G-crash, G-unit, G-tpch |
| S2 | perf acceptance | re-measure at scale 500 (`-M prepared`); the ~361 s pool-internal mutex delay (dominated by the 150.1 s global `pinMu`) should drop; `-S` reload wait share should shrink. | G-perf |

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| buffer-pool concurrency/eviction tests | `internal/storage/bufpool_test.go` | S1 |
| smgr read/extend coherence | `internal/storage/smgr_test.go` | S1 (guard) |
| crash/recovery (dirty-victim durability) | `internal/initdb/`, `TestKillKillRecovery` | S1 |
| TPC-H spotcheck (miss-heavy scans) | `scripts/tpch-spotcheck.sh` | S1 |

## 8. Performance verification

`run_rw50.sh` at **scale 500** (the regime where this matters; 07 is the
baseline), `-M prepared`, `GOOPG_BLOCK_PROFILE_RATE=1`,
`GOOPG_MUTEX_PROFILE_RATE=1`. Success: pool-internal mutex block delay (the
~361 s) drops materially; `-S` `pinSlow` reload-wait share (14.1 %) shrinks;
no TPS regression at scale 100 (the partition overhead must be free when the
working set fits).

## 9. Open questions

- **O-BP-1** — Does the per-file `readBlock` mutex (`smgr.go:826`) need
  sharding too once the global `pinMu` is partitioned, or does it stay benign?
  Re-profile after S1.
- **O-BP-2** — Partition-safe buffer map: sharded-map-per-partition vs. a single
  concurrent map. The former keeps each partition's metadata lock-local; the
  latter is simpler but reintroduces a shared structure. Prototype both under
  the scale-500 miss load.
- **O-BP-3** — Should partition count be a GUC (`shared_buffers`-proportional)
  rather than a fixed 128? Defer until S3 shows whether 128 saturates.

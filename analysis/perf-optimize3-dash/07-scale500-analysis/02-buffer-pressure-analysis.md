# 07-02: Where the waits moved under buffer pressure

profiles: `runs/scale500b_2159d329/profiles/` (CPU = 90 s in-run window;
block/mutex are cumulative since the pre-workload restart, ~105 s). goopg
wait-event columns are still empty (25,519/25,519 blank `-N` samples) — as
in 06, block profiles substitute. PG waits from the 5 Hz sampler
(`pg_[NS].waits`).

## Write path (`-N`): same shape, plus an eviction bill

goopg `-N` block profile total 4,650.2 s. Independent quantities (the
`aio.methodWorker` idle-wait 344.2 s / 7.4 % is worker-pool idling, not
query delay, and is excluded; rows below are non-additive where noted):

| block-delay sink | @500 | @100 (06) |
|---|---:|---:|
| `CommitTransaction` → `walWriteLock.acquireOrWait` (commit flush wait) | 3,072.9 s = **66.1 %** | 59.2 % |
| `updateOp.updateViaIndex` (whole statement) | 596.9 s = 12.8 % | 17.7 % |
| `Pool.pinSlow` (all callers; inside updateViaIndex mostly) | 400.2 s = 8.6 % | 15.6 % |
| — of which: `pinLoad` → `evictVictim` | 172.0 s (+10.7 s via `PinNew` = 182.7 s total, splitting into slot mutex 150.1 + `flushSlot` 32.6) | ~0 (no eviction at scale 100) |
| — of which: `pinLoad` mutex + `ReadBlock` | 92.1 + 17.1 s | — |
| — of which: pinSlow-level slot mutex | 118.8 s | — |
| per-file `relFile.readBlock` mutex (06's #1 UPDATE finding; same samples as the `ReadBlock` 17.1 s above, not additive) | **17.1 s** | 647.8 s |

Three observations:

1. **The commit wait grew to 66 % and PG mirrors it** (87.5 % `LWLock:WALWrite`
   + 2.1 % `IO:WalSync` of 24,868 samples, vs 86.8 % at scale 100). This is
   the FPI regime from 01: ~0.5+ full-page images per transaction inflate
   every commit group's byte volume on both engines. PG shows almost **no**
   read waits on `-N` (10 `IO:DataFileRead` samples = 0.04 %) — its
   scale-500 write cost arrives as WAL volume, not as misses.
2. **06's per-file `readBlock`-mutex serialization dissolved** (647.8 →
   17.1 s) without any code change to that path. Plausible mechanism (not
   proven): at scale 100 the only `-N` misses were re-reads of the freshly
   doubled pkey file — one file, one mutex; at scale 500 misses spread
   across many 1 GB segments of heap+index, so the per-file mutexes no
   longer collide. The wait did not vanish — it moved down into the
   eviction machinery (next).
3. **The new eviction bill is ~8.6 % of block delay, and most of it is
   pool-internal mutexes, not IO.** `evictVictim` spends 150.1 s in slot
   mutexes vs only 32.6 s actually flushing victims (`flushSlot`: WAL
   barrier flush + page write-back — the C3-S3 hint-barrier interplay is
   inside this and is *not* a major cost once the `2159d329` retry fix
   removed its fatality). `pinLoad`'s own reload `pread`s ride the OS page
   cache and barely register (`ReadBlock` 17.1 s).

CPU on `-N` is again not the constraint (554.5 s / 90 s ≈ 6.2 cores at
6,630 TPS): syscalls 21.4 %, `futex` 5.6 %; the notable engine entry is new —
`storage.(*FSM).GetCandidates` **4.9 % cum** (4.65 % flat; free-space candidate scan;
grows with relation size, worth a look before larger scales), then
`captureSnapshot` 4.4 %.

**Why goopg warms up so slowly (the 3.7 k → 7.7 k ramp).** Filling the
327 k-slot pool (2560 MB / 8 KB) takes ~300 k+ misses; at 4–6 k TPS with a
couple of misses per transaction, tens of seconds pass before the hot set
is resident, and during that window each miss pays the pinSlow mutex +
eviction path above while every first touch also emits an FPI. PG fills the
same-size pool through cheap page-cache `pread`s without a comparable
per-miss serialization and is at steady state within the first 30 s tick.
This is why the 120 s average (1.91×) and the steady-state (1.51×) gaps
differ so much at this scale.

## Read path (`-S`): still CPU-bound; pressure adds an 18 µs reload wait

goopg `-S` block profile total 1,328.3 s:

| block-delay sink | @500 | @100 (06) |
|---|---:|---:|
| lockmgr GLOBAL mutex (`acquireRelLockMaybeTransient`: `acquire` 366.1 + `Release` 210.7) | 576.8 s = **43.4 %** ≈ 56 µs/query ≈ 9.6 % of latency | 53.1 %, ≈ 68 µs, 12 % |
| `indexScanOp.Next` → `Pool.Pin` → `pinSlow` (buffer misses) | 186.9 s = **14.1 %** ≈ 18 µs/query (84.8 % `pinLoad` = actual reload; 15.2 % mutex) | ~0 |

(Nesting note: the `OpIterator.Next` 13.9 % row in the raw profile is the
same Pin wait as the pinSlow row — not additive.)

The CPU shape is essentially **unchanged** from 06-03 (909.1 s / 90 s ≈
10.1 cores at 85,324 TPS; 06: 907.9 s at 89,955):

| bucket | @500 | @100 (06) |
|---|---:|---:|
| socket syscalls | 19.8 % | ~20.4 % |
| protocol assembly + flush | 17.8 % | ~19.2 % |
| `opOpen` (incl. eager probe `Rescan` 10.3 %) | 16.8 % | 19.5 % gross / probe 9.6 % |
| planner | 12.9 % | 13.1 % |
| parser | 6.9 % | 7.1 % |
| `mallocgc` (cum) | 14.7 % | 14.7 % |
| `maybeForceGCAfterCommit` | 5.4 % | 5.9 % |
| GC sweep/mark (`sweepone`+`gcBgMarkWorker`) | 5.1 % | 5.6 % |

PG `-S` waits at scale 500 (4,783 samples): `Client:ClientRead` 60.8 %,
CPU 33.4 %, `LWLock:BufferMapping` 3.2 %, `IO:DataFileRead` 2.5 %. PG
remains substantially client-bound (the 06-03 fairness caveat stands — PG's
ceiling is understated), but pressure is visible: PG lost 11.8 % TPS at
scale 500 vs goopg's 5.1 %.

**Why the read gap narrowed (2.03× → 1.89×) — and why not to celebrate.**
goopg's working set (~3.0 GB) is ~85 % pool-resident at 2560 MB; PG's
(~7.8 GB) is ~33 % (README regime point 2). PG therefore misses far more
often; even at page-cache prices that shows up (BufferMapping +
DataFileRead + the extra buffer-header traffic). goopg's compact heap
(44 vs 134 B/row) is a genuine architectural advantage in exactly this
regime — but the narrowing is the *asymmetry* speaking, not a goopg
read-path improvement. At equal byte-pressure (goopg ≈ scale 1200+) the
comparison would tilt back.

## Does scale 500 change the 06 conclusions?

- **06-02's #1 write finding (per-file readBlock mutex behind UPDATE) was a
  scale-100 artifact in its specifics** — the serialization is real but its
  concentration on one file came from the pkey-doubling re-read pattern. The
  general lesson (per-miss path serializes too much) survives: at scale 500
  the same class of cost reappears as pool-internal slot mutexes on the
  miss/eviction path (≈361 s across `evictVictim` 150.1 + `pinLoad` 92.1 +
  `pinSlow` 118.8).
- **06-03's Go-vs-C attribution stands.** The read-path CPU shape is
  byte-for-byte the scale-100 one; buffer pressure added a wait
  (page-cache `pread` + memcpy) that a C engine pays identically. If
  anything, scale 500 strengthens the "architecture over language" reading:
  goopg's row compactness — an architecture property — bought it a ~2.6×
  smaller working set and the observed gap narrowing.
- **C5 (pipelined commit groups) gains priority.** With FPIs inflating
  every group, amortization quality matters more: END is again 52 % of the
  excess (01). The C5 ceiling estimate (END → PG-equal) now applies to a
  3.3 ms PG END: removing the 1.88 ms END excess from the averaged 7.54 ms
  gives 5.66 ms ≈ **8.8 k TPS** (from 6.6 k), more at steady state.

## Ranked next fixes (buffer-pressure regime)

1. **Shard/shorten the pool-internal miss path** (`pinSlow`/`pinLoad`/
   `evictVictim` slot+partition mutexes, ≈361 s of `-N` block delay; also
   15 % of the `-S` reload wait). This is also the main lever on the 120 s warm-up ramp.
   PG analog: buffer-mapping partitions + lock-free clock sweep.
2. **C5 — pipelined commit groups** (52 % of `-N` excess; FPI regime raises
   the payoff; design `05-improvement-designs/05-c5-pipelined-commit-groups.md`).
3. **C3 residual: migrate UPDATE-probe `RangeScan` callers to kill
   collection** (ledgered) — at scale 500 the dead-entry pkey doubling
   (+832 MB in 120 s) directly raises the miss rate; it is no longer just
   disk waste.
4. **lockmgr global-mutex sharding** (unchanged from 06; still 43 % of `-S`
   block delay, ~10 % of read latency).
5. `FSM.GetCandidates` (4.9 % of `-N` CPU, new at this scale) — verify it
   doesn't go superlinear at larger relations.

Not ranked: warm-up-specific prewarming (pg_prewarm analog) — real
deployments run longer than 120 s; fix #1 addresses the same per-miss cost
without a new mechanism.

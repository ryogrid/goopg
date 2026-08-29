# 07 — Remaining candidates: H, F, E landed; D and G declined

[06](06-post-implementation-results.md) landed candidates A, B and C and
re-ranked what was left. This chapter closes out that list: **H, F and E are
implemented** (commit `1a395f2bf`), and **D and G are deliberately not**, each
for a reason the measurements support.

- **Base commit**: `1a395f2bf` (branch `perf-opt-take3`)
- **Compared against**: `ac0fd1267` (the A/B/C tip from 06)
- **Date**: 2026-08-30
- **Raw artifacts**: `tmp/take3/runs/{ab-S3,ab-N3,HF-S}/`

## 1. Headline

Alternating A/B, one cluster, fresh restart per run, binaries interleaved.

| workload | `ac0fd1267` | with H+F+E | delta | significant? |
|---|---:|---:|---:|---|
| `-S` select-only | 114,890 tps (sd 363) | **117,182 tps** (sd 372) | **+2.0 %** | **yes — ranges disjoint** |
| `-N` simple-update | 11,072 tps (sd 933) | 11,529 tps (sd 833) | +4.1 % | **no — ~0.7 SE, ranges overlap** |

`-S` ranges: base [114,652, 115,308], new [116,832, 117,572] — no overlap.

**The `-N` number is not claimed.** +4.1 % is well inside a workload whose
noise floor is ~8 %, which is exactly where candidate E's predicted 2–4 %
ceiling was always going to land. E is justified by its mechanism (§2.3) and by
correctness, not by this figure.

### Against the oracle

goopg's select-only is now **1.02× of PostgreSQL 18.3's 114,388 tps on this
host — i.e. marginally ahead.** Two caveats belong with that sentence:

- The `bpchar` asymmetry from [00 §5](00-methodology.md) still applies: goopg's
  `pgbench_accounts` heap is **3.08× smaller** than PostgreSQL's because pgbench
  blank-pads `filler char(84)` and goopg stores `bpchar` trimmed. That favours
  goopg on a scan-bound read workload.
- PostgreSQL's 114,388 was measured on its own cluster earlier in the study.
  Absolute numbers drift with cluster state; the goopg-vs-goopg A/B above is the
  rigorous comparison, the goopg-vs-PG ratio the indicative one.

Cumulative for the whole take3 series on `-S`: **93,083 → 117,182 tps (+25.9 %)**.

## 2. What landed

### 2.1 H — plan-cache admission filter

The problem [06 §4](06-post-implementation-results.md) exposed: under the simple
protocol every `planCacheKey` is unique, so every `Get` missed and every `Put`
then took the shard **write** lock and evicted a live entry — **66.05 % of all
remaining `-S` mutex delay**.

`internal/postmaster/plancache.go` now runs a lock-free doorkeeper (8192 slots,
one `atomic.Swap`) in front of `Put`: a key earns a cache slot only on its
**second** `Put`. One-shot SQL never reaches the write lock, and the 512 entries
stop being churned by literal noise, so genuinely repeated statements survive
instead of being evicted by traffic that can never hit.

| measure | before | after |
|---|---|---|
| `planCache.Put` in the mutex profile | 12.80 s (66.05 % cum) | **absent** |
| `planCacheKey` CPU | 14.36 s | 9.44 s |

Skipping a `Put` is always safe: the key maps deterministically to the same
plan, so a skipped store costs at most one cache hit, never correctness. Marks
deliberately **survive `Invalidate()`** — "this SQL has been seen before" stays
true across DDL, so a hot statement is re-admitted on its next execution rather
than re-learning.

This is explicitly **not** the de-scoping that ledger row `:1332` (M0132-S13)
warns against. goopg's simple path still reads the cross-session cache, and
repeated SQL is still cached; only provably-single-use keys are kept out.

### 2.2 F — GUC lookup

[05 §F](05-improvement-plan.md) predicted the `strings.ToLower` cost was in the
per-column codec path. **The profile said otherwise**, and the measurement won:
`SessionRegistry.Get` and `Registry.Get` were **4.5 % of read-path CPU** and
between them dominated `strings.ToLower` (54 % of its samples) *and*
`mapaccess2_faststr` (43 %).

A global hit cost **two** `ToLower` scans — one inside `lookupVariable` →
`Registry.Get`, one for the local/session key — plus three map lookups.
`Registry.Get` now probes the map with the raw name first (registration always
stores `strings.ToLower(v.Name)`, `guc.go:541`, so a raw hit is exact), and
`SessionRegistry.Get` lowercases once and threads the key through a `getLower`
variant. `ToLower` does not allocate for an already-lowercase string, but it
still scans every byte and does not stop early.

| frame | before | after | delta |
|---|---:|---:|---:|
| `strings.ToLower` | 46.70 s (1.89 %) | **14.69 s (0.92 %)** | **−69 %** |
| `misc.(*Registry).Get` | 38.63 s (1.56 %) | **absent** (inlined fast path) | — |
| `misc.(*SessionRegistry).Get` | 72.35 s (2.92 %) | 40.25 s (2.53 %) | −44 % |
| `runtime.mapaccess2_faststr` | 72.11 s (2.92 %) | 48.01 s (3.01 %) | −33 % absolute |

### 2.3 E — CLOG terminal-status cache

`clogBufferPool` guards its whole page set with one mutex and `getStatus` takes
it on **every tuple visibility test** — 13.9 % of `-N` mutex delay
([03 §2](03-contention.md)). Upstream fronts the same lookup with
`cachedFetchXid` (`postgres/src/backend/access/transam/transam.c:33-62`),
per-*backend* because PG backends are processes; goopg's are goroutines sharing
one `CLog`, so `internal/access/transam/clog_statuscache.go` is a 4096-slot
sharded array read with a single atomic load. XID and status are packed into one
`uint64` so a reader cannot observe a torn pair.

**Two correctness constraints, both found by tests rather than by reasoning.**

1. **Only terminal statuses may be cached.** `TxnStatusSubCommitted` is
   explicitly non-terminal (`clog.go:27-31` — the parent resolves it) and
   `Unknown` is precisely the status that transitions. Both are excluded by
   construction.
2. **"Terminal" does not hold during recovery.** `MarkUnknownAsAborted` sweeps
   in-progress lanes to `Aborted`, and WAL replay then stamps the durable
   `Committed` over them. A cache filled by a read *between* those two steps
   pinned the swept `Aborted` — **losing a committed transaction**. This broke
   `TestReplayCLogFromWAL_OverridesMarkUnknownAsAborted` in the units gate.

   Fixed by reconciling the cache at the `setStatusWithLSN` write choke point
   (unconditionally, so an idempotent re-stamp still repairs a stale entry), and
   dropping it wholesale in `MarkUnknownAsAborted` and `TruncateCLOG` — the
   latter because a truncated XID range becomes reusable after wraparound, where
   a stale "committed" would make an in-progress transaction visible.

The slot-collision test also caught a packing bug in the first version: the
valid flag sat at bit 63 and **aliased XID bit 31**, so `w>>32` never matched the
stored XID and every lookup missed. Silent, and it would have made the whole
optimisation a no-op rather than a bug. The flag now lives at bit 8, below the
XID field.

## 3. What was declined, and why

Leaving these as open TODOs would misrepresent them: both were examined against
the current profile and found not worth doing *now*.

### D — `OpIndexScan` concrete operator kind: **refuted by measurement**

[05 §C](05-improvement-plan.md) argued the case for `OpIndexScan` was not the
itab (0.38 % of CPU) but the per-row cleanups it would unlock — `Pool.Pin` /
`Unpin` called per row rather than per page, and a per-column enum-map probe per
row (`operators_index.go:602-618`).

**The current profile refutes that premise:**

| claimed cost | measured |
|---|---|
| per-row enum probe (`catalog.LookupEnum`) | **does not appear in the profile at all** |
| `indexScanOp.Next` total | 3.13 % cum |
| `Pool.Pin` total | 1.19 % cum (its `sharedHitCount` increment does not surface separately) |
| adapter dispatch overhead | 0.38 % |

Migrating an operator kind into the concrete slab is a medium-effort change to
the executor's hot path with real correctness surface. Doing that for a ceiling
under half a percent, when the cleanups it was supposed to unlock do not measure,
is exactly the "ceiling exceeds its measured share" error the review caught in
the first draft of 05. **Deferred until a workload shows it mattering.**

The same reasoning retires the last two F sub-items: `Pool.sharedHitCount`
sharding is worth well under 0.5 %, and `FSM.GetCandidates` (2.64 %) is
write-path only, i.e. under `-N`'s ~8 % noise floor.

### G — btree dead-entry reclamation: **still blocked, by design**

Unchanged and still real: `pgbench_accounts_pkey` grows **+104 B/txn** where
PostgreSQL's grows **0** ([01 §5](01-results.md)). It is a space and long-run
stability problem, not a throughput one at this horizon.

No mechanism is proposed, and that is deliberate rather than an omission. The
obvious one — on-probe `LP_DEAD` kills — was implemented, gated and **reverted**
(`bdaa325a4` → `4998c81b9`): no benefit on uniform pgbench `-N`, and an **~18×
pkey regression** on a re-probe-heavy A/B, because marking dead *duplicate*
entries `LP_DEAD` defeats btree deduplication's posting-list consolidation. They
are competing space strategies and dedup wins for duplicate churn.

A real fix must be **dedup-aware** (a no-space purge) or a **background btree
vacuum**. Either is a design project in its own right, not a candidate to be
knocked out alongside micro-optimisations, and shipping the reverted mechanism
again would be a regression.

> **RESOLVED (2026-08-30) — and the premise above was wrong.** Candidate G was
> taken up in
> [`../btree-index-bloat-reclaim/DESIGN.md`](../btree-index-bloat-reclaim/DESIGN.md).
> Instrumentation showed the growth is **not** unreclaimed dead entries at all:
> 1.95 M index entries examined at split time, **zero** dead, while
> splits/txn × 8 KB = 157 B/txn against a measured 152 B/txn. The cause is
> **split amplification** — goopg packed bulk-built leaves to 100%, so the first
> insert into any leaf split it, whereas PostgreSQL leaves 10% free
> (`BTREE_DEFAULT_FILLFACTOR`). Adding that fill factor takes pkey growth to
> **0 B/txn, matching PostgreSQL**, with no throughput regression.

## 4. Where the read path stands now

After A, B, C, H, F and E, the `-S` profile's leaders are architectural rather
than incidental:

| frame | share | nature |
|---|---:|---|
| `executor.opOpen` (incl. the btree scan it performs) | ~25 % | the scan itself |
| `syscall.Syscall6` | ~16 % | client socket I/O |
| `optimizer.Plan` | ~13 % | every statement planned from scratch |
| `parser.Parse` | ~10 % | LALR work; C removed its *allocation*, not this |
| `runtime.mallocgc` | ~11 % | down from 19.6 % pre-B/C |

The two remaining levers are both large and both known:

1. **`optimizer.Plan` (~13 %)** — PostgreSQL plans every simple-protocol query
   too, and evidently far more cheaply. Nothing in this series touched the
   planner.
2. **`parser.Parse` (~10 %)** — candidate **C slice 2**, the `yySymType` shrink
   ([05 §C](05-improvement-plan.md)), remains unimplemented. The 1,568-byte
   union member still has to be zeroed on every parse; that is most of what
   `memclrNoHeapPointers` (~2 %) is. It is a grammar-wide change and needs the
   §3 probe in 05 first.

## 5. Gates

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units` | PASS (caught the CLOG recovery-override bug first time round) |
| `make race-gate` | PASS, 0 data races |
| `parity_goldens.txt` | byte-identical |
| `scripts/tpch-spotcheck.sh` | PASS — Q12=2, Q13=34 |
| pre-commit pgbench smoke | PASS |
| new tests | 4 plan-cache admission, 6 CLOG status-cache (incl. the recovery-override and slot-collision regressions) |
| every benchmark run | `0 failed` |

## 6. Methodology note

`-S` at n=3 per arm gave disjoint ranges at sd ≈ 370 (0.3 %), so a 2 % effect is
comfortably resolvable. `-N` at n=4 gave sd ≈ 900 (8 %), which cannot resolve
anything smaller than ~10 %. **Any future `-N` claim needs either many more
repetitions or a lower-variance harness** — its cost is dominated by `fdatasync`
timing, and no amount of careful A/B ordering removes that.

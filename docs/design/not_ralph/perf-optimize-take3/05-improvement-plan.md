# 05 — Improvement plan

Ranked by measured share, not intuition. Every item cites the measurement it
comes from; every ceiling is derived from that measurement. Where a ceiling
cannot be derived honestly, it says so.

**Framing:** the write path is at 1.11× and its commit machinery is at parity
([04](04-wal-persistence.md)). The read path is at 1.23×. Both remaining gaps are
per-statement overhead. There is no longer a "write problem" and a "read
problem" — there is one per-statement-overhead problem showing up in both.

**Calibration that reorders the obvious ranking:** allocator CPU tracks
allocation *count*, not *bytes*. `DeformPGIndexTuple` is 276 allocations per
query and 11.48 % of read CPU; `yyNewParser` is 1 allocation per statement and
1.94 %, despite being 54 % of allocated bytes ([02 §2.4](02-cpu-and-allocation.md)).

## 1. Ranked candidates

| # | candidate | evidence | est. ceiling | effort | risk |
|---|---|---|---|---|---|
| **A** | Partition the lock manager + PG-style fast-path relation locks | 90.8 % of `-S` mutex delay; 19.9 % of `-S` samples in `Lock:relation` (PG: 0) | **~1.10–1.15× on `-S` standalone** (see §A) | M–L | M |
| **B** | Stop allocating 276 objects per index lookup in `DeformPGIndexTuple` | 55.9 % of `-S` allocation **objects**; 11.48 % of `-S` CPU, 8.70 % in `makeslice` alone | **~8 % on `-S`** | M | M |
| **C** | Stop allocating 26,664 B per statement parse | 54.2 % of allocated **bytes**; but only 1.94 % of CPU | **~2 % direct**; second-order unquantified | S (pool) / M (shrink) | M |
| **D** | Add `OpIndexScan` to the concrete operator slab | adapter overhead 0.38 % of `-S` CPU | ~0.4 % from dispatch; the real prize is the per-row cleanups it unlocks | M | L |
| **E** | Front the CLOG lookup with an xid cache, then bank-lock it | 13.9 % of `-N` mutex delay; PG does **no CLOG access** on the hinted path | ~2–4 % on `-N` | S (cache) / M (banks) | **H** (correctness) |
| **F** | Cheap per-statement wins (`strings.ToLower`, enum probes, `FSM.GetCandidates`, 8 KB FPI copy) | 2–3 % each, see §F | ~5 % combined | S each | L |
| **G** | btree dead-entry reclamation (dedup-aware) | pkey +202.6 MB vs PG +0 | space, not TPS | L | **H** |

### A — Lock manager partitioning + fast-path relation locks

**Evidence.** Two *disjoint* paths through the same mutex dominate `-S` mutex
delay: the acquire path `acquireRelLockMaybeTransient` at **65.12 %**, and the
release path `ReleaseTupleLocks` → `LockManager.ReleaseAll` at **25.71 %** —
together **90.83 %**. (`acquireScanIndexReadLocksTxn`'s 26.28 % is a *subset* of
the acquire row, not an addition to it.) On `-N` the same two paths are 17.25 %
and 36.90 %.

Two independent instruments agree quantitatively, not just qualitatively: the
mutex profile's 1,715.19 s over 50 goroutines × 180 s is **19.06 %** of backend
wall time, against wait sampling's **19.9 %** of backend samples in
`Lock:relation` ([01 §3](01-results.md)) — a wait PostgreSQL records **zero** of.

**Mechanism.** `internal/storage/lmgr/lockmgr.go:310-314` is one global
`sync.Mutex` over one `map[LockTag]*lockState`. `Context.acquireRelLockMaybeTransient`
(`internal/executor/context.go:1603-1631`) acquires and **immediately releases**,
per relation *and per index*, per statement. `ReleaseAll` (`lockmgr.go:569`)
iterates the whole map under the lock, and fires ~6× per `-N` transaction —
`ReleaseTupleLocks` (`context.go:1381`) per Query message (five for `-N`,
`dispatch.go:336`) plus `ReleaseTableLocks` (`context.go:1364`) at transaction
end (`conn_tx.go:340`, `:558`).

**This is already a written, deferred design.**
`analysis/perf-optimize3-dash/08-improvement-designs/07-lockmgr-global-mutex-sharding.md`
specifies exactly this — 16 partitions by `LockTag` hash, a per-backend weak-lock
fast path, the `FastPathStrongRelationLocks` escape hatch, slices S1–S4, and open
questions O-LM-1/2/3. Deferred at that bundle's `README.md:13` and ledger row
`:347`. **Resume it rather than re-deriving it.** Slices, ascending:

1. **Partition** `states` by `LockTag` hash, mirroring `NUM_LOCK_PARTITIONS = 16`
   (`postgres/src/include/storage/lwlock.h:96-97`), and make `ReleaseAll` walk a
   per-transaction held-lock *list* instead of scanning the map.
2. **Fast-path weak relation locks**, per §A-PG below.
3. **Elide the transient acquire/release** where a transaction-scoped lock on the
   same relation is already held.

**What PostgreSQL actually does (§A-PG) — get this right or the mirror is
unsafe.** PG routes weak relation locks into a per-backend slot array via
`FastPathGrantRelationLock` (`postgres/src/backend/storage/lmgr/lock.c:2750`),
eligible when `mode < ShareUpdateExclusiveLock` and the lock is on a
non-shared relation in the current database (`EligibleForRelationFastPath`,
`lock.c:267-272`). Three corrections to the naive reading:

- The array is **not backend-private memory**. It lives in **shared memory
  referenced from PGPROC** (`postgres/src/include/storage/proc.h:82-84`,
  `:308-310`), guarded by `MyProc->fpInfoLock`, and is read and stolen by *other*
  backends via `FastPathTransferRelationLocks` / `FastPathGetRelationLockEntry`.
  What the fast path avoids is the shared **hash table**, not shared memory.
- Even the fast path reads the shared `FastPathStrongRelationLocks->count[]`
  (`lock.c:999`).
- The shared tables are also used for shared catalogs, for backends not bound to
  a database, and — the one an implementer will trip on — when the relation's
  **16-slot fast-path group is full** (`FP_LOCK_SLOTS_PER_GROUP`, `lock.c:987`;
  64 slots per backend at the default `max_locks_per_transaction`). A goopg
  mirror needs that overflow path.

**Ceiling — and why it is lower than the wait share suggests.** Removing a wait
occupying 19.9 % of backend-samples gives a *latency-model* bound of
`1/(1 − 0.199) ≈ 1.25×` (~93 k → ~116 k TPS). **That is not reachable on this
host.** `R2/cpu.pb.gz` reports 2,392.43 s of samples over 180 s = **13.29 of 16
cores** at 93,083 TPS, i.e. 142.8 µs CPU per transaction; 116 k TPS at unchanged
per-transaction CPU needs **16.6 cores**. The `1/(1−w)` model assumes the removed
wait costs no CPU, which holds only for the parked portion. **Treat ~1.10–1.15×
as A's standalone figure, and ~1.2× as A + B + C together** — a per-statement CPU
reduction has to land alongside it.

**Gates.** `RALPH_PRECOMMIT_SCOPE=units`; `make race-gate` (**mandatory** —
concurrency-critical); the D-002 isolation subset, since lock semantics are
directly implicated; `scripts/tpch-spotcheck.sh`.

### B — `DeformPGIndexTuple`: 276 allocations per index lookup

**Evidence.** **55.86 % of all allocated objects** on `-S` (3.95 × 10⁹ objects =
276 per single-row lookup, averaging 23.5 B) and 31.21 % on `-N`; **11.48 % of
`-S` CPU**, of which **208.08 s = 8.70 % of total CPU is `runtime.makeslice`**
directly underneath it ([02 §2.3](02-cpu-and-allocation.md)). 4.72 % cum on `-N`.
Its only caller is `comparePGIndexTuples` — the btree descent.

**Mechanism.** Every index-tuple comparison during a descent deforms the tuple
into freshly-allocated slices. Since the comparison is transient, the
deformed representation never needs to outlive the call.

**Proposed change.** A caller-owned scratch buffer reused across the descent, or
in-place comparison that reads attributes without materialising a slice at all
(`PGIndexInfoFindDataOffset` + `pgAttLength` already give the offsets). PG's
`_bt_compare` (`postgres/src/backend/access/nbtree/nbtsearch.c`) uses
`index_getattr`, a macro that computes an offset into the existing tuple and
allocates nothing.

**Ceiling.** ~8 % on `-S` — the `makeslice` share is directly attributable and
removing the allocations does not remove the comparison work itself. This is
**the largest single actionable CPU item in the study**, and ~4× candidate C.

**Gates.** `RALPH_PRECOMMIT_SCOPE=units`; `scripts/tpch-spotcheck.sh` (canonical
Q12/Q13 row counts — this is index-scan correctness); the TPC-DS SF0.5 gate;
`make race-gate` if the scratch buffer is shared across goroutines (it must not
be).

**Risk.** Medium: a reused buffer aliased into a returned value is a silent
wrong-results bug of exactly the class §5 records for `MaterializedSlot` pooling.

### C — Stop allocating 26,664 bytes per statement parse

**Evidence.** `parser.yyNewParser` is 54.2 % of allocated bytes and 26,664 B per
parse (DWARF-confirmed, matched independently by two runs). PostgreSQL's
equivalent is 1,600 B on the C stack with zero heap traffic
([02 §2.1-2.2](02-cpu-and-allocation.md)).

**Ceiling — corrected, and lower than the byte share implies.** `yyNewParser` is
**1.94 % of CPU on both workloads**, 100 % of it inside `runtime.newobject` —
that is the entire allocate-and-zero cost. It is **0.2 % of allocation count**
against 54 % of allocation bytes, and allocator CPU scales with count and zeroed
bytes, not bytes alone. Its containing frame `parser.Parse` is only 8.70 %, so no
parser change can exceed that even in principle. **The direct ceiling is ~2 %.**
Anything beyond must come from second-order cache effects (26 KB churned per
statement evicts working set), which this study cannot size — hence the §3 probe.

**Proposed change**, two independent slices:

1. **Pool the parser** — a `sync.Pool` of `*yyParserImpl` with an explicit reset.
2. **Shrink `yySymType`** toward PG's shape by replacing its ~14 by-value
   composite members with pointers. Mechanical but grammar-wide.

**Risk — two documented precedents, both directly on point.**

- **The token-arena GC-unsafety ruling.** `2b7861d34` (M0107-0003) retired the
  parser token arena as *GC-unsafe* after `adfb935` produced *"found pointer to
  free object"* in regress; design doc
  `docs/design/0107-0003d-token-pool-gc-safety.md`. Two live hazards: an `mctx`
  slab is a `[]byte` **noscan span**, and **the cross-session plan cache retains
  parser-derived strings by reference** beyond the statement. Note the direction:
  the danger is not only failing to clear the stack (a leak) but **live cached
  plans dangling into recycled parser memory** (corruption). Slice 1 must be
  designed against that, not against the leak.
- **The executor `sync.Pool` precedent.** M0069-0001 attempt 1 (`sync.Pool` of
  `*MaterializedSlot`) regressed TPC-H Q1/Q11/Q21 by 45–90 %; attempt 2 was fast
  but **silently wrong** (Q12 2→0, Q13 35→2) and was reverted twice
  (`336550ce0`/`41dd7154b`, `cf04bce20`/`5d6961d0d`). Same technique, adjacent
  package. This is why C's and D's gate lists must not be trimmed.

**Gates.** `make gen-parser` (never a bare `go build`);
`GOOPG_UPDATE_GOLDENS=1 go test ./internal/parser/` and **read the
`parity_goldens.txt` diff** — it is the review artifact;
`RALPH_PRECOMMIT_SCOPE=units`; `scripts/tpch-spotcheck.sh`. Read
`docs/design/not_ralph/06-goyacc-parser-playbook.md` §12 before slice 2, in
particular the position conventions (`$<p>N`, `lastConsumedPos()`), which the
golden corpus does **not** cover.

### D — Add `OpIndexScan` to the concrete operator slab

**Evidence.** Adapter overhead is **0.38 %** of `-S` CPU — `adapterOpNext`'s
2.27 % cum is 83 % real scan work that survives any migration
([02 §5](02-cpu-and-allocation.md)).

**Mechanism.** The concrete `OpNode` slab exists and covers ten operator kinds,
but has no `OpIndexScan` (verified repo-wide across all branches), so every
pgbench statement runs through `OpAdapter` (`opnode.go:853`).
`docs/design/perf-optimize/03-executor-concrete.md:112` specified `OpIndexScan`
as the second kind to migrate — this is a **resumption of an abandoned plan**,
not a new idea.

**Ceiling.** ~0.4 % from dispatch itself. **Do not sell this as a dispatch win.**
Its value is the per-row cleanups it unlocks in `indexScanOp.Next`: `Pool.Pin`
(`internal/executor/operators_index.go:540`) / `Unpin` (`:597`) called **per row**
rather than per page, and a per-column enum map probe per row (`:602-618`).

**Gates.** `RALPH_PRECOMMIT_SCOPE=units`; `scripts/tpch-spotcheck.sh`;
`make plan-gate`.

### E — Cache, then shard, the CLOG lookup

**Evidence.** `CLog.GetStatus` → `clogBufferPool.getStatus` is **13.87 %** of
`-N` mutex delay. (`Snapshot.SeesCommittedXID` / `clogSaysNotAborted` at 12.03 %
is a *subset* of that row, not an addition — it is 100 % inside `GetStatus`.)

**Mechanism.** One `sync.Mutex` guards the entire CLOG buffer pool
(`internal/access/transam/clog_bufferpool.go:133-139`), taken on **every tuple
visibility test**: `visibility.go:98-106` consults `SeesCommittedXID` on both
branches of the hint-bit test, and `snapshot.go:199` places the CLOG lookup
(`:222`) before the `xid < s.Xmin` fast exit (`:225`).

PostgreSQL does **no CLOG access at all** once `HEAP_XMIN_COMMITTED` is set — it
takes the lock-free `XidInMVCCSnapshot` branch
(`heapam_visibility.c:1076-1082`). See [03 §2](03-contention.md).

**Proposed change**, cheapest first:

1. A `cachedFetchXid`-style single-entry (or per-backend small) cache in front of
   `GetStatus`, mirroring `postgres/src/backend/access/transam/transam.c:33-62`.
   pgbench xid locality is high, so this may capture most of the win.
2. Bank locks. **Note this is a restoration, not novel work**: `c1bfe26de`
   (M0107-0004 D1) landed a per-bank RWMutex and `0ab77d452` (M0117-0006 Part C)
   deleted it as collateral when the SLRU buffer pool became the sole store.
   `clogSLRUBankSize = 16` still sits at `clog_bufferpool.go:22`, and the pool's
   comment still claims *"this matches PG, where `SimpleLruReadPage` takes the
   SLRU bank lock"* — parity it does not currently have. **Fix that misleading
   comment in the same change.**
3. Only if 1–2 are insufficient: revisit whether the hinted branch needs the
   consult at all. **This has been refuted twice, for two different reasons** —
   `f29c44e43` (M0115-0004, 2026-05-29) and `934221287` (M0131-S30.7). Reproduce
   **both** tests before touching it.

**Gates.** `make race-gate` and the **full** D-002 isolation suite, not a subset.

### F — Cheap per-statement wins

- **`strings.ToLower` on the per-column codec path** — 1.82 % of `-S` CPU,
  1.89 % of `-N`. `encodeValuePGCtx` (`internal/executor/codec.go:440`)
  dispatches on `strings.ToLower(t.Name)`, allocating a string per column per
  row; `appendTypedCellText` (`internal/postmaster/dispatch.go:3764`) repeats it.
  Resolve the type once, not per cell.
- **`mapaccess2_faststr`** — 2.73 % of `-S` CPU; largely the per-column enum
  probe in `indexScanOp.Next` (`operators_index.go:602-618`) and catalog lookups.
- **`FSM.GetCandidates`** (`internal/storage/fsm.go:62`) — 2.64 % of `-N` user
  cycles, a linear scan under a global RWMutex (`fsm.go:19`). A written design
  exists — `analysis/perf-optimize3-dash/08-improvement-designs/06-fsm-getcandidates.md`
  (PG three-level max-tree) — but the storage shape has since changed to
  `pages map[fsmKey][]uint16`, so **it needs re-basing before use**.
- **The 8 KB `make(Page, BlockSize)` per FPI** (`internal/storage/bufpool.go:2417`).
- **`Pool.sharedHitCount`** — an unsharded `atomic.Int64` (declared
  `bufpool.go:243`, incremented `:1885` and `:1933`) bouncing one cache line
  across 50 goroutines on every buffer **hit**, while a per-P `stats.Counter`
  already exists (`internal/utils/activity/stats/counter.go:57`).

### G — btree dead-entry reclamation

`pgbench_accounts_pkey` grows +202.6 MB (104.3 B/txn) where PostgreSQL's grows
**0 bytes** ([01 §5](01-results.md)). A space and long-run-stability problem, not
a TPS problem at this horizon. **No mechanism is proposed here** — see §5.

## 2. Sequencing

1. **B** (`DeformPGIndexTuple` scratch buffer) — largest measured CPU item, and
   self-contained within `nbtree`.
2. **C slice 1** (pool the parser) — smallest diff; go early because it is
   cheapest to build and verify in isolation, **not** because it is the biggest
   win (it is ~2 %).
3. **§3 probe** — establish C slice 2's real ceiling before a grammar-wide change.
4. **A step 1** (partition + held-lock list) — resume the existing deferred design.
5. **F** — cheap, parallelisable, low risk; good filler while A is in review.
6. **A step 2** (fast-path relation locks) — the PG-parity move.
7. **D** — after A and B, so the read path is neither lock-bound nor
   allocator-bound when measuring a 0.4 % effect.
8. **E slice 1** (xid cache); slices 2–3 last, highest correctness risk.
9. **G** — an independent correctness/space track.

## 3. Probe required before scheduling C slice 2

**There is currently no benchmark in the tree that measures what C changes.**
`docs/design/not_ralph/PERF-BASELINE.md` cannot serve as the gate: its command
targets `./internal/sqlparser/`, a package deleted by `b18ad15f9`; its "new
parser" rows are labelled in-file as *"the skeleton grammar accepts only empty
input … a floor, not a comparison point"* at 2,586 B/op, which cannot be a
`yyParserImpl` measurement against 26,664 B/parse; and its only real rows
(`LegacyParse*`) measure the recursive-descent parser C does not touch.

So: **add a non-empty-grammar `yyParserImpl.Parse` benchmark to
`internal/parser/bench_test.go` and capture a HEAD baseline first.** Then compare
(a) HEAD, (b) HEAD + `sync.Pool`, (c) HEAD + `yySymType` reduced to pointers for
the ten widest members, reporting ns/op, B/op and allocs/op per input class. If
(c) does not beat (b) clearly on ns/op, slice 2 is not worth its grammar-wide
blast radius.

## 4. Explicitly NOT worth doing (measured, not assumed)

| item | why not |
|---|---|
| Further `GOGC`/`GOMEMLIMIT` tuning, explicit GC control | `gcBgMarkWorker` 0.09 %, `scanObject` 0.06 % — ~700× below the 63.3 %/54.9 % baseline ([02 §1](02-cpu-and-allocation.md)) |
| C5 pipelined commit groups / drain-fsync split — **at this scale** | `END` is at 1.008× parity, and **PG holds `WALWriteLock` across both `pwrite` and `fsync` too**, so this is beyond-PG, not parity ([04 §5](04-wal-persistence.md)). **Regime-specific, not refuted:** `analysis/perf-optimize3-dash/07-scale500-analysis/02-buffer-pressure-analysis.md:118-125` gives it renewed priority under buffer pressure (est. 6.6 k → 8.8 k TPS) |
| Raising `commit_delay` | Wired, at PG's default 0; nothing to buy at parity |
| Reducing the residual 1.28× WAL volume for throughput | Latency is at parity; matters for stream-format parity, not TPS |
| `insertPosTracker.posMu` | `LWLock:WALInsert` is 0.3 % of `-N` samples |
| Buffer-pool `pinMu` / miss-path sharding — at this scale | Does not surface at scale 100; top item at scale **500** (`analysis/perf-optimize3-dash/07-scale500-analysis`) |
| Optimising `runtime.selectgo` out of the block profile | Idle goroutines parked in `select`, not contention ([03 §3](03-contention.md)) |

## 5. Do not re-land (documented no-gos)

- **C3 on-probe `LP_DEAD` kills for the UPDATE-probe RangeScan.** Implemented and
  **reverted** 2026-07-14 (`bdaa325a4` → `4998c81b9`): no benefit on uniform
  pgbench `-N`, and an **~18× pkey regression** (+24.5 MB vs +1.3 MB) on a
  re-probe-heavy c=1 A/B, because marking dead *duplicate* entries `LP_DEAD`
  defeats btree deduplication's posting-list consolidation. **Only the
  UPDATE-probe half was reverted** — the revert states *"The read-path
  `indexScanOp` collector is unaffected."* A real fix for G must be
  **dedup-aware** or a background btree vacuum.
- **`sync.Pool` of executor slots.** M0069-0001: attempt 1 regressed TPC-H
  Q1/Q11/Q21 by 45–90 %; attempt 2 was silently wrong (Q12 2→0, Q13 35→2).
  Reverted twice. Bears directly on C slice 1.
- **Parser token arena.** Retired as GC-unsafe (`2b7861d34`); *"found pointer to
  free object"*. Bears directly on C slice 1.
- **Protocol writev "corking".** `bufio` already coalesces reply frames
  (`analysis/perf-optimize3-dash/08-improvement-designs/README.md:14`).
- **`procPin` as the WAL stripe selector.** Rejected; broke
  `TestConcurrentAppendAcrossSegmentBoundariesNoOverflow`
  (`analysis/perf-optimize2/05-improvement-results.md:96-101`).
- **C1 incremental canonical heap WAL records.** Moot — the canonical `0xFE`
  family was deleted 2026-07-15 (`1f0a3eca9`, `a4ef05ed6`).
- **Re-targeting the commit-path CLOG fsync or per-record FPI.** Both are gone;
  `analysis/perf-optimize3/03-code-attribution.md` M1/M2 are stale.

## 6. A note on the plan cache — not a PG-parity gap

Under **pgbench's literal-substituted SQL**, goopg's plan cache is a 100 % miss:
`planCacheKey` (`internal/postmaster/dispatch.go:2397`) preserves literals by
design, so every statement is a unique key.

It is tempting to file that as a defect. **It is not a gap versus PostgreSQL**:
PG also re-parses and re-plans every simple-protocol query, and still reaches
114 k TPS. The finding is therefore not "goopg should cache more" but "goopg's
parse + plan + operator-build path is expensive" — which B, C and D address.
Per-query setup is ~26 % of `-S` CPU ([02 §3](02-cpu-and-allocation.md)).

Two second-order notes:

- Do **not** naively de-scope cache population on the simple path. Ledger row
  `:1332` (M0132-S13) records that goopg's simple path *deliberately* reads the
  cross-session cache (`dispatch.go:1156-1157`, M0098-0005), which is why goopg's
  `-M prepared` is −1 % vs simple where PG's is +11 %; de-scoping *"mirrors PG but
  slows goopg's simple path"*. The 100 % miss is a property of pgbench's SQL, not
  of the simple protocol generally.
- `BuildFastIterator` rebuilds the `opTreeSlab`/`exprTreeSlab` on every execution
  (`dispatch.go:3398`) even on a cache hit — **1.52 % of `-S` CPU**, small but
  pure re-work.

## 7. Follow-ups outside the performance track

Two PostgreSQL-compatibility gaps found while setting up
([00 §6](00-methodology.md)), now filed as `.ralph/deferral_ledger.md:2007` and
`:2008` (task-id `perf-optimize-take3`, 2026-08-29):

1. **`wal_buffers` rejects a unit suffix.** `wal_buffers = 100MB` fails startup;
   raw bytes are required. Root cause: the registration at
   `internal/utils/misc/defaults.go:545` **omits the `Unit:` field**, so
   `native == UnitNone` and `parseIntWithUnit` (`guc.go:730`) rejects every
   suffix; `shared_buffers` (`defaults.go:328`, `Unit: UnitKB`) is the working
   sibling. Worse than the hard failure: PG declares this GUC `GUC_UNIT_XBLOCKS`
   (`guc_tables.c:3019-3022`) while goopg stores raw **bytes**, so a lifted
   `wal_buffers = 2048` — 16 MB in PG — is **silently accepted as 2,048 bytes**.
2. **The `pg_current_wal_*` family is catalog-visible but not callable.**
   `pg_current_wal_lsn` (OID 2849), `pg_current_wal_insert_lsn`,
   `pg_current_wal_flush_lsn`, plus `pg_walfile_name` and `pg_wal_lsn_diff` are
   listed in `internal/executor/pg_nonimmutable_builtins.go:148` with **no
   executor handler**, so calls error `function ... does not exist`.

Watch item: `internal/port/gls` is build-tagged
`go1.24 && !go1.27 && !noLinkname`, and this host runs **go1.26.3** — one Go
minor release, or one build flag, from silently falling back to the
`runtime.Stack` path that once cost 57 % of server CPU
([03 §4](03-contention.md)). The same tag pattern is on
`internal/runtimeshim/pinp_linkname.go`, so several shims expire together.

## 8. Review record

Adversarially reviewed 2026-08-29 against the raw artifacts in `tmp/take3/runs/`.
The review found and this bundle corrected: an `opOpen` double-count that had
inflated "per-query setup" from 26 % to 48.7 %; a MiB-vs-MB unit error that made
every allocation-per-transaction figure ~7.4 % low; a parser ceiling overstated
by 2.6–6× (bytes were used as a proxy for cycles); a missing candidate
(`DeformPGIndexTuple`, the largest actionable CPU item); nested double-counts in
the mutex rankings; an unreachable candidate-A ceiling; a 3× table-size
asymmetry now disclosed in [00 §5](00-methodology.md); two missing no-gos
bearing on candidate C; and a gate that could not be run as written.

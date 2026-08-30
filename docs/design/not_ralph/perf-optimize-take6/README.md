# perf-optimize-take6 — the OLAP row pipeline, profiled on a plan-identical join query

Scope: an evidence-first profile of **TPC-H Q14** — the one join query in the
TPC-H set whose goopg plan and PostgreSQL 18.3 plan are the *same operator
tree* — turned into a ranked improvement plan for the parts of the executor
that **every** OLAP query shares: tuple visibility, row decode, row copying,
hash-join keying, and aggregation.

- **Base commit**: `b91732783` (branch `perf-opt-take5`)
- **Oracle**: PostgreSQL 18.3, `postgres/local_install`, port 65432
- **Subject**: goopg, TPC-H SF=1, port 65433, S-cold
- **Date**: 2026-08-30
- **Raw artefacts**: `tmp/take4/runs/t6q14/`, `tmp/take4/runs/t6q03/` (not committed; §7)

## Documents

| file | content |
|---|---|
| [README.md](README.md) | this file — query selection, profile, ranked plan |
| [RESULTS.md](RESULTS.md) | measured outcome of candidates A and B |

---

## 1. Why Q14

The goal asked for a **join** query whose goopg and PostgreSQL plans **match**.
Comparing 20 whole TPC-H queries (Q6 is excluded by the sweep script and Q15 is compared as its three fragments) (`tmp/take4/plancmp6.sh`, normalising away the
parallel-execution labels — the `Parallel ` prefix and the `Partial`/`Finalize`
aggregate split — which describe *how* one operator tree is run, not *which*
tree it is), exactly one join query matches:

```
Q14   Aggregate | Gather | Aggregate | Hash Join | Seq Scan
```

Both engines: `Finalize Aggregate → Gather (4 workers) → Partial Aggregate →
Hash Join (lineitem.l_partkey = part.p_partkey) → Parallel Seq Scan lineitem +
Seq Scan part`. Every other join query differs on join *method* or *order* — PG
picks nested-loop + index where goopg picks hash, or commutes the build side —
so a runtime comparison there measures the planner, not the executor.

One residual difference, and it is **not** merely a label. PG's tree has a
`Parallel Hash` node over a `Parallel Seq Scan on part` — workers split the
200 k build rows and cooperatively fill one table. goopg emits **no `Hash` node
at all**: `Hash Join` sits directly over `Parallel Seq Scan lineitem` and a
plain `Seq Scan part`. goopg's tree has one fewer node and a different
build-side scan strategy.

An earlier draft of this section called it "the label differs" and said goopg's
sharing made the trees equivalent. That is the exact overstatement the review of
`../parallel-hash-build-coverage/DESIGN.md` recorded as its own finding M6, and
repeating it here would be regressing a corrected claim. What is true: goopg
does build the table once and share it by pointer (P8), so the *work* is not
duplicated — but P8 is the analogue of PG's non-parallel-aware `Hash`, not of
`Parallel Hash`.

This does not disqualify Q14. The join method, join order, scan methods and
worker count are the same, so a runtime comparison still measures the executor
rather than the planner. It does mean §1's match is "same operator tree modulo
the build-side parallel strategy", not "identical".

**Why this query generalises.** Q14 is deliberately unremarkable — two tables,
one equi-join, one aggregate. Everything it spends time in is shared by every
OLAP query in both benchmarks: visibility checking, heap decode, row copying at
the join build boundary, and aggregation. §3 confirms the same items dominate
**Q3** (a 3-table join with a `HashAggregate` over 11,415 groups), which is why
§5's candidates are ranked by cross-query reach rather than by their Q14 share.

Q14 warm: goopg **0.56 s**, PG **0.65 s** (4 workers each). goopg is *not*
slower here — this study is about where its cycles go, not about closing a gap.

---

## 2. Method

Server started through the mandatory cgroup cap
(`scripts/goopg-test-run.sh`, `GOGC=off`, `GOMEMLIMIT=12GiB`, soft cap above
the Go limit). Binary built to a private path so the nightly CI lane's shared
`tmp/goopg-bench-bin` is untouched.

Captured over a 40 s window (Q14) and 30 s (Q3) with the query looping:

- **pprof** — CPU, heap delta (`-base` before/after, so allocation figures are
  window deltas not lifetime), block delta, mutex delta, goroutine dump.
- **perf** — `task-clock, cycles, instructions, branch-misses, cache-misses`
  attached to the server PID. **No trailing `--`** (with it, perf counts the
  `sleep`, not the process — the take-3 methodology trap).
- **wait events** — `pg_stat_activity` sampled every **500 ms**.

Headline wall times are taken with no profiler attached.

**perf, pprof and the wait-event sampler run in the SAME window**, not separate
runs — `tmp/take6/profile.sh` starts `perf stat` and the pprof CPU capture
concurrently against one server. An earlier draft claimed otherwise. So every
IPC and cache-miss figure below was measured with SIGPROF profiling active and
should be read as indicative, not as a clean hardware measurement. The pprof
*shares* are unaffected (they are relative), which is what the ranking rests on.

---

## 3. Profile

### 3.1 Wait events and the shape of the problem

**71 of 71 wait-event samples are `active` with an empty `wait_event`.** No I/O,
no lock waits, no buffer waits. This is a pure-CPU pipeline, so every candidate
below is a CPU or allocator item.

`perf`, Q14: IPC **1.90**, cache-misses **19.77 %** of cache *references*.

**That 19.77 % must not be read as "memory-bound", and an earlier draft did.**
It is a ratio over cache references, and Q14 issues far fewer references per
second than Q6 (102.8 M/s vs 286.4 M/s), so the denominators are not
comparable. Normalised per instruction:

| | misses | instructions | MPKI |
|---|---:|---:|---:|
| Q14 | 4.483e9 | 1.748e12 | **2.56** |
| Q6 | 3.043e9 | 2.086e12 | 1.46 |
| Q3 | 1.821e9 | 9.889e11 | 1.84 |

Q14 is 1.75× Q6 per instruction, not 4.15×, and IPC 1.90 with 0.19 %
branch-miss is not a memory-bound profile. Q3's cache-miss *rate* is 5.85 %.
The honest statement is that Q14 touches somewhat more memory per instruction
than Q6; nothing below should be justified by "the cache-miss rate".

### 3.2 CPU, Q14 (40 s window, 217.15 s of samples, 5.43 CPUs)

| item | flat | note |
|---|---:|---|
| `sync/atomic.(*Int32).Add` | **10.86 %** | *not* application logic — see §3.4 |
| `evalFastExpr` | 9.58 % (22.84 cum) | compiled expression eval (take 7) |
| `runtime.memmove` | 9.42 % | |
| `DecodeRowRangeIntoMctxPGTupleStyled` | 4.83 % (17.53 cum) | heap decode |
| **`strings.ToLower`** | **4.64 %** | §3.3 |
| `compareDatum` | 4.09 % (7.88 cum) | |
| `decodePhysicalPGValueMctxStyled` | 3.64 % (10.65 cum) | |
| `evalBinary` | 3.56 % (13.88 cum) | |
| `PageGetHeapTupleInto` | 2.98 % (13.13 cum) | |
| `Snapshot.SeesCommittedXID` | 1.26 % (**7.97 cum**) | §3.4 |
| `cloneRowOwned` | 1.16 % (7.88 cum) | §3.5 |
| `aggregateOp.applyAgg` | 0.17 % (4.10 cum) | §3.6 |

### 3.3 `strings.ToLower` — a type-name lowercased once per *value*

4.64 % on Q14 and **6.13 % on Q3**. Callers:

| caller (Q14) | share of ToLower | | caller (Q3) | share |
|---|---:|---|---|---:|
| `isTimestampTZTypeName` | 36.84 % | | `decodePhysicalPGValueMctxStyled` | 45.44 % |
| `decodePhysicalPGValueMctxStyled` | 34.36 % | | `physicalPGTypeAlign` | 38.72 % |
| `physicalPGTypeAlign` | 27.41 % | | `isTimestampTZTypeName` | 15.20 % |
| `isFloatResultType` | 0.70 % | | `isFloatResultType` | 0.32 % |
| `aggregateOp.applyAgg` | 0.70 % | | *(absent)* | — |

There are **five** callers, not four, and the two queries weight them very
differently — an earlier draft printed Q14's table and implied it held for both.

All of them ask the same question — *what type is this column?* — by lowercasing
`catalog.Type.Name`, a **string**, for **every value of every row**. The
column's type does not change between rows.

**PostgreSQL does not do this.** `heap_deform_tuple` reads `attlen`,
`attbyval` and `attalignby` from the `TupleDesc`
(`postgres/src/backend/access/common/heaptuple.c`) — in PG 18 through a
16-byte `CompactAttribute` built specifically to keep the deform loop
cache-dense. There is no string anywhere on PG's deform path, and
`attcacheoff` memoises the offset after the first deform. This is a PG
optimisation goopg has not taken.

**On the aggregate:** `applyAgg` appears at 0.70 % *of ToLower* on Q14 — 0.03 %
of CPU — and does not appear at all on Q3. An earlier draft cited it as
evidence that this reaches aggregation. It is not evidence of anything; the
aggregate benefits here only because it consumes rows the scan decoded.

### 3.4 Per-tuple visibility: two global RWMutexes, and a hint-bit fast path that isn't one

`sync/atomic.(*Int32).Add` at 10.86 % is the single largest CPU item on Q14 and
is entirely lock bookkeeping — 64.42 % `RWMutex.RLock`, 35.58 % `RUnlock`. Those
RLocks come from `SubxactMap.IsSubxact` (50.72 %), `CLog.OldestClogXid`
(46.41 %) and the page content lock (2.87 %). Weighting both lock and unlock
sides, the two visibility lookups are **~94 %** of the atomic traffic (an
earlier draft said 97 %, reading only the RLock side).

**Q3's atomic share is 2.95 %, not 10.86 %.** An earlier draft reported Q3
figures for every other item and omitted this one — the single number that most
qualifies the top-ranked candidate. Both are 4-worker parallel scans of
`lineitem`; the difference is how much other work each row does.

**The deeper finding, and the one that makes this a PG-parity gap.**
`Snapshot.SeesCommittedXID` is 7.97 % cumulative, 83.24 % of it in
`clogSaysNotAborted`. goopg *does* set `HEAP_XMIN_COMMITTED`
(`SetXminHintBit`), but its hint-bit-set branch is **not a fast path**
(`internal/access/transam/subxact_visibility.go`):

```go
if h.Infomask&storage.HeapXminCommitted == 0 {
    if !SeesCommittedXIDWithSubxacts(snap, h.Xmin, r) { return false }
} else if !snap.SeesCommittedXID(h.Xmin) {   // hint bit IS set — and we still consult CLOG
    return false
}
```

`SeesCommittedXID` → `clogSaysNotAborted` → `OldestClogXid()` + `GetStatus()`.
The profile proves this is the branch taken: `SeesCommittedXID`'s only caller is
`TupleVisibleSubxact` at 100 %, and `SeesCommittedXIDWithSubxacts` never
appears — the hint bits are set on this bulk-loaded table and buy **nothing**.

PostgreSQL's corresponding branch does no CLOG work at all
(`heapam_visibility.c`):

```c
else {  /* xmin is committed, but maybe not according to our snapshot */
    if (!HeapTupleHeaderXminFrozen(tuple) &&
        XidInMVCCSnapshot(HeapTupleHeaderGetRawXmin(tuple), snapshot))
        return false;
}
```

PG also keeps a one-entry CLOG cache (`transam.c`'s `cachedFetchXid` /
`cachedFetchXidStatus`) that short-circuits a repeated lookup of the same XID
with no lock — and a bulk-loaded table is overwhelmingly one XID.

### 3.5 Allocation

Window deltas (`-base`):

| | Q14 (181.9 M objects) | Q3 (281.3 M objects) |
|---|---:|---:|
| `Datum.MaterializeArena` | **36.30 %** | **40.61 %** |
| row pool (`executor.init.5.func1`) | 32.85 % | 36.31 % |
| `canonicalNumericKey` | 7.89 % | **14.20 %** |
| `joinOp.lazyHashInsertDatum` | 6.12 % (12.44 cum) | 1.80 % (3.73 cum) |
| `ownedBuildRow` | 6.06 % | 1.81 % |
| `channelSource.Next` | 5.95 % | — |

`MaterializeArena` is 100 % `cloneRowOwned`. **But `cloneRowOwned` is the
SEQUENTIAL SCAN, not the join build** — an earlier draft attributed it to the
join and proposed fixes that would have missed 81–99 % of the cost:

| caller of `cloneRowOwned` | Q14 | Q3 |
|---|---:|---:|
| `(*seqScanOp).Next` | **81.33 %** | **99.01 %** |
| `MaterializeForTransfer` (the parallel handoff) | 18.67 % | 0.95 % |

The join build side is the *separate* `ownedBuildRow` line — 6.06 % (Q14) /
1.81 % (Q3). So this is the scan materialising every row it emits so the row can
outlive the page lock, and it is join-independent. Note also that 47–55 % of
`cloneRowOwned`'s own CPU is `acquireRow` (the row pool), which a
copy-fewer-columns change would not remove either.

### 3.6 Aggregation — the honest finding is that there isn't one here

The goal named aggregation as an improvement target, so this section states
what the profile actually shows rather than manufacturing coverage.

`aggregateOp.Open` is 31.22 % cumulative on Q3, but **98.55 % of that is the
child pipeline**: in a pull-model executor, `Open` on the top operator drains
everything beneath it. Its callees are `gatherOp.Open` 51.02 % and
`gatherOp.Next` 47.53 %; the real aggregate work is
`evalGroupExprs` 0.50 % + `applyAgg` 0.22 % + `setGroupKey` 0.19 % — about
**0.33 % of Q3's CPU**. An earlier draft quoted the 31.22 % as "the
aggregation-relevant measurement", which is not defensible.

Likewise `canonicalNumericKey`, 14.20 % of Q3's allocations, is **not** the
aggregate's: its callers are `joinOp.evalHashKeySlot` 85.34 %,
`joinOp.lazyHashInsertDatum` 13.54 %, and `aggregateOp.evalGroupExprs`
**1.12 %** — i.e. 0.16 % of Q3's allocations. An earlier draft called it "the
aggregate's dominant allocation". It is a join cost with an aggregation label.

**So the aggregation conclusion is:** on these queries the aggregate operator
itself is not a bottleneck, and the way to make aggregation faster is to make
the rows reaching it cheaper — which is exactly what candidate A does, and why
Q3 and Q10 (11,415 and 20,451 groups) benefit from it at all. A genuine
aggregate-side optimisation would need a workload where grouping dominates;
§6 records that as the honest next step rather than claiming it here.

For reference, PostgreSQL's aggregate hashes a `TupleHashTable` on the grouping
columns' `Datum`s with per-column hash functions resolved once at executor start
(`execGrouping.c`), never materialising a string key — so the string-key
criticism is real, it just belongs to the join on this workload.

---

## 4. What is already done, and is not re-proposed

Takes 4–7 on `perf-opt-take4` removed, in order: the `numeric` binary→text→
`big.Int` decode round trip; the whole-row deform and per-row deep copy in the
scan (via a predicate prefilter); three allocations per heap tuple; and the
interpreted expression evaluation for scan predicates (via the existing
`ExprNode` compiler plus constant folding). Q6 went 5.235 s → 0.838 s across
those. Nothing below re-treads them.

---

## 5. Ranked improvement plan

Ranked by **cross-query reach × measured share**. Candidates A and B are
implemented; see [RESULTS.md](RESULTS.md).

### A. Resolve each column's type ONCE, not once per value — **implemented**

**Evidence:** §3.3. 4.64 % (Q14) / 6.13 % (Q3) of CPU across five call sites,
all asking a per-*column* question per *value*.

**Change:** `resolveColTypeInfo` computes the lowered type name and alignment
once per column in the operator's `Open`; the decode loop and the value decoder
take the lowered name as a parameter. Inside `case "timestamp", "timestamptz"`
the lowered name is already exactly one of those literals, so the third scan
(`isTimestampTZTypeName`) becomes a direct comparison.

**What it is NOT:** an integer type code derived from `Type.Name`. That would be
wrong — for an array column `catalog.Type.Name` holds the **element** type and
`IsArray` carries the array-ness, so a name-derived code gives `int4[]` the
`int4` alignment and decode path; `len(t.Args)` likewise separates internal
`"char"` (align 1) from `char(N)` (align 4). Memoising the *lowered name* while
still passing the full `catalog.Type` keeps every `IsArray`/`Args` branch
exactly where it was. `TestColTypeInfoArraySafety` pins this.

**Reach:** every operator that decodes a row — scans, joins, aggregates, sorts,
DML, catalog reads. Closes the `TupleDesc` gap in §3.3.

**Risk:** low, but not "pure memoisation of a function of the type name" as an
earlier draft said — see above. Staleness is handled by resolving where the
column list is resolved (`Open`), so an `ALTER TABLE` that re-resolves `o.cols`
re-resolves this.

### B. Take no lock per tuple for visibility — **implemented (the safe half)**

**Evidence:** §3.4. 10.86 % of Q14 CPU (2.95 % of Q3) is atomic RMW, ~94 % of it
two per-tuple lookups.

**Implemented:**

1. **`CLog.oldestClogXid` becomes `atomic.Uint32`.** Readers `Load()`; writers
   still serialise on `oldestMu` for the monotonic compare-and-store. Removes
   the whole lock without changing the value's liveness.
2. **`SubxactMap.IsSubxact` gets an `atomic.Int64` count.** A zero count means
   the map is empty, so the guarded lookup could only have returned false.

**Explicitly NOT done — hoisting the horizon into the snapshot.** An earlier
draft proposed exactly that and it is **unsafe**. `clog.go` documents the
invariant: `AdvanceOldestClogXid` publishes the new horizon *before*
`TruncateCLOG` removes the bytes, precisely so concurrent readers short-circuit
first. A reader holding a stale, older horizon fails the `XIDPrecedes`
short-circuit and faults in a truncated page as all-zero (= Unknown), which
`statusCache` then memoises. And a snapshot under `REPEATABLE READ` lives for
the whole transaction, so the staleness window is unbounded, not scan-bounded.
The atomic keeps the value **live**, which is why it is safe and the hoist is
not.

**Still open — the larger, PG-faithful half.** §3.4 shows goopg's
`HEAP_XMIN_COMMITTED` branch still calls `clogSaysNotAborted` where PG does only
the snapshot test. Making that branch do only the snapshot test would remove
`GetStatus` as well. It is a change to visibility semantics, so it needs its own
design and differential testing; §6 sequences it.

### C. `cloneRowOwned` in the SCAN — not attempted

**Evidence:** §3.5. Top allocator in both queries (36–41 % of objects); 81–99 %
of it is `seqScanOp.Next`, not the join.

Any fix must target the scan's per-row materialisation, and take 6
(`../tpch-q6-numeric-decode/design-take6.md`) already established that the copy
exists so the row can outlive the page lock, declining the alias route on
lifetime-coupling grounds. No number is offered here, because an earlier draft's
"−5 to −15 %" was derived from the wrong call site.

### D. Numeric/int keys without a string — **join, not aggregation**

**Evidence:** §3.5/§3.6. `canonicalNumericKey` is 14.20 % of Q3's allocations,
**85 % of it the join's `evalHashKeySlot`**.

Two corrections to an earlier draft. It is not an aggregation candidate (§3.6).
And the risk is the reverse of what was claimed: `appendCanonicalNumericKey`
already strips trailing zeros before emitting `m:<mantissa>:<scale>`, so scale
collisions are *solved*; a bare `int64` mantissa lane **without** that
normalisation would make `1.5` (m=15,s=1) collide with `15` (m=15,s=0). Also
`lazyIntHash` is `map[int64][]Row`, a single-key lane, while Q3 groups on three
columns — extending it needs a composite-key design.

### E. Not proposed, with reasons

- **Expression evaluation** — take 7 compiled the scan predicate; `evalFastExpr`
  is real work, not dispatch overhead.
- **`PageGetHeapTupleInto`'s `memmove`** — structurally retained (take 6).
- **The cooperative-build handoff.** The block profile — captured and, in an
  earlier draft, never reported — shows **32.5 s inside
  `parallelBuildLazyHashTable.func1`** over a 40 s window: N producers blocked
  feeding the single map-owning consumer. That is a real serialisation point
  §3.1's "pure-CPU pipeline" glossed over. Not costed here.

## 6. Sequencing

**A and B are implemented** (RESULTS.md). Remaining, in order:

1. **B's PG-faithful half** — make the `HEAP_XMIN_COMMITTED` branch do only the
   snapshot test, as PG does (§3.4). Largest remaining single item; needs a
   visibility design and differential testing.
2. **D** — an int64 key lane for the join's numeric keys, with the
   trailing-zero normalisation preserved.
3. **The cooperative-build consumer** (§5-E) — 32.5 s of producer blocking.
4. **C** — the scan's per-row copy, the biggest allocator and the most invasive.
5. **A real aggregation workload.** §3.6 shows the aggregate operator is ~0.33 %
   of Q3's CPU, so aggregation cannot be optimised meaningfully against these
   queries. TPC-DS has grouping-dominated shapes; that is where an
   aggregate-side candidate should be derived.

Each lands with row counts verified unchanged, `tpch-spotcheck`, `-race`, and an
alternating A/B with fresh servers.

---

## 7. Reproduction

```bash
go build -o tmp/take6/goopg-base ./cmd/goopg
GOOPG_MUTEX_PROFILE_RATE=1 GOOPG_BLOCK_PROFILE_RATE=1 \
  tmp/take4/start-goopg.sh tmp/take6/goopg-base t6
TAG=t6q14 SECS=40 tmp/take6/profile.sh     # pprof + perf + 500ms wait events
TAG=t6q03 SECS=30 tmp/take6/profile03.sh
tmp/take4/plancmp6.sh                      # the plan-match selection in §1
```


---

## 8. Review record

Adversarial agent review, 2026-08-30, against the first draft: **3 critical,
8 major, 13 minor**, every pprof figure re-derived from the committed artefacts.
All corrected above. Three findings changed the *plan*, not just the prose.

| # | finding | resolution |
|---|---|---|
| **C1** | "this is the aggregate's dominant allocation" — **false**. `canonicalNumericKey` reaches `datumKey` from `joinOp.evalHashKeySlot` 85.34 % / `lazyHashInsertDatum` 13.54 % / `aggregateOp.evalGroupExprs` **1.12 %** — 0.16 % of Q3's allocations. Candidate D was a join optimisation with an aggregation label. | §3.6 rewritten to state the real finding: the aggregate operator is ~0.33 % of Q3's CPU, so aggregation gets faster here only by making the rows reaching it cheaper (candidate A). §6 makes "derive an aggregate candidate from a grouping-dominated TPC-DS query" the honest next step. |
| **C2** | §5-C attacked the wrong call site: `cloneRowOwned` is `seqScanOp.Next` 81.33 % (Q14) / **99.01 %** (Q3), not the join build. Both proposed fixes would have missed it, and 47–55 % of its CPU is `acquireRow` besides. | §3.5 and §5-C corrected; the invented "−5 to −15 %" range removed. |
| **C3** | "goopg calls `OldestClogXid`/`IsSubxact` *before* the hint bit can help" — wrong mechanism. `OldestClogXid` is called **inside** the hint-bit-set branch: goopg's `HEAP_XMIN_COMMITTED` path still runs `clogSaysNotAborted`, where PG's does only the snapshot test. The hint bits are set and buy nothing. | §3.4 rewritten around this; it is now the largest **open** item and §6 sequences it first. |
| **M1** | Candidate B(1) — hoist `OldestClogXid` into the snapshot — is **unsafe**, and `clog.go` documents why: the horizon is published *before* the bytes are removed so readers short-circuit first; a stale horizon faults in a truncated page as all-zero and `statusCache` memoises it. A snapshot also outlives a scan. | B(1) replaced with `atomic.Uint32`, which removes the lock while keeping the value **live**. The review's own suggestion, and what shipped. |
| **M2** | "Q14 is genuinely memory-bound" is a denominator artifact — 19.77 % is over cache *references*, and Q14 issues 2.8× fewer per second than Q6. Per instruction it is 2.56 MPKI vs 1.46. Q3 is 5.85 %. | §3.1 corrected with the MPKI table; no candidate is justified by "the cache-miss rate" any more. |
| **M3** | Q3's atomic share is **2.95 %**, not 10.86 % — the one Q3 number the draft omitted, and the one that most qualifies the top candidate. Also "since this is contention" was asserted, never measured. | Both stated in §3.4. |
| **M4** | "perf and pprof numbers come from separate runs" — **false**; `profile.sh` runs them in one window. | §2 corrected, with the caveat that hardware counters were taken under SIGPROF. |
| **M5** | "goopg renders `Hash`" — goopg emits **no `Hash` node at all**; its tree has one fewer node and a plain (not parallel) build-side scan. And this reintroduced the exact overstatement the cited doc records as its own review finding. | §1 corrected, including why Q14 still qualifies. |
| **M6** | `aggregateOp.Open` 31.22 % is 98.55 % `gatherOp` — in a pull executor `Open` drains the child pipeline. | §3.6 corrected. |
| **M7** | Five ToLower callers, not four (`isFloatResultType` dropped); Q3's split is a different distribution; `applyAgg` is 0.03 % of Q14 CPU and absent on Q3. | §3.3 now prints both queries' tables and drops the aggregate claim. |
| **M8** | The block profile was captured, unreported, and shows **32.5 s in `parallelBuildLazyHashTable.func1`** — producers blocked on the single-consumer hash build — contradicting "pure-CPU pipeline". | Added to §5-E and §6. |
| m1–m13 | 20 queries compared, not 22; the sweep's dedup step erases node arity (verified not to change any verdict here); stripping `Parallel ` hides a real build-side scan difference; goopg still evaluates `date + interval` per row (take 7's folding does not cover it); "~97 %" of atomic traffic is ~94 % weighting both lock sides; the array/`char(N)` hazard in a name-derived type code; Q3 groups on three columns so `lazyIntHash`'s single-int64 lane does not extend trivially; the scale-collision risk is already solved and the bare-mantissa lane would *introduce* it; PG 18 uses `CompactAttribute`; §1's PG wall time has no artefact. | All corrected in place; the array hazard is now pinned by a test. |

Verified **correct**: all twelve flat/cum pairs in §3.2 and the sample totals;
the atomic caller splits; `SeesCommittedXID` 7.97 %/83.24 %; both heap deltas in
full; `MaterializeArena` is 100 % `cloneRowOwned`; Q14's IPC and cache-miss;
71/71 wait events; §3.3's and §3.4's PG claims; that `lazyIntHash` exists; that
Q14 is the only join-query plan match and survives removing the dedup step; and
take 4–7's summary including Q6 5.235 s → 0.838 s.

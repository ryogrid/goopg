# 02 — CPU, GC, allocation and dispatch (the Go-cost chapter)

This chapter answers three questions directly: what Go's garbage collector costs
goopg today, what allocation costs, and what interface dispatch costs.

> **Units.** `go tool pprof` reports `MB`/`GB` as **MiB/GiB** (1024-based). All
> byte figures below are stated as KiB/GiB with the binary meaning, and
> per-transaction figures are computed from the exact byte counts.

## 1. The GC verdict: no longer a bottleneck

Both headline CPU profiles, read with `-nodefraction=0` (the default 0.5 %
cutoff hides these entirely):

| symbol | `-S` (R2) | `-N` (R1) |
|---|---:|---:|
| `runtime.gcBgMarkWorker` | 0.090 % | 0.094 % |
| `runtime.gcDrain` | 0.084 % | 0.091 % |
| `runtime.scanObject` | 0.061 % | 0.057 % |
| `runtime.gcAssistAlloc` | 0.017 % | 0.018 % |
| `runtime.bgsweep` / `sweepone` | 1.50 % | 1.19 % |

For context, `docs/design/perf-optimize/00-overview.md:223-224` set acceptance
targets of reducing `gcBgMarkWorker` from **63.3 %** to under 15 %, and
`scanObject` from 54.9 % to under 12 %. Both are now **~700× below the
baseline**. The server runs at `GOGC=200` with `GOMEMLIMIT=15GiB`, and the
collector is simply not being asked to do much work.

**Conclusion: further `GOGC`/`GOMEMLIMIT` tuning, or explicit GC control, has no
headroom on this workload.** The prior work that gated `maybeForceGCAfterCommit`
behind a disabled flag (`77c5d482e`) and restricted it to write transactions
(`cf2b4770`) closed this out.

This does **not** mean Go's memory model is free here. The cost has moved from
*collecting* to *allocating*, which §2 quantifies.

## 2. Allocation

`/debug/pprof/allocs` deltas across each run (end profile with start as `-base`),
divided by the run's transaction count.

| | `-S` (R6, 14,307,007 txns) | `-N` (R5b, 1,722,343 txns) |
|---|---:|---:|
| total allocated | 660.5 GiB | 377.8 GiB |
| **per transaction** | **48.4 KiB** (49,569 B) | **230.0 KiB** (235,527 B) |
| of which `parser.yyNewParser` | **26.3 KiB (54.2 %)** | **128.4 KiB (55.8 %)** |

`-S` runs one statement per transaction; `-N` runs five (`BEGIN`, `UPDATE`,
`SELECT`, `INSERT`, `END`). Normalising to a single parse gives **26,887 B**
(`-S`) and **26,291 B** (`-N`) — two independent runs agreeing, and both matching
the DWARF struct size in §2.1 to within +0.8 % / −1.4 %.

### 2.1 Every statement parse heap-allocates 26,664 bytes

`internal/parser/yacc_parser.go:10243`:

```go
func yyNewParser() yyParser {
	return &yyParserImpl{}
}
```

called once per parse from `yyParse` (`yacc_parser.go:10367`). From the binary's
DWARF:

| type | size |
|---|---:|
| `parser.yyParserImpl` | **26,664 B** |
| ` └ [16]parser.yySymType` (value stack) | 25,088 B |
| ` └ parser.yySymType` | **1,568 B** |

`yySymType` (`internal/parser/yacc_parser.go:17-74`) has **56** fields. Because
**Go has no unions**, a yacc `%union` port becomes a *struct*, so its size is the
**sum** of all members rather than the max: ~14 by-value composites
(`ResTarget`, `RangeVar`, `SortBy`, `FromExpr`, `RowsFromEntry`, `qname`,
`castType`, `ivQual`, `LockTableRelation`, `UpdateAssign`, `NamedWindowDef`,
`TypeField`, `CopyOption`, `wp [2]string`) plus ~28 wide slice/string/interface
headers at 16–24 B each, every one of them present in all 16 stack slots.

### 2.2 What PostgreSQL does instead

`postgres/src/backend/parser/gram.y:218-271` — every member of PG's `%union` is a
pointer or a ≤4-byte scalar:

```c
%union
{
	core_YYSTYPE core_yystype;
	int			ival;
	char	   *str;
	List	   *list;
	Node	   *node;
	...
}
```

`core_YYSTYPE` is itself a union of `{int; char*; const char*}`
(`postgres/src/include/parser/scanner.h:29-34`), so `sizeof(YYSTYPE) == 8`. And
bison allocates the initial stack **on the C stack**
(`postgres/src/backend/parser/gram.c:30656-30672`; `YYINITDEPTH 200` at
`gram.c:30577`):

```c
YYPTRDIFF_T yystacksize = YYINITDEPTH;   /* 200 */
yy_state_t  yyssa[YYINITDEPTH];
YYSTYPE     yyvsa[YYINITDEPTH];
YYLTYPE     yylsa[YYINITDEPTH];
```

| | goopg | PostgreSQL 18.3 |
|---|---:|---:|
| stack-slot type | 1,568 B (struct) | 8 B (union) |
| initial stack depth | 16 | 200 |
| value-stack bytes | 25,088 | 1,600 |
| **allocated where** | **heap, per parse** | **C stack, per parse** |
| **heap bytes per parse** | **26,664** | **0** |

PG parses a deeper grammar with a 200-slot stack using less memory than goopg
uses for 16 slots, and pays no allocator traffic at all.

**But bytes are not cycles — see §2.4 before ranking this.**

### 2.3 The largest *cycle* cost: `DeformPGIndexTuple`

Allocation **count**, not volume, is what drives allocator CPU, and by that
measure one site dominates everything:

| site | `-S` alloc objects | `-S` alloc bytes | `-S` CPU (cum) |
|---|---:|---:|---:|
| `nbtree.DeformPGIndexTuple` | **55.86 %** (3.95 × 10⁹) | 13.1 % | **11.48 %** |
| `parser.yyNewParser` | 0.20 % (1 per stmt) | 54.2 % | 1.94 % |

3.95 × 10⁹ objects over 14,307,007 transactions is **276 allocations per
single-row index lookup**, averaging 23.5 bytes each — the worst possible shape
for an allocator. Of its 11.48 % cum, **208.08 s = 8.70 % of total CPU is
`runtime.makeslice`** directly underneath it. Its only caller is
`comparePGIndexTuples`, i.e. the btree descent itself.

Remaining allocation-by-volume for `-S`, after the parser and deform:
`executor.BuildFast` 4.2 %, `optimizer.tryPromoteIndexOnlyScan` 3.2 %,
`executor.NewContext` 3.1 % (a 119-field struct, per statement),
`optimizer.buildBindingsPosMap` 1.5 %. `-N` adds
`storage.VacuumHeapPageBySlots` 10.7 % (opportunistic pruning) and
`storage.ParseHeapTuple` 1.7 %.

### 2.4 Bytes ≠ cycles

`parser.yyNewParser` is **54 % of allocated bytes but only 1.94 % of CPU** on
both workloads (100 % of that inside `runtime.newobject` — i.e. the entire cost
of allocating *and zeroing* the struct). One 26 KB object is far cheaper per byte
than 276 small ones. This is the single most important calibration in the
chapter, and it is why the improvement plan ranks `DeformPGIndexTuple` above the
parser despite the parser's larger byte share.

## 3. Where the CPU actually goes

### `-S` select-only (R2, 2,392 s of samples across 13.3 cores)

| frame | flat | cum |
|---|---:|---:|
| `syscall.Syscall6` | 15.51 % | 15.51 % |
| `executor.opOpen` | 0.12 % | 28.11 % |
| `runtime.mallocgc` | 2.21 % | 19.57 % |
| `nbtree.comparePGIndexTuples` | 1.40 % | 14.26 % |
| `optimizer.Plan` | 0.05 % | 11.94 % |
| `nbtree.DeformPGIndexTuple` | 2.35 % | 11.48 % |
| `runtime.makeslice` | 1.19 % | 10.86 % |
| `runtime.newobject` | 1.29 % | 8.92 % |
| `parser.(*yyParserImpl).Parse` | 5.11 % | 8.70 % |
| `runtime.memclrNoHeapPointers` | 2.91 % | 2.91 % |
| `strings.ToLower` | 1.56 % | 1.82 % |

**`opOpen`'s 28.11 % is not setup — it contains the scan.** It is 99.38 %
`indexScanOp.Open`, which is 82.07 % `indexScanOp.Rescan` (548.52 s = **22.93 %**,
the btree `rangeScanPos` itself) and 17.78 % `openPrep`. goopg materialises the
index scan eagerly at Open. Genuine open-time setup is therefore
`28.11 − 22.93 = 5.18 %`.

**Per-query setup is ~26 % of read CPU**: `opOpen` minus its scan (5.18 %) +
`optimizer.Plan` (11.94 %) + `parser.Parse` (8.70 %). Because the simple protocol
misses the plan cache on every statement ([00 §5](00-methodology.md)), none of it
is amortised.

`runtime.memclrNoHeapPointers` at 2.91 % flat includes zeroing the parser struct,
but only a fraction of it — the parser's whole allocate-and-zero cost is bounded
by the 1.94 % in §2.4.

### `-N` simple-update (R1, 1,554 s of samples across 8.6 cores)

| frame | flat | cum |
|---|---:|---:|
| `syscall.Syscall6` | 21.31 % | 21.31 % |
| `executor.updateOpKernelNext` | 0.00 % | 15.02 % |
| `runtime.mallocgc` | 1.41 % | 15.13 % |
| `parser.(*yyParserImpl).Parse` | 3.72 % | 6.91 % |
| `optimizer.Plan` | 0.05 % | 5.42 % |
| `nbtree.DeformPGIndexTuple` | 0.92 % | 4.72 % |
| `storage.VacuumHeapPageBySlots` | 1.15 % | 3.01 % |
| `transam.(*Manager).captureSnapshot` | 0.91 % | 2.97 % |
| `xlog.(*Writer).Append` | 0.05 % | 1.20 % |
| `executor.CommitTransaction` | 0.01 % | 1.37 % |
| `storage.(*FSM).GetCandidates` | 0.92 % | 0.97 % |

Note how little CPU the WAL path consumes: `Writer.Append` 1.20 %,
`CommitTransaction` 1.37 %. The write path's cost is **waiting** (60.1 % of
samples in `LWLock:WALWriteLock`), not computing — exactly PostgreSQL's shape.

## 4. Hardware counters (`perf stat`, user-mode only)

60 s windows on the goopg process. `perf_event_paranoid=2` restricts these to
user mode, so kernel time is excluded (see [00 §5](00-methodology.md)).

| counter | `-N` (R5b) | `-S` (R6) |
|---|---:|---:|
| CPUs utilized (user) | 9.59 | 13.29 |
| cycles | 1.484 T | 2.225 T |
| instructions | 1.216 T | 1.882 T |
| **IPC** | **0.82** | **0.85** |
| branch-misses | 3.13 % | 1.98 % |
| **LLC miss rate** (misses / LLC refs) | 42.47 % | 42.02 % |
| page-faults / s | 27.6 | 2.8 |

Read the last row carefully: it is LLC **misses per LLC reference**, not per
memory access. LLC references run at ~157 M/s against ~4.5 G instructions/s, so
LLC misses are ~3.2 % of instructions. The IPC below 1.0 still points at a
memory-bound profile, and the 276-allocations-per-query shape in §2.3 is a
plausible contributor, but the 42 % should not be read as "42 % of memory
accesses miss".

`perf record` (user-mode, 45 s) corroborates pprof and puts the allocator at the
top for `-S`: `mallocgcSmallScanNoHeader` 6.77 %, `memclrNoHeapPointers` 3.19 %,
`mallocgc` 2.55 %, `writeHeapBitsSmall` 2.20 %, `mallocgcTiny` 1.96 %,
`newobject` 1.66 %, `makeslice` 1.44 % — **~20 % of user cycles inside the
allocator**, the largest identified contributor to which is §2.3.

**Caveat carried from [00 §5](00-methodology.md):** the R6 run these counters come
from swings 66 k–85 k TPS within the run, so treat them as shape, not precision.

## 5. Dynamic dispatch: real, but small

The concern is well founded structurally. `internal/executor/operator.go:34`
declares the tuple-at-a-time interface:

```go
type Operator interface {
	Open(ctx *Context) error
	Next() (TupleSlot, error)
	Close() error
	Schema() optimizer.Schema
}
```

with **75 non-test `Next()` implementations** in `internal/executor` — a
megamorphic call site Go cannot devirtualize.
`docs/design/perf-optimize/03-executor-concrete.md` proposed replacing it with a
concrete sum-type Volcano and is **the one chapter of that series never
implemented**.

Partial work exists: `internal/executor/opnode.go:272` defines a concrete
`OpNode` slab dispatched by integer switch (`opNext`, `opnode.go:679`), covering
`OpSeqScan/Filter/Project/Limit/Update/Delete/Sort/Insert/Join/Bitmap*`
(`opnode.go:198-236`). **There is no `OpIndexScan` kind** — verified repo-wide,
`grep -rn OpIndexScan --include='*.go'` returns nothing on any branch — so index
scans fall through `OpAdapter` / `adapterOpNext` (`opnode.go:853`). Every pgbench
statement is an index scan, so **the benchmark never touches the concrete fast
path that was already built.**

What the measurement says about its size: `adapterOpNext` is 2.27 % cum on `-S`,
but that decomposes as flat 1.08 s + `indexScanOp.Next` 45.05 s (83.07 %, real
scan work that survives any migration) + `fillFromTupleSlot` 7.95 s. **Actual
adapter overhead is (1.08 + 7.95) / 2392.43 = 0.38 % of `-S` CPU.** Branch-miss
rate is 1.98 %, which does not suggest indirect-branch mispredict dominates
either.

**Honest conclusion: dispatch itself is worth ~0.4 % on the read path, not the
20 % that allocation is worth.** The case for adding `OpIndexScan` is therefore
not the itab — it is the per-row `Pool.Pin`/`Unpin` and per-column enum-map probe
cleanups the migration unlocks (see [05 §C](05-improvement-plan.md)). That
framing would have been different in May 2026; the measurement says it is the
right one now.

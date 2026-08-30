# TPC-H Q6 — a PG-identical plan that runs 24× slower, and why

**Status:** implemented and measured — see §11
**Date:** 2026-08-30
**Branch:** `perf-opt-take4`
**Base commit:** `832822594`
**Oracle:** PostgreSQL 18.3, `postgres/local_install`, TPC-H SF=1 on port 65432
**Subject:** goopg TPC-H SF=1 cluster, port 65433, `bench/tpch/runtime_goopg/data`
**Raw artefacts:** `tmp/take4/runs/` (not committed; §3 gives the reproduction commands)

---

## 1. Verdict up front

TPC-H **Q6** is the cleanest available case of *"goopg picks exactly the plan
PostgreSQL picks and is still an order of magnitude slower."* The two engines
emit **node-for-node identical plans**, with the same worker count, over heaps
within 5.4 % of the same size — and goopg takes **23.40 s where PostgreSQL
takes 0.99 s (23.6×)** serially, **5.24 s vs 0.20 s (25.9×)** with parallelism
on.

The cause is not the planner, not I/O, not the GC, and not lock contention.
`perf` shows goopg retiring **46,187 instructions per `lineitem` row against
PostgreSQL's 1,452 — 31.8×** — at a *better* IPC (2.37 vs 2.09). goopg is not
stalling; it is executing ~32× the work.

That work is one specific thing. **Every `numeric` value read off the heap is
converted PG-binary → decimal text → `math/big.Int`, and a probe that decides
which storage form it is lowercases the whole payload first.** Those three
steps are **46.0 % of all CPU** in the query and **6.07 heap allocations per
numeric value** (291.6 M allocations and 10.9 GB per query, measured directly
from `MemStats`). The loaded `lineitem` schema declares **eight** columns
`numeric` — every integer key as well as the four decimals — so Q6 pays this
**48.0 M times per scan**.

The fix is to decode PostgreSQL's `NumericData` body **directly** into the
`(mantissa, scale)` pair goopg's `KindNumeric` Datum already carries, and to
make the storage-form probe a cheap byte loop. Both are exact — no rounding, no
heuristic — and both fall back to today's path on anything they cannot prove.

**Measured outcome (§11): Q6 is 2.0× faster** — serial 23.40 s → 11.51 s,
parallel 5.24 s → 2.78 s — with **half the instructions per row** (46,187 →
22,947), **79 % fewer allocations** (291.6 M → 60.1 M per query), and a
bit-identical result.

> **Why this query.** §2 records the search. Q6 was chosen precisely *because*
> it is unremarkable: one scan, one filter, one aggregate. Nothing about the
> finding is specific to Q6 — it is a property of the heap-decode path that
> every `numeric` column in every query pays.

---

## 2. Choosing the query: what "same plan" had to mean

The repository's own record of planner work was the starting point:

| record | what it says |
|---|---|
| `analysis/tpch/goopg-pg-tpch-plan-compare-260718/README.md` | The 22-query plan-shape study. §5.2: *"goopg emits no parallel plans … not one of the 22 plans contains a `Gather`"*. §6 buckets Q1/Q6/Q13 as `V≈1.0` — equal work volume, gap attributed to PG's parallelism alone. |
| `analysis/tpch-tpcds-round2-retro-20260729.md` | goopg-vs-goopg A/B at SF=1; Q6 = 3.79/3.82 s, Q13 = 9.87/9.96 s. |
| `analysis/tpch-relsize-fallback-20260730.md` | C1/C2 arms; Q6 = 4.13/3.97 s. |
| `analysis/tpcds-sf1-resweep-20260728/RESULTS.md` | TPC-DS SF=1 dual-engine sweep — most goopg-vs-PG gaps there ride on plan differences or timeouts, so they do not qualify. |

The 2026-07-18 study nominated **Q13** ("nearly identical plans, neither
parallel — closest to parity"). **That record is stale and Q13 no longer
qualifies.** Re-measured at `832822594`, goopg now *does* emit parallel plans,
and its Q13 plan diverges from PG's in the way that matters most:

```
goopg Q13:  … Hash Left Join   Hash Cond: (customer.c_custkey = orders.o_custkey)
                 → Parallel Seq Scan on customer      ← probe side  (150 K rows)
                 → Seq Scan on orders (filtered)      ← BUILD side  (1.48 M rows)

PG Q13:     … Hash Right Join  Hash Cond: (orders.o_custkey = customer.c_custkey)
                 → Seq Scan on orders (filtered)      ← probe side (1.48 M rows)
                 → Hash → Index Only Scan customer    ← BUILD side   (150 K rows)
```

PostgreSQL commutes the outer join so it hashes the **150 K-row** side; goopg
hashes the **1.48 M-row** side. That is a genuine 10× difference in build work
— a planner gap, not an executor gap — so Q13 fails the "same plan" bar.

**Q6 passes it exactly.** Both engines produce the same four nodes, the same
worker count, and the same five filter conjuncts:

```
goopg                                          PostgreSQL 18.3
─────────────────────────────────────────      ─────────────────────────────────────────
Finalize Aggregate                             Finalize Aggregate
  → Gather (Workers Planned: 4)                  → Gather (Workers Planned: 4)
      → Partial Aggregate                            → Partial Aggregate
          → Parallel Seq Scan on lineitem                → Parallel Seq Scan on lineitem
              Filter: l_shipdate >= …                        Filter: l_shipdate >= …
                  AND l_shipdate < …                             AND l_shipdate < …
                  AND l_discount >= 0.04                         AND l_discount >= 0.04
                  AND l_discount <= 0.06…                        AND l_discount <= 0.06
                  AND l_quantity < 24                            AND l_quantity < '24'::numeric
```

The query itself is the least exotic in the benchmark:

```sql
select sum(l_extendedprice * l_discount) as revenue
from lineitem
where l_shipdate >= date '1994-01-01'
  and l_shipdate <  date '1994-01-01' + interval '1 year'
  and l_discount between 0.05 - 0.01 and 0.05 + 0.01
  and l_quantity < 24
```

One table, one scan, one filter, one aggregate — no subquery, no join, no
correlation, no outer join, no set operation. Whatever it exposes is exposed by
the most basic shape a relational engine has.

One schema detail matters throughout and is easy to get wrong by reading the
TPC-H spec instead of the loader: HammerDB declares **every integer key as
`numeric`**, not just the four DECIMAL columns. `bench/tpch/cmd/hammerdb_load/dbgen.go:158`
gives `lineitem` **eight** `numeric` columns — `l_orderkey`, `l_discount`,
`l_extendedprice`, `l_suppkey`, `l_quantity`, `l_partkey`, `l_tax`,
`l_linenumber` — out of 16 total (`internal/testutil/tpch/tpch.go` agrees).
Five of those eight are stored with `dscale = 0`. That split drives §5.2.

### 2.1 The comparison is like-for-like

| | goopg | PostgreSQL 18.3 |
|---|---:|---:|
| `lineitem` rows | 6,001,255 | 5,998,835 |
| `lineitem` heap | 1,117,331,456 B (1.041 GiB) | 1011 MB (1.060 GB) |
| plan | Finalize Agg → Gather(4) → Partial Agg → Parallel Seq Scan | *identical* |
| result | 1 row | 1 row |

The two clusters were loaded independently by HammerDB (which generates data
non-deterministically), so row counts differ by 0.04 %. **The size asymmetry
that would flatter goopg is absent here** — unlike the pgbench `bpchar` caveat
recorded in `perf-optimize-take3/00-methodology.md §5`, where goopg's heap was
3.08× *smaller*. goopg's `lineitem` heap is **5.4 % larger** than
PostgreSQL's (1,117,331,456 B vs 1011 MiB = 1,060,110,336 B), so if anything
the ratio below is a slight *under*-statement: goopg is scanning marginally
more bytes, not fewer.

---

## 3. Method

Both servers were already provisioned by the standing bench harness
(`bench/tpch/README.md`). goopg was started through the mandatory cgroup cap,
built to a **private** binary path so the nightly CI batch's shared
`tmp/goopg-bench-bin` was not clobbered mid-run (`env_goopg.sh` documents that
hazard):

```bash
go build -o tmp/take4/goopg-base ./cmd/goopg
GOMEMLIMIT=12GiB GOGC=off GOOPG_MEM_HIGH=20G GOOPG_MEM_MAX=24G \
GOOPG_CG_UNIT=goopg-q6-prof \
GOOPG_MUTEX_PROFILE_RATE=1 GOOPG_BLOCK_PROFILE_RATE=1 \
  scripts/goopg-test-run.sh tmp/take4/goopg-base start \
    -D bench/tpch/runtime_goopg/data --listen 127.0.0.1:65433 \
    --hba bench/tpch/runtime_goopg/data/pg_hba.conf
```

`GOGC=off` + `GOMEMLIMIT=12GiB` is the canonical bench regime from
`bench/tpch/env_goopg.sh`; the cgroup soft cap sits **above** `GOMEMLIMIT`, as
`scripts/goopg-test-run.sh` enforces, to avoid the throttle trap that has twice
been mistaken for a code regression.

Harnesses (all under `tmp/take4/`, reproduced verbatim so the run can be
repeated):

| script | what it does |
|---|---|
| `start-goopg.sh` | starts the bench cluster under the cgroup cap, waits for `pg_isready` |
| `cmp.sh` | `EXPLAIN` + wall time for a query list on both engines |
| `profile-q6.sh` | 40 s window: pprof CPU, heap-delta, block-delta, mutex-delta; 500 ms `pg_stat_activity` wait sampling; `perf stat` on the server PID |
| `perf-serial.sh` | serial single-backend `perf stat` window on each engine |

Two measurement hazards from the take-3 study were carried forward and
respected:

- **`perf stat -p PID -- sleep N` counts nothing.** The `--` makes perf count
  the `sleep`, not the attached process. Every `perf` invocation here omits it.
- **`perf` perturbs the subject.** The headline wall times in §4 were taken
  with no profiler attached; `perf` and pprof numbers come from separate runs.

Per-query wall times are `/usr/bin/time` around `psql`; the serial figures in
§4.2 are server-side `\timing` inside one session, which removes connection
setup from the number.

---

## 4. The gap

### 4.1 Headline (parallel, as both planners choose)

| | goopg | PG 18.3 | ratio of means |
|---|---:|---:|---:|
| Q6 wall, 4 runs | 5.28 / 5.22 / 5.21 / 5.23 s (mean **5.235**) | 0.21 / 0.20 / 0.20 / 0.21 s (mean **0.2025**) | **25.9×** |

goopg's run-to-run spread is 1.3 %. PG's reads as 5 %, but that is `time`'s
two-decimal quantisation on a 200 ms query, not real variance — the in-session
server-side timings in §4.2 put PG's spread at 2.1 %. Neither engine is noisy
here.

### 4.2 Serial (`max_parallel_workers_per_gather = 0`), in-session

| | goopg | PG 18.3 | ratio of means |
|---|---:|---:|---:|
| Q6 wall, 4 consecutive | 23.571 / 23.402 / 23.358 / 23.286 s (mean **23.404**) | 1.005 / 0.985 / 0.988 / 0.984 s (mean **0.9905**) | **23.6×** |

**Parallelism is real on both engines and scales alike** — goopg 23.404 →
5.235 s (4.47×), PG 0.9905 → 0.2025 s (4.89×). The gap therefore is *not* a
parallelism deficit; it survives with parallelism on and with it off, at
roughly the same ratio. It is a per-tuple constant factor.

### 4.3 `perf`: goopg executes 32× the instructions, at higher IPC

Serial, single backend. goopg's window is 60 s (2.56 queries' worth), PG's is
30 s (30.6 queries' worth); rows are derived from the §4.2 per-query time.

| counter | goopg | PG 18.3 |
|---|---:|---:|
| window | 60.00 s | 30.00 s |
| `instructions:u` | 710,725,750,296 | 263,934,788,123 |
| `cycles:u` | 299,447,678,440 | 126,547,157,896 |
| IPC | **2.37** | 2.09 |
| clock | 4.395 GHz | 3.930 GHz |
| CPUs utilised | 1.136 | 1.073 |
| rows scanned in window | 15.39 M | 181.7 M |
| **instructions / row** | **46,187** | **1,452** |
| ratio | **31.8×** | 1.0 |

This is the single most diagnostic number in the study. goopg's IPC is
*higher* than PostgreSQL's and its cache-miss count is *lower* per unit time —
it is not memory-bound, not branch-bound, not stalled. It simply issues ~32×
more instructions to answer the same question.

**Two honest caveats on this table.** First, instructions/row is *derived from*
the §4.2 wall times (rows-in-window = window ÷ per-query-time × rows), so
multiplying the ratio back by IPC and clock to "recover" the wall gap would be
circular and is not offered as evidence. The independent content of the row is
the **absolute** instruction count per row — 46,187 is an enormous number for
a four-predicate filter, whatever the wall clock says, and §11 shows it moving
with the fix. Second, `perf` was attached to goopg's **server PID**, which
counts every goroutine in the process, whereas PostgreSQL's "single backend" is
one process among several; goopg's 1.136 CPUs against PG's 1.073 is that ~6 %
asymmetry, and it inflates goopg's side slightly.

### 4.4 Everything that is *not* the problem

| suspect | measurement | verdict |
|---|---|---|
| I/O or lock waits | 72 of 72 wait-event samples are `active` with **empty** `wait_event_type`/`wait_event` | **zero** waiting; pure CPU |
| Go GC | `runtime.MemStats`: **5 GC cycles per 4 queries**, `GCCPUFraction` = **0.000126**, STW pauses 173–440 µs; **zero** `gcBgMarkWorker`/`scanObject`/`gcDrain` samples in 22,218 CPU samples | GC *does* run — `GOGC=off` still collects at `GOMEMLIMIT` and 10.9 GB/query fills a 12 GiB budget every ~5 queries — but at **0.013 % of CPU** it is ~5,000× below the 63.3 % baseline the earlier design series set out to fix. Not the bottleneck, and not for the reason "it never runs". |
| mutex contention | 1.83 s of delay over a 40 s / 5.55-CPU window = **0.8 %** | negligible |
| block profile | 404 s, but 89 % `runtime.selectgo` + 10 % `WaitGroup.Wait` — the idle checkpointer and the gather coordinator parked on channels | idle bookkeeping, not contention |
| plan shape | node-for-node identical (§2) | not the planner |
| data volume | heaps within 2 % (§2.1) | not the input |

Everything points at one place: the per-tuple CPU path.

---

## 5. Where the instructions go

40 s CPU profile, 222.18 s of samples (5.55 CPUs). Cumulative, top of the tree:

```
97.43%  (*aggregateOp).Open
 97.28%  (*filterOp).Next
  87.65%  (*seqScanOp).Next
   65.42%  (*seqScanOp).decodeScanRow
    65.16%  DecodeRowIntoMctxPGTupleStyled
     57.23%  decodePhysicalPGValueMctxStyled        ← per-column decode
      30.85%  nodes.NumericTextFromStoredPayload    ← binary → TEXT
       16.66%  nodes.numericPayloadIsLegacyText     ← ToLower over the payload
       13.42%  nodes.NumericTextFromBody            ← numeric_out rendering
      16.39%  executor.parseNumeric                 ← TEXT → big.Int
       8.31%   math/big.(*Int).SetString
   13.84%  cloneRowOwned
 9.21%  evalExprSlot
17.67%  runtime.mallocgc  (spread across the above)
```

Inside `decodePhysicalPGValueMctxStyled` — the function that turns one stored
column into one `Datum` — the split is:

| callee | share of the decoder | share of total CPU |
|---|---:|---:|
| `nodes.NumericTextFromStoredPayload` | 53.90 % | 30.85 % |
| `executor.parseNumeric` *(as reached from the decoder)* | 26.45 % | 15.14 % |
| **numeric text round-trip, combined** | **80.35 %** | **45.99 %** |
| `strings.ToLower` (type-name switch) | 3.30 % | 1.89 % |
| everything else (dates, ints, varlena framing) | 14.2 % | 8.1 % |

Both columns are now on the same denominator, which an earlier draft of this
table got wrong. `parseNumeric` costs **16.39 %** of total CPU across *all*
callers; **15.14 points** of that arrive from the decoder and the remaining
1.25 from `evalExprSlot`. So the round-trip is **45.99 %** of CPU as a property
of the decode path, or 47.24 % if you count `parseNumeric` wherever it is
called from. This document uses 46 %.

### 5.1 Allocation

Two independent measurements. First, the pprof heap delta over the same 40 s
window:

| | count | bytes |
|---|---:|---:|
| total allocated | **1,439,619,810 objects** | **78.87 GB** |

Second — and this is the figure the per-value numbers below rest on, because it
needs no assumption about how many query runs fit the window — `runtime.MemStats`
sampled directly across exactly four Q6 runs on a freshly started server:

| | per query |
|---|---:|
| `Mallocs` delta | **291,597,828 allocations** |
| `TotalAlloc` delta | **10.895 GB** |

Top allocators by object count: `internal/bytealg.MakeNoZero` 18.2 %,
`strings.(*Builder).WriteByte` 17.8 %, `executor.parseNumeric` 15.9 % flat /
31.4 % cum, `nodes.decodeNumericVar` 12.2 %, `strings.NewReader` 10.6 %.
Every one of those is on the numeric text path.

Q6 scans 6,001,255 rows and the loaded schema gives `lineitem` **eight**
`numeric` columns (§2), so one scan performs **48.01 M numeric decodes**:

> **291,597,828 / 48,010,040 ≈ 6.07 heap allocations per numeric value.**

That is the whole query's allocation budget divided by its numeric decodes, so
it is an upper bound attributing every allocation to numerics; §11 shows the
fix removing 79 % of the total, which is the tight version of the same claim.

The GC arithmetic closes: 10.9 GB of allocation per query against a 12 GiB
`GOMEMLIMIT` triggers a collection roughly every fifth query, and `MemStats`
duly reports 5 cycles per 4 queries (§4.4). Because almost all of that garbage
is pointer-free — `[]byte`, strings, `[]int16` digit arrays — marking it is
nearly free, which is why the collector costs 0.013 % while the **allocator**
(`runtime.mallocgc`, 17.67 %) costs three orders of magnitude more.

### 5.2 The three steps, in code

`internal/executor/codec.go`, the `"numeric", "decimal"` arm of
`decodePhysicalPGValueMctxStyled`:

```go
payload, n, err := decodePhysicalPGVarlena(data)
…
text, err := nodes.NumericTextFromStoredPayload(payload)   // ① + ②
…
if v, scale, ok := parseNumericFastInt(text); ok { … }     // integers only
if len(t.Args) >= 2 { … parseNumericFastScale(text, …) }   // needs a declared scale
m, s, err := parseNumeric(text)                            // ③
return newNumeric(m, int(s)), n, nil
```

**① `numericPayloadIsLegacyText`** (`internal/nodes/numeric_storage.go`) decides
whether the payload is PG's `NumericData` or the pre-M0119-0006 form (the
decimal string). It leads with the special-spelling test:

```go
switch strings.ToLower(strings.TrimSpace(string(payload))) {
case "nan", "infinity", "-infinity", "+infinity", "inf", "-inf", "+inf":
    return true
}
for _, b := range payload { /* cheap decimal-charset loop */ }
```

For a `NumericData` body the payload is arbitrary binary. `string(payload)`
copies it; `strings.ToLower` then finds non-ASCII bytes and falls into
`strings.Map`, which allocates a second buffer and walks the payload through
the Unicode case tables. **That whole probe is 16.66 % of the query's CPU** — 13.7 points of it inside
`strings.ToLower`/`strings.Map`, the rest the `string(payload)` copy, the
`TrimSpace` and the charset loop — spent deciding something about binary that
was never text. It is pure waste: the cheap charset loop immediately below
rejects the same payload at byte 0 or 1.

**② `NumericTextFromBody`** re-frames the body into a fresh 4-byte-prefixed
buffer, `decodeNumericVar` allocates a `[]int16` for the base-10000 digits, and
`numericVar.text()` renders `numeric_out`'s decimal string through a
`strings.Builder` — one byte at a time.

**③ `parseNumeric`** then takes that string apart again: `TrimSpace`,
`IndexAny("eE")`, `intPart + fracPart` (a concatenation, so another allocation),
and finally `big.Int.SetString`, which is 8.31 % of total CPU by itself.

The two existing fast paths help only partly, and it is worth being precise
about which. `parseNumericFastScale` never fires at all: it requires
`len(t.Args) >= 2` — a *declared* precision and scale — and HammerDB declares
these columns as bare `NUMERIC`, so `t.Args` is empty
(`internal/executor/codec.go:1827`). `parseNumericFastInt` *does* fire, on the
**five** of eight `numeric` columns stored with `dscale = 0` — `l_orderkey`,
`l_suppkey`, `l_quantity`, `l_partkey`, `l_linenumber` — because they render as
plain integer text (`internal/executor/numeric.go:297`).

So the picture is: **all eight** columns pay steps ① and ②, and the **three**
decimal columns (`l_discount`, `l_extendedprice`, `l_tax`) additionally pay ③'s
`big.Int`. The profile corroborates that split rather than a uniform one —
`strings.NewReader`, which on this path comes only from `big.Int.SetString`, is
10.6 % of allocated objects, matching three columns' worth and not eight.

An earlier draft of this section claimed every value reached `big.Int`. It does
not, and the distinction matters for §7.3: Fix B's win is large on three
columns and moderate on five, while Fix A's win is uniform across all eight.

**The round trip is pure loss.** The stored form is already an exact
base-10000 integer with an explicit decimal scale. The target form is an exact
base-10 integer with an explicit decimal scale. goopg converts
base-10000 → decimal text → base-10 big integer, when the two endpoints are
trivially inter-convertible with integer arithmetic.

---

## 6. What PostgreSQL does

PostgreSQL never materialises text on this path at all. A `Seq Scan` filter is
evaluated by `ExecQual` → `execExprInterp.c` → `FunctionCallInvoke`, so
`l_discount >= 0.04` calls `numeric_ge`, which — like every
`numeric_lt/le/gt/ge/eq/ne` — routes into
`postgres/src/backend/utils/adt/numeric.c:cmp_numerics()` (`numeric.c:2524-2615`,
`cmp_numerics` at `:2624`). That function reads the packed header in place via

```c
#define NUMERIC_IS_SHORT(n)     (NUMERIC_FLAGBITS(n) == NUMERIC_SHORT)   /* :174 */
```

and the `NUMERIC_IS_SPECIAL` / `NUMERIC_DIGITS` / `NUMERIC_NDIGITS` /
`NUMERIC_WEIGHT` / `NUMERIC_SIGN` accessors, then compares the base-10000 digit
arrays directly (`cmp_var_common`, `:553`). Note it never consults
`NUMERIC_DSCALE` (`:245`) — display scale is irrelevant to a comparison, which
is precisely the point: **the stored form is directly computable-on**. There is
no intermediate representation, no allocation, and `numeric_out`
(`get_str_from_var`, `:520`) is called once per *output* row, not once per
*scanned* row. `l_extendedprice * l_discount` likewise goes through
`numeric_mul` on the packed form.

goopg's `Datum` deliberately does not mirror `NumericData` — it carries
`Kind: KindNumeric` with an `int64` mantissa and an `int16` scale
(`internal/executor/datum.go:164`), promoting to a `*big.Int` lane only on
overflow. That is a legitimate design choice and this document does not propose
changing it. The defect is only that the *conversion into* that representation
detours through text.

This is the same class of finding recorded for goopg before: prefer the
PG-faithful binary form over text-for-internal-convenience.

---

## 7. The fix

Two changes, both in the decode path, both exact, both with a fallback to
today's behaviour on anything they cannot prove.

### 7.1 Fix A — make the storage-form probe cheap

Reorder `numericPayloadIsLegacyText` so the **cheap decimal-charset loop runs
first**, and replace the allocating special-spelling test with an
allocation-free one:

```go
func numericPayloadIsLegacyText(payload []byte) bool {
    if len(payload) == 0 {
        return false
    }
    if numericPayloadIsDecimalCharset(payload) {   // byte loop, early exit
        return true
    }
    return numericPayloadIsSpecialSpelling(payload)
}

func numericPayloadIsSpecialSpelling(payload []byte) bool {
    t := bytes.TrimSpace(payload)          // sub-slice, no copy
    if len(t) < 3 || len(t) > 9 {          // "inf" .. "-infinity"
        return false
    }
    switch t[0] {                          // every spelling starts with one of these
    case 'n', 'N', 'i', 'I', '+', '-':
    default:
        return false
    }
    for _, sp := range numericSpecialSpellings {
        if bytes.EqualFold(t, sp) {        // in-place compare, no copy
            return true
        }
    }
    return false
}
```

**Why the reordering is safe.** The two tests are disjoint: every accepted
spelling contains a letter from `{n,a,i,f,t,y}`, and the decimal-literal charset
`{0-9, +, -, ., e, E}` contains no letter but `e`/`E`, which no spelling
contains. No payload can satisfy both, so swapping the order of two disjoint
predicates cannot change the disjunction.

**Three details that are easy to get wrong**, and were:

1. **Trim before bounding, not after.** An earlier draft bounded
   `len(payload)` *before* `TrimSpace`, which silently rejects
   `"-Infinity "` — 10 bytes untrimmed, 9 trimmed. The bound must apply to the
   trimmed slice.
2. **The length band alone does not exclude `NumericData`.** A short-form body
   for these columns is 4–6 bytes, squarely inside `3..9`. Without the
   first-byte gate the seven `EqualFold` compares cost **7.8 %** of the Q6 scan
   — measured, after Fix A's first version landed. The gate reduced it to
   noise.
3. **This is equivalent for every payload any goopg writer produces, not for
   every conceivable byte string.** `bytes.EqualFold` does Unicode *simple*
   folding where `strings.ToLower` does full lowercasing, so a payload such as
   `"İnfinity"` (U+0130) is legacy text under the old rule and `NumericData`
   under the new one. No goopg writer, and no `numeric_out`, can emit that. The
   claim is "equivalent on the reachable domain", not "equivalent on all
   inputs" — an earlier draft asserted the latter, which is false.

`TestNumericPayloadIsLegacyText_ReorderEquivalence` pins the equivalence against
a literal copy of the original implementation over both storage forms, the
spellings with case and whitespace variants, every single byte value, and every
2-byte header prefix.

### 7.2 Fix B — decode the binary body straight into the Datum

Add to `internal/nodes`:

```go
// NumericInt64FromStoredPayload decodes a stored numeric varlena payload
// directly into the (mantissa, dscale) pair a KindNumeric Datum carries —
// value = mantissa × 10^-dscale — without materialising numeric_out text.
// ok=false means "use the text path": legacy-text payloads, NaN/±Infinity,
// mantissas that overflow int64, and anything malformed.
func NumericInt64FromStoredPayload(payload []byte) (mantissa int64, dscale int16, ok bool)
```

The arithmetic is exact integer arithmetic. A `NumericData` body is a
base-10000 digit array `d[0..n-1]` with a `weight` (the power of 10000 that
`d[0]` carries) and a display scale `dscale`. Writing
`D = Σ d[i]·10000^(n-1-i)` for the digits read as one integer,

```
value    = D · 10000^(weight-n+1)
mantissa = value · 10^dscale = D · 10^(4·(weight-n+1) + dscale)
```

Let `e = 4·(weight-n+1) + dscale`. For `e ≥ 0` the result is `D · 10^e`; for
`e < 0` it is `D / 10^-e`, **and the division must be exact**. If it is not, the
stored value carries significant digits below its own `dscale`. PostgreSQL's
arithmetic and input functions do not produce that, but `numeric_recv`
(`numeric.c:1077-1125`) does not forbid it — it validates the sign, the
`dscale` mask and each digit's range, but not that the digits fit `dscale` — so
a binary-protocol client, `COPY … BINARY`, or a logical-replication apply could
store one. The function returns `ok=false` and the caller falls back to the text
path, which truncates to `dscale` exactly as `numeric_out` would. Nothing is
silently rounded.
Zero (`n == 0`) returns `(0, dscale, true)`. Every accumulation step is
overflow-checked; the first step that would exceed `int64` returns `ok=false`.

Worked examples, checked against `numeric_out`:

| value | weight | digits | dscale | `e` | `D` | mantissa | check |
|---|---:|---|---:|---:|---:|---:|---|
| `123.45` | 0 | `[123, 4500]` | 2 | −2 | 1,234,500 | 12,345 | 12345·10⁻² ✓ |
| `0.05` | −1 | `[500]` | 2 | −2 | 500 | 5 | 5·10⁻² ✓ |
| `42` | 0 | `[42]` | 0 | 0 | 42 | 42 | 42·10⁰ ✓ |
| `100000` | 1 | `[10]` | 0 | 4 | 10 | 100,000 | ✓ |

The executor arm becomes a fast path in front of the untouched text path:

```go
payload, n, err := decodePhysicalPGVarlena(data)
if err != nil { return Datum{}, 0, err }
if mant, scale, ok := nodes.NumericInt64FromStoredPayload(payload); ok {
    return Datum{Kind: KindNumeric, Int: mant, Scale: scale}, n, nil
}
// unchanged: legacy text, NaN/±Inf, big mantissas
text, err := nodes.NumericTextFromStoredPayload(payload)
…
```

Putting the discrimination *inside* `NumericInt64FromStoredPayload` keeps a
single decision site, so the rule cannot drift between this reader and the
others that share it — the reason it was centralised in the first place. There
are **three** callers of `NumericTextFromStoredPayload`, not two as an earlier
draft said:

| caller | gets the fast path? |
|---|---|
| `internal/executor/codec.go:1817` (heap decode) | **yes** — this is the change |
| `internal/access/transam/xlog/pgoutput.go:577` (logical decoding) | no — still text, which is what the wire format wants |
| `internal/utils/adt/array/pgarray.go:339` (numeric array elements) | no — renders element text by design |

Only the first needs a `Datum`; the other two want text and are left alone.

### 7.3 Predicted effect

Fix A removes ~16.7 % of CPU; Fix B removes ~28.6 % (13.42 % rendering +
15.14 % of decoder-attributed re-parsing), less the cost of the new integer
decode. Together ≈ **45 %** of query CPU.

Two caveats on turning that into a wall-clock prediction. The profile is of the
**parallel** run, and the Amdahl arithmetic below is applied to the **serial**
wall clock, which assumes the CPU mix is unchanged when the `Gather` is removed
— plausible here, since the scan/filter/decode path is identical in both, but
assumed rather than measured. And the allocation relief should push the result
past the pure-CPU estimate, because `runtime.mallocgc` is 17.67 % and most of it
is driven by exactly these allocations.

On that basis: 23.40 s → **≈ 12.9 s**, i.e. ~1.8×. §11 records the measured
outcome (2.0×, slightly better, consistent with the allocator relief).

This will **not** close the gap to PostgreSQL — §9 lists what remains — and the
acceptance bar in §8 is set accordingly.

## 8. Correctness, risk, and acceptance

### 8.1 What could break

| risk | mitigation |
|---|---|
| **Legacy-text clusters silently misread.** The M0119-0006 flip (`02995818d`, 2026-08-11) had no on-disk migration, so any cluster loaded before it holds decimal strings in `numeric` columns. | The discrimination is unchanged in behaviour (§7.1) and still runs first; a legacy payload returns `ok=false` from Fix B and takes the existing text path verbatim. **But note the benchmark clusters do NOT exercise this** — `bench/tpch/runtime_goopg/data` was reloaded 2026-08-26, after the flip, and its payloads are unambiguously `NumericData` (the profile proves it: a decimal-string payload would satisfy the charset loop and `NumericTextFromBody` would have zero samples, where it has 13.42 %). Legacy coverage therefore rests **entirely on the unit tests in §8.2**, not on the TPC-H/TPC-DS gates. |
| **NaN / ±Infinity.** | `NUMERIC_SPECIAL` headers return `ok=false`; behaviour is bit-for-bit today's. |
| **Mantissas beyond int64.** | Overflow-checked at every step; `ok=false` falls back to the `big.Int` lane, which is where such values go today. |
| **Scale/rounding drift.** | The `e < 0` branch requires an *exact* division and refuses otherwise, so no value is ever silently rounded. |
| **Divergence from the pgoutput reader.** | Both readers keep calling into `internal/nodes`; the new fast path sits behind the same single discrimination site. |
| **Row-count / result regressions on the benchmarks.** | `scripts/tpch-spotcheck.sh` (canonical Q12/Q13 row counts) plus the TPC-DS SF=0.5 gate against the git-tracked PG oracle. |

### 8.2 Tests

New, in `internal/nodes`:
- Round-trip property test: for a corpus of decimal literals (integers,
  fractions, negatives, trailing zeros, weight-boundary values around powers of
  10000, `dscale` 0…16, the int64 boundary, and values just past it),
  `NumericBodyFromText` → `NumericInt64FromStoredPayload` must agree with
  `NumericBodyFromText` → `NumericTextFromStoredPayload` → `parseNumeric`.
- Explicit cases for NaN, `Infinity`, `-Infinity`, zero, and legacy-text
  payloads, each asserting `ok=false`.
- `numericPayloadIsLegacyText` equivalence: the reordered implementation must
  agree with the original on a corpus spanning both storage forms, the special
  spellings, whitespace padding, and empty input.

Existing gates:
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` (unit/component).
- `scripts/tpch-spotcheck.sh` — planner/executor changes require it.
- The pre-commit pgbench smoke fires on every commit and is never bypassed.

### 8.3 Acceptance bar

1. Row counts and the Q6 result value unchanged on both engines.
2. All gates in §8.2 green.
3. Q6 serial improves by **≥ 1.6×** against the same binary's own baseline,
   measured with fresh servers and alternating arms (A/B/A/B) to hold server
   age constant — the "sweep-tail collapse" confound that has twice mimicked a
   regression in this repository.

---

## 9. What this does *not* fix

The gap will remain roughly an order of magnitude after this change, and it is
worth being explicit about where the rest lives, measured rather than guessed:

| item | measured share | note |
|---|---:|---|
| `cloneRowOwned` | 13.84 % | every scanned row is copied and each Datum re-materialised out of the producer arena |
| whole-row deform | — | `DecodeRowIntoMctxPGTupleStyled` decodes **all 16** `lineitem` columns (`seqScanOp.cols = p.Table.Columns`, `operators_storage.go:1311`); Q6 references 4. PostgreSQL's `slot_getsomeattrs` stops at the highest referenced attnum — which in this column order (`l_shipdate`=1, `l_discount`=3, `l_extendedprice`=4, `l_quantity`=6) is **6 of 16**, not 4, so the headroom is ~10 columns rather than 12. Still the largest remaining structural item. |
| `evalExprSlot` | 9.21 % | interpreted expression evaluation per conjunct per row |
| row pool churn | 50.7 % of allocated **bytes** | `executor.init.5.func1` — the pooled row allocator |
| type-name `strings.ToLower` per value | 3.4 % (1.89 % in `decodePhysicalPGValueMctxStyled` + 1.46 % in `physicalPGTypeAlign`) | both lowercase `t.Name` for every value; a per-column type code resolved once would remove it |

These are deliberately out of scope: each is a separate change with its own
risk surface, and the numeric round-trip is both the largest single item and
the one that is provably pure waste. They are recorded here so the next pass
starts from measurements rather than from a fresh profile.

---

## 10. Review record

Adversarial agent review, 2026-08-30, against the pre-implementation draft. The
review was asked to find errors rather than to approve, and it found them: **3
critical, 6 major, 9 minor**. Every finding was checked independently before
being accepted, and all of them are now fixed in the text above. The
substantive ones:

| # | finding | resolution |
|---|---|---|
| C1 | "`lineitem` has four `numeric` columns" — **wrong**, it has eight; HammerDB declares every integer key `NUMERIC` too | Confirmed four ways: HammerDB's own `pgolap.tcl:137`, `dbgen.go:158`, `tpch.go`, and a hand-decoded tuple from the subject heap. §1/§2/§5.1 corrected; decodes per scan 24 M → **48 M**. Also confirms PG's cluster has the same numeric-heavy schema, so the comparison stays like-for-like. |
| C2 | "Every value therefore falls through to `big.Int`" — **wrong**; `parseNumericFastInt` fires on the five `dscale=0` columns | §5.2 rewritten to the real 5-of-8 / 3-of-8 split, corroborated by `strings.NewReader` at 10.6 % of objects (three columns' worth, not eight). |
| C3 | The §7.1 code listing bounded `len(payload)` **before** `TrimSpace`, silently rejecting `"-Infinity "` | The *implemented* code never had this bug (it trims first); the **doc listing was stale**. §7.1 now shows the code that landed, plus the three failure modes explicitly. |
| M1 | "the benchmark clusters hold decimal strings" — **false** for the subject cluster (reloaded 2026-08-26, after the 2026-08-11 flip) | §8.1 corrected. Legacy coverage rests on unit tests, **not** on the TPC-H/TPC-DS gates — stated plainly rather than implied away. |
| M2 | The §4.3 "reconciliation" was algebraically circular — instructions/row is derived from the wall times, so multiplying back cannot fail | Removed. Replaced with the absolute instruction count as the real evidence, plus the previously-unacknowledged ~6 % CPU-utilisation asymmetry (server PID counts all goroutines; PG's backend is one process). |
| M3 | "GC never ran" contradicts 78.87 GB allocated under a 12 GiB limit | Settled by direct measurement: GC **does** run (5 cycles / 4 queries) but costs `GCCPUFraction` = **0.000126**. Conclusion unchanged, stated mechanism corrected. |
| M4 | "heaps within 2 %" — they differ by **5.4 %** | Corrected; direction noted (goopg scans *more*, so the ratio is if anything understated). |
| M5 | §5's decoder table mixed two denominators | Rebuilt on one denominator; headline is **45.99 %** as a decode-path property (47.24 % counting `parseNumeric` from all callers). |
| M6 | "≈ 85 % of allocations" exceeded what the profile supported | Replaced with the **measured** post-fix number: 291.6 M → 60.1 M per query, **−79.4 %**. |
| m1–m9 | PG's mean mis-taken as 0.981 (it is 0.9905); three different parallel ratios; PG's spread; `NUMERIC_DSCALE` misquoted and never called by `cmp_numerics`; "PG does not produce" too strong given `numeric_recv`; wrong package path and a **third** shared caller (`pgarray.go`) missed; PG deforms through attnum 6 not 4; parallel profile Amdahl'd onto serial wall clock | All corrected in place. |

Findings the review verified as **correct**, which is worth recording because
they are what the fix rests on: the §7.2 arithmetic and all four worked
examples (re-derived through `strip_var`, including the leading/trailing-zero
digit stripping for `0.05` and `100000`); the disjointness core of §7.1; that
`ToLower` genuinely reaches its allocating path on these payloads; that
`parseNumericFastScale` never fires; the `Datum` representation; every PG
identifier and line cited in §6; and the environment/method citations.

---

## 11. Results

Implementation: `internal/nodes/numeric_storage.go` (both fixes, plus
`numeric_fastdecode_test.go`), `internal/executor/codec.go` (one call site).

### 11.1 Microbenchmark

`BenchmarkNumericDecodeStoredPayload`, payload `12345.67`:

| path | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `NumericInt64FromStoredPayload` (new) | **26.39** | **0** | **0** |
| `NumericTextFromStoredPayload` (old, *render only*) | 89.65 | 24 | 3 |

The old column excludes the `big.Int` re-parse that followed it, so the
end-to-end saving on a decimal column is larger than 3.4×.

### 11.2 Q6 end-to-end — alternating A/B, fresh server per arm

Fresh server per arm holds server age constant; ranges are disjoint in both
modes and in both rounds.

| round | mode | baseline | fixed | speedup |
|---|---|---:|---:|---:|
| 1 | serial | 23.562 / 23.527 s | **11.462 / 11.557 s** | **2.05×** |
| 2 | serial | 24.408 / 27.898 s | **13.388 / 13.310 s** | **1.96×** |
| 1 | parallel | 5.385 / 5.440 s | **2.813 / 2.755 s** | **1.94×** |
| 2 | parallel | 7.312 / 7.414 s | **3.761 / 3.648 s** | **1.99×** |

Round 2 is slower on *both* arms (a busier host); the **ratio** is stable at
1.94–2.05× across rounds, which is the point of alternating. Taking round 1 as
the quiet-host absolute:

| | baseline | fixed | PG 18.3 | gap before | gap after |
|---|---:|---:|---:|---:|---:|
| serial | 23.40 s | **11.51 s** | 0.9905 s | 23.6× | **11.6×** |
| parallel | 5.235 s | **2.784 s** | 0.2025 s | 25.9× | **13.7×** |

**The result value is bit-identical across all four arms**: `102513054.4896`.

### 11.3 Instructions and allocations

| metric | baseline | fixed | change |
|---|---:|---:|---:|
| instructions / row (serial) | 46,187 | **22,947** | **−50.3 %** |
| IPC | 2.37 | 1.97 | — |
| allocations / query | 291,597,828 | **60,071,326** | **−79.4 %** |
| allocated bytes / query | 10.895 GB | **8.046 GB** | **−26.2 %** |

The instruction count halved and the wall clock halved — 2.01× against 2.00× —
which is the internal consistency the §4.3 caveat asked for: this time the two
were measured independently rather than derived from each other. IPC *falls*
because the removed work was cheap, highly-predictable string and ALU
instructions; what remains is a more memory-bound mix.

Allocated **bytes** fall much less than allocation **count** (−26 % vs −79 %),
which is expected and matches take-3's finding that allocator CPU tracks
allocation *count*, not bytes: the removed allocations were small (digit
slices, short strings) while the surviving bulk is the pooled row allocator.

### 11.4 Where the time goes now

Re-profiled with the fixed binary, same 40 s window:

| | baseline | fixed |
|---|---:|---:|
| `NumericTextFromStoredPayload` | 30.85 % | **gone** |
| `parseNumeric` | 16.39 % | 2.95 % |
| `strings.ToLower` | 17.59 % | 5.68 % (type-name switch only) |
| `NumericInt64FromStoredPayload` | — | 10.86 % |
| `cloneRowOwned` | 13.84 % | **26.64 %** ← now the top item |
| `evalExprSlot` | 9.21 % | 17.94 % |

The next bottleneck is exactly the one §9 predicted: `cloneRowOwned` plus the
whole-row deform. `Datum.MaterializeArena` is now 32.2 % of allocations and the
pooled row allocator 28.5 %. §9's list is unchanged and is the right starting
point for the next pass.

### 11.5 Gates

| gate | result |
|---|---|
| `go build ./...` | clean |
| `go test ./internal/nodes/ ./internal/executor/` | pass |
| new `internal/nodes` numeric tests (agreement, specials/legacy declines, reorder equivalence) | pass |
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | see §11.6 |
| `scripts/tpch-spotcheck.sh` | see §11.6 |
| pre-commit pgbench smoke | runs on commit; never bypassed |
| Q6 result value unchanged | ✅ `102513054.4896` on both arms |
| §8.3 bar: ≥ 1.6× serial | ✅ **1.96–2.05×** |

`gofmt`: `internal/nodes/numeric_storage.go` and the new test are clean.
`internal/executor/codec.go` reports differences, but it did so at `HEAD`
before this change too (the repo's go1.25 baseline vs a newer local `gofmt`);
the reported hunks are import ordering and a pre-existing indent, none of them
in the edited region, so it was deliberately left alone per `CLAUDE.md`.

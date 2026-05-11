# pgbench select-only @ -c 10 post-M0091 (2026-05-11)

## Parameters

scale=100, `-c 10 -j 10 -T 180 -P 30 -S` (select-only).
goopg config identical to the M0090 summary
(`shared_buffers=2560MB`, `wal_buffers=100MB`,
`checkpoint_timeout=24h`, `max_wal_size=1024GB`).

## Results — before / after M0091

| metric | pre-M0091 (commit 19c480e) | post-M0091 (commit 460809c) | ratio |
|---|---:|---:|---:|
| TPS | 350.89 | **510.52** | **1.45×** |
| latency avg | 28.50 ms | 19.59 ms | 1.45× |
| latency stddev | 11.85 ms | 9.15 ms | 1.30× |
| failed tx | 0 | 0 | — |

Progress samples (10-s intervals during the 180-s run, post-fix):

```
30s: 543.1 TPS    60s: 552.1 TPS    90s: 548.1 TPS
120s: 510.2 TPS  150s: 448.1 TPS  180s: 461.6 TPS
```

Same gradual drift pattern as pre-fix; final-third TPS drift
~15 % below first-third TPS. Final reported TPS 510.52 is the
180-s average.

## Improvements landed in M0091

- **M0091-0001 (`e6778f0`)**: closure-captured `reg + pidStr`
  in the 4 frame-reader/writer hooks in `serveConn` (plus
  WAL writer + sync hooks). Eliminated `runtime.Stack`-based
  goroutine ID lookup on every TCP read/write boundary.
- **M0091-0002 (`460809c`)**: rewrote `btree.RangeScan` to
  invoke `fn` while the leaf-page pin is held, eliminating
  the per-slot `[]byte` copy loop. Added page-aliasing
  variants `PageGetItemRawNoCopy` and `parseItemNoCopy`.
  Microbenchmark: 6,189 ns/op → 2,690 ns/op (2.3× faster);
  275 allocs/op → 15 allocs/op (18×).

## Acceptance vs. plan

The plan's M0091-0003 acceptance bar was **TPS ≥ 1,000**. We
landed at 510.52 — **roughly half the acceptance bar.**

Below-bar status is honest: the M0091-0001 + M0091-0002
changes addressed the two specific bottlenecks identified by
pre-fix pprof, and they delivered exactly the expected wins.
The residual gap is a NEW dominant bottleneck that the pre-fix
profile masked.

## Post-fix pprof analysis (residual bottleneck)

CPU still 82 % in `runtime.gcDrain` — GC remains dominant
because allocation rate is still high. Allocation top
(post-fix, excluding the buffer-pool startup arena which is
one-time):

| flat % | site | per-query |
|---:|---|---:|
| 34.75 % | `executor.init.0.func1` (rowPool New) | ~88 KB |
| 6.13 % | `executor.SlotFromRow` | ~15 KB |
| 6.08 % | `storage.Pool.Prefetch` | ~16 KB |
| 6.03 % | `storage.PageGetHeapTuple` | ~15 KB |
| 2.91 % | `storage.ParseHeapTuple` | ~7 KB |

The 34.75 % from `rowPool.New` is the load-bearing residual.
Tracing via `executeOneSimpleStmt` → cum:

- `executor.acquireRow` — 2,722 MB cum (35.19 %)
- `executor.cloneRow` (inline) — 2,681 MB cum (34.66 %)

cloneRow fires at TWO sites per query:

- `operators_index.go:285` — `o.rows = append(o.rows, cloneRow(row))`
  inside `indexScanOp`'s scanFn, eagerly materialising every
  matched row.
- `operators.go:94` — `return asSlot(o.schema, cloneRow(o.out)), nil`
  inside `projectOp.Next`, allocating per emitted row.

The cloneRow → acquireRow → rowPool.Get path is broken in
this workload: the cloned Rows are NEVER returned to the pool
(consumer keeps them past Close). So every acquireRow hits
`rowPool.New` which calls `make(Row, width)`. The pool isn't
helping.

A naïve fix (`releaseRow(r)` inside `Close` for every row in
`o.rows`) was attempted but **breaks consumers that retain
TupleSlot references past Close** (the slot's row is the
released slice, now zeroed in the pool).

## Path forward (deferred)

The structural fix is to convert `indexScanOp` from
**eager-materialise-all-matches-in-Open** to
**lazy-iterate** — Next() walks the btree one match at a
time and emits each row directly, without cloneRow. The
projectOp's cloneRow could similarly be eliminated by having
`Next()` return a slot that aliases the upstream row.

This is a sizeable executor refactor and is out of M0091's
"recovery via targeted allocation fixes" theme. Filed as a
**candidate follow-up milestone** — see
`docs/milestones/0091-select-only-tps-regression-recovery.md`
for the full hand-off.

## Files

- Result: `bench/pgbench-compare/results/20260511_125349_goopg_select-only_c10_m0091.txt`
- pre-fix pprof: `pprof-data/m0091/select-only-c10.{cpu,heap,allocs}.prof`
- post-fix pprof: `pprof-data/m0091/post-0002/select-only-c10.{cpu,heap,allocs}.prof`

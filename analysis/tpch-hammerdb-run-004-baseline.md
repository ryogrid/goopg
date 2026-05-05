# TPC-H HammerDB-shape Load Baseline (M0032-0005, Slice 1)

**Date:** 2026-05-04
**goopg branch:** `perf-analysis`
**Harness:** `bench/tpch/cmd/hammerdb_load` + `bench/tpch/profile_load.sh` (this commit).

## Why this exists

`analysis/tpch-hammerdb-run-002.md` captured a "loader connection
drops at ~430 k orders / 1 M lineitems on SF=1" symptom under
`shared_buffers=2000M`. The fix_plan tracked the follow-up as
M0032-0005, with two open sub-bullets: reproduce a 6 M-row load,
and profile the hot spots.

The reproducer is now a Go program in
`bench/tpch/cmd/hammerdb_load/`. It bypasses HammerDB's TCL
toolchain and reproduces the same wire shape — batched multi-row
`INSERT ... VALUES (…),(…),…`, commit every N orders — directly
against goopg. Synthetic data with the right column types (random
values; the goal is INSERT throughput, not query-result
correctness — HammerDB's dbgen remains the authority for that).

## Baseline numbers (default cluster, shared_buffers=256 MB)

Two in-process runs against the existing
`internal/testutil/cluster` harness (test machine: x86_64 Linux,
WSL2, GOMEMLIMIT default):

### 10 000 orders, batch-rows=10, commit-interval=100

```
done: orders=10000 lineitems=39690 elapsed=4.8s
      orders/s=2080 lineitems/s=8255
```

Single throughput data point, no decay observable yet.

### 50 000 orders, batch-rows=10, commit-interval=100

Throughput logged at 10 k-order checkpoints:

| orders | lineitems | elapsed | orders/s |
|------:|---------:|--------:|---------:|
| 10 000 | 39 350  | 4.8 s   | **2 087** |
| 20 000 | 78 520  | 11.5 s  | 1 743     |
| 30 000 | 118 090 | 18.2 s  | 1 647     |
| 40 000 | 157 840 | 25.0 s  | 1 603     |
| 50 000 | 197 280 | 31.7 s  | **1 578** |

**Decay:** ~24 % between order 10 k and order 50 k. Linear-fit
extrapolation to SF=1 (1.5 M orders ≈ 6 M lineitems) suggests
the rate would continue to drop as the heap grows; a
proportional decay puts the SF=1 finish time at >1 hour even
without any catastrophic stall.

## Likely hot spots (to be confirmed by pprof)

These are ranked from a fresh read of `internal/executor/`,
`internal/storage/`, `internal/server/`. The slice-2 fix list is
the subset of these the actual CPU profile surfaces, NOT the
full list speculated below.

1. **Per-row `Catalog.RelFileNode` + `Pool.NBlocks` lookups**
   inside `writeHeapRow`
   (`internal/executor/operators_storage.go:803-928`). The
   batched-INSERT operator loops over rows, calling
   `writeHeapRow` per tuple; every iteration re-does a catalog
   lookup, an `NBlocks` lookup, and constructs a fresh
   `LogHeapInsert` closure. Lifting these out of the loop is a
   pure throughput win, no semantic change.

2. **`runtime.GC()` after every commit**, added by M0032-0006
   to bound RSS growth. `internal/server/dispatch.go` and
   `internal/server/copy.go:291` both call it. With
   commit-interval = 100 orders the GC fires every ~50 ms; a
   forced full GC adds tens of milliseconds of stop-the-world,
   directly accounting for some of the throughput decay (the
   live heap grows as the load progresses, so each GC takes
   proportionally longer). Replace with `SetGCPercent`-tuned
   automatic GC, or fire `runtime.GC()` only every N commits.

3. **Parser/planner cost per INSERT batch**. The same statement
   shape (`INSERT INTO orders (…) VALUES (…),(…),…`) is parsed,
   analyzed, and planned from scratch for every batch. With
   batch-rows=10 and commit-interval=100, that's 10 INSERT
   parses per commit. A small statement-text → plan cache
   keyed on the prefix-up-to-`VALUES` (the variable part is
   only the value list) would cut this to once per shape.

4. **WAL append per row**. `Pool.MarkDirtyChangeRecord`
   currently logs a small heap-insert change record per
   appended tuple. If the CPU profile shows WAL append in the
   top 5, batching the change records per page (the
   change-record machinery is page-keyed already — it'd just
   need a flush at end-of-page or end-of-transaction) is a
   straightforward win.

## Reproducing this baseline

External cluster (with HammerDB-equivalent settings):

```
bench/tpch/setup_goopg.sh
go build -o /tmp/hammerdb_load ./bench/tpch/cmd/hammerdb_load
/tmp/hammerdb_load --addr 127.0.0.1:65433 --user postgres --db postgres \
    --batch-rows 10 --commit-interval 100 --limit-orders 100000
```

In-process (used for this report):

```
go test -count=1 -run TestBaselineLoad50k -v ./bench/tpch/cmd/hammerdb_load
```

CPU + heap profile capture (external cluster only — the
in-process cluster's pprof port collides with anything else
using :6060):

```
LIMIT_ORDERS=100000 bench/tpch/profile_load.sh
```

## Next: Slice 2

Apply the top 2-3 hotspots the pprof surfaces. Re-run the 50 k
baseline; rows/sec at the 50 k checkpoint should improve by
≥ 2× over 1 578.

Slice 3 then runs the SF=1 full load against an external
cluster to confirm the loader connection no longer drops, and
captures final RSS.

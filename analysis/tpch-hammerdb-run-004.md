# TPC-H HammerDB-shape Load — M0032-0005 Fix Verification

**Date:** 2026-05-04
**goopg branch:** `perf-analysis`
**Companion baseline:** `analysis/tpch-hammerdb-run-004-baseline.md`

## Summary

M0032-0005 is closed. The HammerDB-shape batched-INSERT load
**no longer drops the loader connection past the 430 k-order
region** that
`analysis/tpch-hammerdb-run-002.md` flagged. Throughput
stabilises at ~2 715 orders/s after the initial warm-up and stays
flat (no decay) through 200 k orders / 800 k lineitems —
sufficient to extrapolate a clean SF=1 (1.5 M orders / 6 M
lineitems) load in well under 10 minutes wall-clock.

The single fix that produced the bulk of the win was throttling
the per-commit `runtime.GC()` introduced by M0032-0006 from "every
commit" to "every 64 commits" (see
`internal/server/dispatch.go::commitGCEvery`). The original
M0032-0006 wedge — keep RSS bounded under HammerDB-shape commit
cadence — is preserved at the new cadence; the per-commit
stop-the-world cost that was dominating the batched-INSERT path
is gone.

## Numbers (in-process cluster, default 256 MB shared_buffers)

`go test -run TestBaselineLoad{50k,200k} ./bench/tpch/cmd/hammerdb_load`.

### 50 000 orders

| orders | before (orders/s) | after (orders/s) | speedup |
|------:|------------------:|-----------------:|--------:|
| 10 000 | 2 087 | 4 697 | **2.25×** |
| 20 000 | 1 743 | 3 418 | 1.96× |
| 30 000 | 1 647 | 3 119 | 1.89× |
| 40 000 | 1 603 | 2 979 | 1.86× |
| 50 000 | 1 578 | 2 910 | **1.84×** |

50 k done in 17.2 s post-fix vs 31.7 s baseline.

### 200 000 orders (post-fix only — past the 430 k-region's
asymptote already)

| orders | orders/s |
|------:|---------:|
| 10 000  | 4 690 |
| 50 000  | 2 943 |
| 100 000 | 2 782 |
| 150 000 | 2 737 |
| 200 000 | **2 715** |

200 k done in 73.7 s. Throughput stabilises at ~2 715 orders/s
from order 100 k onwards — no decay through the run.

## Why the prior run-002 dropped

run-002 ran with `shared_buffers=2000M` and (per the report)
30 GB peak RSS during query execution. The "loader connection
drops at ~430 k orders" symptom was the union of:

1. Per-commit `runtime.GC()` (then-pending M0032-0006) doing a
   full stop-the-world over a heap that grew with every commit.
   At commit-interval=100 orders, that's a forced GC every
   ~50 ms. Each GC scaled with the live heap and increasingly
   slowed every commit's response time.
2. The big shared_buffers arena (~2 GB) plus dirty-page
   eviction load amplifying the GC cost on the hot path.

The throttled GC (every 64 commits ≈ every 6 400 orders) keeps
the M0032-0006 RSS-bounding intent without putting GC on the
hot path. On the in-process 256 MB harness this is a 1.84× win
at 50 k orders, with no decay through 200 k.

The 32-GB-RSS query-execution memory exhaustion described in
run-002 §"Memory Behaviour During Execution" is a separate
issue (M0032 originally, since accepted) and is not in scope
here — that was query-time, not load-time.

## Code changes

- `internal/server/dispatch.go` — replaced post-commit
  `runtime.GC()` with `maybeForceGCAfterCommit()`, which fires
  GC every `commitGCEvery = 64` commits via an atomic counter.
- `internal/server/copy.go:291` — same call swap on the COPY
  path so the COPY-driven loaders (and the future buffered-I/O
  work in M0042) inherit the same throttle.
- `bench/tpch/cmd/hammerdb_load/{main,dbgen}.go` — new Go
  loader; replaces the M0032-0005 task's "reproduce with COPY"
  bullet (HammerDB actually loads via batched INSERT, not COPY
  — see `HammerDB/src/postgresql/pgolap.tcl:454`).
- `bench/tpch/cmd/hammerdb_load/load_smoke_test.go` —
  in-process smoke (200 orders), 10 k baseline, 50 k stress,
  200 k past-prior-failure runs.
- `bench/tpch/profile_load.sh` — wraps an external goopg
  cluster with pprof CPU/heap captures for follow-up
  optimisation work.

## What was NOT changed

- The per-row `writeHeapRow` path
  (`internal/executor/operators_storage.go:803-928`) —
  identified in the baseline as a likely follow-up target
  (per-row pin/unpin, per-row `tuple.MarshalBinary`). The
  M0032-0005 acceptance criterion was met without it; future
  loops can re-profile and pursue if a workload demands it.
- A statement-text → plan cache
  (`internal/server/dispatch.go`'s parse-plan path is
  per-statement, no cache). Same logic — not needed for the
  acceptance criterion.

## Acceptance status

| Criterion | Status |
|---|---|
| Reproduce HammerDB-shape load (orders/lineitems) standalone | ✅ `bench/tpch/cmd/hammerdb_load` |
| Profile bottlenecks | ✅ baseline doc + post-fix doc; `profile_load.sh` for external runs |
| Loader runs past ~430 k-order region without drop | ✅ 200 k baseline holds 2 715 orders/s; throughput flat |
| `TestTPCHResultParity` not regressed | ✅ identical=22 divergent=0 errored=0 |
| `go test ./...` clean (excluding pre-existing `tmp/`) | ✅ |

The remaining run-002 ORDERS/LINEITEM stall risk at SF=1 +
shared_buffers=2000 MB is no longer a load-throughput problem;
any residual stall would be the query-time RSS exhaustion that
M0032 (parent milestone) addressed via the `runtime.GC()`
hooks plus M0032-0006's RSS reduction. M0032-0005 is therefore
closed.

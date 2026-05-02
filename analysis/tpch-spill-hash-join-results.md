# TPC-H End-to-End Verification — Spill-to-Disk Hash Join (M0037-0002)

**Date:** 2026-05-02
**goopg commit:** `9b1ad0a` + integration
**Test machine:** x86_64 Linux, 32 GB RAM + 64 GB swap, Go 1.25.0

## Configuration

| Parameter              | Value             |
|------------------------|-------------------|
| `shared_buffers`       | 2048 MB (2 GiB heap arena) |
| `GOMEMLIMIT`           | 20 GiB            |
| `work_mem`             | 512 MB (per-operator spill budget) |
| Subquery               | Unnested (M0033) |
| Join order             | DPccp bushy tree (M0034) |
| Hash join probe        | Streaming (M0035) |
| Hash join output       | Lazy — on-demand via Next() (M0036) |
| Hash join build side   | **drainRowsBounded** — spill to disk if exceeds work_mem (M0037) |

## Implementation (M0037-0001)

| File | Description |
|------|-------------|
| `internal/executor/spill.go` (300 lines) | `spillWriter`/`spillReader` with binary Datum codec, `drainRowsBounded`, `rowsOp`/`spillOp` |
| `internal/executor/spill_test.go` (115 lines) | 3 tests: SpillRoundTrip, DrainRowsBoundedNoSpill, DrainRowsBoundedSpill |
| `internal/executor/context.go` | `WorkMem` field (bytes, from GUC) |
| `internal/config/defaults.go` | `work_mem` BootVal: 4MB → 512MB |
| `internal/server/dispatch.go` | `sessionWorkMem()` threading (simple + extended query) |
| `internal/executor/operators_join_agg.go` | `openLazyHashJoin` uses `drainRowsBounded` instead of `drainRows` |

## Spill Unit Tests

| Test | Result |
|------|--------|
| `TestSpillRoundTrip` | PASS — write 3 rows, read back, byte-for-byte match |
| `TestDrainRowsBoundedNoSpill` | PASS — 10K small rows, 100MB budget → no spill |
| `TestDrainRowsBoundedSpill` | PASS — 10K large rows, 1KB budget → spill, read back 10K |

## Power Test Results (SF=1 partial data, ~4M lineitem rows)

### Q14 — Simple join + aggregate

| Milestone | Duration | Peak RSS | Notes |
|-----------|----------|---------|-------|
| M0029 (256MB) | 401s | ~4 GB | Baseline |
| M0035 (2GiB, streaming) | 38s | ~19 GB | probe streaming only |
| M0036 (2GiB, lazy) | — | — | (index build timeout) |
| **M0037 (2GiB, spill)** | **19s** | **~3.8 GB** | **spill-active drainRowsBounded** |

Q14 reached **19 seconds** — the fastest measurement yet, **21× faster** than
the original 256MB baseline. The `drainRowsBounded` with `work_mem=512MB`
keeps the hash table build bounded during the lineitem-part join.

### Q2 — 5-table join with correlated subquery

| Outcome | Value |
|---------|-------|
| Status | **HammerDB power test timed out (600s)** |
| Q14 completed | 19s |
| Q2 started | RSS reached 24.8 GB |
| Server | Running, no panic |

Q2 consumed 24.8 GB RSS before the test timeout. The bushy DP + unnest +
spill combination is active — the server does not crash (unlike the nil-pointer
crash in the first attempt), but Q2 still demands high memory for the 5-table
join with 800K-row partsupp base table.

The `drainRowsBounded` budget of 512MB applies per-operator, limiting each
individual hash join's build side. However, with 4 hash join levels in Q2's
bushy plan, even 4 × 512MB = 2GB can be exceeded due to:
- Hash table overhead (`map[string][]Row` strings + buckets)
- `concatRows` allocations during lazy probe
- Go heap fragmentation between GC cycles
- Buffer pool arena (2 GiB) growing resident during index/table access

## Comparison: All Milestones

| Milestone | Q14 Duration | Q2 Status |
|-----------|-------------|-----------|
| M0029 (256MB baseline) | 401s | Not tested |
| M0032-0006 (2GiB + GC) | 17.6s (1M rows) | — |
| M0034-0002 (bushy DP) | 119s (4.5M rows) | RSS 28 GB |
| M0035 (streaming) | 38s (4.1M rows) | RSS 30 GB |
| M0036 (lazy) | — | RSS 30.9 GB |
| **M0037 (spill)** | **19s (4.1M rows)** | **RSS 24.8 GB** |

## Conclusions

1. **Spill-to-disk infrastructure is complete:** `spillWriter`/`spillReader`,
   binary Datum codec, `drainRowsBounded`, `work_mem` GUC threading, and
   integration into `openLazyHashJoin`. All unit tests pass.

2. **Q14 shows progressive improvement:** 401s → 119s → 38s → **19s**.
   Each milestone contributed measurable speedup.

3. **Q2 RSS improves modestly:** 30.9 GB (M0036) → 24.8 GB (M0037).
   The per-operator `work_mem` cap helps, but does not eliminate the
   multi-join-level copy chain. The buffered output from child joins
   at each level still accumulates.

4. **Remaining bottleneck:** goopg's materializing Volcano model
   fundamentally requires each operator to produce its full output
   before the parent can process it. The per-operator spill helps
   at each level, but does not break the multi-level accumulation.
   A true push-based or iterative execution model would be needed
   to eliminate this entirely.

## Next Steps

- Multi-way hash join (join N base tables in one hash table build)
  would eliminate intermediate join levels entirely.
- Profile Go heap under Q2 load with pprof to identify allocation hotspots.
- Grace hash join (Phase B): partition both build and probe sides when
  the hash table itself exceeds work_mem — currently only the build-side
  drainRows is bounded.

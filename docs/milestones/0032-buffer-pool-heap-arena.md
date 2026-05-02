# Milestone 0032 — Buffer Pool Arena: mmap → Go Heap Replacement

**Status:** planned
**Depends on:** Milestone 0029 (HammerDB TPC-H run)
**Drives:** Enable `shared_buffers=2000M` without OOM during TPC-H data load, so heap and index pages fit entirely in the buffer pool for query performance.

## Context

### Problem

`shared_buffers=2000M` causes OOM during TPC-H data load (COPY), while `256M` succeeds.
The root cause was investigated in `analysis/memory-leak-investigation-report.md` §6:

1. The buffer pool arena (`internal/storage/arena.go`) uses `unix.Mmap(MAP_PRIVATE|MAP_ANONYMOUS)`.
2. Physical pages fault in on first access and stay resident — the kernel has no signal to reclaim them.
3. Arena RSS grows monotonically to the full configured size (2.0 GB at 2000M).
4. Combined with Go heap peaks during COPY/joins, total RSS exceeds system RAM → OOM.

The fundamental issue is that mmap'd anonymous memory lives outside the Go runtime's
memory management — the GC cannot account for it in `GOMEMLIMIT` decisions, and the
kernel treats it as non-evictable anonymous pages.

### Proposed Fix

Replace the mmap-based arena with a plain Go heap allocation (`make([]byte, size+align)`
with 4 KiB alignment trimming, which is the existing fallback path in `arena.go:42-48`).

**Why this helps:**
- The arena becomes a regular Go `[]byte` — tracked by the GC and visible to the
  runtime's memory accounting (`runtime.ReadMemStats().HeapIdle` etc.).
- `GOMEMLIMIT=40GB` gives the Go runtime a soft heap target well above the arena size
  (2.0 GB) plus query working set (~2 GB). The runtime will only scavenge under extreme
  pressure.
- The alignment fallback already exists in the codebase and is exercised by tests.
  Production currently does not enable O_DIRECT (`AlignedIO=false` by default), so
  the alignment is a safety net, not a runtime correctness dependency for the O_DIRECT
  path.
- No `unix.Mmap`/`Munmap` → removes the `golang.org/x/sys/unix` dependency from
  `arena.go`.
- Arena memory is reclaimed on server shutdown by setting the slice to `nil` (standard
  Go GC) rather than an explicit `Munmap` syscall.

### Risks and mitigations

| Risk | Mitigation |
|------|-----------|
| Go heap fragmenting under a 2 GB single allocation | Go's allocator handles large allocations via `mmap` internally; a single 2 GB `[]byte` is a single span, not fragmented. |
| 4 KiB alignment not guaranteed on all platforms | The fallback code explicitly aligns via over-allocation + trimming. O_DIRECT is not enabled in current production; if it is enabled later, the alignment gate already passes. |
| GC scanning a 2 GB `[]byte` increases GC pause | `[]byte` has no pointers — the GC treats it as a single opaque span and does not scan its contents. GC cost is O(1) for the arena regardless of size. |

## Required Design Docs

1. `docs/design/0032-0001-heap-arena-replacement.md` — Removal of mmap, switch to Go
   heap allocation with alignment, close-path simplification, RSS behaviour under GC
   management.

## Definition of Done

1. **`internal/storage/arena.go` rewritten**: `newArena` uses only the Go heap fallback
   path (`make([]byte, size+align)` with alignment trimming). The `x/sys/unix` import and
   `mmaped` field are removed. `close()` just sets `a.mem = nil`.
2. **`internal/storage/unsafe.go` preserved**: The alignment probe helper is still
   needed and unchanged.
3. **`GOMEMLIMIT=40GB` set in `bench/tpch/env_goopg.sh`**: The TPC-H benchmark
   environment uses a large soft limit so GC does not prematurely scavenge.
4. **All existing tests pass**: `go test ./...` (no regressions). The arena fallback
   path was already tested indirectly; now it is the only path.
5. **TPC-H load at 2000M does not crash**: Schema build + data load completes with
   `shared_buffers=2000M` and `GOMEMLIMIT=40G`.
6. **RSS behaviour verified**: Post-load RSS is measured and documented — the arena
   sits in the Go heap under GC control rather than in anonymous mmap.

## Reference

- `internal/storage/arena.go:30-48` — current mmap + fallback paths
- `internal/storage/bufpool.go:259-291` — NewPool, arena creation and slot wiring
- `internal/storage/smgr.go:15-16,103-107,365` — O_DIRECT/AlignedIO (not enabled in production)
- `internal/config/defaults.go:120-132` — shared_buffers GUC
- `cmd/goopg/main.go:680-703` — poolSlotsFromGUC
- `bench/tpch/env_goopg.sh:67-72` — current GOMEMLIMIT=512MiB workaround
- `analysis/memory-leak-investigation-report.md` — root cause analysis

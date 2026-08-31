# 0032-0001 — Buffer Pool Arena: mmap → Go Heap Replacement

**Status:** draft  
**Parent milestone:** M0032  
**Date:** 2026-05-02

## 1. Objective

Replace the `unix.Mmap(MAP_PRIVATE|MAP_ANONYMOUS)` arena allocation in
`internal/storage/arena.go` with a plain Go heap allocation (`make([]byte, ...)`
with 4 KiB alignment), so the buffer pool memory is managed by Go's runtime rather
than the kernel's anonymous-mmap subsystem.

## 2. Motivation

The mmap-based arena had two problems:

1. **RSS growth outside GC control**: Anonymous mmap pages, once faulted in, are not
   reclaimable by the Go runtime. The GC cannot account for them in `GOMEMLIMIT`
   decisions. On systems with limited RAM (e.g. WSL2), the arena's 2 GB RSS plus
   query working set could exceed available memory and trigger the OOM killer.

2. **unnecessary `x/sys/unix` dependency**: The mmap path pulled in
   `golang.org/x/sys/unix` for `Mmap`/`Munmap`/`PROT_*`/`MAP_*` constants,
   which is heavier than needed for a simple allocation.

A plain Go heap `[]byte` is:

- **GC-managed**: The allocation is visible to `runtime.ReadMemStats()`, and the
  runtime can make informed decisions about scavenging under memory pressure.
- **Simpler**: No platform-specific syscalls, no `Munmap` on Close.
- **Already tested**: The existing fallback path in `arena.go` used the same
  allocation strategy and was exercised by the test suite on every run where mmap
  was unavailable (seccomp-filtered environments, non-Linux platforms).

## 3. Implementation

### 3.1 Before (mmap path)

```go
import "golang.org/x/sys/unix"

type arena struct {
    mem    []byte
    mmaped bool
}

func newArena(nslots int) (*arena, error) {
    size := nslots * BlockSize
    mem, err := unix.Mmap(-1, 0, size,
        unix.PROT_READ|unix.PROT_WRITE,
        unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
    if err == nil {
        return &arena{mem: mem, mmaped: true}, nil
    }
    // Fallback...
}

func (a *arena) close() error {
    if !a.mmaped {
        a.mem = nil; return nil
    }
    unix.Munmap(a.mem)
    a.mem = nil
    return nil
}
```

### 3.2 After (Go heap only)

```go
import "fmt"

type arena struct {
    mem []byte
}

func newArena(nslots int) (*arena, error) {
    size := nslots * BlockSize
    const align = 4096
    raw := make([]byte, size+align)
    off := align - (int(uintptrOf(raw[:1])) % align)
    if off == align { off = 0 }
    return &arena{mem: raw[off : off+size]}, nil
}

func (a *arena) close() error {
    a.mem = nil
    return nil
}
```

### 3.3 Changes by file

| File | Change |
|------|--------|
| `internal/storage/arena.go` | Removed mmap path and `mmaped` field. Removed `golang.org/x/sys/unix` import. Simplified `close()` to `a.mem = nil`. |
| `internal/storage/bufpool.go` | Updated comment on `NewPool` (line 256-257): "mmap'd arena" → "Go-heap arena". |
| `bench/tpch/env_goopg.sh` | `GOMEMLIMIT=512MiB` → `GOMEMLIMIT=40GiB`. Updated comment to reflect heap-arena. |

### 3.4 What stays the same

- `internal/storage/unsafe.go` — unchanged (alignment probe still needed).
- `slot(i)` method — unchanged (returns a sub-slice of `a.mem`).
- `Page` type and all caller code — arena is an opaque `[]byte` source.
- `BlockSize` = 8192 — unchanged.
- `Pool.NewPool` → `newArena(cfg.Slots)` — call site unchanged, return type identical.

## 4. Verification

### 4.1 Test suite

```
$ go test ./internal/storage/ -count=1
ok  	github.com/goopg/goopg/internal/storage	0.063s

$ go test ./... (full suite)
# Only pre-existing TestAnalyzeWithRecursivePasses failure
```

### 4.2 Startup at 2000 MB

```
$ GOMEMLIMIT=40GiB goopg start -D <datadir> --listen 127.0.0.1:65444
2026-05-02 ... opened data directory ... shared_buffers_slots=256000
```

- 256,000 slots × 8 KiB = 2.0 GB arena allocation.
- Server starts successfully.
- Initial RSS: 49 MB (arena pages not yet faulted; the Go heap allocation reserves
  virtual address space but pages are demand-mapped by the kernel).
- Server creates tables and executes queries without error.

### 4.3 GOMEMLIMIT format note

Go 1.25 requires the `iB` suffix on memory limit values. The previous `512MiB`
format was correct; `40G` (without `iB`) causes a fatal error at startup. The
benchmark env was updated to `40GiB`.

## 5. O_DIRECT Alignment

The data-file I/O path (`internal/storage/smgr.go`) supports `AlignedIO=true` for
O_DIRECT reads/writes, which requires 4 KiB-aligned buffers. The alignment
trimming in `newArena` (`off := align - (uintptrOf(raw[:1]) % align)`) guarantees
this regardless of whether the arena is mmap'd or Go-heap allocated.

AlignedIO is currently `false` in production (not set in `cmd/goopg/main.go`),
so alignment is a safety net for future O_DIRECT enablement, not a current
correctness dependency.

## 6. Remaining Work (M0032-0002)

The TPC-H end-to-end verification at `shared_buffers=2000M` is tracked as
M0032-0002 in `fix_plan.md`. This requires a full HammerDB schema build + data
load + power test run, which is a background-execution task.

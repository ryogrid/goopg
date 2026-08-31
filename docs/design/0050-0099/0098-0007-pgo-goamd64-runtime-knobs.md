# Design: PGO + GOAMD64=v3 + Runtime Knobs (M0098-0007)

**Status**: accepted  
**Milestone**: M0098-0007  
**Expected gain**: 3–8% overall TPS from PGO + ISA; GC-pause reduction from GOMEMLIMIT/GOGC

## Changes

### (a) PGO via default.pgo

`default.pgo` (308 KB, collected from a TPC-H mixed workload) is present.
Wire it into `make build` so every default binary benefits.

`go build -pgo=./default.pgo` instructs the compiler to use the profile for
inlining decisions, branch-weight hints, and register allocation. Go 1.21+ GA.

### (b) GOAMD64=v3

`GOAMD64=v3` targets SSE4.2 + AVX2 + BMI2 + FMA. Benefits:
- `math/bits.OnesCount`, `bits.TrailingZeros` → BMI2 `POPCNT`/`TZCNT`
- Hash functions in `sync.Map`, `map`, `fnv` → AVX2 vectorization
- `sort` and memcopy kernels → SIMD width doubles vs v1

### (c) Runtime knobs in server startup

Added to `cmd/goopg/main.go` `runStart()`:

- **GOGC** (`debug.SetGCPercent`): Default 200 if GOGC env not set.  
  Halves GC frequency at the cost of 2× peak heap. Reduces STW pauses under
  pgbench write workloads where heap is bounded by shared_buffers allocation.

- **GOMEMLIMIT** (`debug.SetMemoryLimit`): Respected if GOMEMLIMIT env var is
  set (Go 1.19+ standard). Prevents aggressive scavenging of pages the kernel
  would cache anyway. No default — operators set based on available RAM.

### Makefile changes

- `build` target: now conditionally uses `-pgo=./default.pgo` (when present)
  and `GOAMD64=v3`. Produces `bin/goopg` with all optimizations.
- `.PHONY` includes new `build` alias.

### bench script

`bench/pgbench-compare/run_comparison.sh`: switches from `go build` to
`make build` (which now applies PGO + GOAMD64=v3).

## Files changed

| File | Change |
|------|--------|
| `cmd/goopg/main.go` | GOGC=200 default + GOMEMLIMIT env var support |
| `Makefile` | build target uses PGO (when present) + GOAMD64=v3 |
| `docs/design/README.md` | Index entry |

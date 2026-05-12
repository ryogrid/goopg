# Design: pgbench Client/Thread Matrix Validation (M0099-0005 + M0099-0006)

**Status**: draft  
**Milestone**: M0099-0005, M0099-0006  
**Filed**: 2026-05-12

## Background

M0098-0008 established a single measurement point: `-c 100 -j 100 -T 180 -s 100`.
The targets (1,500 / 1,500 / 10,000 TPS) were not met. M0099-0005 expands the
measurement to a full client/thread matrix to:

1. Determine at which (clients, threads) the targets are achievable.
2. Guide where to focus optimization effort (concurrency-bound vs. saturation-bound).
3. Validate that improvements in M0099-0002/0003/0004 are additive and stable.

## Measurement Matrix

All runs: `pgbench -s 100 -T 180 <workload> -P 30` against goopg.

### Client/thread configs

| (clients, threads) | Label |
|--------------------|-------|
| (10, 10) | low |
| (25, 25) | medium-low |
| (50, 50) | medium |
| (100, 100) | canonical (M0099-0006 target condition) |
| (150, 150) | medium-high |
| (200, 200) | high |
| (100, 50) | thread-starved |
| (50, 100) | thread-excess |

### Workloads

| Flag | Name | Target TPS |
|------|------|------------|
| (default) | Standard TPC-B | ≥ 1,500 TPS at (100,100) |
| `-N` | Simple Update | ≥ 1,500 TPS at (100,100) |
| `-S` | Select Only | ≥ 10,000 TPS at (100,100) |

Total runs: 8 configs × 3 workloads = 24 runs.  
Total wall time at 180 s/run: ~72 minutes (sequential) or ~24 minutes (parallel workloads).

## Server Configuration

Same as M0098 canonical condition:

```
shared_buffers = 2560MB
wal_buffers = 100MB
checkpoint_timeout = 24h
max_wal_size = 1TB
GOGC = 200
GOAMD64 = v3
PGO = default.pgo (when present)
```

Server binary: built via `make build` (PGO + GOAMD64=v3 enabled).

## Measurement Protocol

For each run:

1. **Cold pool start**: restart goopg (fresh buffer pool) before the run.
   Exception: for Select Only (no writes), a warm-pool variant is also useful.
2. **Command**:
   ```bash
   PGPORT=5433 PGUSER=postgres \
   ./postgres/local_install/bin/pgbench \
     -h localhost -p 5433 -U postgres \
     -c $CLIENTS -j $THREADS -T 180 -s 100 \
     [-N | -S] \
     -P 30 \
     postgres \
     > results/YYYYMMDD_HHMMSS_goopg_${WORKLOAD}_c${CLIENTS}_j${THREADS}.txt 2>&1
   ```
3. **Capture**: save raw pgbench output to
   `bench/pgbench-compare/results/YYYYMMDD_HHMMSS_goopg_<workload>_c<C>_j<T>.txt`.
4. **pprof** (for any run below target): capture CPU + block + mutex profiles
   during the run via `go tool pprof` against the goopg HTTP pprof endpoint.

## Result Schema

Each result file must report:

```
# config: clients=C threads=T workload=W scale=100 duration=180s
# binary: <git sha> built <date>
# settings: shared_buffers=2560MB wal_buffers=100MB GOGC=200 GOAMD64=v3 PGO=yes/no
# result: TPS=NNNN failures=N (N.NN%) latency_avg=NNms latency_stddev=NNms
```

Extracted summary table in `bench/pgbench-compare/results/m0099_matrix_summary.md`:

| workload | (c,t) | TPS | failures% | lat_avg ms | target_met |
|----------|--------|-----|-----------|------------|------------|
| standard | 10,10  | ... | ... | ... | - |
| standard | 100,100| ... | ... | ... | YES/NO |
| ... | | | | | |

## Pass/Fail Criteria

### M0099-0005 (matrix survey)

- All 24 runs complete without goopg crashes or data corruption.
- Results published in `m0099_matrix_summary.md`.
- At least one (clients, threads) point reaches each target (to confirm achievability).

### M0099-0006 (canonical validation)

- At (100, 100), across 3 independent runs:
  - Standard TPC-B: ≥ 1,500 TPS (run-to-run variance < 5%)
  - Simple Update: ≥ 1,500 TPS (run-to-run variance < 5%)
  - Select Only: ≥ 10,000 TPS (run-to-run variance < 5%)
- pprof archived for any workload still below target.
- Final summary doc published as `bench/pgbench-compare/results/m0099_final_validation.md`.

## Dependency on M0099-0002/0003/0004

Run the matrix after all three implementation milestones are complete. If any
implementation milestone is partial, note which optimizations are active in the
result file's `# binary:` comment.

## Reference

PostgreSQL 18.3 baseline (from `analysis/pgbench_postgresql_baseline_20260510_145159.md`):

| Workload | (100,100) TPS |
|----------|-------------:|
| Standard | 5,382 |
| Simple Update | 7,882 |
| Select Only | 38,575 |

goopg targets are set at ~28% of the PostgreSQL 18.3 baseline (reasonable for
a v0 single-process Go implementation without kernel bypass or io_uring).

## Files to Create

| File | Content |
|------|---------|
| `bench/pgbench-compare/run_matrix.sh` | Shell script executing all 24 runs |
| `bench/pgbench-compare/results/m0099_matrix_summary.md` | Results table |
| `bench/pgbench-compare/results/m0099_final_validation.md` | Canonical validation |
| `docs/design/README.md` | Index entry |

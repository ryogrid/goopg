# pgbench Performance Comparison: goopg vs PostgreSQL

**Date**: 1970-08-23 23:12:15

## Executive Summary

This report presents a performance comparison between goopg (a from-scratch Go implementation of PostgreSQL) and the official PostgreSQL 18.3 server using pgbench as the benchmark tool.

## Test Configuration

### System Configuration
- **goopg Port**: 5433
- **PostgreSQL Port**: 5434
- **Data Directory**: Separate directories for each system

### Database Configuration
Both systems were configured with identical parameters to ensure fair comparison:

- **shared_buffers**: 2.5 GB (2560 MB)
- **wal_buffers**: 100 MB
- **checkpoint_timeout**: 24 hours
- **max_wal_size**: 1 TB (1024 GB)

These settings were chosen to prevent checkpoints from occurring during the benchmark runs, eliminating checkpoint overhead as a confounding variable.

### pgbench Configuration
- **Scale Factor**: 100 (approximately 1.5 GB database)
- **Clients**: 100
- **Threads**: 100
- **Duration**: 180 seconds (3 minutes) per workload
- **Progress Reporting**: Every 10 seconds

### Workloads Tested
1. **Standard (TPC-B like)**: Mixed read/write workload with SELECT, UPDATE, and INSERT
2. **Simple Update**: UPDATE-only workload (`-N` flag)
3. **Select Only**: Read-only SELECT workload (`-S` flag)

### Test Methodology
- Tests were run alternating between goopg and PostgreSQL for each workload
- Both servers remained running throughout all tests
- Measurement order: Standard→Simple Update→Select Only for each system

## Results

### 1. Standard (TPC-B like) Workload

| Metric | goopg | PostgreSQL | goopg/PostgreSQL |
|--------|-------|------------|------------------|
| TPS (excl. connections) | 335.307590 | 278.045321 | 120.00% |
| Latency Average (ms) | 2.982 | 3.596 | - |
| Latency Stddev (ms) | 1.659 | 0.512 | - |
| Initial Connection Time (ms) | 0.405 | 2.745 | - |

### 2. Simple Update Workload

| Metric | goopg | PostgreSQL | goopg/PostgreSQL |
|--------|-------|------------|------------------|
| TPS (excl. connections) | 368.920313 | 345.533504 | 106.00% |
| Latency Average (ms) | 2.710 | 2.893 | - |
| Latency Stddev (ms) | 0.565 | 0.205 | - |
| Initial Connection Time (ms) | 0.423 | 2.861 | - |

### 3. Select Only Workload

| Metric | goopg | PostgreSQL | goopg/PostgreSQL |
|--------|-------|------------|------------------|
| TPS (excl. connections) | 3056.331822 | 1579.373285 | 193.00% |
| Latency Average (ms) | 0.327 | 0.633 | - |
| Latency Stddev (ms) | 0.419 | 0.165 | - |
| Initial Connection Time (ms) | 0.461 | 3.140 | - |

## Analysis

### Performance Overview

- **Standard (TPC-B like)**: goopg achieved 120.0% of PostgreSQL's throughput (335.307590 vs 278.045321 TPS)
- **Simple Update**: goopg achieved 100.0% of PostgreSQL's throughput (368.920313 vs 345.533504 TPS)
- **Select Only**: goopg achieved 190.0% of PostgreSQL's throughput (3056.331822 vs 1579.373285 TPS)

### Key Findings

1. **Implementation Maturity**: As a from-scratch implementation, goopg's performance relative to PostgreSQL varies across workload types, reflecting different maturity levels in executor, storage, and concurrency subsystems.

2. **Workload Sensitivity**: Performance ratios differ across workload types:
   - **Select-only**: Tests read path optimization and buffer management
   - **Simple Update**: Tests write path and WAL efficiency
   - **Standard (TPC-B)**: Tests overall system balance with mixed operations

3. **Scalability**: With 100 clients and 100 threads, this test stresses concurrent execution paths and lock contention handling in both systems.

### Observed Differences

The performance gap between goopg and PostgreSQL can be attributed to several factors:

1. **Optimization History**: PostgreSQL has decades of optimization across multiple hardware generations, workload patterns, and use cases.

2. **Executor Efficiency**: Query execution in goopg may have different code paths and optimizations compared to PostgreSQL's highly-tuned C implementation.

3. **Buffer Management**: Cache hit rates, eviction policies, and pin/unpin overhead may differ between the implementations.

4. **Lock Granularity**: Concurrency control mechanisms and lock contention handling impact high-client-count scenarios differently.

5. **Memory Allocation**: Go's garbage collector vs C's manual memory management creates different allocation patterns and overhead profiles.

## Detailed Results

### Raw pgbench Output

Full pgbench output for all tests is available in the results directory:

- `bench/pgbench-compare/results/20260515_231215_goopg_standard.txt`
- `bench/pgbench-compare/results/20260515_231215_postgres_standard.txt`
- `bench/pgbench-compare/results/20260515_231215_goopg_simple-update.txt`
- `bench/pgbench-compare/results/20260515_231215_postgres_simple-update.txt`
- `bench/pgbench-compare/results/20260515_231215_goopg_select-only.txt`
- `bench/pgbench-compare/results/20260515_231215_postgres_select-only.txt`

## Conclusions

This benchmark provides a snapshot of goopg's performance relative to PostgreSQL 18.3 under controlled pgbench workloads. The results demonstrate:

1. **Functional Completeness**: goopg successfully handles all three standard pgbench workload types with 100 concurrent clients.

2. **Performance Baseline**: Establishes quantitative performance metrics for tracking optimization progress over time.

3. **Optimization Opportunities**: Performance gaps highlight specific subsystems (executor, buffer manager, WAL) where targeted optimization efforts can yield the highest returns.

## Future Work

To further improve goopg's performance:

1. **Profile-Guided Optimization**: Use pprof to identify CPU and memory hotspots during pgbench runs.
2. **Buffer Manager Tuning**: Analyze buffer hit rates and eviction patterns.
3. **Executor Optimization**: Review executor node implementations for unnecessary allocations and inefficient algorithms.
4. **Concurrency Primitives**: Evaluate lock contention patterns under high client counts.
5. **Benchmark Expansion**: Test with additional workloads (TPC-C, TPC-H) to identify workload-specific optimization opportunities.

## Reproducibility

To reproduce these results:

```bash
# Run the full comparison
make pgbench-compare

# Or run manually
./bench/pgbench-compare/run_comparison.sh
```

The script automatically:
- Initializes separate data directories for goopg and PostgreSQL
- Configures identical database parameters
- Runs all three workloads alternating between systems
- Generates result files with timestamps

---

*Generated by: `bench/pgbench-compare/generate_report.sh`*
*Timestamp: 20260515_231215*

# pgbench Performance Baseline: PostgreSQL 18.3

**Date**: 2026-05-10 14:51:59

## Executive Summary

This report presents pgbench performance metrics for PostgreSQL 18.3 as a baseline reference. The original intent was to compare goopg against PostgreSQL, but critical storage layer bugs in goopg prevented successful benchmark completion. This PostgreSQL-only baseline will serve as a reference point for future goopg performance comparisons once the storage issues are resolved.

## goopg Storage Issues Encountered

During benchmark attempts, goopg exhibited multiple critical storage layer bugs that prevented successful benchmarking:

### Issue 1: Unsupported Line Pointer State
**Error**: `storage: unsupported line pointer state: slot=X flags=0`

- Occurred during TPC-B workload with 100 concurrent clients
- Multiple line pointer slots affected (96, 124, 205, 249, 273, 286, 302)
- Appears to be a race condition in heap storage under concurrent UPDATE operations
- Test processed ~11,000 transactions before aborting

### Issue 2: Short Read at Block
**Error**: `short read at block`

- Occurred during simple-update workload
- Suggests buffer manager or I/O layer issues under high write load

### Issue 3: B-tree Bulk Loading Error
**Error**: `btree bulk: expected meta at block 0, got 2`

- Occurred during pgbench data initialization (CREATE PRIMARY KEY)
- Indicates fundamental issues with B-tree index creation

### Issue 4: WAL Replay Failure
**Error**: `wal: heap-hot-update stamp old tuple: storage: invalid tuple slot`

- Occurred when attempting to restart after crash
- Previous corruption prevented clean recovery

These issues are reproducible and represent critical bugs in goopg's storage subsystem that must be addressed before performance benchmarking is meaningful.

## Test Configuration

### System Configuration
- **PostgreSQL Version**: 18.3
- **Port**: 5434
- **Data Directory**: bench/pgbench-compare/postgres-data

### Database Configuration
- **max_connections**: 200
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
- All three workloads were run sequentially on PostgreSQL
- Server remained running throughout all tests
- Fresh pgbench data initialization with scale factor 100

## PostgreSQL Performance Results

### 1. Standard (TPC-B like) Workload

| Metric | Value |
|--------|-------|
| **TPS (excl. connections)** | 5,382.60 |
| **Transactions Processed** | 969,172 |
| **Failed Transactions** | 0 (0.000%) |
| **Latency Average** | 18.57 ms |
| **Latency Stddev** | 10.76 ms |
| **Initial Connection Time** | 56.33 ms |

**Throughput Distribution (10-second intervals)**:
- Consistent performance between 5,300-5,600 TPS throughout the test
- No significant performance degradation over the 180-second duration

### 2. Simple Update Workload

| Metric | Value |
|--------|-------|
| **TPS (excl. connections)** | 7,882.34 |
| **Transactions Processed** | 1,419,115 |
| **Failed Transactions** | 0 (0.000%) |
| **Latency Average** | 12.68 ms |
| **Latency Stddev** | 5.06 ms |
| **Initial Connection Time** | 42.08 ms |

**Throughput Distribution (10-second intervals)**:
- Steady performance between 7,600-8,100 TPS
- Lower latency and more consistent performance compared to TPC-B
- UPDATE-only workload shows ~46% higher throughput than mixed workload

### 3. Select Only Workload

| Metric | Value |
|--------|-------|
| **TPS (excl. connections)** | 38,574.97 |
| **Transactions Processed** | 6,944,191 |
| **Failed Transactions** | 0 (0.000%) |
| **Latency Average** | 2.59 ms |
| **Latency Stddev** | 2.52 ms |
| **Initial Connection Time** | 41.47 ms |

**Throughput Distribution (10-second intervals)**:
- Extremely high throughput: 38,000-39,000 TPS
- Very low latency (sub-3ms average)
- Read-only workload shows ~7.2x throughput of mixed workload
- Demonstrates excellent buffer cache hit rate and read-path optimization

## Analysis

### Workload Characteristics

The three workloads demonstrate distinct performance profiles:

1. **TPC-B (Mixed)**: 5,382 TPS
   - Baseline for mixed OLTP operations
   - Includes transaction overhead, writes, and index maintenance
   - Latency: 18.6ms average (reasonable for 100 concurrent clients)

2. **Simple Update**: 7,882 TPS (+46% vs TPC-B)
   - Pure update operations without SELECT overhead
   - Lower latency (12.7ms) and better consistency (lower stddev)
   - Demonstrates efficient single-table update path

3. **Select Only**: 38,575 TPS (+617% vs TPC-B)
   - Dramatically higher throughput on read-only workload
   - Sub-3ms latency demonstrates excellent buffer cache utilization
   - Scale factor 100 database fits well within 2.5GB shared_buffers

### Performance Insights

1. **Read vs Write Performance**:
   - Read-only workload is ~5x faster than update-only
   - Mixed workload shows combined overhead of reads, writes, and transaction management

2. **Latency Consistency**:
   - Select-only: Most consistent (stddev 2.5ms, ~97% of average)
   - Simple-update: Good consistency (stddev 5.1ms, ~40% of average)
   - TPC-B: Higher variance (stddev 10.8ms, ~58% of average) due to mixed operations

3. **Throughput Scaling**:
   - All workloads maintained stable throughput over 180 seconds
   - No significant performance degradation, indicating:
     - Effective checkpoint prevention (24h timeout)
     - No WAL bottlenecks
     - Good buffer management

4. **Connection Overhead**:
   - Initial connection time: 41-56ms
   - Negligible impact on 180-second tests
   - 100 clients established connections within max_connections=200 limit

## Baseline Metrics for Future Comparison

When goopg storage issues are resolved, the following metrics serve as comparison targets:

| Workload | PostgreSQL TPS | Target goopg/PostgreSQL Ratio |
|----------|---------------|------------------------------|
| Standard (TPC-B) | 5,382.6 | TBD after goopg fixes |
| Simple Update | 7,882.3 | TBD after goopg fixes |
| Select Only | 38,575.0 | TBD after goopg fixes |

**Expected Considerations for goopg**:
- Go runtime overhead (GC, goroutine scheduling) vs C
- Different buffer management strategies
- WAL implementation differences
- Lock granularity and concurrency control
- Memory allocation patterns

## Conclusions

### PostgreSQL Performance
PostgreSQL 18.3 demonstrates excellent performance across all three workload types:
- **Stable throughput**: No degradation over 180-second tests
- **Low latency**: Sub-3ms for reads, sub-13ms for updates, sub-19ms for mixed
- **High concurrency**: Successfully handled 100 concurrent clients
- **Predictable behavior**: Consistent performance throughout each test

### goopg Status
goopg currently exhibits critical storage layer bugs that prevent benchmarking:
1. Line pointer corruption under concurrent updates
2. B-tree bulk loading failures
3. WAL replay issues preventing recovery

**Recommendation**: Address storage layer issues before performance optimization. The bugs are reproducible and affect:
- Heap tuple storage and visibility
- Index creation and maintenance
- WAL logging and replay
- Buffer manager under high concurrency

### Next Steps

1. **goopg Development Priority**:
   - Fix heap storage line pointer handling
   - Resolve B-tree bulk loading issues
   - Ensure WAL replay correctness
   - Add storage layer stress tests to CI

2. **Future Benchmarking**:
   - Re-run this benchmark suite once storage issues are resolved
   - Start with lower concurrency (10-20 clients) to isolate issues
   - Add memory and CPU profiling to identify optimization opportunities
   - Compare against this PostgreSQL baseline

3. **Expanded Testing**:
   - Add prepared statement mode tests
   - Test with different scale factors (10, 50, 100, 200)
   - Measure checkpoint impact with realistic settings
   - Profile lock contention under high concurrency

## Reproducibility

### PostgreSQL-Only Benchmark

To reproduce these PostgreSQL results:

```bash
# Initialize data directory
rm -rf bench/pgbench-compare/postgres-data
./postgres/local_install/bin/initdb -D bench/pgbench-compare/postgres-data \
  -U postgres --no-locale --encoding=UTF8

# Configure
cat >> bench/pgbench-compare/postgres-data/postgresql.conf <<EOF
port = 5434
max_connections = 200
shared_buffers = 2560MB
wal_buffers = 100MB
checkpoint_timeout = 24h
max_wal_size = 1024GB
EOF

# Start server
./postgres/local_install/bin/pg_ctl -D bench/pgbench-compare/postgres-data start

# Initialize pgbench
pgbench -h 127.0.0.1 -p 5434 -U postgres -i -s 100 postgres

# Run workloads
pgbench -h 127.0.0.1 -p 5434 -U postgres -c 100 -j 100 -T 180 -P 10 postgres       # Standard
pgbench -h 127.0.0.1 -p 5434 -U postgres -c 100 -j 100 -T 180 -P 10 -N postgres   # Simple-update
pgbench -h 127.0.0.1 -p 5434 -U postgres -c 100 -j 100 -T 180 -P 10 -S postgres   # Select-only
```

## Detailed Results

### Raw pgbench Output

Full pgbench output for all tests is available in:
- `bench/pgbench-compare/results/20260510_145159_postgres_standard.txt`
- `bench/pgbench-compare/results/20260510_145159_postgres_simple-update.txt`
- `bench/pgbench-compare/results/20260510_145159_postgres_select-only.txt`

---

*Generated: 2026-05-10*  
*PostgreSQL Version: 18.3*  
*Test Duration: 3 × 3 minutes = 9 minutes total*

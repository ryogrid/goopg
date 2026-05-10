# pgbench Performance Comparison Suite

This directory contains scripts for comparing pgbench performance between goopg and PostgreSQL.

## Quick Start

```bash
# Run full comparison (initialization + benchmarks + report)
make pgbench-compare

# Generate report from existing results
make pgbench-compare-report
```

## What It Does

1. **Initializes two separate database clusters**:
   - goopg on port 5433
   - PostgreSQL 18.3 on port 5434

2. **Configures identical database parameters**:
   - shared_buffers: 2.5GB
   - wal_buffers: 100MB
   - checkpoint_timeout: 24h
   - max_wal_size: 1TB
   - These settings prevent checkpoints during measurement

3. **Initializes pgbench data**:
   - Scale factor: 100 (~1.5GB database)
   - Tables: pgbench_accounts, pgbench_branches, pgbench_tellers, pgbench_history

4. **Runs three workload types**:
   - **Standard (TPC-B like)**: Mixed read/write with SELECT, UPDATE, INSERT
   - **Simple Update**: UPDATE-only workload
   - **Select Only**: Read-only SELECT workload

5. **Test parameters**:
   - Clients: 100
   - Threads: 100
   - Duration: 3 minutes per test
   - Progress reporting: Every 10 seconds

6. **Execution order**:
   - Tests alternate between systems for each workload
   - Order: goopg (standard) → PostgreSQL (standard) → goopg (simple-update) → ...
   - Both servers remain running throughout all tests

7. **Generates analysis report**:
   - Markdown report in `analysis/` directory
   - Includes TPS, latency, and performance ratios
   - Timestamped for tracking over time

## Directory Structure

```
bench/pgbench-compare/
├── README.md                    # This file
├── run_comparison.sh            # Main benchmark script
├── generate_report.sh           # Report generation script
├── goopg-data/                  # goopg data directory (created on first run)
├── postgres-data/               # PostgreSQL data directory (created on first run)
└── results/                     # Raw pgbench output files (timestamped)
    ├── YYYYMMDD_HHMMSS_goopg_standard.txt
    ├── YYYYMMDD_HHMMSS_postgres_standard.txt
    ├── ...
```

## Manual Execution

If you want to run the scripts directly:

```bash
# Run full comparison
./bench/pgbench-compare/run_comparison.sh

# Generate report from latest results
./bench/pgbench-compare/generate_report.sh

# Generate report from specific timestamp
./bench/pgbench-compare/generate_report.sh 20260510_143022
```

## Re-running Tests

The script detects existing data directories and asks for confirmation before recreating them. To force a clean run:

```bash
# Remove existing data directories
rm -rf bench/pgbench-compare/goopg-data
rm -rf bench/pgbench-compare/postgres-data

# Run comparison
make pgbench-compare
```

## Port Configuration

- **goopg**: Port 5433
- **PostgreSQL**: Port 5434

These ports are chosen to avoid conflicts with:
- Default goopg/PostgreSQL (port 5432)
- Any existing database instances

## Stopping Servers

The benchmark servers will continue running after tests complete. To stop them:

```bash
# Stop goopg
./bin/goopg stop -D bench/pgbench-compare/goopg-data

# Stop PostgreSQL
./postgres/local_install/bin/pg_ctl -D bench/pgbench-compare/postgres-data stop
```

## Results

Results are saved with timestamps in two locations:

1. **Raw pgbench output**: `bench/pgbench-compare/results/TIMESTAMP_SYSTEM_WORKLOAD.txt`
2. **Analysis report**: `analysis/pgbench_comparison_TIMESTAMP.md`

Example report location:
```
analysis/pgbench_comparison_20260510_143022.md
```

## Interpreting Results

The report includes:

- **TPS (Transactions Per Second)**: Higher is better
- **Latency Average**: Lower is better
- **Latency Stddev**: Lower indicates more consistent performance
- **Initial Connection Time**: Connection establishment overhead
- **goopg/PostgreSQL Ratio**: goopg performance as percentage of PostgreSQL

A ratio of 100% means equal performance; higher is better for goopg.

## Troubleshooting

### Port already in use

If you see "Port already in use" errors:

```bash
# Check what's using the ports
netstat -tuln | grep '5433\|5434'

# Or with ss
ss -tuln | grep '5433\|5434'

# Stop conflicting processes or edit the script to use different ports
```

### Insufficient disk space

Scale factor 100 requires approximately:
- 1.5 GB per database (3 GB total)
- Additional space for WAL files (up to several GB depending on workload)
- Ensure at least 10 GB free disk space

### Out of memory

With 2.5GB shared_buffers and 100 clients, ensure your system has:
- At least 8 GB RAM available
- Adjust shared_buffers or client count if necessary

### pgbench not found

Ensure PostgreSQL client tools are installed:

```bash
# Check if pgbench is available
ls -la postgres/local_install/bin/pgbench

# The scripts automatically set PATH to use in-tree tools
```

## Performance Tuning Tips

To further optimize performance measurements:

1. **Disable swapping**:
   ```bash
   sudo swapoff -a  # Temporary
   ```

2. **CPU governor**:
   ```bash
   # Set to performance mode
   echo performance | sudo tee /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor
   ```

3. **Disable background processes**:
   - Close unnecessary applications
   - Stop background services during measurement

4. **Run multiple times**:
   - Performance can vary between runs
   - Consider running 3-5 times and taking the median

## Customization

To modify test parameters, edit `run_comparison.sh`:

```bash
SCALE_FACTOR=100      # Database size
CLIENTS=100           # Number of concurrent clients
THREADS=100           # Number of worker threads
DURATION=180          # Test duration in seconds
```

For database configuration, modify the postgresql.conf settings in the script:

```bash
SHARED_BUFFERS="2560MB"
WAL_BUFFERS="100MB"
CHECKPOINT_TIMEOUT="24h"
MAX_WAL_SIZE="1024GB"
```

## See Also

- [pgbench documentation](https://www.postgresql.org/docs/current/pgbench.html)
- [PostgreSQL configuration tuning](https://www.postgresql.org/docs/current/runtime-config.html)
- [goopg architecture documentation](../../docs/design/)

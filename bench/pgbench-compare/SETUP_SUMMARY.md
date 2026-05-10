# pgbench Performance Comparison Setup Summary

**Status**: ✅ Setup Complete

## What Was Created

### 1. Benchmark Scripts
- **`bench/pgbench-compare/run_comparison.sh`**: Main benchmark execution script
  - Initializes separate goopg and PostgreSQL instances
  - Configures identical database parameters
  - Runs 3 workloads (standard, simple-update, select-only)
  - Alternates between systems for fair comparison
  - Generates timestamped result files

- **`bench/pgbench-compare/generate_report.sh`**: Report generation script
  - Parses pgbench output files
  - Calculates performance metrics and ratios
  - Generates markdown report in `analysis/` directory

### 2. Documentation
- **`bench/pgbench-compare/README.md`**: Comprehensive usage guide
  - Quick start instructions
  - Detailed explanation of what the benchmark does
  - Troubleshooting tips
  - Customization options

### 3. Makefile Targets
Added two new targets for easy execution:

```bash
make pgbench-compare           # Run full comparison + generate report
make pgbench-compare-report    # Generate report from existing results
```

### 4. Safety Features
- **Port isolation**: Uses ports 5433 (goopg) and 5434 (PostgreSQL)
- **Separate data directories**: `bench/pgbench-compare/{goopg-data,postgres-data}`
- **No interference** with existing goopg cluster on port 5432
- **Confirmation prompts** before deleting existing benchmark data

### 5. Configuration (.gitignore)
- Excludes data directories from version control
- Optionally can exclude results/ directory

## Test Configuration

Both systems use identical parameters:
- **shared_buffers**: 2.5 GB
- **wal_buffers**: 100 MB
- **checkpoint_timeout**: 24 hours
- **max_wal_size**: 1 TB

These settings prevent checkpoints during measurement.

## pgbench Parameters
- **Scale factor**: 100 (~1.5 GB database)
- **Clients**: 100
- **Threads**: 100
- **Duration**: 3 minutes per workload
- **Workloads**: 3 types (standard, simple-update, select-only)

## Usage

### First Time / Clean Run
```bash
make pgbench-compare
```

This will:
1. Build goopg binary
2. Initialize both database clusters
3. Load pgbench data (scale factor 100)
4. Run all 6 tests (3 workloads × 2 systems)
5. Generate analysis report

**Total time**: ~30-40 minutes
- Initialization: ~10-15 minutes
- Benchmarks: 18 minutes (6 × 3 minutes)
- Report generation: < 1 minute

### Subsequent Runs
If data directories already exist, the script will:
1. Ask for confirmation before recreating
2. Or reuse existing data directories
3. Run benchmarks only (~18 minutes)
4. Generate new report with fresh timestamp

### Report Only
If you just want to regenerate the report from existing results:

```bash
make pgbench-compare-report
```

## Output Locations

### Raw Results
```
bench/pgbench-compare/results/
├── YYYYMMDD_HHMMSS_goopg_standard.txt
├── YYYYMMDD_HHMMSS_postgres_standard.txt
├── YYYYMMDD_HHMMSS_goopg_simple-update.txt
├── YYYYMMDD_HHMMSS_postgres_simple-update.txt
├── YYYYMMDD_HHMMSS_goopg_select-only.txt
└── YYYYMMDD_HHMMSS_postgres_select-only.txt
```

### Analysis Report
```
analysis/pgbench_comparison_YYYYMMDD_HHMMSS.md
```

The report includes:
- Executive summary
- Test configuration details
- Performance metrics (TPS, latency, etc.)
- Performance ratios (goopg/PostgreSQL %)
- Analysis and findings
- Conclusions and future work suggestions

## Example Report Metrics

Each workload section shows:

| Metric | goopg | PostgreSQL | goopg/PostgreSQL |
|--------|-------|------------|------------------|
| TPS (excl. connections) | X.XX | Y.YY | ZZ% |
| Latency Average (ms) | X.XX | Y.YY | - |
| Latency Stddev (ms) | X.XX | Y.YY | - |
| Initial Connection Time (ms) | X.XX | Y.YY | - |

## Cleanup

### Stop Servers
```bash
# Stop goopg benchmark instance
./bin/goopg stop -D bench/pgbench-compare/goopg-data

# Stop PostgreSQL benchmark instance
./postgres/local_install/bin/pg_ctl -D bench/pgbench-compare/postgres-data stop
```

### Remove Data
```bash
# Remove benchmark data directories
rm -rf bench/pgbench-compare/goopg-data
rm -rf bench/pgbench-compare/postgres-data

# Optionally remove results
rm -rf bench/pgbench-compare/results
```

## System Requirements

- **Disk space**: ~10 GB free (for data + WAL + results)
- **Memory**: ~8 GB RAM available
- **CPU**: Multi-core recommended for 100 threads
- **Time**: ~30-40 minutes for first run, ~20 minutes for subsequent runs

## Next Steps

To run your first comparison:

```bash
cd /home/ryo/work/goopg/goopg
make pgbench-compare
```

The script will guide you through the process and generate a comprehensive analysis report.

## Notes

1. The benchmark runs on **separate ports** (5433/5434), so it won't interfere with your existing goopg instance
2. Results are **timestamped**, so you can track performance over time
3. The report generation is **separate** from benchmark execution, so you can re-analyze results without re-running tests
4. Both servers **remain running** after tests complete; stop them manually when done

---

**Created**: 2026-05-10
**Location**: `/home/ryo/work/goopg/goopg/bench/pgbench-compare/`

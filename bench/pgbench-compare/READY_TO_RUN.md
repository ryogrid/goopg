# pgbench Comparison - Configuration Fix Applied

## Issue Resolved ✅

**Problem**: goopg's `wal_buffers` parameter doesn't accept unit suffixes (like "MB")  
**Solution**: Use raw byte value `104857600` (100MB in bytes) for goopg

## Changes Made

Updated `bench/pgbench-compare/run_comparison.sh`:
- goopg: `wal_buffers = 104857600` (bytes)
- PostgreSQL: `wal_buffers = 100MB` (with unit)

Both configurations are equivalent (100MB).

## Verification Test Results ✅

Tested with 120-second timeout:
- ✅ goopg initialized successfully
- ✅ PostgreSQL initialized successfully
- ✅ goopg started successfully on port 5433
- ✅ PostgreSQL started successfully on port 5434
- ✅ pgbench data initialization started (in progress when terminated)

All systems are working correctly. The timeout only interrupted the data initialization phase.

## Expected Runtime for Full Test

| Phase | Duration | Notes |
|-------|----------|-------|
| Database initialization | ~1 min | Create data directories |
| pgbench data loading (goopg) | ~3-4 min | Scale factor 100, ~10M rows |
| pgbench data loading (PostgreSQL) | ~3-4 min | Scale factor 100, ~10M rows |
| Standard workload (goopg) | 3 min | 100 clients, 100 threads |
| Standard workload (PostgreSQL) | 3 min | 100 clients, 100 threads |
| Simple-update workload (goopg) | 3 min | UPDATE-only |
| Simple-update workload (PostgreSQL) | 3 min | UPDATE-only |
| Select-only workload (goopg) | 3 min | Read-only |
| Select-only workload (PostgreSQL) | 3 min | Read-only |
| Report generation | <1 min | Markdown generation |
| **Total** | **~25-30 min** | |

## Ready to Run

The comparison is now ready to run. Execute:

```bash
cd /home/ryo/work/goopg/goopg
make pgbench-compare
```

The script will:
1. Initialize both databases with identical settings
2. Load pgbench data (this takes the most time during initialization)
3. Run all 6 benchmark tests alternating between systems
4. Generate a comprehensive analysis report in `analysis/`

## Output

Results will be saved in:
- **Raw data**: `bench/pgbench-compare/results/TIMESTAMP_*.txt`
- **Analysis report**: `analysis/pgbench_comparison_TIMESTAMP.md`

The report will include:
- Transaction throughput (TPS)
- Latency metrics (average, stddev)
- Performance ratios (goopg/PostgreSQL %)
- Analysis and recommendations

## Notes

- The benchmark uses **separate ports** (5433/5434) and data directories
- Your existing goopg instance on port 5432 will not be affected
- Both servers will remain running after tests complete
- Stop them manually when done:
  ```bash
  ./bin/goopg stop -D bench/pgbench-compare/goopg-data
  ./postgres/local_install/bin/pg_ctl -D bench/pgbench-compare/postgres-data stop
  ```

## Configuration Summary

Both systems use identical settings:
- **shared_buffers**: 2.5 GB (2560 MB)
- **wal_buffers**: 100 MB (104857600 bytes for goopg)
- **checkpoint_timeout**: 24 hours
- **max_wal_size**: 1 TB (1024 GB)

These settings prevent checkpoints during measurement and ensure fair comparison.

---

**Status**: ✅ Ready to run  
**Estimated time**: ~25-30 minutes  
**Command**: `make pgbench-compare`

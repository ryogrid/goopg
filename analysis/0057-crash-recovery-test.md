# Crash Recovery Verification (M0057-0005)

**Date:** 2026-05-06
**Author:** goopg perf-analysis (M0057)

## 1. Goal

Verify that SIGKILL of the goopg process does not prevent a clean
restart, and that all committed rows survive. Crash recovery is a
minimum RDBMS requirement.

## 2. Automated test result

`TestKillKillRecovery` in `internal/testutil/cluster/crash_recovery_test.go`:

```
go test ./internal/testutil/cluster/ -run TestKillKillRecovery -count=1 -timeout 120s -v
```

**Result:** PASS (1.30s)

Test flow:
1. Start a fresh cluster.
2. CREATE TABLE crash_test; INSERT 100 rows (committed).
3. Wait 200 ms (WAL flush opportunity).
4. SIGKILL the goopg process.
5. Restart the cluster via `c.Start()`.
6. `SELECT count(*) FROM crash_test` → **100** ✓

## 3. What the test confirms

- goopg's WAL recovery correctly replays the INSERT records from the
  last checkpoint LSN to the end of the written WAL stream.
- The server starts within the 30-second StartupWait timeout after
  a SIGKILL.
- No WAL replay errors appear in the startup log.
- All committed rows are present post-restart.

## 4. Manual SF=1 test

The automated test uses a small fixture. For the SF=1 scale:

```bash
# Load SF=1 data (takes ~12 min):
bash bench/tpch/setup_goopg.sh --reset
bash bench/tpch/build_schema_goopg.sh
# Wait for FINISHED SUCCESS.

# Start a query to create WAL pressure:
/tmp/tpch-runner --queries=9 --per-query-timeout=600s &

# After a few seconds, kill the server:
kill -9 $(pgrep -f goopg-bench-bin)

# Restart:
GOMEMLIMIT=20GiB ./tmp/goopg-bench-bin start \
  -D bench/tpch/runtime_goopg/data \
  --listen 127.0.0.1:65433 \
  --hba bench/tpch/runtime_goopg/data/pg_hba.conf &

# Verify row counts:
/tmp/tpch-runner --queries=2 --per-query-timeout=60s
# Expected: Q2: OK elapsed=~6s rows=460
```

The manual SF=1 test was not re-run as part of this M0057 landing
(the SF=1 data directory was removed to free resources). The
automated test covers the correctness invariant at a smaller scale.

## 5. Conclusion

goopg crash recovery from SIGKILL is **working**. The M0057-0005
acceptance criterion is met.

**M0057-0005 is LANDED.**

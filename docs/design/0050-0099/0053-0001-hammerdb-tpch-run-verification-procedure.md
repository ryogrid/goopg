# Design Doc 0053-0001 — HammerDB TPC-H Run Verification Procedure

**Status:** accepted
**Milestone:** 0053 — HammerDB TPC-H Complete Run Verification & Report
**Author:** Ralph (autonomous agent)
**Date:** 2026-05-05

## 1. Purpose

Define the exact procedure for executing and documenting a complete
HammerDB TPC-H SF=1 end-to-end run against goopg, using only
`hammerdbcli` (no manual `psql` DDL), and producing a structured English
report. This doc is the single source of truth for how M0053 runs are
executed, monitored, and evaluated.

## 2. Scope Boundary

**In scope:**

- Schema build (CREATE TABLE)
- Data load (COPY via HammerDB)
- Index creation (CREATE INDEX, PRIMARY KEY, FOREIGN KEY via HammerDB)
- ANALYZE (via HammerDB)
- Power test — all 22 TPC-H queries (Q1–Q22) via HammerDB

**Explicitly out of scope:**

- Manual `psql` DDL for tables, indexes, or statistics
- Multi-user throughput / scale test
- Result-parity comparison with upstream PostgreSQL (covered by M0041)
- Performance regression root-cause analysis (covered by dedicated milestones)

## 3. Environment

| Parameter | Value |
|-----------|-------|
| Scale factor | SF=1 |
| goopg port | 65433 |
| goopg host | 127.0.0.1 |
| Database | tpch |
| User | tpch / postgres (trust auth) |
| Build threads | 1 (single virtual user) |
| Power-test VUs | 1 |
| Wall-clock timeout | 7200 s (2 h) |
| `GOMEMLIMIT` | 20 GiB |

All parameters are defined in `bench/tpch/env_goopg.sh`. Do not override
them unless an explicit test variation is intended and noted in the report.

## 4. Execution Procedure

### Step 0 — Build binary

```bash
go build -o tmp/goopg-bench-bin ./cmd/goopg
```

### Step 1 — Start fresh cluster

```bash
bash bench/tpch/setup_goopg.sh --reset
```

This drops any existing `runtime_goopg/data` directory, calls `goopg initdb`,
and starts the server. Verify the server is up:

```bash
./postgres/local_install/bin/psql -h 127.0.0.1 -p 65433 -U postgres -c "SELECT 1" postgres
```

### Step 2 — Schema build + data load (background, timeout 2 h)

```bash
LOG_BUILD="bench/tpch/logs/build_$(date +%Y%m%dT%H%M%S).log"
timeout 7200 bash bench/tpch/build_schema_goopg.sh \
    > "${LOG_BUILD}" 2>&1 &
BUILD_PID=$!
echo "Build PID: ${BUILD_PID}  Log: ${LOG_BUILD}"
```

Monitor with:

```bash
tail -f "${LOG_BUILD}"
# or: watch -n 30 'tail -20 "${LOG_BUILD}"'
```

Wait for the build to finish (or for `timeout` to kill it):

```bash
wait "${BUILD_PID}"; echo "Build exit: $?"
```

Expected exit code: `0` (success). Exit `124` means timeout.

### Step 3 — Power test (background, remaining budget of total 2-h window)

```bash
LOG_RUN="bench/tpch/logs/run_$(date +%Y%m%dT%H%M%S).log"
REMAINING_SECS=...  # 7200 minus elapsed build time
timeout "${REMAINING_SECS}" bash bench/tpch/run_power_test_goopg.sh \
    > "${LOG_RUN}" 2>&1 &
RUN_PID=$!
echo "Run PID: ${RUN_PID}  Log: ${LOG_RUN}"
```

Monitor with:

```bash
tail -f "${LOG_RUN}"
```

### Step 4 — Stop server

```bash
bash bench/tpch/stop_goopg.sh
```

## 5. Row-Count Verification

After the load completes, the build log should contain HammerDB's internal
row-count output. The expected SF=1 counts:

| Table | Rows |
|-------|------|
| region | 5 |
| nation | 25 |
| supplier | 10,000 |
| customer | 150,000 |
| part | 200,000 |
| partsupp | 800,000 |
| orders | 1,500,000 |
| lineitem | ~6,000,000 |

## 6. Index Verification

HammerDB's `createsqlindex` phase creates (in order):

1. `PRIMARY KEY` constraints on all 8 tables (sql 1–8)
2. `FOREIGN KEY` constraints (sql 9–16)
3. 7 supplementary `CREATE INDEX` statements (sql 17–23)
4. `IDX_LINEITEM_ORDERKEY_FKIDX ON LINEITEM (L_ORDERKEY)` (sql 24)

Log scraping: search for lines matching `Vuser 1:` + `sql` to extract
per-statement outcomes. A line containing `Error` indicates a failure.
One transient failure followed by a successful retry is acceptable; a
persistent failure on any PRIMARY KEY index is a `FAIL`.

## 7. Query Timing Extraction

HammerDB's `runtimer` output includes per-query timing. Extract lines of
the form:

```
Vuser 1:...Q<N>...Time: <N.NN> s
```

or similar. The exact format depends on the HammerDB Tcl driver version
in `bench/tpch/tcl/`. Adapt the grep pattern to the observed output.

## 8. Monitoring Cadence (for autonomous agent)

When running as Ralph:

1. Launch both background commands, record their PIDs.
2. Sleep 10 minutes, then `tail -20` each log.
3. Repeat every 10 minutes until both PIDs have exited.
4. Proceed to report writing.

## 9. Report Schema

Write the report to `analysis/tpch-hammerdb-run-NNN.md` (NNN = next
sequential run number). The report must contain the following sections:

```markdown
# HammerDB TPC-H SF=1 Run NNN — goopg perf-analysis HEAD

**Date:** YYYY-MM-DD
**goopg commit:** <sha>
**Run status:** COMPLETE | PARTIAL | FAILED

## 1. Environment
[table: parameter / value]

## 2. Schema Build & Data Load
[row-count table; elapsed time; error summary if any]

## 3. Index Creation
[per-index status table]

## 4. ANALYZE
[pass/fail; elapsed time]

## 5. Power Test Results (Q1–Q22)
[per-query timing table; overall elapsed time]

## 6. Summary
[bullet list: phases passed, phases failed, notable issues]

## 7. Comparison with Previous Run (run-010)
[comparison table: stage / run-010 / run-NNN]
```

## 10. Pass/Fail Criteria

| Phase | PASS | FAIL |
|-------|------|------|
| Schema build | All 8 tables at SF=1 row counts | Any table missing or short |
| Index creation | All PK+FK+supplementary created (transient retries OK) | Any PK index not present at end |
| ANALYZE | Completes without error | Error or timeout before completion |
| Power test | All 22 queries return a result set (even if slow) | Any query panics the server or hangs beyond timeout |

## 11. References

- `bench/tpch/env_goopg.sh` — environment parameters
- `bench/tpch/build_schema_goopg.sh` — schema build driver
- `bench/tpch/run_power_test_goopg.sh` — power test driver
- `analysis/tpch-hammerdb-run-010.md` — previous run reference
- `docs/design/0052-0001-oversized-message-graceful-recovery.md` — regression fix doc
- `docs/milestones/0053-hammerdb-tpch-complete-run-verification.md` — milestone

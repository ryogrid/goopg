# TPC-H HammerDB Run-009 — End-to-End Verification on `perf-analysis`

**Date:** 2026-05-05
**Branch / commit:** `perf-analysis` @ `c7562fc` (working tree dirty: parser edits + new bench artifacts; no source changes were made for this run)
**Goal:** Verify whether HammerDB can drive the full TPC-H workflow against goopg
without any side-channel help — schema build, COPY load, CREATE INDEX, ANALYZE,
Q1–Q22 power test — using only the standard `bench/tpch/*.sh` wrappers and the
shipped `tcl/build_schema.tcl` / `tcl/run_power_test.tcl`.
**Outcome:** **FAIL — schema build aborted partway through ORDERS/LINEITEM
load. Index creation, ANALYZE, and the power test were never reached.**

## Environment

| Knob | Value |
|------|-------|
| Host | x86_64 Linux (WSL2 6.6.87.2-microsoft-standard-WSL2) |
| goopg binary | `tmp/goopg-bench-bin` (rebuilt by `setup_goopg.sh`) |
| Listen | `127.0.0.1:65433` |
| `shared_buffers` | 2048 MB |
| `GOMEMLIMIT` | 20 GiB |
| TPC-H scale | 1 (HammerDB minimum) |
| `TPCH_BUILD_THREADS` | 1 |
| `TPCH_TOTAL_QUERYSETS` | 1 |
| `TPCH_DEGREE_OF_PARALLEL` | 1 |
| HammerDB | 5.0 (`HammerDB-5.0/`) |
| Driver | `bench/tpch/build_schema_goopg.sh` → `tcl/build_schema.tcl` (no manual DDL) |

The procedure performed strictly the user-requested sequence:

1. `./bench/tpch/setup_goopg.sh --reset` (init + start)
2. `./bench/tpch/build_schema_goopg.sh` (HammerDB-driven CREATE TABLE → COPY → CREATE INDEX → ANALYZE)
3. *(would have been)* `./bench/tpch/run_power_test_goopg.sh`
4. `./bench/tpch/stop_goopg.sh`

No `psql` CREATE TABLE / CREATE INDEX / ANALYZE was issued. `psql` was used
only after the failure for read-only diagnostics (row counts).

## Phase results

### 1. Cluster start — PASS
`setup_goopg.sh --reset` rebuilt the binary, initialized a fresh PGDATA at
`bench/tpch/runtime_goopg/data`, and started goopg cleanly. `pg_isready`
returned 0 within seconds.

### 2. HammerDB schema build — PARTIAL → FAIL

Driver log: `bench/tpch/logs/build_goopg_20260505-092313.log`

| Sub-phase | Status | Notes |
|-----------|--------|-------|
| `CREATE DATABASE tpch` | OK | |
| `CREATE TABLE` for all eight TPC-H tables | OK | |
| Load REGION (5) | OK | |
| Load NATION (25) | OK | |
| Load SUPPLIER (10 000) | OK | |
| Load CUSTOMER (150 000) | OK | |
| Load PART / PARTSUPP (200 000 / 800 000) | OK | |
| Load ORDERS / LINEITEM | **FAIL** | Aborted at ORDERS row 61 000 / LINEITEM row 244 591 — `Error in Virtual User 1: server closed the connection unexpectedly` |
| CREATE INDEX | **NOT REACHED** | `buildschema` never proceeded past the load stage |
| ANALYZE / VACUUM | **NOT REACHED** | |

Post-failure row counts (read-only `psql` diagnostic):

```
region    5
nation    25
supplier  10000
customer  150000
part      200000
partsupp  800000
orders    61000     (expected 1 500 000)
lineitem  244591    (expected ~6 001 215)
```

The HammerDB virtual user surfaced the libpq error
`server closed the connection unexpectedly. This probably means the server
terminated abnormally before or while processing the request.` and finished as
`FINISH FAILED`. HammerDB's TCL driver still emitted `SCHEMA BUILD COMPLETED`
because the wrapper polls `vustatus` for terminal states and exits whether
the result was success or failure.

### 3. Power test — NOT RUN
Skipped because the load did not complete; running the queries against the
half-loaded ORDERS/LINEITEM tables would not have answered the user's
question (whether HammerDB can drive the workflow end-to-end). No tables
have indexes or up-to-date statistics, so any wall-time numbers would be
non-comparable and misleading.

### 4. Shutdown — PASS
`./bench/tpch/stop_goopg.sh` stopped goopg cleanly.

## Failure analysis

The goopg process **was still running and accepting new connections** after
the abort. Only the COPY backend goroutine for the ORDERS/LINEITEM virtual
user disappeared. The server log
(`bench/tpch/runtime_goopg/goopg.log`) is silent about the event:

* No `level=ERROR` line, no panic stack trace, no `connection closed` log
  for `pid=4` (the failing tpch-user backend).
* The only entries between server start and the diagnostic `psql` reconnect
  are the four `connection established` records from the HammerDB launch.

That silence is itself a finding: a backend goroutine died (or its
connection writer returned without producing the expected COPY response)
without the server logging anything. Either:

1. A `recover()` somewhere in the COPY/extended-protocol path swallows
   panics without re-emitting them — i.e. observability gap; or
2. The backend exited a goroutine cleanly but failed to flush a
   COPY/CommandComplete frame before tearing down the connection, leaving
   libpq with a half-finished response stream.

Either path is an observability + correctness bug that surfaces only under
HammerDB's COPY-driven loader. Notably, the `internal/parser` working tree
contains uncommitted edits on `perf-analysis` (`ast.go`, `token.go`); those
changes have not landed in `master` yet, so this regression is local to the
in-flight parser branch.

Past runs at this exact configuration (run-008, 2026-05-04) loaded SF=1 to
completion. The regression therefore appeared between `master` and the
current `perf-analysis` working tree.

## Comparison with run-008

| Stage | run-008 | run-009 |
|-------|---------|---------|
| Schema | OK | OK |
| Load (all eight tables) | OK | **fails at ORDERS ~61k rows** |
| CREATE INDEX | OK | not reached |
| ANALYZE | OK | not reached |
| Q1–Q22 wall times | recorded | not measurable |

run-009 cannot validate the M0044-0006 wall-time gate
(Q3/Q6/Q14/Q15/Q19 ≥ 30 % vs run-007) because the load never finished.

## Conclusion

**No** — at HEAD of `perf-analysis` HammerDB cannot drive the TPC-H
workflow to completion without manual intervention. The first failure is
deterministic-looking (ORDERS load consistently breaks at ~61 k rows in
this single trial) and surfaces as a silent backend disappearance, not a
surfaced error. Index creation, ANALYZE, and Q1–Q22 are gated on this load
finishing and were not exercised.

## Recommended follow-up (added to fix_plan)

1. Investigate why the ORDERS/LINEITEM COPY backend disconnects with no
   server log entry on `perf-analysis`. Likely candidates: parser changes
   on this branch (`internal/parser/ast.go`, `internal/parser/token.go`),
   or a recover-without-relog in the COPY/extended-protocol handler.
2. Add a structured log on backend goroutine exit (panic-or-not) so a
   future occurrence is not silent.
3. Re-run HammerDB end-to-end after the fix and append the result as
   run-010.

## Reproduction

```bash
# from repo root
./bench/tpch/setup_goopg.sh --reset
./bench/tpch/build_schema_goopg.sh   # fails as above on perf-analysis
./bench/tpch/stop_goopg.sh
```

Logs:
* HammerDB: `bench/tpch/logs/build_goopg_20260505-092313.log`
* goopg server: `bench/tpch/runtime_goopg/goopg.log`

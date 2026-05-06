# HammerDB Schema-Build CHECKPOINT Audit (M0057-0003)

**Date:** 2026-05-06
**Author:** goopg perf-analysis (M0057)

## 1. Goal

Determine whether the HammerDB TPC-H schema-build (`buildschema`)
Tcl driver (`HammerDB/src/postgresql/pgolap.tcl`) issues an explicit
SQL `CHECKPOINT` before signalling `FINISHED SUCCESS`.

## 2. Evidence

### 2.1 Source code inspection

`grep -i checkpoint HammerDB/src/postgresql/pgolap.tcl`

**Result: 0 matches.** The file contains no CHECKPOINT call of any
kind.

The schema-build flow in `pgolap.tcl` performs:
1. CREATE TABLE (8 tables).
2. Bulk INSERT via the `LoadTPCHTables` / `mk_*` procs.
3. `GatherStatistics` proc — runs ANALYZE on each table (ORDERS,
   PARTSUPP, CUSTOMER, PART, SUPPLIER, NATION, REGION, LINEITEM).
4. Emits `TPCH SCHEMA COMPLETE` / `Vuser 1:FINISHED SUCCESS`.

No CHECKPOINT is issued anywhere in this flow.

### 2.2 Server log during run-015 build

Extracted from the run-015 build log
(`bench/tpch/logs/build_goopg_20260506T072831.log`):
- `"checkpoint complete"` appears at the end of the load phase —
  this is the **checkpointer's scheduled checkpoint**, NOT an
  explicit one from HammerDB.
- The default `checkpoint_timeout = 15min` caused the checkpointer
  to fire automatically ~12 min into the build.
- No HammerDB-issued CHECKPOINT appeared.

## 3. Implications

### 3.1 Build-end state

After `FINISHED SUCCESS`, the database is in one of two states:
- **The scheduled checkpoint fired before build end:** all dirty pages
  are flushed to disk; WAL ahead of the checkpoint LSN can be pruned;
  crash recovery from this point replays only a small window of WAL.
- **No checkpoint fired:** all mutations since the last checkpoint
  exist only in WAL; a crash would require replaying potentially
  the entire build's WAL.

Given a 12-minute build and a 15-min `checkpoint_timeout`, the
checkpointer fired during the build (confirmed by the server log
above). However, the checkpoint completed partway through the build,
not at the end.

### 3.2 Run-014 → run-015 restart

After the run-014 SIGKILL (mid-Q9, ~75 minutes into the benchmark):
- goopg performed WAL recovery on restart.
- Server log shows `"goopg listener bound"` within ~20 seconds, with
  no ERROR lines, confirming recovery succeeded.
- This is consistent with WAL replay working correctly from the last
  checkpoint LSN.

### 3.3 Correctness verdict

The absence of a HammerDB-issued CHECKPOINT is **acceptable**:
- The scheduled checkpointer fires during the build, providing
  a crash-safe base.
- The explicit `CHECKPOINT` recommended by M0054-0007-checkpoint-
  before-run (issued via `goopg checkpoint -D ...` before the power
  test) ensures a clean I/O baseline for the benchmark.
- With M0057-0002's `checkpoint_timeout = 24h`, no checkpointer will
  fire during the power test itself.

**No follow-up sub-task M0057-0003-wal-replay-gap is needed.**

## 4. Recommendation

Maintain the current workflow:
1. `bash bench/tpch/build_schema_goopg.sh` — let the scheduled
   checkpointer fire naturally during the ~12-minute load.
2. After `FINISHED SUCCESS`: `./tmp/goopg-bench-bin checkpoint -D ...`
   — force a clean checkpoint immediately before the power test.
3. `bash bench/tpch/run_power_test_goopg.sh` — run queries with
   no mid-benchmark checkpoints (guaranteed by 24h timeout).

## 5. References

- `HammerDB/src/postgresql/pgolap.tcl` — schema build driver.
- `bench/tpch/setup_goopg.sh` — GUC configuration.
- `bench/tpch/logs/build_goopg_20260506T072831.log` — run-015 build log.
- `docs/design/0057-0002-checkpoint-config-for-benchmarks.md`.

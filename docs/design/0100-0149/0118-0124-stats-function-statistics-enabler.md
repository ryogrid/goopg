# 0118-0124 — `stats` enabler rung 2: cumulative function statistics + setup-result echo

**Status:** accepted
**Milestone:** M0118-0009 (Upstream isolation-spec suite pass-through — misc/system-level specs)
**Spec:** `postgres/src/test/isolation/specs/stats.spec`
**Predecessor:** [0118-0123](0118-0123-stats-flush-gucs-enabler.md) (rung 1 — GUCs + flush/snapshot void no-ops)
**Kind:** ENABLER, not a promotion. `stats.spec` stays `defer`.

## Summary

Rung 2 of the `stats` isolation-spec enabler builds the **cumulative
function-statistics** subsystem (PostgreSQL's `pgstat` per-function call
counters) plus the isolation-runner change needed to even observe it. Together
they advance the spec's first divergence from line **4** (the global-setup
result block was never echoed) to line **449** — the first ≈8 permutations (the
entire function-stats counting, multi-connection accumulation, cross-transaction
flush, and `pg_stat_reset*` block) now match PostgreSQL 18.3 byte-for-byte,
including the call counts and the `total_time > 0 / self_time > 0` columns.

The spec remains `defer`: the new first divergence is the **uncommitted
`DROP FUNCTION` cross-session visibility** case (a session that drops a function
inside an open transaction must still let *other* sessions call it until commit —
goopg has no per-session MVCC catalog, so the drop is immediately visible and the
concurrent call 42883s). That, the 2PC stat-drop handling, the
`stats_fetch_consistency` snapshot/cache models, relation tuple stats, and SLRU
stats are later rungs.

## Why two changes in one rung

The function-stats engine is unobservable through the spec without the
setup-result echo, and the echo alone does nothing without the engine. They form
one coherent, independently-verified deliverable: "make function statistics
correct and observable." Both are scoped to the `stats` feature and carry nil
blast radius elsewhere.

## Background — PostgreSQL's model

For each tracked function OID, `pgstat` records `calls`, `total_time` (including
nested calls), and `self_time` (excluding nested calls). A backend accumulates
these in **backend-local pending** state during execution
(`pgstat_init_function_usage` / `pgstat_end_function_usage`, gated by the
`track_functions` GUC) and periodically flushes them into **shared memory**
(`pgstat_report_stat`, rate-limited to once per `PGSTAT_MIN_INTERVAL` ≈ 1 s). The
getters `pg_stat_get_function_calls / _total_time / _self_time` read the shared
(flushed) counters, so a value is not visible to other backends — or even
re-read in the same backend — until a flush. The spec forces a flush between
mutating and observing steps via `pg_stat_force_next_flush()`. Reference:
`src/backend/utils/activity/pgstat_function.c`,
`src/backend/utils/adt/pgstatfuncs.c`.

## Design

### 1. Two-tier function-stats store (`internal/executor/pgstat_functions.go`)

A single process-global `functionStatsManager`:

- `pending[sessionID][oid]` — backend-local counters, incremented on every
  tracked invocation.
- `shared[oid]` — cluster-global counters, merged from a session's pending set
  when that session calls `pg_stat_force_next_flush()`. The getters read here.

The goopg server is one OS process per cluster and each isolation-spec run spawns
a *fresh* server, so the global store starts empty per cluster — the same
lifecycle as a freshly `initdb`'d PostgreSQL cluster. No reset hook is needed.

`sessionID` is the stable per-connection `SessionRegistry.UniqueID()` carried in
`Context.AdvisorySessionIdentity` (the same identity advisory locks use). Zero
("no session identity": internal/test contexts) still accumulates under a single
bucket and flushes correctly.

### 2. Counting hook (`internal/executor/plpgsql_runtime.go`)

`executeStoredRoutine` is the single chokepoint for stored-function calls
(`SELECT f()`, `PERFORM f()`, nested calls). The language-dispatch tail is split
into `dispatchStoredRoutineByLanguage`; when `shouldTrackFunction(ctx, r)` holds,
`executeStoredRoutine` times the dispatch with `time.Now()/time.Since` and
records one call into the session's pending counters. `self_time` is set equal to
`total_time` (goopg does not separate nested-call time; the spec only checks
`> 0`, which holds for any executed body — the spec's functions `pg_sleep(10µs)`
to guarantee a positive interval).

`shouldTrackFunction` mirrors `pgstat_track_functions`: `all` tracks every
language, `pl` tracks only procedural languages (not `sql`/`internal`/`c`),
`none` (the default) tracks nothing — so the normal call path keeps
zero overhead.

### 3. Getters and reset (`internal/executor/expr.go`, `evalFuncCall`)

New builtin cases dispatched before the stored-routine fallback:

- `pg_stat_get_function_calls(oid)` → `bigint`, or `NULL` when no flushed stats
  exist for the OID.
- `pg_stat_get_function_total_time(oid)` / `_self_time(oid)` → milliseconds as a
  `NUMERIC` (compares correctly against the spec's `> 0`), or `NULL`.
- `pg_stat_reset_single_function_counters(oid)` → void; drops the shared OID
  entry (a non-existent OID is a silent no-op, matching PG).
- `pg_stat_reset()` → void; clears the shared function-stats store (PG resets all
  DB stats; goopg currently tracks only function stats).

`pg_stat_force_next_flush()` is upgraded from rung 1's pure no-op to flush the
calling session's pending counters into the shared store.

The OID argument resolves via `'name'::regproc`, which already maps to the
`catalog.Routine.OID`; the spec stores those OIDs in `test_stat_oid` and the
getters key on them.

### 4. Isolation-runner setup-result echo (`internal/testport/framework/isolation_runner.go`)

`isolationtester.c run_permutation` prints the result set of any global `setup`
block whose final command returns tuples (`PGRES_TUPLES_OK`) — right after the
`starting permutation:` line, before any step. `stats.spec`'s setup ends with
`SELECT pg_stat_force_next_flush()`, so its one-row block is echoed before every
permutation. The runner previously ran global setup via `execConn` (no capture);
it now uses the existing `execConnSetupCapture` helper, accumulates the result
text, and hands it to `runPermutation`, which writes it immediately after the
header.

This is safe for every other spec: a currently-passing strict spec whose setup
returned tuples would already have diverged on the missing block, so passing
specs have non-tuple (COMMAND_OK) setups → empty echo → byte-unchanged.

## Blast radius

- Function counting fires only when `track_functions != 'none'`; the boot default
  is `none`, so TPC-H, pgbench, and every other isolation spec take the
  untouched dispatch path.
- The getters/reset builtins fire only for their exact names.
- The runner echo adds output only for setups that return tuples (≈ this one
  spec); all currently-strict specs are byte-unchanged.

## Verification

- `TestFunctionStatsManager` + `TestShouldTrackFunction` (executor) — store
  two-tier semantics (pending invisible until flush, cross-session accumulation,
  single/all reset, OID-0 guard) and the GUC gating matrix.
- Probe `RunAndCompare` on `stats.spec`: first divergence advanced L4 → L449;
  the function-stats counting/multi-connection/cross-txn/reset permutations match
  byte-for-byte (counts and `>0` time columns correct).
- Full `TestPort_Isolation*` strict suite re-run to confirm the runner change
  regresses no promoted spec.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Remaining rungs (each Effort-L; `stats` stays `defer`)

1. **Uncommitted-DROP cross-session visibility** — per-session MVCC catalog so a
   function dropped inside an open transaction stays callable by other sessions
   until commit (the current first divergence; shared with alter-table-4 /
   partition-concurrent-attach).
2. **2PC stat-drop handling** — `PREPARE`/`COMMIT PREPARED`/`ROLLBACK PREPARED`
   interaction with pending stat drops (rides 0118-0110).
3. **`stats_fetch_consistency` snapshot/cache models** — per-transaction snapshot
   caching so `cache`/`snapshot` freeze the observed values.
4. **Relation tuple stats** — `pg_stat_get_numscans/_tuples_*`,
   `pg_stat_get_live/dead_tuples`, `track_counts` gating, `pg_stat_get_xact_*`.
5. **SLRU stats** — `pg_stat_slru` (`blks_zeroed` etc.) driven by the async-notify
   SLRU.

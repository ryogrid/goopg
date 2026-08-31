# 0118-0123 — `stats` enabler rung 1: cumulative-statistics GUCs + flush/snapshot void no-ops

**Milestone:** M0118-0009 (Misc / system-level isolation specs)
**Spec:** `postgres/src/test/isolation/specs/stats.spec`
**Status:** accepted — **enabler, NOT a promotion** (`stats` stays `defer`)

## Context

`stats.spec` exercises PostgreSQL's *cumulative statistics* subsystem
(`pgstat`, `src/backend/utils/activity/pgstat*.c`): per-function call/time
counters, per-relation tuple counters (`n_tup_ins/upd/del`, `seq_scan`, …),
the reset functions (`pg_stat_reset*`), the `track_functions` / `track_counts`
GUCs, the `stats_fetch_consistency` snapshot modes, and the interaction of all
of this with two-phase commit. It is one of the four remaining failed isolation
specs, all genuinely Effort-L unbuilt subsystems. goopg has no `pgstat`
subsystem today; this spec is being advanced one rung per loop in the
established M0118 enabler style.

### First divergence before this change

```
run error: global setup (permutation 0):
  pq: function pg_stat_force_next_flush does not exist (42883)
```

The spec's global `setup` block ends with `SELECT pg_stat_force_next_flush();`,
so **every** permutation aborted before any step ran. The pg_proc seed already
carries the catalog rows (`pg_stat_force_next_flush` OID 2137,
`pg_stat_clear_snapshot` OID 2230), but the executor's `evalFuncCall` had no
dispatch case, so the name fell through to `evalStoredRoutineFuncCall`, found no
user routine body, and raised `42883`. Two of the session setups also
`SET stats_fetch_consistency = …` and steps `SET track_functions = …` /
`SET track_counts = …`, none of which were registered GUCs.

## Change

Three low-blast-radius pieces, all faithful to goopg's architecture:

1. **GUC registration** (`internal/config/defaults.go`, beside `track_activities`):
   - `track_counts` — `bool`, boot `on`, `PGC_SUSET`.
   - `track_functions` — `enum {none, pl, all}`, boot `none`, `PGC_SUSET`.
   - `stats_fetch_consistency` — `enum {none, cache, snapshot}`, boot `cache`,
     `PGC_USERSET`.

   Names, types, contexts, and boot values mirror
   `src/backend/utils/misc/guc_tables.c`. Matching lines added to
   `internal/config/postgresql.conf.sample` (the `TestSampleConfigCoversRegistry`
   M0108 invariant requires every registered, file-allowed GUC to have a sample
   entry whose default equals the registry `BootVal`).

2. **`pg_stat_force_next_flush()` → void** (`evalFuncCall`): a faithful no-op.
   Upstream skips a backend's pending-stats flush if the rate-limit interval has
   not elapsed; this function forces the *next* flush through. goopg has no
   separate statistics-collector process — there is nothing to flush *to* and no
   pending-stats buffer to drain — so forcing a flush is a no-op. The spec calls
   it between mutating and observing steps purely to make pending stats visible,
   an ordering goopg already satisfies.

3. **`pg_stat_clear_snapshot()` → void** (`evalFuncCall`): a faithful no-op.
   It discards the transaction's cached statistics snapshot (relevant under
   `stats_fetch_consistency = 'snapshot'`/`'cache'`). goopg reads cumulative
   stats directly with no per-transaction snapshot cache, so there is nothing to
   clear.

Both return `NewStringDatum("")` — the non-NULL void-like value goopg's other
void builtins (`pg_notify`, the advisory-lock family) return, so `IS NOT NULL`
holds and isolationtester renders an empty value.

## Result

First divergence advanced from *global setup failure on permutation 0* to the
**first permutation's step output**:

```
L14 actual: ERROR:  function pg_stat_get_function_calls does not exist
```

i.e. the GUC `SET` steps and the flush now succeed; the spec reaches its real
content. It stays `defer` — the remaining rungs are the actual statistics
subsystem and are each Effort-L:

- **Runner:** echo a global/session **setup query's** result rows (PG's
  isolationtester prints the setup `SELECT pg_stat_force_next_flush()` block
  once before the steps; goopg's runner currently does not).
- **Function stats:** `pg_stat_get_function_calls/total_time/self_time(oid)`, a
  cluster-global per-function counter store incremented on user-function
  invocation gated by `track_functions`, and
  `pg_stat_reset_single_function_counters(oid)`.
- **Relation stats:** per-table tuple counters gated by `track_counts`, the
  `pg_stat_get_*` getters, `pg_stat_get_xact_*` (in-transaction pending), and
  the live/dead-tuple accounting the spec checks across (auto)vacuum.
- **Reset:** `pg_stat_reset()`.
- **Snapshot semantics:** real `stats_fetch_consistency = 'snapshot'`/`'cache'`
  caching behaviour + `pg_stat_clear_snapshot()` actually invalidating it.
- **2PC interaction:** stats accumulated inside a `PREPARE TRANSACTION` becoming
  visible at `COMMIT PREPARED` (rides the 0118-0110 same-backend 2PC machinery).

## Blast radius

Nil for existing behaviour. The two new `evalFuncCall` cases fire only for the
two named functions (previously hard errors). The three GUCs are newly
recognised; goopg does not yet gate any behaviour on them, so unset/default
sessions (TPC-H, pgbench, all other specs) are byte-unchanged.

## Gates

- New `internal/executor/pg_stat_flush_test.go::TestPgStatFlushSnapshotVoidNoops`
  — both functions evaluate to a non-NULL empty void value.
- New `internal/config/stats_gucs_test.go::TestStatsGUCs` — registration, boot
  values, types, contexts, enum options, valid-value acceptance, enum rejection.
- `TestSampleConfigCoversRegistry` PASS (sample/registry parity preserved).
- `go test ./internal/config/ ./internal/executor/` PASS; `go build ./...` clean.
- `stats.spec` re-probed: global setup passes, divergence advanced to
  `pg_stat_get_function_calls` (first permutation). CSV row stays `defer`.
- pgbench smoke = pre-commit hook.

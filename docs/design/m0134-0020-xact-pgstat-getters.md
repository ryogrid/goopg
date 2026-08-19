# M0134-0020 — transaction-scoped pgstat getters (`pg_stat_get_xact_*`)

**Status:** implemented (slice 1 of the `stats.sql` digestion; the case itself
stays PARKED — see `.ralph/deferral_ledger.md`, 2026-08-20, M0134-0020).
**Case:** `postgres/src/test/regress/sql/stats.sql` (CSV row was `not-tried`;
ran at HEAD 2026-08-20 → 1391 diff lines / 31 hunks / 101 `+ERROR` lines, so it
is genuinely failing, not a stale status).

## 1. The gap

PostgreSQL exposes two tiers of cumulative statistics:

- the **shared/flushed** tier — what other sessions can see
  (`pg_stat_get_function_calls`, `pg_stat_get_tuples_inserted`, ...); and
- the **transaction-scoped** tier — the *uncommitted* deltas of the calling
  backend's current transaction (`pg_stat_get_xact_*`).

goopg implements the first tier and had **no member of the second tier at all**.
`SELECT pg_stat_get_xact_function_calls(oid)` failed engine-wide with SQLSTATE
42883 `function ... does not exist`.

This read as "supported" because it was half-built, exactly like the missing `^`
operator of M0134-0019:

- `internal/initdb/pg_proc_seed_data.go` is a *generated mirror of all 3397 real
  PG 18 `pg_proc.dat` rows* (`cmd/gen-pg-proc-data`), and `initdb.go`'s
  `pgProcAllEntries()` loads every one of them into the on-disk `pg_proc` heap.
  So `pg_stat_get_xact_function_calls` (OID 3046, `seed_data.go:2003`) and
  `pg_stat_get_xact_tuples_inserted` (OID 3040, `seed_data.go:1997`) already had
  correct catalog rows — `int8` return, single `oid` argument — with **no Go code
  behind their `HandlerName`s**. A populated `HandlerName` never implies an
  implementation exists; it is copied verbatim from PG's catalog data.
- The dispatch is one `switch name` in `evalFuncCall`
  (`internal/executor/expr.go:8440`). An unmatched name falls through to
  `evalStoredRoutineFuncCall` (`plpgsql_runtime.go:255`), which looks for a
  user-defined routine and — not finding one — emits the generic 42883 text seen
  in the diff.

The backing state, by contrast, **already existed and is PG-shaped**:
`funcStats.pending[sessionID][oid]` (`pgstat_functions.go:49-58`) and
`relStats.stagingFor(sessionID, oid)` (`pgstat_relations.go:78-132`) are the
per-transaction delta tiers, folded into the shared tier at commit/abort
(`commitXact`/`abortXact`, `pgstat_relations.go:243-268`). Only the read path
was missing.

## 2. PG semantics being reproduced

Cited to `postgres/src/backend/utils/adt/pgstatfuncs.c`:

| function | PG source | no pending entry | else |
|---|---|---|---|
| `pg_stat_get_xact_function_calls(oid) → int8` | `pgstatfuncs.c:1804` | **SQL NULL** (`find_funcstat_entry` → `PG_RETURN_NULL()`) | `numcalls` |
| `pg_stat_get_xact_tuples_inserted(oid) → int8` | `PG_STAT_GET_XACT_RELENTRY_INT64` macro, `pgstatfuncs.c:1758`, instantiated line 1796 | **0**, never NULL (`find_tabstat_entry` → `result = 0`) | `counts.tuples_inserted` |

The NULL-vs-0 asymmetry is not an inconsistency to be smoothed over — it is PG's
actual behavior, and goopg's **existing shared-tier twins already encode the same
split**: `pg_stat_get_function_calls` returns `NullDatum` on absent
(`expr.go:12566-12577`), while the relation-counter case group deliberately
discards its found-bool and yields 0 (`expr.go:12636-12688`, with a comment
saying so). The new arms mirror their respective twins (Hard-won Rule #2).

Both functions take the OID argument through the existing `statFuncOIDArg`
helper (which already returns `ok=false` → NULL for a NULL argument) and return
`NewIntDatum` (int8). No new Datum kind, no new state shape, no catalog change.

## 3. Design decision: non-allocating reads

`find_funcstat_entry`/`find_tabstat_entry` in PG are pure lookups — they never
create an entry. goopg's `funcStats.record()` and `relStats.stagingFor()` both
*allocate on access* ("creating if needed"). Reusing them for the read path would
work for `stats.sql` but would make a bare
`SELECT pg_stat_get_xact_function_calls(<oid>)` materialise a pending entry for
an OID the session never touched — a silent divergence from PG that a later
flush-visibility test could trip over.

So each store gains a small **read-only peek** accessor that looks up without
allocating and reports a found-bool. The function-calls arm honours the
found-bool (NULL on absent); the tuples-inserted arm discards it and reports 0,
matching both PG and its goopg twin.

## 4. Scope shipped, and what this does NOT fix

`stats.sql` calls exactly two `pg_stat_get_xact_*` functions (verified by grep of
the oracle file), so this is a complete 2-of-2 slice for the case, not a partial
one. PG's other `pg_stat_get_xact_*` siblings all have seed rows already and
would extend by the same "add a `case`" pattern when a case demands them.

Measured leverage: ≈95 of 1391 diff lines (≈6.8%) and 21 of 101 `+ERROR` lines
(7 direct + 14 transaction-aborted cascades). **The case remains far from green**
— ~93% of the diff survives across the `pg_stat_have_stats`, `pg_stat_io` /
per-backend IO, `pg_stat_reset_*`, FROM-clause table-valued-function,
missing-GUC, `VACUUM (BUFFER_USAGE_LIMIT)` and tuple-counter-accounting buckets.
Those are recorded with resume points in `.ralph/deferral_ledger.md`; the CSV row
stays `failed`/`pass_required=no` and `make regen-testport` is NOT run.

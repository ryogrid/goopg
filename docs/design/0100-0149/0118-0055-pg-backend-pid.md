# 0118-0055 — `pg_backend_pid()` scalar built-in (M0118-0008 enabler)

**Status:** accepted
**Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency — isolation tail)
**Kind:** Enabler, NOT a promotion.

## Problem

`pg_backend_pid()` was seeded in `pg_proc` (OID 2026, `RetType` 23/int4,
`HandlerName "pg_backend_pid"`) but had **no executor handler**. A SQL call to
it fell through `evalFuncCall`'s switch to `evalStoredRoutineFuncCall`, which
found no user/stored routine and hard-failed with:

```
ERROR:  function pg_backend_pid does not exist
```

This was the **first divergence** of two isolation specs in the M0118-0008
tail — `detach-partition-concurrently-3` and `-4` — whose opening step is

```
step s2snitch { INSERT INTO d3_pid SELECT pg_backend_pid(); }
```

so the entire spec aborted at line 1 of every permutation. `pg_backend_pid()`
is also a standard, widely-used introspection function (clients, monitoring,
`pg_stat_activity.pid` / `pg_cancel_backend(pid)` joins), so the gap is broader
than these two specs.

## Design

goopg is a single OS process multiplexing many connections, so there is no
per-connection OS PID. The faithful analog already exists: at connect time the
server assigns a per-connection integer (`Server.serveConn`:
`pid := s.nextPID.Add(1)`) and reports it in `BackendKeyData`. That same value
is:

- registered in the activity registry (`activity.Backend.PID`, keyed by
  `ProcNum`), which is what `pg_stat_activity.pid` surfaces and what
  `pg_cancel_backend(pid)` targets; and
- stamped into the per-session GUC `goopg.backend_pid`
  (`sess.Set("goopg.backend_pid", pidStr, false)`).

The executor `Context` already exposes `(*Context).backendPID()` (added for
synthetic `pg_locks` rows), which resolves the live PID via
`Activity.PIDForProcNum(ProcNum)`.

`pg_backend_pid()` is therefore implemented as a nullary case in `evalFuncCall`
(`internal/executor/expr.go`, beside `current_database`) that returns the
integer PID resolved as:

1. `ctx.backendPID()` (activity registry — the production path); else
2. `ctx.GetSetting("goopg.backend_pid")` (GUC fallback); else
3. `0` (PG's `pg_backend_pid()` is never NULL; a wired session always hits 1/2).

Return kind is `KindInt` (int4), matching `pg_proc.RetType` 23.

## Scope / non-goals

This is a single-function enabler. It does **not** promote `-3`/`-4`: after this
fix the probe shows their first divergence advances from the `s2snitch` line to

- L12 — `ALTER TABLE … DETACH PARTITION … CONCURRENTLY; <waiting ...>` (the
  two-phase concurrent-detach wait-out-old-snapshots semantics, still
  synchronous in goopg), and
- L14 — `ERROR: function pg_cancel_backend does not exist` (the cross-backend
  cancellation handler, also seeded-but-unwired).

Both are separate, larger follow-ups (DETACH-CONCURRENTLY two-phase visibility;
`pg_cancel_backend` cross-backend signalling via the existing cancel registry).

## Testing

- `TestPgBackendPID` (`internal/executor/pg_backend_pid_test.go`): registry path
  (`ProcNum`→PID 4242), GUC fallback (→7), and unwired (→0, no error).
- Live-server probe confirms `detach-partition-concurrently-3` step `s2snitch`
  no longer errors; first divergence advances to L12/L14 as above.
- `go build ./...` + `internal/executor` package tests clean; pgbench smoke via
  the pre-commit hook (no hot-path change — a new nullary function case).

## Oracle

`src/backend/utils/adt/misc.c` `pg_backend_pid()` → `MyProcPid`.

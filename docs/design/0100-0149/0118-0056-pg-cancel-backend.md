# 0118-0056 — `pg_cancel_backend(pid)` scalar built-in (M0118-0008 enabler)

**Status:** accepted
**Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency — isolation tail)
**Kind:** Enabler, NOT a promotion.

## Problem

`pg_cancel_backend(pid int4) → bool` was seeded in `pg_proc` (OID 2171,
`RetType` 16/bool, `ArgTypes [23]`/int4, `HandlerName "pg_cancel_backend"`) but
had **no executor handler**. A SQL call fell through `evalFuncCall`'s switch to
`evalStoredRoutineFuncCall`, which found no user/stored routine and hard-failed:

```
ERROR:  function pg_cancel_backend does not exist
```

This is the next divergence (after `pg_backend_pid()`, design 0118-0055) of the
`detach-partition-concurrently-3` / `-4` isolation specs in the M0118-0008 tail,
whose cancel step is

```
step s1cancel { SELECT pg_cancel_backend(<other backend pid>); }
```

It is also a standard, widely-used admin/introspection function (clients,
monitoring, killing a stuck peer query), so the gap is broader than these two
specs.

## Design

goopg multiplexes many connections in one OS process, so a query cancel is not
an OS signal but a call into the **process-wide cancel registry**
(`internal/server/cancel.go`, `backendCancelRegistry`) that already backs the
wire-protocol `CancelRequest` path: at connect time each backend registers its
`pid → *cancelEntry`, and each in-flight query installs its
`context.CancelFunc` via `setQueryCancel`.

The wire path (`cancelQuery(pid, secretKey)`) checks the per-connection secret
key, because a `CancelRequest` arrives unauthenticated off a fresh socket.
`pg_cancel_backend(pid)`, by contrast, is invoked **inside an authenticated
backend**, so no secret check applies. Two small additions express that:

- `(*cancelEntry).cancelNoSecret()` — fires the active query's cancel func
  without a secret check; a no-op when the target is idle (`queryCancel == nil`).
- `(*backendCancelRegistry).cancelByPID(pid uint32) bool` — looks up the entry
  and calls `cancelNoSecret`. Returns `true` if a backend with that pid is
  registered (the signal was "sent" — `true` even when the target is idle, as PG
  returns `true` for any live backend), `false` if the pid is unknown (PG also
  emits a `WARNING` in that case; goopg returns `false` without the WARNING).

The executor side is a new case in `evalFuncCall`
(`internal/executor/expr.go`, beside `pg_backend_pid` / `current_database`):

- strict in its argument — `NULL` pid → `NULL` result;
- delegates to `ctx.CancelBackend(pid)` when wired, returning `bool`;
- returns `false` when `ctx.CancelBackend` is nil (unit/embedded contexts with
  no peer backends).

`Context.CancelBackend func(pid int32) bool` is the injection seam. The server
wires it in **both** dispatch paths — `dispatchSimpleQueryViaExecutor`
(`dispatch.go`) and the extended-query path (`dispatch_extended.go`) — so the
function works regardless of protocol. The closure rejects non-positive pids and
forwards to `s.cancelReg.cancelByPID`. Unlike `pg_backend_pid`, this seam
depends only on the registry, not on per-session `Activity`, so the extended
path needs no other wiring.

## Scope / non-goals

This is a single-function enabler. It does **not** promote `-3`/`-4`: after this
lands, their first divergence advances to the `DETACH … CONCURRENTLY`
two-phase `<waiting>` blocking (wait out old snapshots before `relpartbound`
flips to NULL at commit), a larger follow-up coupled to cross-session catalog
visibility. `pg_terminate_backend(pid)` (close the connection, not just cancel
the query) is a sibling not implemented here.

## Sibling-path note

The simple- and extended-query dispatch paths both build an `executor.Context`;
the `CancelBackend` seam is wired in **both** to avoid a protocol-dependent
latent gap (the recurring `pattern_sibling_paths_must_agree` failure mode). The
executor switch case sits beside `pg_backend_pid`, the companion enabler.

## Tests / gates

- `TestCancelByPID` (`internal/server/cancel_by_pid_test.go`): unknown pid →
  `false`; registered-but-idle → `true`, nothing fired; busy backend → `true`
  and the query cancel func fires exactly once; secret key irrelevant.
- `go build ./...`, `go vet ./internal/server/ ./internal/executor/` clean.
- Full `internal/server` and `internal/executor` package tests PASS.
- pgbench smoke = pre-commit hook.

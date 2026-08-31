# 0118-0093 — `temp-schema-cleanup.spec` PROMOTED: pg_terminate_backend self-termination + backend-exit temp cleanup

**Milestone:** M0118-0009 (Misc / system-level isolation specs)
**Status:** accepted
**Date:** 2026-06-25
**Spec:** `postgres/src/test/isolation/specs/temp-schema-cleanup.spec`
**Test:** `internal/testport/isolation_port_test.go` → `TestPort_IsolationTempSchemaCleanup` (now `runIsoSpecStrict`)

## Summary

Closes `temp-schema-cleanup.spec`: both permutations now match PostgreSQL 18.3
byte-for-byte and the dedicated test is promoted from soft `runIsoSpec` to
`runIsoSpecStrict`. Builds on 0118-0091 (per-session temp namespace +
`DISCARD TEMP`) and 0118-0092 (temp-type dependency cascade). The remaining
work was permutation 2, the **process-exit** path:

```
s1_advisory            -- s1 takes a session-level advisory lock
s2_advisory            -- s2 blocks waiting for the same lock
s1_create_temp_objects -- s1 creates temp table + a non-temp fn on its rowtype
s1_exit                -- SELECT pg_terminate_backend(pg_backend_pid())
s2_check_schema        -- s2 (now unblocked) sees an EMPTY temp schema
```

Expected output for `s1_exit`:

```
FATAL:  terminating connection due to administrator command
server closed the connection unexpectedly
	This probably means the server terminated abnormally
	before or while processing the request.
```

Three pieces were missing: (1) the `pg_terminate_backend(pid)` function and
self-termination semantics; (2) backend-exit temporary-object cleanup ordered
**before** advisory-lock release; (3) harness rendering of the libpq
connection-death message.

## Engine changes

### 1. `pg_terminate_backend(pid)` + self-termination (`ErrSelfTerminate`)

- `executor.ErrSelfTerminate` (new sentinel, `operator.go`). Returned by the
  `pg_terminate_backend` evaluator when `pid == ` the caller's own
  `pg_backend_pid()`. Aborting the query immediately (no result row) mirrors
  PostgreSQL, where the SIGTERM is processed at `CHECK_FOR_INTERRUPTS` inside
  the function and the connection dies before a value is returned.
- `expr.go` `case "pg_terminate_backend"`: NULL arg → NULL; self pid →
  `ErrSelfTerminate`; peer pid → `ctx.TerminateBackend(pid)` (returns bool).
- `executor.Context.TerminateBackend func(pid int32) bool` (new callback,
  sibling of `CancelBackend`). Wired in both `dispatch.go` and
  `dispatch_extended.go` to `s.cancelReg.terminateByPID`.
- `cancel.go`: `cancelEntry.terminate` holds the connection's **root-context**
  cancel func (`setTerminate`, called once at connection start with the
  `connCtx` cancel). `terminateByPID(pid)` fires it, tearing a **peer**
  connection down via the existing `ctx.Err() != nil` FATAL path at the top of
  the serve loop. Self-termination never routes here.

`ErrSelfTerminate` propagation: the executor returns it raw from
`op.Open`/`op.Next`; `executeOneSimpleStmt` short-circuits the
`writeQueryError` conversion for it (so it is NOT sent as a normal `ERROR`
response) and returns it up; `runPostStartupLoop`'s `MsgQuery` handler
recognises it, emits `writeFatal(AdminShutdown, "terminating connection due to
administrator command")`, and returns — closing the connection.

### 2. Backend-exit temp cleanup, ordered before advisory release

`Server.cleanupSessionTempObjects(sess)` runs PostgreSQL's `RemoveTempRelations`
analog at backend exit: `SessionTempTableNames` → `DropSessionTempObjects` →
`DropRoutinesReferencingTypes` (the 0118-0092 name-keyed cascade, reused) →
`DropTempNamespace` (unlike `DISCARD TEMP`, backend exit also drops the
namespace registration).

It is registered as a `defer` in `runPostStartupLoop` **after** the
`ReleaseAllAdvisoryLocks` defer, so LIFO runs temp cleanup **first**. The
ordering is load-bearing: a peer blocked on the same session-level advisory lock
only unblocks once the catalog is clean, so `s2_check_schema` (which runs after
`s2_advisory` completes) observes zero temp relations/types/functions. This
matches the spec's own comment: *"session level advisory locks are released only
after temp table cleanup."*

## Harness change (`isolation_runner.go`)

Go's `lib/pq` collapses a mid-query FATAL+close into `driver.ErrBadConn` (the
FATAL `ErrorResponse` is dropped once the socket EOFs in `simpleQuery`'s recv
loop → `handleError(io.EOF)` → `ErrBadConn`), so the server's FATAL text never
reaches `QueryContext`'s return. The runner reconstructs the canonical libpq
output: `isBackendTerminationError(err, sqlText)` returns true when the step SQL
contains `pg_terminate_backend` **and** the error is `driver.ErrBadConn`; in
that case `execOneStatement` returns `backendTerminationMessage` (the FATAL line
+ libpq's standard "server closed the connection unexpectedly" note, with a
trailing newline reproducing the blank separator). Gating on the step SQL keeps
an unrelated connection drop (a real crash) from being mislabelled as an
administrator termination.

## Blast radius

- `pg_terminate_backend`/`TerminateBackend` are new surface; nil callback in
  unit/embedded contexts. Peer termination reuses the existing connCtx-cancel
  teardown path (same mechanism as parent-ctx cancellation / admin shutdown).
- `cleanupSessionTempObjects` is a no-op for any session that never created a
  temporary object (`SessionTempTableNames` empty, `DropTempNamespace`
  unregistered owner). Owner token matches `executor.sessionTempOwner`.
- The harness FATAL synthesis is gated on the step SQL naming
  `pg_terminate_backend`, so other specs are unaffected.

## Oracle

Mirrors `src/backend/utils/adt/misc.c` `pg_terminate_backend` (SIGTERM →
`die()` → FATAL "terminating connection due to administrator command") and
`src/backend/utils/init/postinit.c` / `RemoveTempRelations` backend-exit cleanup
ordering. Connection-death text is libpq's `pqsecure_read`/`PQerrorMessage`
"server closed the connection unexpectedly" note. Compared against
`./postgres/local_install` PG 18.3 expected output.

## Gates

- `TestPort_IsolationTempSchemaCleanup` strict PASS (both permutations).
- Full dedicated `TestPort_Isolation*` advisory-lock / cancel / row-lock family
  PASS (no regression from the connection-teardown + temp-cleanup defer).
- `-race` `internal/executor` + `internal/server`.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

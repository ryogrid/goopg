# 0100-0005l — IsolationRunner: strip lib/pq `(SQLSTATE)` suffix from error output

**Status:** accepted (2026-05-15)

## Problem

`internal/testport/framework/isolation_runner.go::formatPQError` rendered every
spec error as:

```
ERROR:  insert or update on table "fk_parted_pk" violates foreign key constraint "fk_parted_pk_a_fkey" (23503)
```

The trailing ` (23503)` comes from lib/pq v1.12.3
`(*pq.Error).Error()` (`error.go:177-195`):

```go
if e.Code != "" {
    return "pq: " + msg + " (" + string(e.Code) + ")"
}
```

`formatPQError` stripped the `"pq: "` prefix but left the `(<code>)` suffix
intact. Upstream PostgreSQL isolationtester
(`postgres/src/test/isolation/isolationtester.c::printResultSet`) prints only
the `PG_DIAG_MESSAGE_PRIMARY` field — there is no `(SQLSTATE)` decoration.

`fk-snapshot.spec` L21 carries the canonical shape (no suffix):

```
ERROR:  insert or update on table "fk_parted_pk_2" violates foreign key constraint "fk_parted_pk_a_fkey"
```

Every spec that surfaces an FK or unique violation (fk-snapshot,
partition-key-update-2/3/4, the insert-conflict family's DEFERRED-FK
permutations) diverged at the error line with the SQLSTATE suffix masking
downstream diff investigation. Prior loops fixed structural issues (the
M0100-0005k constraint-name shape) but did not surface this trailing-suffix
parity gap — it only appears in lib/pq's stringified form, not in the wire
bytes that the server emits, so the gap is invisible to server-side
regressions.

## Fix

Detect `*pq.Error` in `formatPQError` and use `pqErr.Message` directly. The
non-pq fallback path (legacy `"pq: "` trim) is retained for harness-internal
errors (Scan failures, context cancellation) that may or may not carry a
`pq.Error`.

```go
func formatPQError(err error) string {
    if err == nil {
        return ""
    }
    if pqErr, ok := err.(*pq.Error); ok {
        return "ERROR:  " + pqErr.Message
    }
    msg := err.Error()
    if strings.HasPrefix(msg, "pq: ") {
        msg = strings.TrimPrefix(msg, "pq: ")
    }
    return "ERROR:  " + msg
}
```

## Why type assertion (and not `errors.As`)

`*sql.Conn.QueryContext` / `Rows.Scan` return the bare `*pq.Error` value
without wrapping (verified against the lib/pq driver code path — pq surfaces
its `*Error` through `(*conn).handleError` and `(*rows).Next` without using
`fmt.Errorf("%w", …)`). A type assertion is sufficient and avoids the
allocation cost `errors.As` incurs on every error from every spec step. The
regression test exercises `errors.As(error(pqErr), new(*pq.Error)) == true`
to document that future wrapping is also handled by the contract — if the
driver ever starts wrapping, the assertion can be widened without changing
the call sites.

## Why not normalize away the suffix in the diff comparator

The diff comparator is per-line; injecting a regex-strip step there would
hide a real bug if a future spec genuinely surfaces a `(SQLSTATE)`-suffixed
string in its expected output. Fixing the producer (formatPQError) keeps
producer/consumer contracts symmetric — the harness emits the same byte
sequence upstream's PQprint emits, and the comparator stays as a plain
string compare.

## Scope clarification

This fix is harness-only. The server-side `ErrorResponse` wire shape is
already correct (verified at
`internal/server/query.go:237::writeQueryError` — `FieldMessage` carries the
bare message, `FieldSQLState` carries the code on a separate field). The
fault was purely on the consumer side, in the way lib/pq's helper
`(*Error).Error()` re-assembles wire fields into a single Go string.

## Regression pins

- `TestFormatPQErrorStripsSQLStateSuffix` in
  `internal/testport/framework/isolation_test.go` — byte-equal pin against
  the upstream fk-snapshot.spec L21 form; documents the `errors.As` contract
  for future callers that wrap `*pq.Error`.
- `TestFormatPQErrorFallsBackOnNonPQ` — non-pq error path (with and without
  the legacy `"pq: "` prefix) plus the nil-error sentinel.

## Out of scope (follow-up)

- The partition-routed name in the MESSAGE for partitioned FK targets
  (`fk_parted_pk_2` vs the declaring `fk_parted_pk`) — separate slice; see
  the M0100-0005k design doc for the larger insertOp-side refactor.
- Concurrent-DELETE-on-FK-parent wait + 40001 serialize-error semantics
  (fk-snapshot L131-145) — a real EPQ/RR-isolation gap, not a formatter
  issue.
- Stripping the suffix from DETAIL / HINT lines if any spec ever surfaces
  them. Today the isolation runner only emits the MESSAGE line; DETAIL/HINT
  bytes never reach `formatPQError`, so the strip is unnecessary.

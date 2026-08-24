# M0134-0091 — `async.sql`: `pg_notification_queue_usage()` column type + `pg_notify()` channel-name validation

## Status: accepted (case now `pass` / `pass_required=yes`)

## Summary

`postgres/src/test/regress/sql/async.sql` sized live against the PG 18.3
oracle (`scripts/pg-regress-runner.sh --verbose async`) at a 35-line diff,
0% parity. LISTEN/NOTIFY itself (M0118-0009) was already fully implemented
and correct — the case's divergence was two narrow, independent gaps in the
`pg_notify`/`pg_notification_queue_usage` SQL-function surface:

1. `SELECT pg_notification_queue_usage();` rendered its `0` result
   left-justified with a single leading space instead of right-justified to
   the header's width — a **silent column-alignment divergence**, no error
   raised, easy to miss without a byte-for-byte oracle diff.
2. `pg_notify('', ...)`, `pg_notify(NULL, ...)` and an over-length channel
   name were all silently accepted instead of raising PG's
   `ERRCODE_INVALID_PARAMETER_VALUE` errors.

Both are now fixed; the case is 100% parity (`PASS async (35 lines)`).

## Root cause 1 — missing `exprType` arm for `pg_notification_queue_usage`

`internal/optimizer/planner.go`'s `exprType(*FuncCall)` is a hand-maintained
switch that advertises the wire type for builtin scalar functions whose
return type isn't otherwise derivable (no `ReturnType` populated at plan
time, since builtins aren't registered in `catalog.Routines` — that registry
only holds user-created routines). `random()`/`random_normal()`/`drandom()`
had their own `case` returning `float8`; `pg_notification_queue_usage()` had
none, so it fell through to the `default: return catalog.Type{Name:
"unknown"}` at the end of the switch.

psql's column-formatting rule (`printTableAddCell` / `PQfformat` in
`postgres/src/bin/psql/print.c` via the numeric-type check in
`fe-exec.c`/`print.c`) right-justifies a cell only when the reported type OID
is numeric; an `unknown`/text-shaped OID left-justifies. The runtime value
(`"0"`) was byte-identical between goopg and PG — only the column's
advertised *type*, and therefore its justification, diverged. This is the
same class of bug as PG's own float8 rendering: correctness of the value is
necessary but not sufficient — the wire-level type must match too, or psql's
client-side formatting silently disagrees with the oracle.

Fix: `internal/optimizer/planner.go`, `exprType`'s `*FuncCall` switch, add

```go
case "pg_notification_queue_usage":
    return catalog.Type{Name: "float8"}
```

right next to the existing `random`/`random_normal`/`drandom` arm.

## Root cause 2 — `pg_notify` had no channel-name validation

`internal/executor/expr.go`'s `case "pg_notify"` evaluated the channel and
payload arguments and called `ctx.QueueNotify` unconditionally — a NULL
channel was silently treated as a no-op (not even a NOTIFY was buffered),
and there was no length check at all.

Upstream (`postgres/src/backend/commands/async.c`):

- `pg_notify(PG_FUNCTION_ARGS)` (:556-576): a NULL channel argument is
  substituted with `""` (not skipped) before calling `Async_Notify`.
- `Async_Notify` (:604-621):
  - `channel_len == 0` → `ereport(ERROR, (errcode(ERRCODE_INVALID_PARAMETER_VALUE), errmsg("channel name cannot be empty")))`
  - `channel_len >= NAMEDATALEN` (64) → `errmsg("channel name too long")`
  - Neither `ereport` call includes `errposition()`, so PG attaches **no**
    cursor position to either error — the client sees a bare `ERROR: ...`
    line with no `LINE N: ...` / `^` pointer underneath it.

Fix: `internal/executor/expr.go`'s `case "pg_notify"` now:
- Treats a NULL channel the same as PG: substitutes `""` rather than
  early-returning.
- Raises `&ExecError{Code: "22023", Message: "channel name cannot be empty"}`
  for an empty (post-substitution) channel.
- Raises `&ExecError{Code: "22023", Message: "channel name too long"}` for a
  channel `len(channel) >= 64`.
- Leaves `Pos` unset (0) on both errors, matching the no-`errposition()`
  behavior — a first pass that did set `Pos: x.Pos()` reproduced PG's error
  *text* correctly but still diverged because goopg's LINE/^-pointer printer
  fires off any nonzero `Pos`, adding two lines PG never emits. This
  "no-`errposition()`-call → `Pos: 0`" pattern recurs across the M0134
  series (ledgered as M0134-0070's abs/gcd/lcm/mod/timestamp findings; see
  `internal/executor/abs_gcd_lcm_mod_error_position_test.go`).

`ERRCODE_INVALID_PARAMETER_VALUE` is SQLSTATE `22023`
(`postgres/src/backend/utils/errcodes.txt`).

## Testing

- `internal/executor/async_notify_test.go`:
  - `TestPgNotificationQueueUsageIsFloat8` — asserts the planner's `Output()`
    schema column type directly (not a round-trip through `pg_typeof`'s
    OID-rendering wire path, which returns an opaque `KindInt` datum that
    only resolves to a display name at wire-encoding time).
  - `TestPgNotifyChannelNameValidation` — empty, NULL, and over-length
    channel names each raise `22023` with the exact PG message and `Pos ==
    0`; a valid channel name does not error.
- `scripts/pg-regress-runner.sh --verbose async`: `PASS async (35 lines)`,
  100% parity (was 0%, 35-line diff).
- Full `RALPH_PRECOMMIT_SCOPE=units` gate: PASS.

## Scope not covered

Neither of these two fixes exposed any further bucket in this file — unlike
most of the M0134 series, `async.sql` needed no PARK; it closed clean. The
`NOTIFY channel` **statement** form's channel-name validation (as opposed to
the `pg_notify()` function form fixed here) was not audited in this pass —
if it shares the same missing-validation gap, it is a distinct code path
(`internal/executor/operators_ddl.go` DDL dispatch, not `expr.go`'s
`evalFuncCall`) and would need its own check; no divergence for it surfaced
in this case's expected output, so it is not ledgered as a confirmed gap,
only noted here for a future NOTIFY-focused pass.

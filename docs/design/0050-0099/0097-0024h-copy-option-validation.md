# 0097-0024h — PG-compatible COPY option validation (plan-time)

**Milestone:** M0097-0024 (Port COPY / sequence / identity regress tests)
**Test:** `copy2` ("incorrect options" block)
**Date:** 2026-05-25

## Problem

`copy2`'s "incorrect options" block exercises every COPY option-validation
error PostgreSQL raises. goopg diverged two ways:

1. **Wrong messages / wrong recognition set.** goopg's `validateCopyOptions`
   only knew `format`, `freeze`, `binary`, `csv`, `header`, the four
   string options, and the three `force_*` options. Anything else
   (`on_error`, `log_verbosity`, `reject_limit`, `convert_selectively`,
   `encoding`) hit the catch-all and produced
   `COPY option "x" is not supported` (0A000) instead of PG's
   per-option behaviour. Duplicate options produced
   `option "x" specified more than once` instead of PG's
   `conflicting or redundant options`.

2. **Desync — the load-bearing bug.** For option combinations goopg
   *accepted* but PG rejects (e.g. `COPY x FROM STDIN (format BINARY,
   delimiter ',')`), the planner produced a valid `Copy` node, the wire
   layer sent `CopyInResponse`, and the executor then tried to read the
   *following SQL statements as COPY data* — failing with a bogus
   `invalid signature byte 0` and desyncing the remaining ~780 lines of
   the test. The fix has to reject these **at plan time**, before
   copy-in mode is entered.

## Fix

Rewrote `validateCopyOptions` (`internal/planner/copy.go`) to mirror
PostgreSQL's `ProcessCopyOptions` (`src/backend/commands/copy.c`) as two
passes:

**Pass 1 — per-option loop.** Recognises the full PG option set. Each
option type tracks a "specified" flag; a second occurrence returns
`conflicting or redundant options` (caret on the redundant option, PG
`errorConflictingDefElem`). Value-bearing options validate inline, in
PG's order:

- `format` → `text` / `csv` / `binary`, else `COPY format "x" not recognized`.
- `on_error` → **direction check first** (`COPY ON_ERROR cannot be used
  with COPY TO` for `TO`), then `stop` / `ignore`, else
  `COPY ON_ERROR "x" not recognized`.
- `log_verbosity` → `silent` / `default` / `verbose`, else
  `COPY LOG_VERBOSITY "x" not recognized`.
- `reject_limit` → numeric and `> 0`, else
  `REJECT_LIMIT (n) must be greater than zero`.
- `force_*` → require `*` or a column list.

**Pass 2 — incompatible-combination checks**, in PG's exact order:
`BINARY`+`DELIMITER`/`NULL`/`HEADER`; `QUOTE`/`ESCAPE`/`FORCE_QUOTE`/
`FORCE_NOT_NULL`/`FORCE_NULL` require CSV; `FORCE_QUOTE` cannot be used
with `COPY FROM`; `FORCE_NOT_NULL`/`FORCE_NULL`/`FREEZE` cannot be used
with `COPY TO`; `BINARY` allows only `ON_ERROR STOP`; `REJECT_LIMIT`
requires `ON_ERROR`.

Ordering matters where a statement trips two checks: `on_error`'s
direction check sits in pass 1 so `COPY … TO … (format BINARY, on_error
unsupported)` reports the direction error, not the BINARY/value error —
matching PG.

### SQLSTATE vs. message

The regress harness sorts ERROR message text and strips `LINE:`/caret
detail lines (it keeps `CONTEXT:`), so only the message text is compared.
Codes are still set to the PG-faithful values (`42601`, `22023`, `0A000`)
for the planner unit tests.

### Options accepted but not yet executed

`on_error`, `log_verbosity`, `reject_limit`, `convert_selectively`, and
`encoding` now pass validation. The executor's CSV/text formatter
tolerates unknown options (no-op). `copy2` only uses these in error
cases, so accept-then-ignore introduces no new desync; full execution
semantics are out of scope here.

## Tests

- `TestPlanCopyIncorrectOptions` (`internal/planner/copy_test.go`) —
  pins all 33 PG-exact messages from the copy2 block plus 9 valid
  combinations that must still plan.
- `TestPlanCopyOptionsAcceptedAndRejected` — updated: `FORMAT bogus`
  → `22023`, unknown option → `42601`.

Verified live on port 5533 (all 14 representative messages byte-exact)
and via `GOOPG_REGRESS_DIFF_DIR`: the entire `copy2` option-validation
block now matches PG and no longer desyncs the rest of the file.

## Remaining copy2 gaps (out of scope)

`copy2` still defers on deeper features unrelated to option validation:

- `CONTEXT:  COPY x, line N, column c: "…"` detail lines on COPY data
  errors (goopg emits no COPY error CONTEXT).
- BEFORE triggers firing on `COPY FROM` (row rewriting, `before trigger
  fired`).
- Custom single-byte delimiter (`;`, `:`) data parsing.

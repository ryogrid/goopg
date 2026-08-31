# M0134-0001 P3b — `LINE n:` / `^` error-position context (cross-case)

**Status:** accepted — **LANDED 2026-08-15**
**Date:** 2026-08-15
**Task:** M0134-0001 P3b (the engine half of the P3 error-position gap) — but the
root cause is **cross-case**: every parse-analysis / planner error in *all* 189
M0134 regress-sql cases is missing PG's `LINE n:` / `^` caret context, not just
`aggregates.sql`. This note is the shared resume point for the whole M0134 family.

**Landed (2026-08-15):** `P` emitted on both protocol paths; 7 raise sites
re-pointed to the offending subexpression. Raw-verify vs `aggregates.out`: **23/23
common error carets byte-match PG**; the 3 wrong-token divergences are gone. The
two remaining `-LINE` pairs are the two deferrals below (22P02 type-coercion
location; the pre-existing GROUP-BY-via-USING "returns 0 rows" gap).

## Divergence observed

`scripts/pg-regress-runner.sh aggregates` → `tmp/regress-diffs/aggregates.diff`.
The residual error-position gap is two-directional:

**Direction A (bulk, ~25 lines) — goopg emits the bare `ERROR:`, PG adds position:**
```
ERROR:  aggregate function calls cannot be nested
-LINE 1: select max(min(unique1)) from tenk1;
-  ^
```
(`-` = PG expected, absent from goopg output.)

**Direction B (2 lines) — goopg already emits `LINE n:`/`^`, proving the wiring works:**
```
ERROR:  column ref f1/0 on nil slot
LINE 1: ...reate index minmaxtest3i on minmaxtest3(f1) where f1 is not ...
  ^
```
(These are P8 real engine bugs — PG succeeds where goopg errors — but they prove
the `P`-field plumbing exists and psql renders the caret from it.)

## Root cause

`LINE n:` / `^` / `...`-truncation is **not rendered server-side** — the PostgreSQL
libpq *client* draws it (`reportErrorPosition`, `fe-protocol3.c:1200-1430`, called
from `pqBuildErrorMessage3`). The server's only job is to emit the ErrorResponse
**`P` field** (`protocol.FieldPosition`, a 1-based offset into the query). goopg
already does this correctly on two of three error paths:

- parser `SyntaxError`: `syntaxErrorMsg` (`internal/server/copy.go:772-787`) emits
  `P = se.Pos+1` when `se.Pos >= 0`.
- executor `ExecError`: `execErrDetailFields` (`copy.go:827-847`) emits
  `P = ee.Pos+1` when `ee.Pos > 0`.
- **planner `PlanError`: dropped.** `planErrorFields` (`copy.go:792-798`) returns
  only `(code, message)` and `planErrorHintFields` (`copy.go:802-815`) appends only
  `Detail`/`Hint` — neither emits `FieldPosition`, even though `PlanError.Pos` is
  populated at 135 of 186 raise sites (e.g. `planner.go:7271/7283` nested-agg,
  `7643` FILTER, `7667` WITHIN GROUP, all from the `FuncCall.pos` AST location).

The extended protocol drops the position too: `dispatch_extended.go:114-115` builds
`extendedQueryError{Code, Message}` from `planErrorFields`, discarding
Detail/Hint/Position entirely (the executor analogue `newExtendedQueryError`,
`copy.go:861-876`, does carry them).

## Fix (two sites, no struct/threading change)

1. **Simple path** — `planErrorHintFields` (`internal/server/copy.go:802`): append
   `FieldPosition = pe.Pos+1` when `pe.Pos > 0` (guards the 51/186 sites that leave
   `Pos` unset = 0; mirrors `execErrDetailFields`). All five simple-path planner
   error sites (`copy.go:140,318`; `dispatch.go:986,2718,3628`) already funnel
   through `planErrorHintFields`, so one edit covers them all.
2. **Extended path** — `dispatch_extended.go:114-115`: build the `extendedQueryError`
   carrying Detail/Hint/Position from the `*planner.PlanError` (a
   `newPlannerExtendedQueryError` mirroring `newExtendedQueryError`), preserving the
   `FeatureNotSupported` fallback for non-`PlanError`s.

## PG oracle

- `postgres/src/backend/parser/parse_node.c:106-120` `parser_errposition` — converts
  a byte location to a **1-based character index** (`pg_mbstrlen_with_len + 1`) then
  `errposition`; no-op when `location < 0`.
- `postgres/src/backend/utils/error/elog.c:1468` `errposition` → `cursorpos`;
  emitted as `P` at `elog.c:3622-3627`.
- `postgres/src/interfaces/libpq/fe-protocol3.c:1200-1430` `reportErrorPosition` —
  the client-side formatter: `LINE %d: ` + optional leading `...` + line bytes +
  optional trailing `...` + `\n`, then spaces + a single `^` + `\n`. Single `^`
  always (no `^...^` span). `DISPLAY_SIZE=60` screen cols, `MIN_RIGHT_CUT=10`;
  tabs→spaces, multibyte-aware.
- Raise sites for the same errors: `parse_agg.c:690-693` (nested agg, `aggloc`),
  `611-615` (FILTER), `parse_func.c:510-515` (ordered-set).

## Known limitation / deferral

- **Byte vs character index (deferral):** goopg emits `Pos+1` (a byte offset); PG
  sends a character index. On multibyte query text the caret drifts. The faithful
  fix needs the query string at emission time (currently only the offset travels).
  The entire regress corpus is ASCII, so this is latent — see deferral ledger.
- **`Pos = 0` unset convention:** 51/186 planner sites leave `Pos` unset (=0); the
  `> 0` guard (matching `ExecError`) suppresses those rather than spuriously pointing
  at char 1. A genuine error-at-byte-0 would lose its caret; accepted, matching the
  existing executor convention.
- goopg appends extras after `R`; PG emits `P` before file/line/routine — clients
  treat field order as irrelevant, no action.

## Gate

`scripts/pg-regress-runner.sh aggregates` — the 25 `-LINE n:`/`-  ^` lines in
`tmp/regress-diffs/aggregates.diff` drop to 0 (Direction B's two `+LINE` lines are
P8 engine bugs and remain). Cross-check a second case (e.g. `strings.sql` or
`select_having.sql`) to confirm the shared gain. On the aggregates case passing
fully, flip its CSV row per M0134 discipline.

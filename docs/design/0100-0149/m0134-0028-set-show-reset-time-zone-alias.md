# M0134-0028 — `SET`/`SHOW`/`RESET TIME ZONE` two-word GUC alias

Status: accepted
Task: M0134-0028 (`horology.sql`)
Related: `docs/design/m0134-0026-timestamptz-literal-session-timezone.md` (prior loop)

## Summary

PostgreSQL's grammar treats `TIME ZONE` as a dedicated two-word alias for the
`timezone` GUC in all three of `SET`, `SHOW`, and `RESET` — a separate
grammar production, not a name looked up through the ordinary
`SET name = value` / `SHOW name` / `RESET name` path. goopg's parser has no
such carve-out: `SET TIME ZONE 'America/New_York'`, `SHOW TIME ZONE`, and
`RESET TIME ZONE` all fall through to `parseGUCName`, which greedily consumes
just the bare ident `TIME` as the GUC name and then chokes on the trailing
`ZONE` token, raising a syntax error. Every regress file that uses the
canonical PG spelling of this statement (not the equivalent
`SET timezone = ...`) fails outright, and any assertion depending on the zone
actually having changed cascades further mismatches downstream (the `SET`
silently errors, so the session zone never moves).

This is not a `horology.sql`-local bug — it is an engine-wide parser gap that
`horology.sql` happens to exercise repeatedly (multiple `SET TIME ZONE`
blocks across floating-point, named-zone, and fixed-GMT-offset zone tests).

## PostgreSQL's rule (the oracle)

`postgres/src/backend/parser/gram.y`:

- line 1709, inside `set_rest`: `| TIME ZONE zone_value` — `SET TIME ZONE
  <value>` sets `VariableSetStmt` with `name = "timezone"`.
- line 1904, inside `generic_reset`: `| TIME ZONE` — `RESET TIME ZONE` resets
  the `timezone` GUC.
- line 1974, inside `VariableShowStmt`: `| SHOW TIME ZONE` — `SHOW TIME ZONE`
  shows the `timezone` GUC.

`TIME` and `ZONE` are both unreserved-category keywords in PG's grammar (as
they are in goopg's lexer — plain idents, not `Kw*` tokens), so this is a
pure two-token lookahead carve-out, the same shape as the `SET ROLE` /
`SET SESSION AUTHORIZATION` / `RESET SESSION AUTHORIZATION` special-cases
already present in `internal/parser/parser.go`.

## Fix

Three sibling call sites in `internal/parser/parser.go`, each gaining a
`TIME ZONE` intercept immediately before falling through to the generic
`parseGUCName` path (mirroring the existing `role`/`session
authorization`/`constraints` intercepts already in the same functions):

1. `parseSet` (~parser.go:2280) — after the existing `SET ROLE` /
   `SET [LOCAL] SESSION AUTHORIZATION` / `SET [LOCAL] TRANSACTION` / `SET
   CONSTRAINTS` intercepts and before the fallthrough `parseGUCName` call:
   if the next two tokens are the idents `time` then `zone` (both consumed
   via `acceptIdentKeyword`), set `s.Name = "timezone"` and parse the
   zone value the same way the generic path does — `DEFAULT` sets
   `s.Default = true`; otherwise a `zone_value` (a string literal, a numeric
   literal for `SET TIME ZONE <float>`/`INTERVAL <literal> [HOUR [TO
   MINUTE]]`, or `LOCAL`/`DEFAULT`) is captured via the existing
   `parseSetValue`/`parseSetValueAtoms` machinery (numeric and `INTERVAL
   '...' HOUR TO MINUTE` zone forms already tokenize as ordinary value atoms
   there — no new atom kind needed).
2. `parseShow` (~parser.go:2266) — before `parseGUCName`: `TIME ZONE` →
   `ShowStmt{Name: "timezone"}`.
3. `parseReset` (~parser.go:2829) — before `parseGUCName`, alongside the
   existing `RESET SESSION AUTHORIZATION` intercept: `TIME ZONE` →
   `ResetStmt{Name: "timezone"}`.

No executor change is needed — `SetStmt`/`ShowStmt`/`ResetStmt` with
`Name == "timezone"` already routes through the ordinary GUC machinery today
(`SET timezone = '...'` already works); this slice is parser-only.

## Scope boundary (what this does NOT fix)

Sizing for M0134-0028 found `horology.sql`'s diff dominated by two unrelated,
larger issues, deliberately left out of this slice:

- **~73% of the diff is a harness gap**, not a goopg bug:
  `scripts/pg-regress-runner.sh` runs each `.sql` file in isolation, but real
  `pg_regress` (per `parallel_schedule`) runs `horology.sql` in a session that
  already ran `timestamp.sql`/`timestamptz.sql`/`time.sql`/`timetz.sql`/
  `interval.sql`/`date.sql`, which create and populate `TIMESTAMP_TBL` /
  `TIMESTAMPTZ_TBL` / `TIME_TBL` / `TIMETZ_TBL` / `INTERVAL_TBL` /
  `DATE_TBL`. Every reference to those tables in `horology.sql` fails with
  `relation "..._tbl" does not exist` in the single-file runner. Recorded as
  its own deferral row (harness gap, same family as the M0134-0026 env-export
  finding but a different mechanism — schedule-grouping, not env vars); not
  fixed here per the "don't smuggle a harness fix into a case fix" rule.
- **`to_timestamp()`/`to_date()` format-string parsing is REFACTOR-tier.**
  `internal/executor/expr.go:13439` `evalToTimestamp` translates PG format
  codes to a Go `time.Layout` and calls `time.Parse` — a deliberately scoped
  stand-in, per its own doc comment, that does not implement real upstream
  parity (locale-aware month names, `FM`/`FX` modifiers, ISO-week fields,
  fractional-second precision, ambiguous-field-order errors). PG's real
  implementation is a stateful per-token scanner
  (`postgres/src/backend/utils/adt/formatting.c`, `DCH_from_char`/
  `do_to_timestamp`). A from-scratch token parser is required; deferred with
  a re-arm trigger.
- Fixed-GMT-offset timezone abbreviation formatting in `to_char(..., 'TZ')`
  output (e.g. `<-01:30>+01:30` style) was flagged as possibly a second,
  smaller gap layered on top of the alias-parsing bug — not independently
  confirmed; re-diff after this fix lands before sizing it.

Both bullets are ledgered in `.ralph/deferral_ledger.md` (2026-08-20,
M0134-0028 rows).

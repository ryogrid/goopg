# M0134-0125 — `geometry.sql` sizing (PARKED)

**Date:** 2026-08-24
**Status:** sizing only, no engine change landed this loop — PARKED per the
established M0134 pattern (cf. M0134-0109..-0124).

## Result

`scripts/pg-regress-runner.sh geometry` against PG 18.3: **0/1, 0% parity,
5623-line diff** (531-line source, 5322-line expected output) — the largest
M0134 case sized to date (previous largest was `foreign_data.sql` at ~2460
lines).

## Root cause: this file is not self-contained

Unlike every other M0134 case sized so far, `geometry.sql`'s failures are
**not this file's own gap** — PG's regress schedule runs `geometry.sql` in
the same parallel group as, and *after*, six sibling files that each
`CREATE TABLE` one geometric-type fixture:

| sibling file | table it creates | M0134 status |
|---|---|---|
| `point.sql` | (uses `POINT_TBL`, created in `test_setup.sql`) | not-tried |
| `box.sql` | `BOX_TBL` | `failed`, **sized live M0134-0094** (box_in parsing landed) |
| `lseg.sql` | `LSEG_TBL` | not-tried |
| `line.sql` | `LINE_TBL` | not-tried |
| `path.sql` | `PATH_TBL` | not-tried |
| `polygon.sql` | `POLYGON_TBL` | not-tried |
| `circle.sql` | `CIRCLE_TBL` | `failed`, **sized live M0134-0098** (circle_in parsing landed) |

`scripts/pg-regress-runner.sh` runs regress files individually (one
`setup` pass + one target file), so when `geometry.sql` runs alone none of
`BOX_TBL`/`CIRCLE_TBL`/`LINE_TBL`/`LSEG_TBL`/`PATH_TBL`/`POLYGON_TBL` exist —
of the 166 `ERROR:` lines in the diff, 85 (51%) are bare
`relation "..._tbl" does not exist`. This is a **test-harness setup gap**,
not a `geometry.sql`-specific engine gap: even a goopg with full geometric
operator support would still fail this file when run standalone through the
current runner, because the runner has no notion of "run this file's sibling
group first."

## The remaining ~50% of errors: known, already-tracked type-system gaps

The other half of the diff is real engine gaps, but every one of them is a
symptom of the **same underlying gap already being chipped away at by
`box.sql` (M0134-0094) and `circle.sql` (M0134-0098)**: goopg's geometric
type family (`point`, `lseg`, `line`, `box`, `path`, `polygon`, `circle`) has
no typed-literal parsing, no operator lexing, and (for `point`/`lseg`/
`line`/`path`/`polygon` — unlike `box`/`circle`, which already got
`box_in`/`circle_in`-faithful parsing) no `*_in`-faithful value parsing at
all (`internal/executor/codec.go`'s catch-all "Unknown type (e.g. \"point\",
\"path\", custom types)" raw-varlena pass-through, same shape `box`/`circle`
were in before M0134-0094/-0098).

Error breakdown (166 total `ERROR:` lines):

- 85 — `relation "..._tbl" does not exist` (cross-file setup dependency, see
  above)
- 14 — `syntax error ... (got ->)` — no lexer support for the point/box
  distance-adjacent `->` construct used in this file's `dist_ppath`/point
  extraction expressions
- 7 — `syntax error ... (got #)` — `#` (number-of-points / intersection)
  operator not lexed
- 6 — `syntax error ... (got @)` — `@@` (center) operator not lexed
- 5 — `syntax error ... (got =)`, 4 — `(got <)`, 2 — `(got >)`, 2 —
  `(got |)`, 2 — `(got ^)`, 2 — `(got >>)`, 2 — `(got &)` — the geometric
  comparison/containment operator family (`<<`, `>>`, `&<`, `&>`, `<->`,
  `<@`, `@>`, `?#`, `?-`, `?|`, `~=`, etc.) is almost entirely unlexed; only
  a handful of single-char operators overlap with already-lexed arithmetic
  tokens
- 5 + 4 — `lex error at byte N: unexpected character '?'` — the `?-`
  (is-horizontal) / `?|` (is-vertical) operators aren't even single tokens
  in the lexer, so PG's dedicated "is horizontal/vertical" operator forms
  (as opposed to the `ishorizontal()`/`isvertical()` *functions*, which are
  catalog-registered in `pg_proc_seed_data.go` as OID 1406/1407/1410/1411/
  1414/1415 but have **no goopg handler implementation** behind
  `point_horiz`/`point_vert`/`lseg_vertical`/`lseg_horizontal`/
  `line_vertical`/`line_horizontal` — catalog metadata only, same
  "registered but not wired" shape flagged before) never parse
- 3 — `division by zero`, 2 — `value out of range: overflow` — real
  numeric-edge-case divergences in whatever partial geometric arithmetic
  does execute (deprioritized: only visible once the above unblocks)

## Why this is PARKED with no fix landed this loop

Every prior M0134 park case (0109..0124) landed at least one independent,
narrowly-scoped fix alongside the sizing. This file has no such fix
available: unlike those cases' authz/validation gaps (a few lines each),
`geometry.sql`'s blockers are either (a) a test-harness limitation outside
this file's own scope, or (b) the SAME large, already-in-flight type-system
gap that `box.sql`/`circle.sql` are each individually chipping away at as
their own dedicated M0134 tasks. Landing a fragment of "point type literal
parsing" here would duplicate, rather than extend, that in-flight work
without a clear boundary — the right unit of work is `point.sql`,
`lseg.sql`, `line.sql`, `path.sql`, `polygon.sql` each getting their own
M0134-task-sized `*_in`-faithful parsing pass (mirroring M0134-0094/-0098),
after which `geometry.sql` should be re-sized (its 0% parity will likely
jump substantially once those land — even without the harness fix, since
the aggregate operator-lexing work is shared).

## Resume points, priority order

1. **Highest leverage: promote `point.sql`, `lseg.sql`, `line.sql`,
   `path.sql`, `polygon.sql` to their own M0134 tasks**, each following the
   box.sql/circle.sql template (`parseBoxLiteral`/`parseCircleLiteral` in
   `internal/executor/codec.go`, wired into column-assignment coercion,
   typed-literal syntax, and `pg_input_is_valid`). `point` is the highest-
   value single pickup since `POINT_TBL` is used pervasively across many
   other regress files (not just geometry-family ones) and its typed-literal
   syntax (`point '(x,y)'`) doesn't parse at all today — confirmed live via
   `WHERE ishorizontal(p1.f1, point '(0,0)')` raising a bare parser syntax
   error, not a semantic one.
2. **Geometric operator lexer family** — `?-`, `?|`, `?#`, `@@`, `#`, `<->`,
   `<@`, `@>`, `~=`, `&<`, `&>` need lexer tokens (`internal/parser/token.go`
   + `internal/parser/lexer.go`, same shape as the bit-string-literal work
   in M0134-0092) before any operator-form geometric query parses. Cross-cuts
   `box.sql`/`circle.sql`/`polygon.sql`/`lseg.sql`/`line.sql`/`path.sql`, not
   scoped to `geometry.sql` alone.
3. **Function-handler wiring for already-catalog-registered geometric
   procs** — `pg_proc_seed_data.go` already has OIDs for `point_horiz`,
   `point_vert`, `point_slope`, `lseg_vertical`, `lseg_horizontal`,
   `line_vertical`, `line_horizontal`, etc., but none have an executor
   dispatch handler (same "registered but not wired" gap pattern noted for
   FDW/event-trigger objects). Grep `HandlerName: "point_` /
   `"lseg_` / `"line_` in `internal/initdb/pg_proc_seed_data.go` for the
   full registered-but-unimplemented list.
4. **Test-runner harness gap (lower priority — orthogonal to goopg
   engine work)**: `scripts/pg-regress-runner.sh` has no notion of a
   parallel-schedule group's sibling setup files; `geometry.sql` (and any
   other schedule-grouped file with cross-file table dependencies) will
   always show inflated 0%-parity numbers until the runner either (a) runs
   an entire parallel group's files in one session before diffing the target
   file, or (b) `geometry.sql`'s sizing is understood to include "run
   `point.sql`/`box.sql`/`lseg.sql`/`line.sql`/`path.sql`/`polygon.sql`/
   `circle.sql` first" as an implicit precondition, as documented here.

## Not fixed this loop

No engine or parser change landed — this is a sizing-only park, matching
the "no independent isolable fix available" case explicitly allowed by the
M0134 workflow when every candidate fix either falls outside the file's own
scope or duplicates another task's in-flight work.

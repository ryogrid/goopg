# M0134-0173 — range type input, canonicalization and the built-in range constructors

*Landed 2026-08-29. Task: M0134-0173 (`stats_import.sql`). Design + implementation
in the same loop.*

## Why this exists

`stats_import.sql` was selected per the M0134 task order and sized live for the
first time (`not-tried` → `failed`, 1461 diff lines / 74 `^+ERROR`). Its residual
is REFACTOR-tier and is parked (see "What is parked" below). What the case
*exposed* — and what this loop shipped — is an engine-wide correctness gap it
merely points at with a single statement:

```sql
SELECT 1, 'one', …, int4range(-1,1), array['blue','yellow']
-- ERROR:  function int4range does not exist
```

Probing that led to the real finding: **goopg treated every range-typed value as
opaque, unvalidated text.**

```
goopg (before)                          PostgreSQL 18.3
------------------------------------    ------------------------------------
'garbage'::int4range   → garbage        ERROR: malformed range literal
'[5,1)'::int4range     → [5,1)          ERROR: range lower bound must be …
'[1,4]'::int4range     → [1,4]          [1,5)
'[1,4]'::int4range
   = '[1,5)'::int4range → f             t
int4range(1,4)         → 42883          [1,4)
```

The third and fourth rows are the serious ones. They are not a message gap: PG
normalises every **discrete** range (int4range, int8range, daterange) to `[)`
through the type's `rngcanonical` procedure, and *that normalisation is what makes
two spellings of the same value compare equal*. Without it every equality test,
`ORDER BY`, btree index probe and exclusion-constraint check over a range column
compared the raw literal spelling the user happened to type. A row inserted as
`[1,4]` and probed with `[1,5)` silently did not match.

This is the fourth instance of the recurring **"missing `evalCast` arm =
unvalidated text"** pattern (`xid`, `circle`, `float8` were the first three,
M0134-0166 being the most recent), and the second instance this month of
**"the catalog advertises a function the executor never implemented"**
(M0134-0167 was the first: `IndexAMCapability` could *report* that spgist cannot
do unique indexes while `execCreateIndex` happily created one). goopg's `pg_proc`
seed has carried all twelve `range_constructor2`/`range_constructor3` rows —
OIDs 3840/3841 (int4range), 3844/3845 (numrange), 3933/3934 (tsrange),
3937/3938 (tstzrange), 3941/3942 (daterange), 3945/3946 (int8range) — since the
range-type catalog work, so `pg_proc` resolved the name while `evalFuncCall`
had no case for it.

## Upstream model

`postgres/src/backend/utils/adt/rangetypes.c`:

| upstream | role |
|---|---|
| `range_in` (:90) | parse → subtype input per bound → `make_range` |
| `range_out` (:138) | deserialize → subtype output per bound → `range_deparse` |
| `range_parse` (:2386) | literal → flag bits + two RAW bound strings |
| `range_parse_bound` (:2492) | de-quote one bound; empty input means "no bound" |
| `range_serialize` (:1791) | validation: lower > upper is 22000; equal-and-not-both-inclusive collapses to `empty`; an infinite bound is never inclusive |
| `make_range` (:2016) | `range_serialize` + the type's `rngcanonical` |
| `range_deparse` (:2571) / `range_bound_escape` (:2601) | re-render, quoting a bound that is empty or contains `" \ ( ) [ ] ,` or whitespace |
| `int4range_canonical` / `int8range_canonical` / `daterange_canonical` | normalise to `[)`, raising at the subtype's maximum rather than wrapping |

Flag bits are `rangetypes.h:38-42`.

## What goopg does now

New file `internal/executor/rangetypes.go` is a faithful port of the pipeline
above. Two deliberate modelling decisions:

1. **The value model stays PG's TEXT rendering, not a new `Datum` kind.** goopg
   carries range values as `KindString` everywhere — storage, wire, comparison.
   The invariant this file establishes is therefore *the stored string is always
   the canonical `range_out` spelling*, which is exactly what makes goopg's text
   comparison agree with PG's `range_eq` for the canonicalizing types. A native
   range Datum with the operator family is a separate, larger slice (ledger row
   0173a).

2. **The subtype's own input/output functions do the bound work.**
   `rangeBoundIn` is `evalCast(<bound text>, subtype)` and `rangeBoundOut` is
   `evalCast(<value>, "text")` — goopg's spelling of "the type's I/O functions".
   That is why `'[a,4)'::int4range` raises int4in's
   `invalid input syntax for type integer: "a"`, byte-identical to PG, with no
   range-specific error text.

   `rangeBoundOut` carries one non-obvious guard. `evalCast`'s `"text"` arm
   converts only the kinds that need a session GUC to render (`KindTime` needs
   DateStyle/TimeZone, `KindBytes` needs `bytea_output`) and returns every other
   kind **unchanged** — a `KindNumeric` comes back as itself, not as a string, so
   `StringValue()` on it yields `""`. This was caught by the oracle A/B, not by
   reading: `numrange(1.0,4.0)` rendered `["","")` on the first cut. The fix
   falls back to `Datum.Format` whenever the cast did not actually produce a
   string.

Two wiring points, both revert-checked by the guard test:

* `evalCast` (`internal/executor/expr.go`) — a range type name is now recognised
  immediately before the `return d, nil // pass-through for unknown types` line
  it used to fall through. `evalCastTyped` delegates to `evalCast`, so one hook
  covers both (sibling-paths check performed).
* `evalFuncCall` (`internal/executor/expr.go`) — the six constructor names.
  The case sits inside the switch that runs *after* the `pg_catalog.` strip, so
  `pg_catalog.daterange(...)` (which is how `stats_import.sql` spells everything)
  reaches the same code.

`exprType` (`internal/optimizer/planner.go`) additionally learns that each
constructor returns its own range type, so `pg_typeof(int4range(1,4))` no longer
answers `unknown`.

**User-defined range types** (`CREATE TYPE … AS RANGE`) share the whole input
pipeline via `catalog.LookupRangeType`, so `'garbage'::myrange` now raises too.
They are deliberately never canonicalized: goopg writes `pg_range.rngcanonical =
0` for them (`internal/executor/sys_pg_range.go`), and applying one here would
diverge from what the catalog advertises. They do **not** get an auto-created
constructor function — upstream's `DefineRange`/`makeRangeConstructors` does that
and goopg has no equivalent (ledger row 0173b).

## Verification

* **Oracle A/B.** A 43-statement fixture was run against a throwaway PostgreSQL
  18.3 (`initdb` + `pg_ctl`, port 5542) and against goopg. Every VALUE and every
  error message/DETAIL/HINT now matches. The residual diff is three known,
  pre-existing goopg conventions, all ledgered: the caret column for a cast error
  (goopg points at the end of the literal, PG at the start), an extra `LINE`/caret
  on function-level errors where PG emits none, and `pg_typeof`/wire type
  reporting `text` for a range — which goopg already did for a *declared*
  `int4range` column before this change, so no new inconsistency was introduced.

* **14-case regress A/B** against a HEAD worktree:

  | case | before | after |
  |---|---|---|
  | `rangetypes` | 2543 lines / 234 `^+ERROR` / 30 `^-ERROR` | **2166 / 182 / 20** |
  | `multirangetypes` | 4252 | **4235** |
  | `stats_import` | 1461 / 74 | **1457 / 73** |
  | `plpgsql` | 4407 | 4412 (see below) |
  | `create_index` | 3335 | 3335 (delta is a pre-existing Go pointer address leaking into `pg_get_indexdef`, nondeterministic run to run) |
  | the other 9 | — | **byte-identical** |

  `plpgsql`'s +5 is the expected shape of progress, verified line by line: every
  changed line is inside the file's polymorphic `f1(anyrange, …)` block, where
  `int4range(42,49)` used to abort the statement with `function int4range does
  not exist` and now evaluates. The statement upstream labels `-- range type
  doesn't fit` still diverges — it now succeeds where PG rejects it — but it
  diverged before too, and for a worse reason; the underlying gap is goopg's
  polymorphic overload resolution, not this change. No statement went from
  matching to diverging.

* **Guard.** `TestRangeTypeInputAndConstructors`
  (`internal/executor/rangetypes_test.go`), 8 subtests, every expectation
  captured from the live 18.3 oracle. Revert-checked twice: deleting the
  `evalCast` arm fails `cast path is wired to rangeIn`; deleting the
  `evalFuncCall` case fails `evalFuncCall dispatches the constructors`.

## What is parked (`stats_import.sql`)

The case's remaining 1457 lines are ~100 % the PG 18 **statistics import**
function family — `pg_restore_relation_stats`, `pg_restore_attribute_stats`,
`pg_clear_relation_stats`, `pg_clear_attribute_stats` (55 of the 73 `^+ERROR`s)
— plus the absence of a `pg_statistic` relation in goopg's catalog (5 more).
Those are a subsystem, not a fix: the restore functions are variadic
name/value-pair functions that write `pg_class` stats columns and synthesise
`pg_statistic` rows with per-`stakind` slot layout. Resume points are ledger rows
0173c (the restore/clear function family) and 0173d (`pg_statistic` as a
queryable relation). The CSV row moves `not-tried` → `failed`.

## Related

* `docs/design/m0134-0166-float8in-shared-input.md` — the previous
  "missing evalCast arm = unvalidated text" instance.
* `docs/design/m0134-0167-index-am-capability-gate.md` — the previous
  "catalog advertises what the executor never implemented" instance.

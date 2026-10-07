# M0146-0087 — numeric(p,s) columns apply their typmod on write

Status: done 2026-10-07 (`7e1d110da`).

## Symptom

```sql
CREATE TABLE nt (n numeric(6,2), m numeric(4,1));
INSERT INTO nt VALUES (2.5, 1.26), ('3', 7);
SELECT * FROM nt;   -- goopg: 2.5|1.26, 3|7     PG: 2.50|1.3, 3.00|7.0
UPDATE nt SET n = 1.234;                     -- goopg stored 1.234, PG 1.23
INSERT INTO nt (m) VALUES (999.95);          -- PG: 22003 numeric field overflow
```

Every write path stored the value as given:

- INSERT (VALUES, SELECT, DEFAULT), UPDATE, MERGE, ON CONFLICT and COPY
  FROM;
- ALTER COLUMN TYPE between typmods of the same type name. That case was
  a no-op that kept even the old column definition.

`numeric field overflow` did not exist anywhere in goopg. An explicit
`::numeric(6,2)` cast was right.

## PG

`apply_typmod` (numeric.c) runs whenever a value is coerced to a
`numeric(p,s)` target. That covers the INSERT/UPDATE target list, column
defaults (`build_column_default`), COPY FROM's input function, and the
ALTER TYPE rewrite. It does three things:

- `round_var(var, s)` rounds half away from zero; a negative scale rounds
  to tens or hundreds.
- The display scale is `max(s, 0)`.
- After rounding, a value with more than `p − s` integer digits raises
  22003 `numeric field overflow`, with DETAIL `A field with precision p,
  scale s must round to an absolute value less than 10^(p-s)` (`1` when
  `p = s`). Zero never overflows.

## Change

- **`numeric_typmod.go`.**
  - `applyNumericTypmod` ports `apply_typmod`. It has an int64 fast path
    for values already in range, and converts int and string datums
    first.
  - `numericRoundedMantissa` does the exact big.Int rounding.
  - `roundNumericExact` implements numeric_round.
  - `numericColumnTypmod` reads `Type.Args`; `numeric(p)` means scale 0.
- **`coerceRowForConstraintChecks`.** The numeric arm applies the typmod
  after its cast. INSERT and every UPDATE site already call it. COPY FROM
  now admits `numeric(p,s)` columns as well as reg*.
- **Defaults.** `applyDefaultNumericTypmods` covers DEFAULT-filled columns,
  which that pass skips, in INSERT and MERGE INSERT.
- **Paths that never coerced.**
  - MERGE UPDATE SET (four new-row sites): SET columns are coerced before
    generated columns.
  - INSERT … ON CONFLICT: the insert row and the DO UPDATE SET columns
    are coerced, as plain INSERT/UPDATE do.
- **ALTER COLUMN TYPE.** It counts as a no-op only when the typmod is
  unchanged too (`int64SlicesEqual`), and the rewrite applies the new
  typmod.
- **`round(numeric, s)`** used float64 arithmetic: `round(-4.31, 40)`
  gave `-4.3099999999999996…`. It now goes through `roundNumericExact`.
  Regress `numeric` exposed it: it compares the (now correctly rounded)
  stored results against `round(expected, 40)`.

## Verification

- `TestNumericColumnTypmodAppliedOnWrite` covers:
  - INSERT VALUES / SELECT / DEFAULT;
  - UPDATE, ON CONFLICT DO UPDATE, MERGE INSERT/UPDATE;
  - three overflow errors;
  - ALTER TYPE;
  - apply_typmod edge cases (zero, `numeric(2,2)`, rounding that adds a
    digit, ints, strings).

  Every want is PG 18.3's.
- Server probes over every write path, the overflow errors, `numeric(30,10)`
  with a large value, ALTER TYPE and `round()` all match PG.
- Regress A/B over 8 files:
  - numeric 3806 → 3690;
  - alter_table, insert, update, copy2, merge, insert_conflict and domain
    are identical.
- Gates: units, tpch-spotcheck, arm 24/24 (values match), sf025 96/96
  (plan shapes 99/99), ea-ratchet PASS.

## Not covered (ledgered)

- `numeric(p, -s)` (negative scale) is a grammar syntax error;
  `applyNumericTypmod` already handles a negative scale.
- NaN / ±Infinity into `numeric(p,s)` (`apply_typmod_special`: Infinity
  overflows) — goopg does not model numeric specials.
- Array elements (`numeric(5,2)[]`) and domains over `numeric(p,s)` do not
  get the typmod.
- `round(float8)` uses half-away-from-zero; PG's `rint` rounds half to
  even (`round(2.5::float8)` = 2).
- ALTER COLUMN TYPE does not re-sync `pg_attribute` (`format_type` keeps
  the old type, even for int → bigint).
- A same-name typmod change always rewrites the table. PG skips the
  rewrite when the change is binary-coercible, e.g. widening a varchar.

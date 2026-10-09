# M0146-0083 — constraint-violation DETAILs use each type's output function

Status: done 2026-10-07 (`1e4427fab`).

## Symptom

```sql
CREATE TABLE dz (a int NOT NULL, d date DEFAULT '2020-01-02');
INSERT INTO dz (d) VALUES (DEFAULT);
-- goopg: DETAIL:  Failing row contains (null, 01-02-2020).
-- PG:    DETAIL:  Failing row contains (null, 2020-01-02).
```

Other values were wrong too:

- a timestamp printed six fraction digits (`03:04:05.500000`);
- a time printed a 1970-01-01 date;
- an interval DEFAULT still showed its input text (`1 day 2 hours`);
- under `SET datestyle = 'SQL, DMY'` PG prints `02/01/2020`, which goopg
  ignored.

The unique (23505), exclusion (23P01) and FK (23503) DETAILs had the same
problem.

## PG

Every DETAIL value goes through its type's output function
(`OidOutputFunctionCall`) under the session GUCs:

- ExecBuildSlotValueDescription (NOT NULL, CHECK, partition routing);
- BuildIndexValueDescription (unique, exclusion);
- ri_ReportViolation (FK, using the reporting relation's column types).

## Change

- **`detailValueText(ctx, type, datum)`** (operators_storage.go) wraps
  `datumToCopyText`, COPY TO's typed output path. It uses the session
  DateStyle, TimeZone and bytea_output.
  - A string datum in a non-text column first goes through the type's
    input function. A row rejected before storage can still hold a DEFAULT
    as text, and PG has already converted it by then.
  - On an error it falls back to `Datum.Format`.
- **Callers.**
  - `formatRowForDetail(ctx, …)`: NOT NULL, CHECK, partition routing,
    COPY FROM.
  - `buildUniqueConstraintDetail` and `nndDetail`: 23505.
  - `buildExclusionConstraintDetail`: 23P01.
  - `fkValsForDetail(ctx, table, columns, vals)`: 23503. It looks up each
    key column's type in the table; a missing column keeps the old
    DateStyle-aware time rendering.
- **Lazy unique DETAIL.** `uniqueCheckWithWait` takes the DETAIL as a
  func and renders it only when a conflict is raised. It used to be built
  for every insert into a unique index, which the typed path would have
  made costly.
- **COPY TO interval.** `datumToCopyText` had no `KindInterval` arm, so
  COPY TO of any interval column failed `kind 6 cannot encode as interval
  in COPY TEXT`. It now emits interval_out (the `postgres` IntervalStyle).

## Verification

- `TestConstraintDetailUsesTypeOutput` covers:
  - NOT NULL with date, timestamp, time, interval and bool defaults;
  - CHECK, unique and FK DETAILs;
  - DateStyle `ISO, MDY` and `SQL, DMY`;
  - the interval COPY arm.

  Every want is PG 18.3's.
- A server probe is identical to PG under the same TimeZone, except
  numeric scale (below). COPY TO / CSV of interval columns matches PG.
- Regress A/B over 8 files is identical, apart from two reordered `\d`
  lines in foreign_key. pg_regress runs with `Postgres, MDY`, where the
  old rendering coincided.
- Gates: units, tpch-spotcheck, arm 24/24, sf025 96/96 (plan shapes
  99/99; Q39 is bimodal), ea-ratchet PASS.

## Not covered

- `numeric(p,s)` columns do not apply their typmod on INSERT or UPDATE,
  so a DETAIL shows `1.5` where PG shows `1.50`. SELECT is wrong the same
  way. Filed as M0146-0087.
- `btreeBuildKeyDescription` (CREATE UNIQUE INDEX "is duplicated") still
  uses `Datum.Format`: the description is captured per row during the
  build, and it has no Context (ledgered).
- The FK "still referenced" DETAIL renders with the referencing column's
  type; PG uses the referenced relation's. They differ only when the two
  column types render differently (ledgered).
- IntervalStyle is not consulted; only the default `postgres` style is
  rendered.

# M0122-0015g — the built-in postgresql\_fdw\_validator

Status: landed 2026-10-12.
Parent: M0122-0015. Found by the M0122-0015e regress A/B (`foreign_data`).

## Problem

`CREATE FOREIGN DATA WRAPPER postgresql VALIDATOR postgresql_fdw_validator`
failed with `function postgresql_fdw_validator(text[], oid) does not
exist`. goopg's validator resolver searched only user routines. The function
is PostgreSQL's one built-in validator (foreign.c, pg\_proc OID 2316), and
goopg's pg\_proc seed already listed it.

goopg also never ran a validator at all: options were stored unchecked for
wrappers, servers, user mappings and foreign tables. regress `foreign_data`
builds its servers on that wrapper, so every server failed once CREATE SERVER
began requiring an existing wrapper (M0122-0015e).

## Design (`internal/executor/fdw_validator.go`)

- **Resolution.** `resolveFDWValidatorFunc` falls back to the built-in when
  the name is `postgresql_fdw_validator`, unqualified or `pg_catalog.`.
  `fdwvalidator` then stores 2316, and pg\_dump's `::regproc` renders it by
  name.
- **The check.** `validateFDWOptions(validatorOID, options, catalogOID)` is
  `transformGenericOptions`' validator call. For the built-in it ports
  `postgresql_fdw_validator`:
  - each option must be one of `libpq_conninfo_options` for that catalog
    (`user` / `password` on user mappings, the rest on servers);
  - otherwise it raises 42601 `invalid option "x"`;
  - the HINT is `Perhaps you meant the option "y".` for a close match
    (`closestMatch`, a port of varlena.c's initClosestMatch /
    updateClosestMatch: Levenshtein ≤ 4 and ≤ half the source length, the
    first strictly closest candidate wins);
  - when the catalog has no valid options at all (wrapper, foreign table,
    column), the HINT is `There are no valid options in this context.`.
  - A user-defined validator is recorded but not invoked (ledgered).
- **Call sites**, each validating against its own catalog OID:
  - CREATE FDW: the wrapper's options (2328).
  - ALTER FDW: the merged options with the possibly new validator. The new
    handler, validator and options are applied together only after
    validation, so a failed ALTER changes nothing.
  - CREATE SERVER (1417).
  - CREATE USER MAPPING (1418), with the server's wrapper's validator.
  - CREATE FOREIGN TABLE: table options (3118) and column options (1249),
    checked before the table is created.
  - ALTER FOREIGN TABLE: OPTIONS and ALTER COLUMN OPTIONS, on the merged
    lists.
- **ALTER FDW extras:**
  - It now enforces "superuser only", checked before the lookup as
    foreigncmds.c does. A non-superuser's ALTER used to be accepted.
  - Giving a non-null VALIDATOR emits PG's warning that dependent objects'
    options may become invalid.

## Verification

- Probe on PG 18.3 vs goopg (FDW / server / user mapping / foreign table
  options, the hint variants, ALTER atomicity, `fdwvalidator::regproc`):
  byte-identical apart from two pre-existing differences:
  - the WARNING is not delivered when the same statement then errors;
  - CREATE FOREIGN TABLE's command tag is `CREATE TABLE`.
- Tests: `TestPostgresqlFDWValidator`, `TestClosestMatch`.
- Regress A/B `foreign_data`: 2459 → 2428 diff lines. It now matches PG's
  invalid-option errors and hints, the superuser refusal on ALTER FDW, and
  the listings of the `postgresql` wrapper and its servers. `foreign_key` and
  `privileges` show only noise.
- Gates: units, executor / postmaster, the pg\_dump testport family,
  tpch-spotcheck, acceptance arm, sf025 PASS=99.

## Residuals (ledgered)

- **User-defined validators** are recorded, never called.
- **ALTER SERVER / ALTER USER MAPPING `OPTIONS`** are not implemented (the
  parser skips them as compat no-ops), so neither validation nor the option
  change happens there.
- **Warnings before an error are lost.** A WARNING raised by a statement
  that then fails is dropped (PG delivers it before the ERROR).
- **Command tag.** CREATE FOREIGN TABLE reports the command tag
  `CREATE TABLE`.

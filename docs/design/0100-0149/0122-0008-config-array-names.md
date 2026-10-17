# M0122-0008 — proconfig / setconfig go through GUCArrayAdd and GUCArrayDelete

Status: landed 2026-10-10 (6502879b6, table and test follow-up 5d739cad7).
Parent: M0122-0008.

## Problem

Stored settings kept the name as typed, and nothing was validated. Stored
settings here means `CREATE / ALTER FUNCTION ... SET / RESET`
(`pg_proc.proconfig`) and `ALTER DATABASE / ROLE ... SET / RESET`
(`pg_db_role_setting.setconfig`).

| statement | goopg stored | PG 18.3 stores |
|---|---|---|
| `CREATE FUNCTION g() ... SET datestyle = iso, mdy` | `datestyle=iso, mdy` | `DateStyle=iso, mdy` |
| `ALTER ROLE r SET TIMEZONE TO 'UTC'` | `TIMEZONE=UTC` | `TimeZone=UTC` |
| `ALTER ROLE r SET SORT_MEM = 2000` | `SORT_MEM=2000` | `work_mem=2000` |
| `ALTER ROLE r SET no_such_guc = 1` | stored | 42704 unrecognized |
| `ALTER ROLE r SET shared_buffers = '1GB'` | stored | 55P02 cannot be changed without restarting the server |
| `ALTER ROLE r SET work_mem = 'bogus'` | stored | 22023 invalid value |

## PG behaviour

`AlterSetting` (pg_db_role_setting.c) and `update_proconfig_value`
(functioncmds.c) call `GUCArrayAdd` / `GUCArrayDelete` (guc.c). Both start
with `validate_option_array_item(name, value, false)`, which accepts a name
only in these cases:

- **Unknown names.** An unknown name must have a valid custom-variable shape
  (42704 otherwise). It becomes a placeholder, which requires a superuser.
- **Known names.** A known variable goes through `set_config_option(name,
  value, superuser() ? PGC_SUSET : PGC_USERSET, PGC_S_TEST, ...)`. These
  context checks give four 55P02 messages:

  | context | message |
  |---|---|
  | internal | `cannot be changed` |
  | postmaster | `cannot be changed without restarting the server` |
  | sighup | `cannot be changed now` |
  | (su-)backend | `cannot be set after connection start` |

  The value is then parsed. A RESET passes value NULL: context checks only.
- **Normalisation.** The name is then normalised by `find_option`:
  `map_old_guc_names` (`sort_mem` → `work_mem`, `vacuum_mem` →
  `maintenance_work_mem`, `ssl_ecdh_curve` → `ssl_groups`), then the
  variable's own spelling.

## Change

- **`misc.ConfigArrayItem(name, value *string)`** does the above and returns
  the name to store. Errors are `*AlterSystemError` carrying the SQLSTATE.
- **`pg_gucs_gen.go`.** goopg registers only 216 of PG's parameters, so
  `pg_gucs_gen.go` carries PG 18.3's built-in parameters, generated from
  `guc_tables.c`:
  - each entry has PG's own spelling and context;
  - context heads on the following line are handled;
  - `#ifdef` entries are kept only for `DEBUG_NODE_TESTS_ENABLED`, which the
    assert-enabled oracle defines.

  Its 407 names are exactly the oracle's `pg_settings` (401, contexts
  identical) plus the six `GUC_NO_SHOW_ALL` parameters (`role`, `seed`,
  `session_authorization`, `is_superuser`, `default_with_oids`,
  `ssl_renegotiation_limit`).
- **How the table and the registry divide the work.**
  - The table decides acceptance, context and spelling, so `SET role`,
    `temp_tablespaces` and `log_connections` behave as in PG even though
    goopg lacks them.
  - goopg's registration only supplies the value check.
  - Without the table, `CREATE FUNCTION ... SET role` (regress
    `select_parallel`) was rejected.
- **Callers.**
  - The executor's `flattenFunctionConfigOps` covers SET and RESET entries.
  - The ALTER DATABASE / ROLE apply paths call `configArrayItem`, after the
    object checks and the flattening error, as `AlterSetting` orders them.
  - Their text parsers now downcase unquoted names
    (`splitLeadingConfigName`), as `var_name` does.
- **Existing entries.** The registries match entry names case-insensitively,
  so entries stored before the change are still replaced or reset.

## Verification

- **Live against a scratch PG 18.3:** these are identical:
  - the proconfig and setconfig arrays;
  - RESET by any spelling;
  - unknown names, internal / postmaster / sighup / backend contexts, and
    obsolete names;
  - parameters goopg does not register.

  The only exception is the invalid-value wording, which is the open "GUC
  range errors" task.
- **Tests.**
  - `TestConfigArrayItem`;
  - `TestPGGUCTable`;
  - `TestExecFunctionConfigCanonicalNamesAndValidation`;
  - `TestAlterRoleConfigCanonicalName`;
  - `TestPort_ConfigArraysStoreCanonicalNames`.
- **Gates.**
  - units, `go test ./internal/postmaster/`, and 30 config / role / dump /
    function testport tests;
  - regress A/B: `select_parallel` is at baseline; `stats_ext` and
    `alter_generic` flap on HEAD reruns too;
  - TPC-H spotcheck and acceptance arm;
  - fire set (no fires) and SF0.25 sweep (PASS=99).
- **Collateral.** Three `internal/postmaster` unit tests still pinned
  pre-fix behaviour from the two previous M0122-0008 commits. That package
  is outside the units gate. They are updated to PG's behaviour.

## Not covered (filed / ledgered)

- **Value checks need a registration.** Values are checked only for
  parameters goopg registers; for the other 191 PG parameters any value is
  stored.
- **No permission checks.** There is no non-superuser check: a placeholder
  needs a superuser, and a `PGC_SUSET` parameter needs a superuser or the
  parameter ACL.
- **`role` is not checked.** `check_role` would reject an unknown role.
- **Other databases skip validation.** ALTER DATABASE on a database other
  than the connection's own is still a silent no-op.
- **Filed: goopg's registry diverges from PG.**
  - 191 parameters are missing.
  - 7 goopg-only names exist, three of them misspellings of PG's
    `max_pred_locks_per_*`.
  - 13 contexts differ. `commit_delay`, `compute_query_id` and
    `track_io_timing` are `user` in goopg but `superuser` in PG, so
    non-superusers can SET them.

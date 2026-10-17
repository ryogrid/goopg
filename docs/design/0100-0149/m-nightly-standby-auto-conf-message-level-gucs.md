# M-NIGHTLY — a PG base backup's postgresql.auto.conf stops the goopg standby (TestE2E\_PGStandbyFullCycle)

Status: landed 2026-10-12.
Nightly item: AI-20261012-033252-001.

## Symptom

The 2026-10-12 nightly failed `TestE2E_PGStandbyFullCycle` in phase D, the
reverse attach. There, goopg starts as a standby from a `pg_basebackup` of
the promoted PG cluster. The goopg server exited at startup with:

```
postgresql.auto.conf:4: unknown parameter "log_min_messages"
postgresql.auto.conf:5: unknown parameter "log_error_verbosity"
```

The test passed in the 2026-10-10 nightly. In between, ALTER SYSTEM landed
(`27a3009c5`), and goopg began reading `postgresql.auto.conf` at boot. That
file is the PG standby's own. The test's helper had written the standby
settings and two logging settings into it. pg\_basebackup then copied it
into the reverse standby's data directory.

## Analysis

There were two separate problems.

1. **Missing parameters.** PG's `ProcessConfigFileInternal` (guc.c) also
   treats an unknown name in a config file as fatal at postmaster startup
   ("During postmaster startup, any error is fatal"). So goopg's refusal was
   PG-faithful. The gap was that goopg did not register two real PG
   parameters.
2. **A stale primary in the auto file.** Once the names were registered, the
   standby dialled the *original* goopg primary. The copied auto file still
   carried the PG standby's `primary_conninfo` and `primary_slot_name`. The
   auto file overrides `postgresql.conf` in goopg as in PG. The test helper
   wrote the new primary only into `postgresql.conf`, which was correct only
   while the auto file was ignored.

## Fix

- **Registered `log_min_messages` and `log_error_verbosity`.** Both are
  `PGC_SUSET` enums with guc\_tables.c's option lists and boot values:
  `warning`, from `server_message_level_options`, and `default`.
  - A `postgresql.conf.sample` entry was added for each, keeping
    `TestSampleConfigCoversRegistry` green.
  - goopg's server log does not yet filter or format by either value.
- **Hidden enum rows.** guc\_tables.c marks some `config_enum_entry` rows
  `hidden`. Those names are accepted on input but kept out of `enumvals` and
  the error HINT. SHOW prints the first row carrying the same value
  (`config_enum_lookup_by_value`).
  - `Variable.EnumHidden` maps a hidden name to the option shown for it, and
    `canonicalize` consults it after the visible options.
  - `log_min_messages` gets `debug` → `debug2`.
  - `client_min_messages` (the sibling) gets `debug` → `debug2` and
    `info` → `info`. INFO's only row is hidden, so it shows as itself.
  - These were checked against PG 18.3: SET / SHOW and `pg_settings.enumvals`
    for all three GUCs.
- **Test helper.** `configureGoopgStandbyFromPGBackup` now also appends the
  standby settings to `postgresql.auto.conf`, where the later occurrence
  wins. This is what `pg_basebackup -R` does (`WriteRecoveryConfig`).

## Verification

- `TestE2E_PGStandbyFullCycle` passes, as do all 31 `TestE2E_*` tests and
  `TestMessageLevelGUCs`.
- Units gate and `internal/postmaster` pass.
- Regress `guc`, `sysviews` and `misc_functions` are unchanged against HEAD.
  `psql` differs only in the schema listing, an artefact of run order.
- tpch-spotcheck and tpcds-sf025 (PASS=99) pass.

## Residuals

- The two GUCs are not consumed. goopg's slog output has no
  message-level threshold and no terse/verbose field selection.
- Other PG enums with hidden rows still accept only their visible names,
  except where they have an on/off pair and fall back to the boolean parser.
- `log_min_error_statement` (the same option list) is among the parameters
  PG has and goopg does not. That registry gap is tracked under M0122-0008's
  "GUC registry diverges" task, which is parked.

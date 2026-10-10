# M0122-0008 — ALTER SYSTEM, postgresql.auto.conf and pg_reload_conf()

Status: landed 2026-10-10 (27a3009c5). Parent: M0122-0008.

## Problem

`ALTER SYSTEM SET geqo_effort = 11` and `ALTER SYSTEM SET ssl = on` both
reported `ALTER SYSTEM`, and nothing changed. Three pieces were missing:

- **Parsing.** The hand-written compat scanner swallowed every ALTER SYSTEM
  form into a `CompatNoopStmt`.
- **The auto file.** Nothing read or wrote `postgresql.auto.conf`.
- **Reloading.** `pg_reload_conf()` did not exist.

## PG behaviour

- **`AlterSystemSetConfigFile`** (guc.c) does five things:
  1. checks permission: superuser, or the parameter's ALTER SYSTEM ACL;
     RESET ALL is superuser-only;
  2. rejects `PGC_INTERNAL` / `GUC_DISALLOW_IN_FILE` /
     `GUC_DISALLOW_IN_AUTO_FILE` parameters (55P02 `parameter "x" cannot be
     changed`);
  3. validates the value (`parse_and_validate_value`); an unknown name must
     be a valid custom name;
  4. rejects embedded newlines;
  5. under `AutoFileLock`, reads the old file, applies
     `replace_auto_config_value` (delete every match, append), then writes
     header + `name = 'value'` lines (`escape_single_quotes_ascii`) to a temp
     file and fsyncs and renames it.
- **utility.c** wraps it in `PreventInTransactionBlock` (25001).
- **`ProcessConfigFile`** reads `postgresql.conf` and then
  `postgresql.auto.conf`, so the latter wins. On reload, a variable whose
  file entry disappeared is reset to its default.
- **`pg_reload_conf()`** SIGHUPs the postmaster and returns true. EXECUTE is
  revoked from PUBLIC.

## Change

### Parser

- **Grammar.** `alter_system_stmt` in `grammar/goopg_ext.y` (gram.y
  AlterSystemStmt) reuses set_stmt's `set_guc_name` / `set_eq_to` /
  `set_value_list` and `setValueAtoms` flattening.
- **AST.** `parser.AlterSystemStmt{Name, Value, Default, Reset, ResetAll}`.
- **Routing.** The `("alter","system")` pair goes to the grammar, and the
  legacy stub entry is gone.
- **Gates.** Conflicts are unchanged at 60. The goldens gain only the new
  pins.

### GUC package (`internal/utils/misc/alter_system.go`)

- **`Registry.AlterSystemSetConfigFile(dataDir, name, value, set, resetAll)`**
  is the guc.c function minus the permission check:
  - errors are `AlterSystemError{Code, Msg, Hint}` carrying PG's SQLSTATEs;
  - writes go through one process mutex, a temp file, fsync, rename and a
    directory fsync.
- **`SessionRegistry.AlterSystem`** delegates to the global registry.
- **`AutoConfEntries(dataDir)`** parses the auto file; a missing file is
  fine.
- **Config lexer.** `readSingleQuoted` now follows `DeescapeQuotedString`
  (backslash escapes, octal, `''`), so the doubled backslashes ALTER SYSTEM
  writes read back unchanged.
- **Placeholders.** `ApplyConfigEntries` / `ApplyReloadEntries` treat a
  custom dotted name as a placeholder instead of an error. A custom name
  ALTER SYSTEM wrote can therefore no longer stop the server from starting.
- **Reload reset.** `ApplyReloadEntries` resets variables whose config-file
  entry disappeared (PG's "removed from configuration file, reset to
  default"). Postmaster-context ones only warn.

### Server and executor

- **Boot** (`cmd/goopg/main.go`) and **reload** (`Server.reloadConfig`)
  apply the auto file after postgresql.conf.
- **`Context.AlterSystem` / `Context.ReloadConfig`** are wired in both
  dispatch paths.
- **`utilitySettingsOp.execAlterSystem`** checks, in order:
  1. the transaction block;
  2. the non-superuser cases: RESET ALL → 42501; otherwise the
     parameter-ACL `A` bit for the role or PUBLIC.
- **`pg_reload_conf()`** is superuser-only and runs the SIGHUP reload path
  synchronously before returning true.

## Verification

- **Live against a scratch PG 18.3:**
  - `postgresql.auto.conf` is byte-identical after the same statements: the
    custom name, `a''b\\c` escaping, and the replace-moves-to-end order;
  - the 25001 / 55P02 / 42704 messages are identical;
  - `pg_reload_conf()` applies a value, and RESET plus a reload reverts it;
  - a restart keeps the value.
- **Tests.**
  - `TestAlterSystemSetConfigFile` checks the exact bytes, the round trip,
    RESET and RESET ALL.
  - `TestAlterSystemRejects` checks the four SQLSTATEs and that the file is
    left untouched.
  - `TestReadSingleQuotedDeescapes`.
  - `TestReloadResetsRemovedFileEntries`.
  - `TestAlterSystemParity` (parser pins).
  - `TestPort_AlterSystemWritesAutoConfAndSurvivesRestart` runs end to end
    through the real binary.
- **Gates.**
  - units, parser goldens, and 27 GUC/config/parameter testport tests;
  - regress A/B (join flap only);
  - TPC-H spotcheck and acceptance arm;
  - fire set (no fires) and SF0.25 sweep (PASS=99).

## Not covered (ledgered / filed)

- **List-element quoting.** PG quotes list elements of `GUC_LIST_QUOTE`
  variables (`search_path` → `"x'y\z", public`). goopg writes them
  unquoted, the same flattening SET already does. Filed as a separate
  M0122-0008 task, because the fix is shared with SET.
- **Range-error wording.** The value-range error is goopg's own wording
  ("value 11 out of range [1, 10]"). That is the existing "GUC range
  errors" task.
- **Reload timing.** `pg_reload_conf()` reloads synchronously, where PG is
  asynchronous.
- **The "removed" message.** PG logs "removed from configuration file, reset
  to default"; goopg only records the change.
- **Parameter ACL check.** It ignores role-membership inheritance.
- **Reserved prefixes.** There is no reserved extension-prefix check for
  custom names (`assignable_custom_variable_name`).

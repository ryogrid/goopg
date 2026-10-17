# M0122-0008 — SET value lists are flattened like flatten_set_variable_args

Status: landed 2026-10-10 (aaf76f11e). Parent: M0122-0008.
Follow-up of [ALTER SYSTEM](0122-0008-alter-system-auto-conf.md).

## Problem

`SET search_path = 'x''y\z', public` must leave `search_path` holding
`"x'y\z", public`. goopg produced a different string on every path:

| path | goopg value |
|---|---|
| simple-query SET (string-matching fast path) | `'x''y\z', public` (the raw text) |
| executor SET / ALTER SYSTEM (grammar) | `x'y\z, public` |
| CREATE / ALTER FUNCTION ... SET (proconfig) | `x'y\z,public` |
| ALTER DATABASE / ROLE ... SET (setconfig) | `x'y\z,public` |
| extended-protocol SET (fast path) | the raw text |

None of these paths knew the variable's list flags. Two consequences
followed:

- **Wrong splits.** A schema name containing a space or comma, or one
  spelled as a reserved word, split wrongly once the value was read back.
- **No single-value check.** `SET work_mem = '1MB', '2MB'` was accepted, or
  failed with a unit error, where PG says `SET work_mem takes only one
  argument`.

## PG behaviour

`flatten_set_variable_args` (guc_funcs.c) is reached through
`ExtractSetVariableArgs` from SET, ALTER SYSTEM, the function SET clause
and ALTER DATABASE / ROLE SET. It:

1. **Looks up the flags.** It takes the variable's flags; an unknown name
   gets flags 0.
2. **Checks the count.** Without `GUC_LIST_INPUT`, more than one element is
   rejected with 22023 `SET %s takes only one argument`.
3. **Joins the elements with `", "`:**
   - `Integer` prints with `%d`, so `007` becomes `7` and `0x10` becomes
     `16`;
   - `Float` keeps its text;
   - `String` is a string literal or an identifier. Identifiers are
     downcased unless double-quoted (`TRUE`/`FALSE`/`ON` become the
     strings `true`/`false`/`on`). For a `GUC_LIST_QUOTE` variable the
     value goes through `quote_identifier`.

The list-quote variables are `search_path`, `temp_tablespaces`, the three
`*_preload_libraries`, `unix_socket_directories` and
`oauth_validator_libraries`. List-only variables include `DateStyle`,
`listen_addresses` and `synchronous_standby_names`.

## Change

- **Element typing.** `parser.scanSetArgs` reads a `var_list` from a token
  slice into `[]misc.SetArg{Kind: Integer|Float|String, Val}`:
  - signs are applied;
  - an integer that overflows int32 becomes a Float kept as written (scan.l
    `process_integer_literal`).
- **The grammar path.**
  - `SetStmt`, `AlterSystemStmt` and `FunctionConfigOp` gain `Args`.
  - The SET and ALTER SYSTEM actions fill it from the statement's value
    tokens (`setValueArgs`).
  - The function SET clause fills it by position (`setArgsAt`). This needed
    `fn_config_value` to become a `node`-typed `*fnConfigVal`; the grammar
    structure is unchanged and conflicts stay at 60.
  - The legacy function-clause parser fills it too.
- **The raw-text paths.** `parser.ParseSetArgList(text)` serves the callers
  that start from statement text. It rejects anything that is not exactly a
  list, including a bare `DEFAULT`.
- **Flattening.** `misc.FlattenSetArgs(name, args)` is
  `flatten_set_variable_args`. Flags come from a once-built table of the
  default registry; `FlagListInput` / `FlagListQuote` are new `VarFlag`
  bits. `sqlkeywords.QuoteIdentifier` is `quote_identifier`.
- **Consumers.**
  - **Executor SET.**
  - **ALTER SYSTEM**, flattened before the permission check, as upstream.
  - **CREATE / ALTER FUNCTION**, through `flattenFunctionConfigOps`, which
    returns a copy so the cached AST is never mutated.
  - **ALTER DATABASE / ROLE SET.** `flattenConfigValueList(name, text)`;
    the error is carried on the op and raised at apply time, after the
    object checks. Text that does not lex as a list keeps the old text-level
    flattening.
  - **SET fast paths.** The simple-query and extended-protocol paths go
    through `splitSetFlattened`. `DEFAULT`, `SET TIME ZONE LOCAL` and the
    INTERVAL form keep `splitSet`'s value.
- **Canonical spellings.** Downcasing unquoted identifiers is correct, but
  without canonicalisation `SET timezone = UTC` would have shown `utc`.
  The check hooks now store PG's canonical spellings:
  - `client_encoding` takes `encodingNameToCanonical` (check_client_encoding
    stores `pg_encoding_to_char`);
  - `TimeZone` / `log_timezone` are matched against the tz database
    case-insensitively, one path component at a time, and stored with the
    file's own spelling (pgtz.c `pg_open_tzfile`).

## Verification

- **Live against a scratch PG 18.3, side by side.** These are identical:
  - `SHOW search_path` after nine different spellings (quotes, reserved
    words, `$user`, numbers, `''`);
  - `SHOW DateStyle` for a list;
  - the `takes only one argument` errors;
  - `pg_proc.proconfig` and `pg_db_role_setting.setconfig`;
  - the `postgresql.auto.conf` line;
  - SET through the simple, multi-statement and extended protocols.
- **Tests.**
  - `TestFlattenSetArgs`;
  - `TestCanonicalSettingSpellings`;
  - `TestParseSetArgList`;
  - `TestSetStatementArgs` (SET, ALTER SYSTEM, CREATE FUNCTION with two SET
    clauses, ALTER FUNCTION, TO DEFAULT);
  - `TestQuoteIdentifier`;
  - `TestFlattenConfigValueList`;
  - `TestSplitSetRawValue`;
  - the proconfig executor tests now pin PG's `", "` join.
- **Gates.**
  - units, parser goldens (the 47 SET-family lines gain `Args`), and 31
    GUC/SET/dump/role-setting testport tests;
  - regress A/B: the standard set changed only by the `join` flap; `guc`,
    `namespace`, `horology`, `timestamptz`, `timetz`, `date`,
    `create_function_sql` and `collate` are identical between HEAD and the
    new binary;
  - TPC-H spotcheck and acceptance arm;
  - fire set (no fires) and SF0.25 sweep (PASS=99).

## Not covered (filed / ledgered)

- **`quote_all_identifiers`.** It is not registered, so `QuoteIdentifier`
  ignores it.
- **Extension-defined list variables.** These have no list flags; only the
  built-in table is consulted.
- **Consumers split naively.** `search_path` readers split on commas and
  trim quotes instead of using `SplitIdentifierString`, so a quoted element
  that contains a comma or `""` is still read wrongly.
- **Found during the live comparison; pre-existing, filed:**
  - SET LOCAL outside a transaction block takes effect (PG warns and does
    nothing);
  - proconfig and setconfig store the GUC name as typed (PG stores the
    variable's own name, e.g. `DateStyle=`);
  - `SET TIME ZONE INTERVAL '+02:00' HOUR TO MINUTE` is stored verbatim;
  - `CREATE FUNCTION f(x int = 1)` is a syntax error (gram.y
    `func_arg '=' a_expr`);
  - `temp_tablespaces` is not registered (already ledgered 2026-08-03).

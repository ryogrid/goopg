# `collate.utf8.sql`: validate `CREATE COLLATION ... provider=builtin` locale names (M0134-0102)

## Status: PARKED (`failed`)

## Summary

`collate.utf8.sql` sized live, 0% parity. Unlike the ICU/libc collation-family
PARKs (M0134-0099/-0100), this file's own `\if :skip_test` guard only checks
`getdatabaseencoding() = 'UTF8'` — goopg's fixed encoding is always UTF8, so
the file runs in full (no early self-skip), and it exercises the `builtin`
collation provider, which goopg DOES parse and register end to end. The
divergence is real semantic gaps, not a self-skip mismatch.

The first divergence, and the one that cascaded the widest through the file,
was a missing validation step:

```sql
CREATE COLLATION regress_pg_unicode_fast (
  provider = builtin, locale = 'unicode'); -- fails
CREATE COLLATION regress_pg_unicode_fast (
  provider = builtin, locale = 'PG_UNICODE_FAST');
-- PG: first CREATE COLLATION errors 22023 invalid locale name "unicode" for
--     builtin provider; second CREATE COLLATION then succeeds.
-- goopg (before this fix): first CREATE COLLATION silently succeeds with a
--     bogus locale value; second CREATE COLLATION then fails with
--     "collation \"regress_pg_unicode_fast\" already exists" — wrong error,
--     cascading into every following statement that expects the collation
--     to exist with the correct definition.
```

PostgreSQL's `DefineCollation` (`postgres/src/backend/commands/collationcmds.c:256`)
calls `builtin_validate_locale` (`postgres/src/backend/utils/adt/pg_locale.c:1510`)
before the `pg_collation` insert whenever `provider = builtin`. Only three
spellings are recognized:

```c
if (strcmp(locale, "C") == 0)
    canonical_name = "C";
else if (strcmp(locale, "C.UTF-8") == 0 || strcmp(locale, "C.UTF8") == 0)
    canonical_name = "C.UTF-8";
else if (strcmp(locale, "PG_UNICODE_FAST") == 0)
    canonical_name = "PG_UNICODE_FAST";
if (!canonical_name)
    ereport(ERROR, (errcode(ERRCODE_WRONG_OBJECT_TYPE),
             errmsg("invalid locale name \"%s\" for builtin provider", locale)));
```

goopg's `execCreateCollation` (`internal/executor/operators_ddl.go`) already
had the identical canonicalization logic for `goopg init`'s `--builtin-locale`
option (`internal/initdb/locale.go: resolveLocale`, `initdb.c:2477-2488`), but
the runtime `CREATE COLLATION` DDL path had never been given the same check —
it stored whatever locale string the parser handed it verbatim into
`uc.Locale`. Fixed by porting the same three-way switch directly into
`execCreateCollation`'s builtin-provider branch, raising `22023` with PG's
exact message on an unrecognized spelling. `internal/initdb/locale.go` could
not be reused directly — `internal/executor` cannot import `internal/initdb`
(leaf-package-only import direction, see `goopg_version_constants_leaf_config_import_cycle`
memory) — so the three-case switch is duplicated inline; it is small and
stable (PG has shipped the same three spellings unchanged since the
`builtin` provider's introduction in PG 17).

goopg always runs UTF8 (`internal/initdb/locale.go`'s `resolveLocale` forces
this at cluster-bootstrap time), so PG's additional encoding-vs-locale
cross-check (`pg_locale.c:1528-1533`, requiring UTF8 for `C.UTF-8`/
`PG_UNICODE_FAST`) can never fire in goopg and was not reproduced.

New test: `internal/executor/create_collation_builtin_locale_test.go` — three
subtests (invalid name rejected + catalog stays clean, underscore-spelled
name rejected, all three canonical spellings accepted with the expected
canonicalized `Locale` value).

## Remaining buckets (ledgered, PARKED)

Six further independent root causes remain, all beyond a single loop's
contained-fix budget:

1. **No builtin-provider Unicode case-mapping engine.** `lower()`/`upper()`/
   `initcap()` in `internal/executor/expr.go` call `strings.ToUpper`/
   `ToLower`/etc. unconditionally, ignoring the argument's declared
   collation entirely. Under a real `provider=builtin, locale='C'`
   collation, PG's case mapping is ASCII-only (non-ASCII bytes pass through
   unchanged); under `PG_UNICODE_FAST` it does full Unicode case mapping
   (including multi-codepoint expansions like `ß` → `SS` for `upper`/
   `initcap`). goopg does the full-Unicode mapping unconditionally
   regardless of which builtin locale (or non-builtin collation) is in
   play — same "no collation execution engine" class flagged in
   M0134-0099/-0100/-0101 bucket (1)/(3).
2. **`Final_Sigma` context rule unimplemented.** PG's `PG_UNICODE_FAST`
   `lower()` maps Greek Σ to lowercase ς only when the character is at the
   end of a word (Unicode's `Final_Sigma` condition); goopg always emits
   plain ς. Needs the same Unicode case-mapping engine as bucket 1, applied
   with the position-sensitive rule.
3. **`casefold()` SQL function does not exist.** PG 18 added it
   (Unicode case-folding for equality comparison, distinct from `lower()`).
   No pg_proc row, no handler, in goopg.
4. **`convert_to()`/`convert_from()` are pg_proc metadata only.**
   `internal/initdb/pg_proc_seed_data.go:1058` declares `convert_to` (OID
   1717, `HandlerName: "pg_convert_to"`), but no Go function anywhere in
   `internal/executor` implements that handler — the encoding-conversion
   family is entirely unimplemented.
5. **Systemic bug, broader than this file: nested function-not-found errors
   are silently swallowed to NULL.** Every one of `length`/`char_length`/
   `octet_length`/`bit_length`/`upper`/`lower`/(and more) in
   `internal/executor/expr.go` evaluates its argument with the pattern
   `s, err := evalExprSlot(...); if err != nil || s.IsNull() { return
   NullDatum, nil }` — collapsing a genuine evaluation error into the same
   branch as a genuine NULL. Live repro: `SELECT octet_length(nonexistent_
   func_xyz('x'))` returns NULL with no error, while the same call bare or
   under `||` correctly raises `42883 function nonexistent_func_xyz does not
   exist`. This masked bucket 4 above inside this test file: `length(convert_
   to(t, 'UTF8'))` should have raised the missing-function error but instead
   printed a blank byte-count column, in both of the file's per-provider
   result tables.
6. **`initcap()` fullwidth-digit edge case** — one row in the file's
   PG_C_UTF8 table shows `initcap('...１a')` diverging on the fullwidth
   digit's following letter case (`１A` expected vs `１a` produced); not yet
   isolated to a minimal repro.

Bucket 5 is flagged as its own concern: fixing it requires auditing every
`err != nil ||` occurrence across `internal/executor/expr.go` (dozens of
call sites are expected, not just the ones this file happens to exercise)
and splitting each into a proper `if err != nil { return Datum{}, err }` /
`if X.IsNull() { return NullDatum, nil }` pair, then re-running the full
regress-port suite per Hard-won Rule #5 — too large a blast radius for this
loop's contained-fix budget, but independently high-value and worth its own
dedicated loop.

## References

- `postgres/src/backend/utils/adt/pg_locale.c:1510` (`builtin_validate_locale`
  — the exact rule ported here) and `:169-192`
  (`get_collation_actual_version_builtin`, same three-name enumeration).
- `postgres/src/backend/commands/collationcmds.c:248-257` (`DefineCollation`
  calling `builtin_validate_locale` before the catalog insert).
- `internal/initdb/locale.go: resolveLocale` — goopg's pre-existing port of
  the same three-way canonicalization for `goopg init --builtin-locale`
  (initdb-time, not reachable from `internal/executor` due to the leaf-
  package import direction).
- `internal/executor/operators_ddl.go` — `execCreateCollation`'s builtin-
  provider branch (new validation).
- `internal/executor/create_collation_builtin_locale_test.go` — new test.
- `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0102.

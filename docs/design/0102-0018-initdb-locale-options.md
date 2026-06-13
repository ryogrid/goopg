# 0102-0018 — initdb `--locale-provider` / `--locale` / `--lc-*` options

**Milestone:** M0102-0010 (initdb CLI option coverage)
**Status:** accepted
**Author:** Ralph (loop #27, 2026-06-13)

## Problem

`goopg init` accepted no locale options, so it always wrote a fixed
`pg_database` collation configuration (`datlocprovider='c'`,
`datcollate`/`datctype`=`"C"`, `datlocale` NULL) and could not match the
non-ICU locale cases of upstream `initdb`'s
`src/bin/initdb/t/001_initdb.pl`:

- `--locale-provider builtin --locale C` → success
- `--locale-provider builtin --encoding UTF-8 --lc-collate C --lc-ctype C --builtin-locale C.UTF-8` → success
- `--locale-provider builtin` (no locale) → fail (`locale must be specified`)
- `--locale-provider builtin --encoding SQL_ASCII --builtin-locale C.UTF-8` → fail (encoding requirement)
- `--locale-provider builtin --icu-locale en` / `--icu-rules ""` → fail (combo)
- `--locale-provider xyz` → fail (unrecognized provider)
- `--locale-provider libc --icu-locale en` → fail (invalid combination)
- `--locale-provider icu` → fail (no ICU support in this build)

## Scope

This slice adds the **libc and builtin collation providers** plus the
`--locale` / `--lc-collate` / `--lc-ctype` / `--lc-messages` /
`--lc-monetary` / `--lc-numeric` / `--lc-time` / `--builtin-locale` options,
and recognizes (but rejects) `icu` / `--icu-locale` / `--icu-rules`.

goopg's running engine uses a fixed C / UTF8 locale and does **not** vary
collation behavior at runtime, so — exactly like the `-E`/`--encoding`
(0102-0017) and `--pwfile` verifier (0102-0016) work — these options affect
only the **on-disk catalog** (`pg_database.datlocprovider`/`datcollate`/
`datctype`/`datlocale`) and the seeded `lc_*` GUCs. What is reproduced
faithfully is initdb's **acceptance/rejection of the option combinations** and
the **values written into `pg_database`**.

**Out of scope (deferred):** the ICU runtime (a build without `USE_ICU`,
mirrored by rejecting the provider), and the locale-derived default encoding
(`pg_get_encoding_from_locale` on an unset `--encoding`) — goopg's fixed C
locale keeps the default UTF8 (see 0102-0017). The remaining initdb option is
`--data-checksums` (the PG 18 default-on page-checksum write/verify path; high
blast radius).

## Implementation

New `internal/initdb/locale.go`, ported from `src/bin/initdb/initdb.c`:

- `resolveLocaleProvider` — `--locale-provider` parsing (initdb.c:3367):
  `""`/`libc` → `'c'`, `builtin` → `'b'`, `icu` → `'i'`, else
  `unrecognized locale provider: %s`. The byte codes match
  `Form_pg_database.datlocprovider` (`COLLPROVIDER_*`).
- `pgGetEncodingFromLocale` — a faithful-enough port for a frontend that
  cannot call `setlocale`: `C`/`POSIX`/empty → `SQL_ASCII`; otherwise the
  encoding is taken from the `.CODESET` suffix (with `@modifier` stripped) via
  `pgValidServerEncoding`; unknown/absent codeset → `-1`.
- `checkLocaleEncoding` — `check_locale_encoding` (initdb.c:2265): the encoding
  must agree with the locale's implied encoding, except `SQL_ASCII` (either
  side) and an unknown locale encoding are always accepted.
- `resolveLocale` — the post-parse resolution + validation:
  1. provider parse;
  2. option-combination checks (initdb.c:3424-3434): `--builtin-locale` needs
     the builtin provider, `--icu-locale`/`--icu-rules` need icu;
  3. `setlocales` (initdb.c:2424): unset `lc_*` fall back to `--locale`, then to
     `"C"`; `datlocale` is sourced from `--builtin-locale`/`--icu-locale` or
     from `--locale` for a non-libc provider;
  4. `locale must be specified if provider is <name>` (initdb.c:2471);
  5. builtin canonicalization (initdb.c:2477): `C`, `C.UTF-8`/`C.UTF8` →
     `C.UTF-8`, `PG_UNICODE_FAST`, else `invalid locale name`;
  6. icu provider → `ICU is not supported in this build` (the `#ifndef USE_ICU`
     fatal, initdb.c:2503);
  7. `setup_encoding` (initdb.c:2772): `check_locale_encoding` on `lc_ctype`
     and `lc_collate`, then the builtin `C.UTF-8`/`PG_UNICODE_FAST` ⇒ UTF8
     requirement (initdb.c:2778-2783).
- `localeSettings.localeGUCSettings` — the `lc_messages`/`lc_monetary`/
  `lc_numeric`/`lc_time` overrides seeded into `postgresql.conf` (setup_config,
  initdb.c:1351-1366), but only when a locale option was given (the established
  "no-op when unset" pattern; `lc_collate`/`lc_ctype` are per-database in modern
  PG and live in `pg_database`, not `postgresql.conf`).

Wiring:

- `Options` gains `LocaleProvider`, `Locale`, `LCCollate`, `LCCtype`,
  `LCMessages`, `LCMonetary`, `LCNumeric`, `LCTime`, `BuiltinLocale`,
  `ICULocale`, `ICURules`.
- `Init` calls `resolveLocale` up front (right after `resolveEncoding`, before
  auth/trust-warning and any filesystem layout) so every rejection aborts
  before the tree is created.
- `seedPostgresqlConf` gains a `localeGUCs` parameter applied first (matching
  upstream order: `lc_*` before `default_text_search_config` and before the
  `-c`/`--set` loop, so an explicit `-c` override still wins).
- `bootstrapPostgresDatabase(dir, encodingID, locale)` writes
  `datlocprovider`/`datcollate`/`datctype`/`datlocale` from the resolved
  settings. The no-option default is **byte-identical** to the previous
  hard-coded libc / "C" row (`datlocale` stays NULL, so the null bitmap and
  `t_hoff` are unchanged). For the builtin provider `datlocale` becomes a
  non-NULL text, but `daticurules`/`datcollversion`/`datacl` remain NULL so
  `HEAP_HASNULL` stays set and the fixed-prefix offset of `encoding` is
  unchanged — **no on-disk format change** (same 18-column tuple; only values
  vary).
- CLI: long-form `--locale-provider`, `--locale`, `--lc-collate`, `--lc-ctype`,
  `--lc-messages`, `--lc-monetary`, `--lc-numeric`, `--lc-time`,
  `--builtin-locale`, `--icu-locale`, `--icu-rules` on the `init` command
  (matching upstream, which has no short forms for these).

## Testing

- `internal/initdb/locale_test.go` — `resolveLocaleProvider`,
  `pgGetEncodingFromLocale`, libc default (byte-identical to prior behavior),
  libc explicit + GUC seeding, builtin success + canonicalization, and a table
  of every rejection path.
- `internal/initdb/pg_database_encoding_test.go` /
  `pg_database_pg18_schema_test.go` — updated callers (default libc settings)
  confirm the unchanged tuple format.
- `cmd/goopg/main_test.go::TestInitCommandLocaleProvider` — drives the CLI for
  the builtin success cases and the five always-run non-ICU rejection cases of
  `001_initdb.pl`, asserting exit code, diagnostic text, and that a rejected
  init lays out nothing.

This touches only `internal/initdb` + `cmd/goopg`; no executor/planner/codec/
WAL-format change and no `pg_database` tuple-format change (values only), so
the TPC-H spotcheck gate is N/A.

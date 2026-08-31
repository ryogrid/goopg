# 0102-0017 — initdb `-E`/`--encoding` default-encoding option (M0102-0010)

Status: accepted
Date: 2026-06-13
Milestone: M0102-0010 (initdb CLI option coverage)

## Problem

`goopg init` hard-coded the new cluster's database encoding to `PG_UTF8`
(integer `6`) in the `pg_database.encoding` column. Upstream `initdb` accepts
`-E`/`--encoding=NAME` to choose the default encoding for the cluster's
databases (`postgres/src/bin/initdb/initdb.c`, `get_encoding_id`,
`setup_locale_encoding`). Without the option goopg could neither record a
non-UTF8 default encoding on disk nor reject an invalid encoding name the way
`initdb` does, leaving an option gap under M0102-0010.

## Scope (this slice)

This slice adds **only** the `-E`/`--encoding` name validation and the
name→ID mapping that lands in `pg_database.encoding`. It deliberately does
**not** add:

- The **locale-derived default** encoding. Upstream derives the default from
  `LC_CTYPE` via `pg_get_encoding_from_locale` when `-E` is omitted. goopg's
  locale handling is still fixed at `C` / `UTF8` (the `--locale` /
  `--locale-provider` / `--lc-*` / `--icu-locale` family is a separate,
  deferred initdb option), so when `-E` is omitted the default encoding stays
  `UTF8` — the value goopg has always written.
- The **encoding ↔ locale compatibility checks** (`check_locale_encoding`,
  `check_icu_locale_encoding`, the `encoding mismatch` errors). These only fire
  against a real locale/ICU codeset; goopg's fixed `C` locale maps to
  `SQL_ASCII`, which is compatible with every encoding, so the check is a no-op
  until the `--locale` work lands. The `001_initdb.pl` cases that exercise
  `encoding mismatch` (lines 165-170, 236-242) are all entangled with
  `--locale-provider`/`--builtin-locale`/ICU and are therefore out of scope
  here.
- Any **server-side enforcement** of the chosen encoding. Like the `--pwfile`
  verifier in 0102-0016, the encoding ID written here is for on-disk
  PG-compatibility; goopg's server continues to operate in UTF8 internally. A
  user choosing `-E LATIN1` gets a cluster whose `pg_database.encoding` reads
  `8`, matching what `initdb -E LATIN1` would record.

## Upstream behavior ported

`get_encoding_id` (initdb.c:846):

```c
if (encoding_name && *encoding_name)
{
    if ((enc = pg_valid_server_encoding(encoding_name)) >= 0)
        return enc;
}
pg_fatal("\"%s\" is not a valid server encoding name", ...);
```

and the encoding-name machinery in `src/common/encnames.c` +
`src/include/mb/pg_wchar.h`:

- `clean_encoding_name` — lowercase + strip non-alphanumeric, so `"UTF-8"`,
  `"utf_8"`, and `"UTF8"` all collapse to the alias key `utf8`.
- `pg_char_to_encoding` — alias-table lookup (`pg_encname_tbl`), including the
  `NAMEDATALEN` (64-byte) length guard.
- `pg_valid_server_encoding` — recognize the name, then require
  `PG_VALID_BE_ENCODING(enc)` = `enc >= 0 && enc <= PG_ENCODING_BE_LAST`
  (`PG_KOI8U` = 34). The seven client-only encodings above that bound (SJIS,
  BIG5, GBK, UHC, GB18030, JOHAB, SHIFT_JIS_2004) are recognized but rejected
  as server encodings.
- `pg_encoding_to_char` — ID→canonical-name (`pg_enc2name_tbl`), used for
  diagnostics and round-trip tests.

## Implementation

New file `internal/initdb/encoding.go`:

- `pgEncSQLASCII` constant (= 0) completes the `pgEnc*` integer set started in
  `pg_conversion_bootstrap.go` (which omitted `PG_SQL_ASCII` because no
  conversion references it).
- `pgEncNames` — `[...]string` indexed by encoding ID, the canonical-name table
  ported verbatim from `pg_enc2name_tbl`'s `DEF_ENC2NAME` args.
- `pgEncnameTbl` — `map[string]int32` from cleaned alias to ID, the full
  `pg_encname_tbl` (every alias upstream recognizes, so the accepted `-E` name
  set is byte-identical to initdb's).
- `cleanEncodingName`, `pgCharToEncoding`, `pgValidServerEncoding`,
  `pgEncodingToChar` — direct ports.
- `resolveEncoding(name) (int32, error)` — `get_encoding_id`: empty → `UTF8`
  default; valid server encoding → its ID; unknown/client-only → an error with
  initdb's exact `"%s" is not a valid server encoding name` wording.

Wiring:

- `Options.Encoding string` (empty = default UTF8).
- `Init` calls `resolveEncoding(opts.Encoding)` up front — right after the
  superuser-name check, **before** the auth resolution and trust-default
  warning and before any filesystem layout — so a bad encoding aborts cleanly
  with no partial tree and no spurious warning.
- `bootstrapPostgresDatabase(dataDir string, encodingID int32)` now takes the
  resolved ID and writes it into the `encoding` column for all three seeded
  databases (template1/template0/postgres) instead of the hard-coded `6`.
- `cmd/goopg/main.go` registers `-E` and `--encoding` (both bind one variable),
  threaded into `initdb.Options{Encoding: ...}`.

No on-disk **format** change: the `pg_database` tuple still has the same 18
columns and the same fixed-width layout; only the integer value of the
`encoding` column varies. `TestPgDatabaseAttrsMatchesPG18FormPgDatabase` and the
physical-load tests are unaffected (the default path still writes `6`).

## Testing

- `internal/initdb/encoding_test.go` — `cleanEncodingName`, `pgCharToEncoding`
  (aliases + `NAMEDATALEN` guard + unknown), `pgValidServerEncoding`
  (server-valid vs client-only), `pgEncodingToChar` (round-trip + range),
  `resolveEncoding` (default / valid / rejected).
- `internal/initdb/pg_database_encoding_test.go` — decodes the `encoding` int4
  column out of the written `global/1262` heap page and confirms the resolved
  ID lands there for UTF8 (default), LATIN1, and SQL_ASCII (the wiring half).
- `cmd/goopg/main_test.go` `TestInitCommandEncoding` — CLI contract: a valid
  name lays out a cluster; a client-only (`SJIS`) or unknown name exits 1 with
  initdb's diagnostic and lays out nothing.

## Remaining M0102-0010 initdb options

- `--locale` / `--lc-*` / `--locale-provider` / `--icu-locale` (libc/ICU/builtin
  locale; pulls in `pg_get_encoding_from_locale`, the locale-derived default
  encoding, and the `check_locale_encoding` / `check_icu_locale_encoding`
  mismatch checks deferred above).
- `--data-checksums` (page-checksum write/verify path; high blast radius —
  needs more than a control-file field).

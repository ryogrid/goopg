# 0102-0013 — initdb `-T`/`--text-search-config` + `-c`/`--set` config seeding (M0102-0010)

Status: accepted

## Context

`goopg init` writes `postgresql.conf` verbatim from the embedded
`config.SampleConfig()` template (every GUC commented out, so the effective
configuration equals the registry's `BootVal`s). Upstream `initdb` instead
*seeds* a handful of GUCs into `postgresql.conf` at init time via
`setup_config()` (`postgres/src/bin/initdb/initdb.c:1283`), and exposes two
user-facing options that let the caller inject GUC values directly:

- `-T`/`--text-search-config CFG` — sets `default_text_search_config`.
- `-c`/`--set NAME=VALUE` — sets an arbitrary GUC (repeatable).

Both appear in `postgres/src/bin/initdb/t/001_initdb.pl`'s first
"successful creation" command (lines 51-59):

```perl
command_ok([ 'initdb', '--no-sync',
    '--text-search-config' => 'german',
    '--set' => 'default_text_search_config=german',
    '--waldir' => $xlogdir, $datadir ], 'successful creation');
```

`--no-sync` (0102-0012) and `--waldir` (0102-0011) already landed; `-T` and
`-c`/`--set` were the remaining options in that command. This change closes
them, so the entire "successful creation" option set is now accepted by
`goopg init`.

## What upstream does

`setup_config()` builds the new `postgresql.conf` by repeatedly calling
`replace_guc_value(conflines, name, value, mark_as_comment)`
(`initdb.c:526`):

1. `default_text_search_config` is always written as
   `'pg_catalog.<cfg>'` (`initdb.c:1343-1346`). With no `-T`, `<cfg>` is
   derived from the locale; `-T` overrides it (`initdb.c:3347`).
2. Every `-c`/`--set` pair is applied **last** (`initdb.c:1430-1436`), so a
   `-c` switch overrides any earlier assignment — including
   `default_text_search_config` itself. This is exactly why the test sets
   both `-T german` (→ `pg_catalog.german`) and
   `--set default_text_search_config=german` (→ bare `german`): the final
   value is `german`.

`replace_guc_value` finds the first line that — after skipping a leading
`#` and whitespace — names the GUC followed by `=`, and rewrites it in
place using the file's canonical casing, preserving any trailing inline
comment. If no line matches, it appends `name = value`. Values are quoted
via `guc_value_requires_quotes` (`initdb.c:644`): bare identifiers and
numbers-with-units stay unquoted; everything else is single-quoted with
embedded quotes doubled.

## Design

### Options (`internal/initdb/initdb.go`)

```go
TextSearchConfig string        // -T/--text-search-config
ExtraGUC         []GUCSetting   // -c/--set (ordered, repeatable)
```

`GUCSetting{Name, Value}` lives in the new `internal/initdb/config_seed.go`.
Both default to the zero value, so every existing caller is unchanged and no
seeding happens unless an option is supplied.

### Seeding (`internal/initdb/config_seed.go`)

`seedPostgresqlConf(abs, tsConfig, extra)` runs immediately after the
`SampleFiles()` loop writes `postgresql.conf`, and before catalog bootstrap
/ the final fsync. It is a no-op when both inputs are empty. Otherwise it
reads the just-written `postgresql.conf`, splits it into logical lines,
applies (in upstream order):

1. `default_text_search_config` ← `pg_catalog.<tsConfig>` (when `-T` given),
2. each `ExtraGUC` entry,

then writes the file back at mode `0o600`.

`replaceGUCValue([]string, name, value)` is a faithful port of
`replace_guc_value`: it matches a leading-`#`/whitespace-skipped, case-
insensitive `name =` line; rewrites it in place with the file's canonical
casing and any trailing inline comment (upstream's tab-column re-indent is
cosmetic and collapsed to a single space); or appends a new line on no
match. `formatGUCValue`/`gucValueRequiresQuotes` port the quoting rules
verbatim.

### CLI (`cmd/goopg/main.go`)

- `-T`/`--text-search-config` → `*string` (both forms bind one variable).
- `-c`/`--set` → a `flag.Value` (`gucFlag`) collecting repeated
  `NAME=VALUE` pairs into `[]initdb.GUCSetting`. A value lacking `=` is
  recorded and reported after parsing as `-c <v> requires a value` (exit 2),
  matching `initdb.c`'s `pg_log_error("-c %s requires a value")`.

## Faithfulness / divergence

- goopg does not derive `default_text_search_config` from the locale (no
  locale subsystem yet), so without `-T` the template's commented default is
  left untouched rather than being set to a locale-matched config. `-T`
  behaves identically to upstream. (Locale/encoding options remain tracked
  under M0102-0010's "remaining initdb options".)
- The `-c` → bootstrap-server GUC-name validation upstream relies on does
  not exist here; an unknown name is appended verbatim (the server validates
  GUC names at startup, not at init).

## Testing

- `internal/initdb/config_seed_test.go` — `replaceGUCValue` table (in-place
  rewrite + comment preservation, append-on-absent, dot/space quoting,
  canonical casing, first-match-only), `formatGUCValue` quoting table, and
  `Init`-level checks: `-T german` → `'pg_catalog.german'`; the full
  001_initdb.pl option set → a single unquoted `german` (the `-c` override
  wins and does not duplicate the line); in-place rewrite of a template GUC
  preserving its inline comment; append of an unknown GUC; no seeding leaves
  the template untouched.
- `cmd/goopg/main_test.go` — `TestInitCommandSeedsGUCs` drives the full
  001_initdb.pl command through the CLI; `TestInitCommandSetRequiresValue`
  pins the `--set bogus` → exit 2 error path (and that nothing is laid out).

## Status note

No on-disk format change. Fourth initdb-option gap closed under M0102-0010;
this completes the `001_initdb.pl` "successful creation" option set
(`--no-sync` + `--text-search-config` + `--set` + `--waldir`). Remaining
options (encoding/locale/checksums/auth/group-access/sync-method/…) tracked
under M0102-0010.

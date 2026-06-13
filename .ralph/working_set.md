Task: M0102-0010 — initdb CLI options. Loop #27 landed the `--locale-provider`
/ `--locale` / `--lc-*` / `--builtin-locale` family (9th option gap). Committed
+ pushed → idle on this slice.

Files (this loop):
- internal/initdb/locale.go (NEW) — resolveLocaleProvider (initdb.c:3367),
  pgGetEncodingFromLocale (codeset-suffix port; C/POSIX→SQL_ASCII), 
  checkLocaleEncoding (2265), resolveLocale (= setlocales 2424 + setup_encoding
  2772 validation: combo checks 3424-3434, "locale must be specified" 2471,
  builtin canon C/C.UTF-8/PG_UNICODE_FAST 2477, ICU reject 2503, builtin-UTF8
  req 2778-2783), localeSettings + localeGUCSettings().
- internal/initdb/initdb.go — Options.{LocaleProvider,Locale,LCCollate,LCCtype,
  LCMessages,LCMonetary,LCNumeric,LCTime,BuiltinLocale,ICULocale,ICURules};
  Init calls resolveLocale after resolveEncoding; bootstrapPostgresDatabase
  signature +localeSettings, row writes datlocprovider/datcollate/datctype/
  datlocale (default byte-identical to old libc/"C" row — datlocale NULL).
- internal/initdb/config_seed.go — seedPostgresqlConf +localeGUCs param applied
  FIRST (before tsConfig + -c/--set, matching setup_config order).
- cmd/goopg/main.go — 11 long-form locale flags on `init`.
- internal/initdb/locale_test.go (NEW), cmd/goopg/main_test.go
  (TestInitCommandLocaleProvider); pg_database_encoding_test.go +
  pg_database_pg18_schema_test.go callers updated (default libc localeSettings).
- docs/design/0102-0018-initdb-locale-options.md (NEW) + README index row.
- .ralph/fix_plan.md (loop #27 progress + trimmed remaining-options note).

Key facts:
- Scope: on-disk PG-compat only (engine keeps fixed C/UTF8 locale; no runtime
  collation). icu provider recognized but rejected (no USE_ICU build).
- DEFERRED: locale-derived default encoding (no-op under fixed C locale).
- NO format change (same 18-col pg_database tuple, only values) → TPC-H
  spotcheck gate N/A. Touches only internal/initdb + cmd/goopg.
- ~19 files of FOREIGN uncommitted changes remain untouched; commit
  selectively, never git add -A.

Next step (next loop): only `--data-checksums`/`--no-data-checksums` remains
for M0102-0010 — but it needs the FULL page-checksum write/verify path (high
blast radius), NOT just the pg_control field (faking the field breaks a PG
standby reading the pages). Design doc first; consider whether to bound it or
pick a different fix_plan item. M0102-0010 could otherwise be near-closeable.

Gates run: gofmt clean (my files); go build ./... PASS; go vet
./internal/initdb ./cmd/goopg PASS; go test ./internal/initdb (111s) full pkg
PASS; go test ./cmd/goopg -run TestInitCommand PASS; make ralph-state-guard
(run before status block).

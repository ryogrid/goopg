Task: M0102-0010 — add the next initdb CLI option. Loop #26 landed
`-E`/`--encoding` (default database encoding). Committed + pushed → idle on
this slice.

Files (this loop):
- internal/initdb/encoding.go (NEW) — full encnames.c port:
  cleanEncodingName, pgCharToEncoding (pgEncnameTbl full alias map +
  NAMEDATALEN guard), pgValidServerEncoding (PG_VALID_BE_ENCODING ≤
  pgEncodingBELast=34=KOI8U), pgEncodingToChar (pgEncNames canonical
  table), resolveEncoding=get_encoding_id (""→pgEncUTF8; valid→ID;
  bad→`"%s" is not a valid server encoding name`). Added pgEncSQLASCII=0
  (the rest of pgEnc* live in pg_conversion_bootstrap.go).
- internal/initdb/initdb.go — Options.Encoding; Init resolves it up front
  (after superuser check, BEFORE auth/trust-warning + layout);
  bootstrapPostgresDatabase(dir, encodingID) — writes encodingID into the
  `encoding` col of all 3 seeded DBs (was hard-coded 6).
- cmd/goopg/main.go — `-E`/`--encoding` flags → Options.Encoding.
- internal/initdb/encoding_test.go (NEW),
  internal/initdb/pg_database_encoding_test.go (NEW, decodes encoding int4
  at off+t_hoff+72 in global/1262), cmd/goopg/main_test.go
  (TestInitCommandEncoding).
- internal/initdb/pg_database_pg18_schema_test.go — caller now passes pgEncUTF8.
- docs/design/0102-0017-initdb-encoding-option.md (NEW) + README index row.
- .ralph/fix_plan.md (loop #26 progress).

Key facts:
- DEFERRED (with --locale family): locale-derived default encoding
  (pg_get_encoding_from_locale) + check_locale_encoding /
  check_icu_locale_encoding mismatch. goopg's fixed C locale → SQL_ASCII,
  compatible with any encoding, so these are no-ops today. The
  001_initdb.pl encoding cases (lines 165-170, 236-242) are entangled with
  --locale-provider/ICU → out of scope until --locale lands.
- No server-side encoding enforcement (on-disk PG-compat only, like the
  0102-0016 pwfile verifier). Server stays UTF8 internally.
- Touches ONLY internal/initdb + cmd/goopg → NO executor/planner/codec/
  WAL-format and NO pg_database tuple-FORMAT change (only the value) →
  TPC-H spotcheck gate N/A.
- ~19 files of FOREIGN uncommitted changes remain untouched. Commit
  selectively; never git add -A.

Next step (next loop): continue M0102-0010. Remaining: `--locale`/`--lc-*` +
`--locale-provider`/`--icu-locale` (ICU — big, also unlocks the deferred
encoding-from-locale default + mismatch checks), `--data-checksums`
(page-checksum write/verify path — high blast radius). Design doc first.

Gates run: gofmt clean (my files); go build ./... PASS; go vet
./internal/initdb ./cmd/goopg PASS; go test ./internal/initdb (112s) +
./cmd/goopg (20s) full pkgs PASS; make ralph-state-guard (run before status block).

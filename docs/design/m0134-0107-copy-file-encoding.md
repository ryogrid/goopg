# M0134-0107 — copyencoding.sql: COPY-to-file + COPY ENCODING wiring

Status: PASS (100% parity). Sized 2026-08-24.

## Case

`postgres/src/test/regress/sql/copyencoding.sql` exercises server-side
`COPY ... TO 'file'` / `COPY ... FROM 'file'` (no leading backslash — this
runs on the server, unlike psql's client-side `\copy`) combined with the
`ENCODING` COPY option and the `client_encoding` GUC, checking that:

- Reading UTF8 bytes back in as `LATIN1` never errors (every byte value is a
  valid LATIN1 character).
- Reading the same UTF8 bytes back in as `EUC_JP` DOES error, since the raw
  UTF8 encoding of U+3042 (`0xE3 0x81 0x82`) is not a structurally valid
  EUC_JP byte sequence.

Diff at HEAD: every statement in the file failed, either with "COPY to/from
file is not supported" (COPY TO) or a bogus open-path error (COPY FROM via
the psql variable `:abs_builddir`, unresolved because the test runner never
exported `PG_ABS_BUILDDIR`).

## Root causes found (three engine gaps + one test-infra bug)

1. **`COPY ... TO 'file'` was entirely unimplemented.** `RunCopyTo`
   (`internal/executor/copy.go`) called `rejectFileEndpoint`, which
   unconditionally errors `"COPY to/from file is not supported"` for
   `CopyEndpointFile`/`CopyEndpointProgram` regardless of direction. The FROM
   direction already had a working file path (`RunCopyFromFile`, added
   earlier) that bypasses this check entirely by constructing the executor
   directly — but nothing analogous existed for TO. Fixed: extracted the
   row-encoding loop out of `RunCopyTo` into `runCopyToCore`, and added
   `RunCopyToFile` (opens the file with `os.Create`, drives `runCopyToCore`
   with an `emit` that writes to a buffered file writer instead of wire
   `CopyData` frames). Wired into both COPY-dispatch sites in
   `internal/postmaster/copy.go` (the top-level query path and the inline
   multi-statement-batch path), mirroring the existing
   `plan.Endpoint == CopyEndpointFile` branch already present for
   `CopyFrom`.

2. **COPY FROM's `ENCODING` option (and `client_encoding` GUC) were parsed
   and validated but never applied to the byte stream.** `validateCopyOptions`
   (`internal/optimizer/copy.go`) rejects a duplicate `ENCODING` option but
   discards the value — nothing downstream ever converted a byte. This
   happened to be invisible for `LATIN1` (every byte 0x00-0xFF is a valid
   LATIN1 character, so "reinterpret as LATIN1 then insert" and "insert
   as-is" both raise no error) but is directly observable for `EUC_JP`,
   which has real grammar. Fixed: `resolveCopyFromEncoding`
   (`internal/executor/copy.go`) resolves a source PG encoding ID — an
   explicit `ENCODING` option wins over the session's `client_encoding` GUC,
   matching PG's `BeginCopyFrom`/`ProcessCopyOptions` precedence — and
   `CopyFromExecutor.PushLine` runs `mb.DoEncodingConversion(line, srcEnc,
   PG_UTF8, mb.BuiltinLookup)` before decoding whenever a non-UTF8 source is
   resolved.

3. **EUC_JP had no conversion proc registered in `internal/utils/mb` at
   all** (only LATIN1/LATIN2 existed, proc OIDs 4374/4375/4492/4493).
   Added `internal/utils/mb/euc_jp.go`: `eucJPVerifyChar` is a direct port of
   `pg_eucjp_verifychar` (`postgres/src/common/wchar.c:1102-1150`) — the
   structural EUC_JP grammar check (SS2/JIS-X-0201, SS3/JIS-X-0212,
   0xA1-0xFE two-byte JIS X 0208, else ASCII). `euc_jp_to_utf8`/
   `utf8_to_euc_jp` walk the source running this check per character; on an
   invalid sequence they raise `ErrInvalidEncoding` with the actual failing
   bytes attached, matching PG's `report_invalid_encoding_int`
   (`mbutils.c:1832`) byte-count/format exactly (`0xe3 0x81`, using the
   lead-byte's *expected* length the same way PG's `pg_encoding_mblen`
   helper does for the error message, not the length that turned out to be
   invalid).

   Scope note: a character that passes the structural grammar check but has
   no real translation (because goopg has no JIS X 0208/0212↔Unicode mapping
   table — see deferral ledger) is reported `ErrUntranslatableChar` (22028)
   rather than silently emitting non-UTF8 bytes into a UTF8-declared column.
   No case in the current regress corpus exercises a genuinely valid
   non-ASCII EUC_JP round-trip, so this was safe to defer.

   `ErrInvalidEncoding` (`internal/utils/mb/wchar.go`) gained a `Bytes
   []byte` field and its `Error()` was rewritten to render PG's
   `invalid byte sequence for encoding "X": 0xNN 0xNN ...` format (quoted
   encoding name, up to 8 hex bytes) instead of the previous bare `invalid
   byte sequence for encoding X` — no existing caller depended on the old
   format (grep confirmed nothing outside the `mb` package read `.Byte`/
   `.Len`, and no test asserted the exact string).

4. **CONTEXT line wiring.** `ExecError` already had an unused `Context`
   field with wire-protocol plumbing (`execErrDetailFields` in
   `internal/postmaster/copy.go` already turned it into the `FieldWhere`
   ('W') error field) — nothing populated it or passed
   `execErrDetailFields(err)` at the two `RunCopyFromFile` call sites. Added
   a `lineNo` counter to `CopyFromExecutor` (bumped once per `PushLine`
   call, matching PG's `cur_lineno`) and a `copyContext()` helper rendering
   `"COPY <table>, line <N>"`; both file-endpoint COPY FROM error paths in
   `internal/postmaster/copy.go` now pass `execErrDetailFields(err)...`.

5. **Test-infra bug (not a goopg engine gap):** `scripts/pg-regress-runner.sh`
   exports `PG_ABS_SRCDIR`/`PG_LIBDIR` but never `PG_ABS_BUILDDIR`, which
   `pg_regress.c:735` also sets (`setenv("PG_ABS_BUILDDIR", outputdir, 1)`).
   `copyencoding.sql`'s `\getenv abs_builddir PG_ABS_BUILDDIR` therefore left
   the psql variable `abs_builddir` unset, so every `:'utf8_csv'` reference
   stayed the literal string `":abs_builddir/results/copyencoding_utf8.csv"`
   — a bogus path, unrelated to goopg's COPY implementation. Fixed by
   exporting `PG_ABS_BUILDDIR` (same value as `PG_ABS_SRCDIR`, since this
   runner doesn't separate build/src trees) and `mkdir -p`'ing its
   `results/` subdirectory before running tests. This was blocking, in
   principle, every regress case that does server-side
   `COPY ... TO/FROM :'filename'`, not just this one.

## Verification

`scripts/pg-regress-runner.sh --verbose copyencoding`: 0/1 → 1/1 PASS (100%
parity). `go test ./internal/utils/mb/... ./internal/executor/...
./internal/postmaster/... ./internal/optimizer/...` and the full
`RALPH_PRECOMMIT_SCOPE=units` gate both pass.

## Resume point

The EUC_JP↔UTF8 real character-mapping tables (JIS X 0208/0212, ~11,000
lines combined across `postgres/src/backend/utils/mb/Unicode/
{euc_jp_to_utf8,utf8_to_euc_jp}.map`) are not ported — see the deferral
ledger row for M0134-0107. Any future regress case exercising a genuinely
valid non-ASCII EUC_JP character will need that table; the same gap likely
recurs for other EUC_*/SJIS/BIG5/GBK pairs goopg has not yet wired, all of
which follow PG's identical `LocalToUtf`/`UtfToLocal` radix-tree pattern.

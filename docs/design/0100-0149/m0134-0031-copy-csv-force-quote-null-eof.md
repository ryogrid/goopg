# M0134-0031: CSV COPY TO must force-quote fields that collide with the NULL marker or the EOF marker

Status: contained fix landed (this slice). Case remains `failed` overall — see
Remaining gaps below; `.ralph/deferral_ledger.md` carries the rest of
`copy2.sql`'s buckets.

## Problem

PostgreSQL's CSV output writer (`postgres/src/backend/commands/copyto.c`,
`CopyAttributeOutCSV`, ~line 1300-1350) force-quotes a field even when none of
the ordinary CSV special characters (delimiter/quote/newline) are present, in
two specific situations:

1. **The field's text rendering equals the session's CSV null representation**
   (`cstate->opts.null_print`, default `""` for CSV format). Since CSV
   represents NULL as the bare null string and an actual empty string also
   renders as nothing, PG disambiguates by force-quoting the empty string so
   `""` on the wire unambiguously means "empty string", never NULL.
2. **The field is the row's only column and its text equals the literal
   two-byte sequence `\.`** (the old COPY end-of-data marker). Without
   quoting, a single-column value of exactly `\.` on its own line would be
   misread as EOF by any COPY FROM re-reading the file.

goopg's `EncodeCopyCsvRow`/`appendCsvField`
(`internal/executor/copy_csv.go:126-153`) only force-quotes a field via the
`FORCE_QUOTE` option list (`f.forceQuoteAll || f.forceQuote[c.Name]`,
`copy_csv.go:140`) or via `appendCsvField`'s own delimiter/quote/newline scan.
Neither of PG's two extra force-quote rules exists, so:

- An actual empty-string value is written identically to NULL (bare nothing
  between delimiters), losing round-trip fidelity for empty-string vs. NULL in
  CSV — a real data-integrity gap, not just a `copy2.sql` cosmetic mismatch.
- A single-column table whose value is literally `\.` is written unquoted and
  would break the ordinary COPY FROM re-read convention.

This was ~20 of `copy2.sql`'s 955 diff lines (rows 296-360 of the diff,
sized by the M0134-0031 researcher pass) — the smallest CONTAINED bucket in
the case; the case's dominant buckets (legacy `WITH DELIMITER AS`/`NULL AS`
option grammar, `COPY FROM stdin WHERE <expr>`, and the `DEFAULT` marker
option) are REFACTOR-tier missing-feature gaps and are NOT part of this
slice — see `.ralph/deferral_ledger.md` M0134-0031 rows.

## Fix

`EncodeCopyCsvRow` (`internal/executor/copy_csv.go:126`) computes the
force-quote flag `fq` today from `FORCE_QUOTE` alone. Extend it to also force
quoting when:

- `s == f.nullStr` (using the CSV format's *actual* configured null string,
  not a literal `""` — `copyToFormatFromOptions` already defaults CSV's
  `nullStr` to `""` at `copy_csv.go:86-87`, and a `NULL '...'` option can
  override it), OR
- `len(cols) == 1 && s == "\\."` (single-column row whose rendered value is
  exactly `\.`).

Both checks are cheap string compares alongside the existing
`appendCsvField` special-character scan; no new option parsing, no grammar
change, no format-detection change. `DecodeCopyCsvRow` (the read side) is
unaffected: a quoted `""` already round-trips as empty string vs. an unquoted
nothing as NULL per existing CSV-quote-state tracking, so this is purely an
encode-side fix — no decode twin required for this specific bucket (verified:
`parseCopyCsvFields` at `copy_csv.go:193` already distinguishes quoted vs.
unquoted null-string matches via `sawQuote` at `copy_csv.go:236`).

## Oracle citation

`postgres/src/backend/commands/copyto.c:1300-1350` `CopyAttributeOutCSV`:

```c
/* force quoting if it matches null_print (before conversion!) */
if (!use_quote && strcmp(string, cstate->opts.null_print) == 0)
    use_quote = true;
...
/* Quote '\.' if it appears alone on a line, so that it will not be
 * interpreted as an end-of-data marker.
 */
if (single_attr && strcmp(ptr, "\\.") == 0)
    use_quote = true;
```

## Remaining gaps (deferred, see ledger)

`copy2.sql` stays `failed` after this slice — 955 → ~935 diff lines. The
untouched buckets, largest first:

1. Legacy `WITH DELIMITER AS 'x' NULL AS 'y'` COPY-option grammar (~90 lines).
2. `COPY ... FROM stdin WHERE <expr>` clause, entirely unparsed (~120+ lines,
   cascading).
3. `COPY ... WITH (DEFAULT '…')` option, entirely unimplemented (~150 lines).
4. Row-field-count error message shape (doubled `COPY:` prefix at
   `internal/executor/copy.go:297` wrapping `internal/executor/copy_text.go:75`)
   doesn't match PG's per-column messages + CONTEXT line (~30 lines).
5. RLS/column-ACL not enforced on `COPY … TO stdout` (~25 lines, two bugs).
6. `FORCE_NOT_NULL`/`FORCE_NULL` validation and wildcard-combination gaps
   (~35 lines).
7. Misc: check-constraint whole-row reference inside COPY's constraint
   evaluator raising spurious `42P01`; a bare `TRUNCATE` inside a plpgsql
   function body reported "unsupported PL/pgSQL statement"; `COPY FREEZE`
   subtransaction-detection silently missing its expected ERROR.

None of these were touched by this slice.

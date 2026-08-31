# 0097-0024g — COPY legacy bare option trail (CSV / HEADER / FORCE QUOTE) + CSV TO-output rendering

Status: accepted
Milestone: M0097-0024 (Port COPY / sequence / identity regress tests)

## Problem

This closes the **sole remaining `copyselect` diff** (2 lines). The
upstream case is:

```sql
copy (select t from test1 where id = 1) to stdout csv header force quote t;
```

with expected output

```
t
"a"
```

goopg failed it for two independent reasons:

1. **Parser.** The parenthesised-query COPY form (`COPY (query) TO …`)
   only accepted the modern parenthesised option list (`WITH (format
   csv, header, force_quote (t))`) or — for the table form — a bare
   legacy trail *only when preceded by `WITH`*. The bare,
   parenthesis-free, `WITH`-less trail (`csv header force quote t`)
   stopped at `csv`, surfacing `expected ';' or end of input (got csv)`.
   PostgreSQL's `gram.y` `copy_options` accepts `[WITH] copy_opt_list`
   for both forms, where `opt_with` is optional and `copy_opt_list`
   includes the legacy CSV column options `FORCE QUOTE`, `FORCE NOT
   NULL`, `FORCE NULL`.

2. **Executor.** `RunCopyTo` only knew TEXT and BINARY format. There was
   no CSV row encoder, no `HEADER` line emission, and no `FORCE_QUOTE`
   handling, so even once the options parsed they had no effect.

## Fix

### Parser (`internal/parser/copy.go`)

- `parseCopy`: the post-endpoint option handling now accepts the legacy
  trail with or without `WITH` and for both the table and query forms.
  The old `else if withConsumed` branch became an unconditional `else`
  that calls `parseCopyLegacyTrail` (which returns an empty list for a
  non-option lookahead, so `COPY … TO STDOUT;` is unaffected).
- `parseCopyLegacyTrail`: new `case "force"` dispatches to
  `parseCopyLegacyForce`, which parses `FORCE QUOTE columnList | '*'`,
  `FORCE NOT NULL columnList`, and `FORCE NULL columnList`. Each is
  normalised to the **same `CopyOption` shape** the modern parenthesised
  form produces (`force_quote`/`force_not_null`/`force_null` with `Star`
  or `Cols`), so the planner/executor interpret legacy and modern syntax
  identically — a sibling-path concern ([[pattern_sibling_paths_must_agree]]).
  `parseColumnNameList` already parses a paren-free comma list, exactly
  what the legacy form needs.

### Executor (`internal/executor/copy_csv.go`, new)

- `copyToFormat` + `copyToFormatFromOptions` interpret the option list
  into TEXT/CSV knobs: `csv`, `header`, `delim`, `quote`, `escape`,
  `nullStr`, and the `forceQuote` set / `forceQuoteAll`. CSV flips the
  defaults to comma delimiter and empty NULL string (TEXT keeps tab /
  `\N`). Unknown options are ignored here — the planner's
  `validateCopyOptions` is the gatekeeper for the table form.
- `EncodeCopyCsvRow` renders a row per CSV rules; `appendCsvField`
  quotes a field when forced or when it contains the delimiter, the
  quote char, or a line break, doubling embedded quote/escape chars
  (mirrors PG `CopyAttributeOutCSV`). NULL → unquoted null string.
- `appendHeader` emits the column-name header line (CSV-quoted for CSV,
  text-escaped for TEXT; never force-quoted, matching PG).
- `RunCopyTo` computes the format once (when not binary), emits the
  header line before the rows, and dispatches each row to the binary /
  CSV / text encoder.

## Why the query form skips `validateCopyOptions`

`planCopy`'s `s.Query != nil` / `s.QueryDML != nil` branches return early
with `Options: s.Options` and never call `validateCopyOptions` (only the
table form does). That is pre-existing; the executor's
`copyToFormatFromOptions` tolerates anything, so the legacy options flow
through untouched. Tightening query-form validation is out of scope here.

## Tests

- Parser: `TestParseCopyQueryLegacyForceQuoteTrail` (the exact copyselect
  shape), `TestParseCopyLegacyForceVariants` (FORCE QUOTE `*` / col-list,
  FORCE NOT NULL, FORCE NULL).
- Executor: `TestCopyCsvForceQuoteHeader` (header `t` + force-quoted
  `"a"`), `TestCopyCsvDefaultsAndQuoting` (CSV defaults + auto-quote +
  doubled embedded quote + NULL → empty), `TestCopyTextHeaderUnaffectedByCsv`
  (TEXT HEADER stays tab-delimited).
- Verified live on `127.0.0.1:5599`: the query-form case emits `t` then
  `"a"` byte-for-byte; `copy (select id,t …) to stdout csv force quote *`
  emits `"1","a"` / `"2","b"`.

## Result

`copyselect` loses its last 2 diff lines. The COPY-family parser/executor
now accept the legacy CSV option trail uniformly and render CSV output
with HEADER and FORCE_QUOTE.

# 0119-0006 — The CSV reader `COPY … FROM` never had

**Status:** accepted (landed 2026-08-13, M0119-0006 45th slice)
**Source:** deferral-ledger row filed by the 43rd slice (2026-08-12): *"`COPY …
FROM` ignores `FORMAT csv` ENTIRELY"*.
**Oracle:** PostgreSQL 18.3, `postgres/src/backend/commands/copyfromparse.c`
(`CopyReadAttributesCSV`, `CopyReadLineText`, `NextCopyFrom`), measured live on
the reference cluster (port 65432) on 2026-08-13.

## 1. The defect

`internal/executor/copy_csv.go` had a write side only — `EncodeCopyCsvRow` /
`appendCsvField`, added by M0097-0024 for `COPY … TO`. There was no reader, and
nothing routed to one: `CopyFromExecutor.PushLine` called `DecodeCopyTextRow`
unconditionally, and interpreted exactly one option (`NULL`).

So `COPY t FROM STDIN WITH (FORMAT csv)` split each line on TAB as COPY TEXT.
The failure was not subtle — an unquoted `plain,7` into a two-column table
raised `COPY: row has 1 fields, expected 2`. A session could not read back what
it had just written with `COPY … TO … (FORMAT csv)`, and the option was accepted
by the planner throughout, so nothing warned.

## 2. What upstream does that the TEXT reader cannot be reused for

CSV is not "TEXT with a different delimiter". Four rules differ, and the ones
this slice implements are:

| rule | COPY TEXT | COPY CSV |
|---|---|---|
| escaping | per-byte backslash escapes (`\t`, `\NNN`, `\xHH`, …) | none; only the escape character INSIDE a quoted section |
| NULL | field content equals the null string | field content equals the null string **and the field was not quoted** |
| record | one physical line | a quoted field may contain the record terminator |
| quoting | absent | a quoted section can open and close mid-field, so `"ab"cd` is one field `abcd` |

The NULL rule is the one that silently corrupts data rather than erroring, and
it is the reason `parseCopyCsvFields` tracks `sawQuote` per field: with the CSV
default null string (empty), `,,` is two NULLs while `"",""` is two empty
strings. Measured on the oracle, not inferred.

## 3. Where the record boundary is decided

Upstream splits the two concerns: `CopyReadLineText` tracks the quote state to
find the true end of a record and only ever hands a COMPLETE record to
`CopyReadAttributesCSV`. goopg's wire layer already split `CopyData` payloads on
`'\n'` before the executor sees anything, across four call sites
(`internal/server/copy.go` ×3, `tablesync.go`, plus `RunCopyFromFile`).

Rewriting that splitter CSV-aware would have touched every one of those sites.
Instead the re-join lives in the executor, at the single point that knows the
format: `pushCsvLine` parses, and on `errCsvIncompleteRecord` buffers the line
in `csvPartial` and returns without inserting; the next line is appended after
the `'\n'` that the wire layer removed. Two consequences had to be handled
explicitly, because "PushLine returned nil" no longer means "a row was
inserted":

- **End of stream.** A record left inside a quoted field is now reported by a
  new `Finish()`, called at both `CopyDone` sites and at the end of
  `RunCopyFromFile`. Upstream's message is `unterminated CSV quoted field`.
- **The `\.` marker.** Inside a quoted field `\.` is DATA. The oracle proves it:
  feeding `"unterminated` + `\.` yields `unterminated CSV quoted field` with the
  `\.` swallowed into the field, not a clean end-of-data. The wire layer now
  consults `InCsvQuotedField()` before honouring the marker.

A CR immediately before an embedded newline survives inside the quotes (upstream
folds CR/LF only when *not* in a quoted field); only the record's own terminator
is trimmed, by `trimCopyLineCR`.

## 4. Collateral corrections

- **`HEADER` on input was never honoured**, in either format. Upstream discards
  the first line for TEXT as well as CSV, so the skip sits in `PushLine` ahead of
  the format split rather than in the CSV arm. `HEADER match`'s *name
  validation* is still not implemented — deferred with a ledger row.
- **The two constructors had diverged by construction.** `NewCopyFromExecutor`
  and `RunCopyFromFile` each hand-built the struct and each read only the `NULL`
  option; the file endpoint would therefore have kept ignoring CSV even after
  the STDIN endpoint learned it. Both now go through `newCopyFromExecutor`, and
  the executor holds one `copyToFormat` — the same struct `COPY … TO` uses, so
  the two directions cannot interpret the option list differently.
- **Field-count messages.** The CSV reader reports upstream's two distinct
  messages (`extra data after last expected column`, `missing data for column
  "b"`). The TEXT reader keeps its goopg-shaped message; unifying them is a
  ledger row, as is the `CONTEXT: COPY t, line N: "…"` line goopg omits from
  both.

## 5. Verification

`internal/executor/copy_csv_read_test.go` covers the field grammar against the
oracle transcript; `internal/server/copy_csv_from_test.go` drives the real
executor path end-to-end (`startCopyExecServer`; `startTestServer` cannot reach
the decoder at all — see the 44th slice's ledger row) for the basic grammar, the
embedded newline, `HEADER`, and the three errors. All six server tests were
verified red by deleting the two-line route in `PushLine`, each reporting the
exact symptom the ledger row filed: `COPY: row has 1 fields, expected 2`.

The oracle session of §1–§3 was replayed against a live goopg on port 5533 and
matches PG byte for byte, including the per-column NULL flags and all three
error messages.

Gates: `go test ./internal/executor/ ./internal/server/`,
`RALPH_PRECOMMIT_SCOPE=units`, `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35),
pgbench smoke via the commit hook.

## 6. Still missing (ledger rows filed)

`FORCE_NOT_NULL` / `FORCE_NULL` (both CSV-input-only, both fully validated by
the planner), `ON_ERROR` / `LOG_VERBOSITY` / `REJECT_LIMIT`, `CONVERT_SELECTIVELY`
and `ENCODING` are accepted by the planner and then ignored by the reader;
`HEADER match` skips without validating the names; the `CONTEXT` line and the
TEXT-path field-count messages are not PG-shaped.

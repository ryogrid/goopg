# 0103-0016: `publication_names` Quoted-Identifier Unquoting (M0103-0008 rung 11)

Status: accepted
Owner: rung-11 of M0103-0008
Date: 2026-05-14

## Diagnosis

With rung 10 closed by 0103-0015 (publication-table canonicalization),
dropping `t.Skip` on `TestPort_PgoutputInteropGoopgToPG` produces the
same observable symptom — apply worker connects, stays alive past
`wal_receiver_timeout`, no rows replicate. Diagnostic logging added
to `buildPublicationFilter`, `runLogicalWalsender` (filter contents),
and `PgOutput.Change` shows two distinct facts:

1. `execCreatePublication` correctly stores `Tables = ["public.t"]`
   (rung-10 fix verified end-to-end against the live harness).
2. `runLogicalWalsender`'s `buildPublicationFilter` is invoked with
   `pubNames = [`"p"`]` — the publication name itself carries embedded
   double-quotes. `PubSub.LookupPublication(`"p"`)` returns
   `ok=false` because the registry key is `p`, so the resulting filter
   has `byTable = {}` and `allTablesAllowed = {false,false,false}`.
   Every decoded change is then rejected.

The libpq logical-replication client (`libpqwalreceiver`) issues

```
START_REPLICATION SLOT "g2pg_sub" LOGICAL 0/<lsn>
    (proto_version '4', publication_names '"p"')
```

— each publication name inside the `publication_names` option value is
wrapped in double-quotes so names containing commas remain safe to
split. Upstream PG's pgoutput plugin parses the option via
`SplitIdentifierString(rawstring, ',', &data->publication_names)`
(`postgres/src/backend/utils/adt/varlena.c`), which:

- Splits on `,` outside of quoted regions.
- Strips the surrounding `"..."` and collapses doubled `""` into a
  single `"` inside the identifier.
- Lowercases unquoted identifiers via
  `downcase_truncate_identifier`.

goopg's `splitPublicationNames` (`internal/server/logicalwalsender.go`)
shortcut to `strings.Split(raw, ',')` + `strings.TrimSpace`, which
keeps the surrounding quotes verbatim. The mismatch is silent because
the function's only consumer treats "no publication matched" as
"empty filter" rather than as a configuration error — preserving the
v0 "no filter ⇒ pass everything" behaviour that empty `publication_names`
also exercises. Net effect: with at least one publication requested
but the names quoted, every change is dropped.

## Fix

Port `SplitIdentifierString`'s semantics into Go, specialised for the
`','` separator that `publication_names` always uses. The new
`splitPublicationNames`:

- Walks the input rune-by-rune.
- On a leading `"`, reads a quoted identifier, treating `""` as an
  escaped `"` literal and the next bare `"` as the terminator.
- Otherwise reads an unquoted identifier until the next separator
  or whitespace, then `strings.ToLower`s it (Go's stdlib lowercase
  is a close-enough analogue to PG's `downcase_truncate_identifier`
  for the ASCII identifiers libpq emits).
- Tolerates whitespace around each entry, between separator and
  next entry, and a trailing comma is rejected (mirrors upstream's
  return-`false` path).
- Returns `nil` on any syntax error so the caller stays on the
  "no publication matched, empty filter" path — upstream rejects
  malformed `publication_names` at SUBSCRIPTION DDL, well before
  the wire-level slot is started, so a malformed value here means
  a probe sent it directly and the caller-side "drop everything"
  behaviour is the safest fallback.

A small helper `unicodeIsSpace` mirrors `scanner_isspace` (space,
tab, newline, CR, FF, VT) instead of `unicode.IsSpace`, so the
split matches upstream byte-for-byte for the inputs libpq emits.

## Why this matters before rung 12

The cluster-log evidence after rung 10 was misleading. With every
change silently rejected, the failure mode was indistinguishable from
"the pgoutput emission layer never ran" — yet the actual byte stream
was empty for a much simpler reason. Closing rung 11 first exposes
the *next* layer of bugs (rung 12): the `SlotDecoder.Classify`
dispatch has no cases for `RecordKindHeapHotUpdate` (kind 13) or
`RecordKindPageImage` (kind 1, emitted by the first INSERT into a
freshly-allocated heap page), so UPDATE and the first INSERT of a
session are still dropped at the classifier stage even with the
filter open. That work has its own design doc once rung 12 lands.

## Tests

- `internal/server/logicalwalsender_test.go::TestSplitPublicationNamesQuotedIdentifiers`
  pins each `SplitIdentifierString`-equivalent shape that libpq
  actually emits:
    - single quoted name `"p"` → `["p"]`
    - multiple quoted names `"p","q"` → `["p","q"]`
    - doubled-quote escape `"a""b"` → `[`a"b`]`
    - unquoted lowercased `Foo` → `["foo"]`
    - whitespace tolerance `  "p"  ,  "q"  ` → `["p","q"]`
    - empty input `""` → `nil`
    - syntax errors (unterminated quote, trailing comma, empty unquoted)
      → `nil` (caller falls back to "no filter")

The live `TestPort_PgoutputInteropGoopgToPG` stays `t.Skip`d with the
rung-11 diagnosis quoted verbatim so rung 12 can resume from the
exact failing surface.

## Cross-references

- 0103-0015 (rung 10): publication-table canonicalization — the
  pre-requisite that makes `pub.Tables == ["public.t"]` so the
  filter has the right value once the lookup key is correct.
- 0103-0014 (rung 9): logical-walsender keepalive + slot
  RestartLSN off-by-one — the prerequisite for the connection
  staying alive long enough for the silent filter rejection to be
  the observable failure mode.

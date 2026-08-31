# M0119-0006 (64th slice) — `jsonb` is canonicalised at input, not echoed verbatim

Closes the two 56th-slice deferral rows that named `jsonb` canonicalisation
(ledger 2026-08-13): goopg stored whatever JSON text the client handed it and
rendered that text back unchanged, where PostgreSQL parses the value into a tree
and re-emits `jsonb_out`'s canonical form on every read. This slice closes the
**text** divergence at the input boundary. The **heap-image** divergence — goopg
stores the text varlena where upstream stores a `JsonbContainer`/`JEntry` tree —
is a separate, larger storage-format slice and stays open.

## The defect

`'{"b":1,"a":2}'::jsonb` renders as `'{"b":1,"a":2}'` on goopg and as
`{"a": 2, "b": 1}` on PG 18.3. The divergence is on every non-canonical literal,
not just key order:

| axis | PG (`jsonb_out`, `jsonb.c` `JsonbToCStringWorker`) | goopg (before this slice) |
|---|---|---|
| object keys | ordered length-then-bytewise (`lengthCompareJsonbStringValue`, `jsonb_util.c`) | input order |
| duplicate keys | collapsed last-wins (`uniqueifyJsonbObject` keeps the highest-`order` pair) | kept, both rendered |
| whitespace | `: ` and `, ` separators, none around braces | input verbatim |
| numbers | `numeric_in` → `numeric_out` (`1e0`→`1`, `3e1`→`30`, `1.00` keeps scale, `-0.0`→`0.0`) | input verbatim |
| strings | `escape_json_char` (`json.c`): `\b\f\n\r\t\"\\` + `\u00xx` for control bytes, non-ASCII verbatim | input verbatim |

All three input boundaries — the `::jsonb` cast, a `jsonb` column's coercion, and
the binary-COPY wire — went through a bare pass-through, so none validated and
none canonicalised. `'not json'::jsonb` succeeded silently and raised 22P02 only
on a later `->`.

## The fix

One `canonicalizeJSONB` function plus three one-line wiring sites.

- `internal/executor/jsonb_canonical.go` — `canonicalizeJSONB(s string) (string,
  error)`. It parses with `encoding/json`'s `Token()` stream (so object member
  order and duplicates survive, unlike `Decode` into `map[string]any`; strings
  arrive unescaped and numbers arrive as `json.Number`), then re-emits
  `jsonb_out`'s compact form:
  - `canonicalJSONBObjectPairs` walks input order backwards to keep the LAST
    occurrence of each key, then sorts by `lengthCompareJSONBKey` (length, then
    `strings.Compare` — bytewise).
  - `canonicalizeJSONBNumber` round-trips `pgnodes.NumericBodyFromText` →
    `NumericTextFromBody` (the existing numeric_in/numeric_out port), so `-1.5e300`
    expands to its 302-character numeric_out form exactly as PG does.
  - `appendJSONBEscaped` reproduces `escape_json_char` byte-for-byte, including
    passing non-ASCII through verbatim (which is where `encoding/json`'s default
    marshaler diverges: it HTML-escapes and re-escapes non-ASCII).
  - malformed input, or more than one value (`{} {}`), returns the 22P02
    `invalid input syntax for type json` error.
- `codec.go` `coerceTextLikeDatum` — a `jsonb` arm returns `canonicalizeJSONB(s)`;
  this is the single column-coercion point, so `INSERT`/`COPY FROM` (text and
  binary) all canonicalise through the heap encode (`encodeValuePG` → default arm
  → `coerceTextLikeDatum`).
- `expr.go` `evalCast` — a `case "jsonb"` arm canonicalises the `::jsonb` cast.
- `copy_binary.go` `datumToCopyBinary` — the jsonb COPY-TO arm now canonicalises
  before prepending the version byte, so `jsonb_send` ships the canonical text
  (matching `jsonb_send(jsonb_out(v))`), not the input spelling.

`json` (text) is deliberately untouched at every site: `json` preserves the input
spelling by definition, and `json_send`/`json_out` do not canonicalise.

## Sibling audit (Hard-won Rule #2)

| path | verdict |
|---|---|
| `copyBinaryToDatum` jsonb arm | unchanged — strips the version byte, validates via `isValidJSONText`, returns the raw text; the heap encode (`coerceTextLikeDatum`) canonicalises it on the way in, matching the bpchar pattern (`copyBinaryToDatum` has "deliberately no bpchar arm"). |
| `evalJSONArrow` (`->`/`->>`) | unchanged — re-encodes navigated elements; its `json.Marshal` key order is bytewise rather than length-then-bytewise, a pre-existing and separate divergence not in this slice's scope. |
| `TestCopyBinaryJsonbAgreesWithHeapEncode` | its long-input case was 20 concatenated documents, now (correctly) rejected as one-of-many; replaced with one long value. Its other inputs were already canonical, so encode/COPY and heap now agree. |

## Gates

`go build ./...` clean; `internal/executor` PASS (5.9 s); `go test -run
'TestCanonicalizeJSONB|TestJSONBCastAndColumnAreTwins' ./internal/executor/`
PASS; `RALPH_PRECOMMIT_SCOPE=units` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35 — the TPC-H schema carries no jsonb column, so this is a
regression guard); pgbench smoke via the commit hook.

New guards (all byte-identical against PG 18.3 measured on a throwaway
`postgres/local_install`):
- `internal/executor/jsonb_canonical_test.go` — 19 canonicalisation cells
  (key order, last-wins dups, whitespace, number folding, escape round-trip,
  non-ASCII pass-through), malformed-input 22P02, and the cast/column twins with
  the `json` verbatim control.

## Deferred

- The `jsonb` **heap image** remains a text varlena where upstream stores a
  `JsonbContainer`/`JEntry` tree (`typstorage 'x'`) — a hosted PG reading a
  goopg `jsonb` column off disk reads the leading `{` as a JEntry header. A full
  serialiser + parser for the recursive on-disk format, with its own guard
  corpus; ledger 2026-08-13.
- `evalJSONArrow`'s `json.Marshal` re-encode keeps Go's bytewise key sort (and
  HTML escaping), so `->` on an object with mixed-length keys still diverges from
  `jsonb_out`; out of scope for the input-boundary slice.

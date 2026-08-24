# M0134-0120 — `encoding.sql`: sizing + two contained fixes (rune-aware column-hint edit distance, `::json` cast validation)

Status: **PARKED** (case remains genuinely `failed`; two independent, bounded
root causes fixed; the dominant residual gap is REFACTOR-tier — see below).

## What the file tests

`postgres/src/test/regress/sql/encoding.sql` is PG 18.3's multi-byte-encoding
regression case. It runs entirely against `getdatabaseencoding() = 'UTF8'`
(goopg's only server encoding, so the file never `\quit`s early) and exercises
three unrelated things end to end:

1. **Corrupted-string semantics** (the bulk of the file, ~230 of 247 lines):
   `length`/`substring`/`reverse`/`regexp_replace`/`convert_to` behavior on
   deliberately-truncated or NUL-containing multi-byte UTF-8 strings, and
   direct `pg_mblen`-family unit tests via `test_mblen_func`. Every single one
   of these depends on `test_bytea_to_text`/`test_text_to_bytea`/
   `test_mblen_func`/`test_text_to_wchars`/`test_wchars_to_text`/
   `test_valid_server_encoding` — six `CREATE FUNCTION ... AS :'regresslib'
   LANGUAGE C STRICT` functions loaded from PG's `regress.so` test harness
   library (`postgres/src/test/regress/regress.c`), used to synthesize the
   invalid byte sequences (`'\xc3'`, `'\xc300'`, etc.) the corrupted-string
   tests operate on and to call `pg_mblen`/`pg_encoding_mblen` variants
   directly.
2. **mb↔wchar round-trip fuzzing** (`test_encoding` plpgsql wrapper over
   `test_text_to_wchars`/`test_wchars_to_text`/`test_valid_server_encoding`,
   another 6 C functions) across LATIN1/EUC_JP/EUC_CN/EUC_TW/UTF8/
   MULE_INTERNAL byte sequences.
3. **TOAST substring incomplete-char detection** (bug #19406 regression):
   inserting/updating a toasted `text` column with a dangling multi-byte tail
   and checking `SUBSTRING` either surfaces or doesn't surface an
   `invalid byte sequence` error depending on whether the incomplete
   character falls inside the requested slice. This block also calls
   `test_bytea_to_text` to append the dangling bytes.
4. **Two encoding-adjacent one-liners with no C dependency at all** (the
   file's tail): a `U&"..."` Unicode-escape identifier typo that should
   fuzzy-hint at a real column, and a `::json` cast on a string of repeated
   non-ASCII characters that should be rejected as invalid JSON syntax.

## Sizing

`scripts/pg-regress-runner.sh --verbose encoding` against the PG 18.3 oracle:
0/1 PASS, 0% parity, 361-line diff, `^+ERROR` 2 / `^-ERROR` 9 at first live
run.

## Root cause #1 (REFACTOR-tier, not attempted): `LANGUAGE C` functions are stubs

`internal/executor/operators_ddl.go` (search `LANGUAGE C: store as a stub`)
accepts `CREATE FUNCTION ... LANGUAGE C` syntactically but has no C-function
executor — goopg is a from-scratch Go binary with no dynamic-library loader,
no `regress.so`, and no FFI boundary matching PG's `fmgr` C-call ABI. Calling
one of these stub functions returns SQL NULL. This is why every value the
first two blocks of the file compute — `test_bytea_to_text('\xc3')`, every
`wchars`/`round_trip` column in the `test_encoding` NOTICE trace, every
`test_mblen_func(...)` result — comes back NULL/failed on goopg regardless of
the underlying `internal/utils/mb` multi-byte logic's correctness. This blocks
essentially the entire file (blocks 1-3 above, ~230+ of 247 SQL lines) and is
squarely out of scope for a single contained fix: it would require a genuine
C-shared-library-or-equivalent execution engine, a multi-milestone feature on
its own. No attempt was made to shim individual `regresslib` functions with
Go-native equivalents (that path was considered and rejected — it would only
mask the real gap for this one file without generalizing to any other
regress case that also loads `regress.so`, e.g. `copyencoding.sql`'s already
completed M0134-0107 port deliberately worked around the same C-function wall
by testing COPY-path multi-byte behavior directly rather than through
`regresslib`).

## Root cause #2 (CONTAINED, fixed): byte-based, not character-based, column-suggestion edit distance

The file's next-to-last statement:

```sql
-- Levenshtein distance metric: exercise character length cache.
SELECT U&"real\00A7_name" FROM (select 1) AS x(real_name);
```

`U&"real\00A7_name"` is a Unicode-escape delimited identifier that decodes to
the column reference `real§_name` (§ = U+00A7, 2 bytes in UTF-8). PG's
oracle output:

```
ERROR:  column "real§_name" does not exist
LINE 1: SELECT U&"real\00A7_name" FROM (select 1) AS x(real_name);
  ^
HINT:  Perhaps you meant to reference the column "x.real_name".
```

goopg produced the `ERROR`/`LINE`/`^` block correctly but omitted the `HINT`
line entirely. Two independent gaps combined to cause this:

1. **The unqualified-miss fallback never attempted a fuzzy-match hint.**
   goopg has two near-identical column-not-found error sites — the analyzer's
   `resolveColumnRefType` (`internal/parser/analyzer/analyzer.go`) and the
   planner's `resolveColumnRef` (`internal/optimizer/planner.go`) — twin
   input boundaries per the project's Hard-won Rule #2 (sibling code paths
   must agree). Both already had a working fuzzy-hint helper
   (`suggestAnalyzerColumnHint`/`suggestColumnHint`, edit-distance-1 against
   a single relation's columns) but it was wired **only** into the
   *qualified*-reference miss path (`t.col` where `t` matched a relation but
   `col` didn't). The final fallback for an *unqualified* miss (`x.Table ==
   ""`, reached after walking every lexical scope with no hit) returned a
   bare `42703` with no hint at all — even though PG's own
   `errorMissingColumn` (`parse_relation.c`) scans the *same* local
   FROM-clause namespace for a near-miss regardless of whether the original
   reference was qualified, and hints with the RTE-qualified name of
   whatever it finds (`"x.real_name"` here, even though the query wrote a
   bare, unqualified identifier). Fixed by adding
   `suggestColumnHintAllBindings`/`suggestAnalyzerColumnHintAllRels` — both
   scan the local (non-`qualifiedOnly`) bindings/relations of the resolve
   context, reusing the existing single-relation hint helpers, and are called
   from the unqualified-miss fallback in both files.
2. **The edit-distance-1 comparators (`columnEditDistance1` /
   `analyzerColumnEditDistance1`) compared BYTES, not characters.** Both
   implementations (previously byte-for-byte identical between the planner
   and analyzer copies) walked `len(a)`/`len(b)` and indexed `a[i]`/`b[i]` —
   Go string byte semantics. `"real_name"` is 9 bytes; `"real§_name"` is 11
   bytes (§ is a 2-byte UTF-8 sequence) — a byte-length gap of 2, so the
   `lb-la > 1` guard rejected the pair as more than one edit away, even
   though it is exactly one CHARACTER insertion away. This is precisely what
   the SQL file's own comment flags — `"Levenshtein distance metric: exercise
   character length cache"` — a direct reference to PG's `varstr_levenshtein`
   (`fuzzystrmatch.c`, reused by the ruleutils column-suggestion machinery),
   which measures character distance via a cached `pg_mbstrlen`, not byte
   length. Fixed by rewriting both comparators to operate on `[]rune` instead
   of raw string bytes (same edit-distance-1 algorithm — equal-length
   substitution-count check, or single-rune deletion scan for a length gap of
   exactly one — just measured in runes).

Both fixes were required together: fixing only the wiring (#1) without the
rune-awareness (#2) would still reject this specific pair as "too far", and
fixing only the comparator (#2) without the wiring (#1) would never reach the
suggestion code for an unqualified miss.

## Root cause #3 (CONTAINED, partially fixed): `::json` cast performed no syntax validation

The file's last statement:

```sql
-- JSON errcontext: truncate long data.
SELECT repeat(U&'\00A7', 30)::json;
```

PG oracle:

```
ERROR:  invalid input syntax for type json
DETAIL:  Token "§§§§§§§§§§§§§§§§§§§§§§§§§§§§§§" is invalid.
CONTEXT:  JSON data, line 1: ...§§§§§§§§§§§§§§§§§§§§§§§§
```

goopg's `::json` cast (`evalCast` in `internal/executor/expr.go`) had **no
`"json"` case at all** — the switch fell through to the default pass-through
arm (`return d, nil`, unchanged text, no validation whatsoever), so a
30-character run of bare `§` characters — not valid JSON by any reading (not
a quoted string, not a number, not `true`/`false`/`null`, not an
object/array) — was silently accepted. This is a real, general `json`-type
correctness bug, not just an artifact of this one test: PG's `json_in`
(`json.c`) validates every `json` input for syntax, it just doesn't
re-serialize the way `jsonb_in` does (jsonb parses into `JsonbContainer` and
`jsonb_out` re-emits canonically; `json` stores the input text verbatim after
confirming it parses). goopg already had this split half-built: the
`::jsonb` cast arm canonicalizes via `canonicalizeJSONB` (M0119-0006,
validating as a side effect), and `coerceTextLikeDatum`'s `jsonb` column-
storage arm shares the same call — but the parallel `json` arms in both
functions had been left as bare pass-through with a comment explicitly
noting `json` is "untouched" (true for canonicalization, silently also true
for validation, which was the bug).

Fixed by extracting `validateJSONText` (in `internal/executor/
jsonb_canonical.go`) — the same `parseJSONBValue`/trailing-EOF parse-and-check
`canonicalizeJSONB` already used, minus the canonical-rendering step, so it
reports the same `invalidJSONBError` (`22P02 invalid input syntax for type
json`) without altering the text — and wiring it into both twin boundaries:
`evalCast`'s new `"json"` case and `coerceTextLikeDatum`'s new `tname ==
"json"` branch (`internal/executor/codec.go`), mirroring the existing
`jsonb` arms in both files per Hard-won Rule #2. `evalCast`'s `json` arm
deliberately does **not** set `ExecError.Pos` (unlike every other 22P02 arm
in the same function): PG's json parser reports this class of error via
`DETAIL`/`CONTEXT` (`json_ereport_error`, not `errposition`), so it never
emits a `LINE`/`^` marker — checked against `numeric.c`'s pattern (which DOES
set `errposition` and DOES get a `LINE` marker in `numeric.out`) to confirm
this is a genuine per-error-class difference in PG, not an oversight; the
wire layer (`internal/postmaster/copy.go`) only emits `LINE` when
`ExecError.Pos > 0`, so leaving `Pos` at its zero value suppresses it.

**Not attempted** (still parked, this file's residual diff): the `DETAIL`
(`Token "..." is invalid.`) and `CONTEXT` (`JSON data, line 1: ...` with
its own truncation/`...`-prefix rule for long inputs) lines. Reproducing
these byte-for-byte requires porting PG's `json_errdetail`
(`postgres/src/backend/utils/adt/json.c`) — a dedicated formatter that
re-derives the specific invalid token text and its surrounding context window
from the `JsonLexContext`'s error state, with its own truncation heuristic
for long tokens — which is a distinct, self-contained follow-up (not
REFACTOR-tier, but bigger than this loop's remaining budget) rather than a
one-line addition to `validateJSONText`.

## Verification

- `go build ./...` — clean.
- `go test ./internal/parser/... ./internal/executor/... ./internal/postmaster/... ./internal/catalog/... ./internal/utils/... ./internal/optimizer/...` — all pass.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — pass.
- `scripts/pg-regress-runner.sh --verbose encoding`: diff 361 → 353 lines,
  `^-ERROR` 9 → 8 (one fewer, no new false positives), `^+ERROR` unchanged at
  2. Both target lines (the `HINT:` line and the JSON `ERROR:` line) now
  match the oracle byte-for-byte; the file otherwise remains 0% parity
  (single-transaction diff — a fully passing case needs every line to
  match), dominated by the LANGUAGE-C gap above.

## Files touched

- `internal/parser/analyzer/analyzer.go`: `resolveColumnRefType`'s
  unqualified-miss fallback now calls the new
  `suggestAnalyzerColumnHintAllRels`; `analyzerColumnEditDistance1` rewritten
  rune-based.
- `internal/optimizer/planner.go`: `resolveColumnRef`'s unqualified-miss
  fallback now calls the new `suggestColumnHintAllBindings`;
  `columnEditDistance1` rewritten rune-based.
- `internal/executor/jsonb_canonical.go`: new `validateJSONText`.
- `internal/executor/expr.go`: `evalCast` gains a `"json"` case.
- `internal/executor/codec.go`: `coerceTextLikeDatum` gains a `"json"`
  branch.

## Ledger / tracking

- `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0120 (LANGUAGE-C execution
  engine + JSON `DETAIL`/`CONTEXT` formatting, both REFACTOR/follow-up tier).
- `docs/test-port/postgres-oracle-target-inventory.csv`: `encoding` row
  flipped `not-tried` → `failed` (still `pass_required=no`) via
  `make regen-testport`.

# M0135-0001 — SQL/JSON `jsonpath` subsystem (grammar, type I/O, evaluator)

Status: draft
Filed: 2026-08-20, from M0134-0039/0040/0041 sizing findings.

## Problem

Three regress-sql cases are dominated by the same absent subsystem: goopg has
**no jsonpath (SQL/JSON path language) grammar, parser, canonical pretty-printer,
or evaluator anywhere** — only `pg_type`/`pg_proc` catalog scaffolding for the
`jsonpath` type and the `jsonb_path_*` function family.

- **M0134-0039 (`jsonb.sql`)** — `@@`/`@?` jsonpath-match operators and part of
  the `jsonb_path_*` bucket fail (REFACTOR-tier subset of a larger diff that
  also has independent CONTAINED buckets, several already shipped: `#>`/`#>>`
  path-extraction, M0134-0039 itself).
- **M0134-0040 (`jsonb_jsonpath.sql`)** — 817/818 (99.9%) of `^+ERROR` lines
  trace to this one gap: `jsonb_path_query[_tz/_array/_first]`/`_match`/
  `_exists` cataloged in `pg_proc` but zero executor dispatch (717 lines), plus
  `@?`/`@@` entirely unlexed (87 lines).
- **M0134-0041 (`jsonpath.sql`)** — a different *shape* of the same root gap:
  this file never calls `jsonb_path_query`/`@?`/`@@` at all. It exclusively
  exercises `'<text>'::jsonpath` — the type's own input/output functions
  (parse text → tree → canonical pretty-print text). goopg's cast is a bare
  passthrough (store input verbatim, echo unchanged), so ~950/1443 diff lines
  are canonicalization mismatches (no `."key"` quoting, no `?(...)` operator
  compaction, no numeric-literal normalization) and 36 are `^-ERROR` lines
  where PG rejects malformed jsonpath text goopg silently accepts (leading
  zeros, bad digit separators, `last`/`@` used outside their valid context,
  bad regex flags).

Net: this is not one bug fix but a subsystem build, spanning parser, a new
scalar type's I/O functions, and an evaluator — big enough to warrant its own
milestone (M0135) rather than a fourth M0134 park.

## PG oracle

- `postgres/src/backend/utils/adt/jsonpath_scan.l` — lexer: numeric literals
  (decimal/hex/oct/bin, SQL:2023 `_` digit separators — NOT JS separator
  rules), string escapes (`\x`, `\u`, `\u{...}`, `\v`→canonical), keywords
  (`lax`, `strict`, `last`, `true`/`false`/`null`, `is`, `unknown`, `exists`,
  `like_regex`, `flag`, `starts with`, `to`), operator tokens.
- `postgres/src/backend/utils/adt/jsonpath_gram.y` — grammar: path steps
  (`.key`, `."quoted key"`, `[*]`, `[n]`, `[n1,n2 to n3]`, `.*`, `**` recursive
  descent with `{n}`/`{n to m}`/`{last}` quantifiers), arithmetic/comparison/
  boolean expression precedence (needed to reparenthesize `$+1` →`($ + 1)`),
  filter expressions `?(...)`, variables (`$var`), `@` current-item (rejected
  at root context — "@ is not allowed in root expressions"), method calls
  (`.type()`, `.size()`, `.double()`, `.keyvalue()`, `.datetime()`).
- `jsonpath.c:98 jsonpath_in`, `:134 jsonpath_out`, `:521 printJsonPathItem`
  (canonical pretty-printer — the function goopg needs an equivalent of),
  `:239 flattenJsonPathParseItem` (parse tree → binary serialized form).
- `jsonpath_exec.c` — the evaluator (`executeJsonPath` entry point), needed
  for `jsonb_path_query`/`@@`/`@?`, not needed for `jsonpath.sql` itself
  (I/O-only test).

## Design

Four slices, sequenced so the type-I/O half (which alone flips M0134-0041)
lands before the evaluator half (which unblocks M0134-0039/0040). Each slice
is its own coordinator brief when selected.

### S1 — jsonpath lexer + parser + canonical pretty-printer (type I/O only)

New package `internal/executor/jsonpath/` (mirrors the existing
`internal/parser/` split: `lexer.go`/`parser.go`/`ast.go`). Scope:
- Hand-written recursive-descent parser (goopg's SQL parser precedent, not a
  yacc port) producing an AST covering: root (`$`), key/member access
  (`.key`, `.\"quoted\"`), wildcard (`.*`, `[*]`), array subscript/slice
  (`[n]`, `[n1,n2 to n3]`, `last`), filter `?(...)`, arithmetic/comparison/
  boolean operators with PG's precedence table, variables (`$var`), `@`,
  method calls, `like_regex ... flag "..."`, `strict`/`lax` mode prefix.
  Enforce the numeric-literal grammar strictly (reject leading zeros, bad
  separators, trailing junk) and the context rules (`last` only inside `[]`,
  `@` not at root) — this is the source of M0134-0041's 36 `^-ERROR` lines.
- `PrintJsonPath(ast) string` canonical pretty-printer mirroring
  `printJsonPathItem` (quote-if-needed keys, compact `?(...)` spacing,
  reparenthesize arithmetic, normalize numeric literals, lowercase keywords,
  suppress default `lax`).
- Wire `jsonpath_in`/`jsonpath_out` (`internal/initdb/pg_proc_seed_data.go:
  2713-2715` already has the catalog rows; no executor dispatch exists —
  confirmed zero grep hits under `internal/executor/`) to call
  parse-then-print. Store the value as the serialized AST (or the
  canonicalized text — decide at implementation time by how the M0134-0039/
  0040 evaluator slice wants to consume it) rather than the raw input text.
- **Acceptance:** `scripts/pg-regress-runner.sh jsonpath` diff shrinks from
  1443 lines toward the residual that needs `pg_input_error_info`/
  `pg_input_is_valid('jsonpath')` (S4) and the FROM-clause alias bug (tracked
  separately, see "Out of scope" below).

### S2 — jsonpath evaluator core

`internal/executor/jsonpath/eval.go`: tree-walking evaluator over decoded
jsonb (reuse the existing jsonb decode path already used by `evalJSONPathGet`,
`internal/executor/expr.go:1867-2010`, for consistency with the sibling `#>`
operator — do not fork a second jsonb walker). Cover: lax/strict mode
(auto-unwrap arrays in lax mode, error in strict), path step evaluation,
arithmetic/comparison over SQL/JSON values, filter expression `?(...)`
short-circuit, `@`/`$`/named-variable binding, method calls
(`.type()`/`.size()`/`.double()`/`.keyvalue()`/`.datetime()`).
- **Acceptance:** targeted unit tests in `internal/executor/jsonpath/eval_test.go`
  covering each grammar feature against hand-picked PG-oracle-verified
  expected results (not yet wired to SQL functions — S3 does that).

### S3 — wire `jsonb_path_*` functions + `@?`/`@@` operators

- Lex `@?`/`@@` as new 2-char operator tokens (mirror the M0134-0039 `#>`
  precedent: `internal/parser/lexer.go`, new `OpJSON*` OpCodes at `precJSON`).
- Dispatch `jsonb_path_query`/`_array`/`_first`/`_tz` variants (SRF, mirrors
  existing table-function dispatch), `jsonb_path_match`, `jsonb_path_exists`,
  and the `@?`/`@@` operator forms in `internal/executor/expr.go`, all calling
  into the S2 evaluator.
- **Acceptance:** `scripts/pg-regress-runner.sh jsonb_jsonpath` and
  `scripts/pg-regress-runner.sh jsonb` both re-run; the `@@`/`@?`/`jsonb_path_*`
  buckets recorded in the M0134-0039/0040 ledger rows should clear.

### S4 — `pg_input_is_valid`/`pg_input_error_info('jsonpath')` error surfacing

Small slice once S1 exists: `jsonpath.sql` exercises
`pg_input_error_info(text, 'jsonpath')` to check that malformed jsonpath text
produces a structured SQLSTATE/message/detail instead of raising. Requires
`jsonpath_in` to return a `(value, error)` pair the generic
`pg_input_is_valid`/`pg_input_error_info` machinery can catch instead of
propagating (same pattern PG uses for every type's soft-error `_in` variant —
check whether goopg already has this machinery for another type, e.g. numeric
or int4, before building new plumbing).

## Out of scope (separate ledger rows, not part of this epic)

- **FROM-clause single-column SRF alias resolution.** `jsonpath.sql` has
  `FROM unnest(ARRAY[...]) str, LATERAL pg_input_error_info(str,'jsonpath')`
  and goopg errors `column "str" does not exist"` — PG's rule is that a bare
  alias on a single-column SRF (`unnest(...) str`) names both the RTE and the
  sole output column. This is a parser/analyzer FROM-clause bug orthogonal to
  jsonpath and likely affects other `unnest(...) alias` regress cases too.
  File its own M0134 task/ledger row when selected; do not fold into M0135.
- **`jsonb::<numeric type>` cast over-strictness** and **function
  column-definition-list / table-valued JSON SRFs** — both already ledgered
  under M0134-0039 as shared REFACTOR-tier gaps with `json.sql`; independent
  of jsonpath.

## Sibling-path risk

`internal/executor/expr.go:1867-2010` (`evalJSONPathGet`/`OpJSONPathGet[Text]`,
the M0134-0037/0039 `#>`/`#>>` plain path-extraction operators) is **not** the
SQL/JSON jsonpath language — it's PostgreSQL's separate simpler
"array-of-keys" path type. Do not conflate the two or attempt to reuse its
lexer/parser for the jsonpath grammar; only its jsonb-decode helpers
(`jsonElemAsJSONDatum`/`jsonElemAsTextDatum`) are worth sharing with S2's
evaluator.

## Resume / next selection

When M0135 is selected (per the `## Current Priority` banner ordering — it is
filed under M0134's parked-jsonpath-file trio and picked up per normal
milestone ordering, not automatically prioritized over M0134's remaining
single-file tasks), start with S1: it is the smallest slice, independently
verifiable via `scripts/pg-regress-runner.sh jsonpath`, and unblocks
M0134-0041 outright without needing S2/S3.

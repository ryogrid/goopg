# 0118-0100 — JSON accessor operators `->`, `->>`, `#>`, `#>>` (M0118-0009, M0134-0039)

Status: accepted
Milestone: M0118 (Upstream Isolation Spec Suite Pass-Through), task M0118-0009;
extended by M0134-0039 (jsonb.sql sizing) for `#>`/`#>>`
Spec advanced (NOT promoted): `postgres/src/test/isolation/specs/horizons.spec`
Tests: `internal/executor/json_arrow_test.go`, `internal/parser/json_arrow_test.go`

## Summary

Adds the PostgreSQL JSON accessor operators `->` (get element/field as json) and
`->>` (get element/field as text) to the lexer, parser, and expression
evaluator. This is an **enabler, not a promotion** — `horizons.spec` stays
`failed`/`defer` (it has further blockers, below). The same precedent as
0118-0038 (the `INSERT … SELECT` arity fix that enabled but did not promote
`index-only-bitmapscan`): clear one spec blocker cleanly and document the rest.

`->`/`->>` are also a broadly useful, self-contained SQL feature in their own
right — previously a hard lex error (`syntax error at or near "…(got >)"`).

## Why horizons needs it

`horizons.spec` inspects the heap-fetch count of an index-only scan by parsing
`EXPLAIN (FORMAT json, …)` output with a json navigation chain:

```sql
SELECT explain_json($$
    EXPLAIN (FORMAT json, BUFFERS, ANALYZE)
      SELECT * FROM horizons_tst ORDER BY data;$$)->0->'Plan'->'Heap Fetches';
```

`->0` selects the first plan object from the top-level json array, `->'Plan'`
and `->'Heap Fetches'` descend by key. Without `->` the outer `SELECT` fails to
parse before any execution. (Confirmed by probe: the first divergence moved from
the `->` lex error to the *next* blocker once this landed — see "Remaining".)

## Mechanism

goopg has no distinct `json`/`jsonb` Datum kind; both are carried as
`KindString` (text). So the operators are ordinary binary operators whose left
operand is a json-typed string:

1. **Lexer** (`internal/parser/lexer.go`) — the greedy multi-char operator match
   adds `->` to the two-char set and, like the existing `!~` → `!~*` three-char
   special-case, promotes `->` followed by `>` to the three-char token `->>`.
   `--` (comment) is matched earlier, and `->`'s lookahead is `>` not `-`, so
   comment handling is unaffected.

2. **OpCode** (`internal/parser/op.go`) — new `OpJSONGet` / `OpJSONGetText`,
   wired into `ParseBinaryOp` and `String()` (round-trips `->` / `->>`).

3. **Precedence** (`internal/parser/select.go`) — `peekBinaryOp` maps both at a
   new `precJSON = 6`, the "other operators" group (same as `||`), matching PG's
   precedence table where json operators sit below `+`/`-` and above comparison.
   Left-associative chaining (`prec+1` recursion) gives the
   `((j->0)->'Plan')->'Heap Fetches'` shape the spec relies on.

4. **Evaluator** (`internal/executor/expr.go`, `evalJSONArrow`) — `evalBinary`
   already returns SQL NULL when either operand is NULL (correct for json
   operators). Otherwise the left operand is decoded with `json.Decoder` +
   `UseNumber()` so integer/exponent formatting round-trips exactly (the
   "Heap Fetches" value the spec compares is a bare integer). An `int` right
   operand indexes an array (negative counts from the end, PG-style); a
   `text` right operand selects an object field. A non-array with an int key, a
   non-object with a text key, or a missing index/key yields SQL NULL — matching
   PG. `->` re-encodes the navigated element as canonical json (a json `null` →
   the literal text `"null"`); `->>` returns scalars as bare text, a json `null`
   as SQL NULL, and objects/arrays as compact json text.

## Limitations (documented, acceptable for scope)

- goopg has no separate `json` vs `jsonb` storage. `->` re-encodes via
  `encoding/json`, so object key order / insignificant whitespace of the `json`
  (text) type is normalized jsonb-style. The *scalar* surface form is identical
  to PG; only object/array key-order fidelity differs. horizons extracts a scalar
  integer, so this does not affect it.
- The json containment/existence operators (`@>`/`<@` on jsonb, `?`/`?|`/`?&`)
  and the jsonpath match operators (`@@`/`@?`) remain out of scope — none is
  a mechanical mirror of `->`, each needs its own evaluator semantics
  (`@>`/`<@` needs recursive `JsonbDeepContains`-style deep-containment logic;
  `?`-family needs an "existence" evaluator; `@@`/`@?` need a jsonpath
  parser). Sized during M0134-0039 (jsonb.sql), ledgered separately.

## `#>` / `#>>` path-extraction operators (M0134-0039)

Added while sizing `jsonb.sql` (M0134): the case's diff had 38 `^+ERROR` lines
from `#>`/`#>>` lexing as `#` then `>` (a bare `#` is already a lexable
single-char operator token, so `col#>array['a']` tokenized as two operators and
blew up in the parser). PG oracle: `postgres/src/backend/utils/adt/jsonfuncs.c`
`jsonb_extract_path`/`jsonb_extract_path_text` (`#>`/`#>>` are operator aliases
for the same `get_path_all` walk as the function forms, see
`postgres/src/backend/utils/adt/jsonpath_gram.y`... functionally:
`postgres/src/include/catalog/pg_operator.dat` entries for `#>`/`#>>` map
straight to `jsonb_extract_path(jsonb, text[])` /
`jsonb_extract_path_text(jsonb, text[])`).

Mechanism (mirrors `->`/`->>` exactly, one precedence/associativity level
lower is NOT needed — PG gives `#>`/`#>>` the same "other operator" precedence
class as `->`/`->>`, `postgres/src/include/parser/gram.y` operator precedence
table, both in the unnamed op-class bucket):

1. **Lexer** (`internal/parser/lexer.go`) — `#>` joins the two-char greedy
   set (alongside `->`), and `#>` followed by a further `>` promotes to the
   three-char token `#>>`, mirroring the existing `->`/`->>` 3-char bump.
2. **OpCode** (`internal/parser/op.go`) — new `OpJSONPathGet` /
   `OpJSONPathGetText`, wired into `ParseBinaryOp` and `String()`.
3. **Precedence** (`internal/parser/select.go`) — both map to `precJSON`
   (same bucket as `->`/`->>`), left-associative.
4. **Evaluator** (`internal/executor/expr.go`) — the right operand is a text
   array literal (`array['a','0']` or `'{a,0}'::text[]` — goopg carries
   arrays as text, see `isArrayLiteralText`/array-literal parsing already
   used by the box/array `@>`/`<@`/`&&` operators in the same function). The
   evaluator decodes the left json/jsonb text once, then walks the path
   left-to-right reusing `jsonPathStep` (already shared by
   `json[b]_extract_path[_text]`, M0134-0037) for each path element. Final
   result rendering reuses `jsonElemAsJSONDatum` (`#>`) /
   `jsonElemAsTextDatum` (`#>>`) — the exact same tail as `->`/`->>` and the
   `extract_path` builtins. Any path element that doesn't resolve (per
   `jsonPathStep`'s existing not-found rules) yields SQL NULL, matching PG.

## Remaining horizons blockers (spec stays deferred)

Re-probing after this change, the next divergences are, in order:

1. **plpgsql `EXECUTE … INTO STRICT <var>`** — the `explain_json` function body
   uses `EXECUTE p_query INTO STRICT v_ret`; goopg's plpgsql parser currently
   rejects it (`expected ':=' or '=' after "v_ret"`).
2. **`EXPLAIN (FORMAT json, …)` "Heap Fetches"** — goopg's json EXPLAIN
   (`operators_explain.go`) does not emit a `Heap Fetches` field for index-only
   scans.
3. **Pruning-/vacuum-horizon-reflecting heap-fetch counts** — the actual content
   of the spec: an index-only scan's heap fetches must reflect whether dead
   tuples were pruned, and pruning/vacuum must respect a concurrent session's
   older snapshot for a permanent table but not for a temporary one. This is the
   Effort-L MVCC core of the spec.

Recorded in the deferral ledger; `horizons` CSV row stays `failed`.

## Gates

- `internal/parser` JSON-arrow tests (lex `->`/`->>`, left-assoc parse chain,
  `->>` opcode) PASS; `internal/executor` `TestJSONArrow*` (field/index/negative
  index/OOB/type-mismatch→NULL/json-null/`->>`-text/invalid-json 22P02/chained)
  PASS.
- Full `internal/parser` and `internal/executor` unit suites PASS — no
  regression.
- `go build ./...`, `go vet ./internal/parser ./internal/executor` clean.
  `gofmt -l` flags only pre-existing go1.25↔1.26 version-mismatch noise in
  unrelated regions of `op.go`/`lexer.go` (no fmt check in pre-commit; my added
  lines are gofmt-clean).
- pgbench smoke = pre-commit hook.

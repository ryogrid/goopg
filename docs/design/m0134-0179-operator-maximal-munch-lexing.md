# M0134-0179 — operator lexing: maximal munch instead of an allowlist

**Status:** landed 2026-08-29
**Scope:** `internal/parser/lexer.go` (scanner only; grammar and adapter untouched)
**Oracle:** `postgres/src/backend/parser/scan.l:886-990` (`{operator}` rule),
`scan.l:367` (`op_chars`), `scan.l:366` (`self`)

## What was wrong

goopg's scanner recognised multi-character operators from a **hand-maintained
allowlist** of two-character spellings:

```go
case "<=", ">=", "<>", "!=", "||", "<<", ">>", "~*", "!~", "=>",
     "<@", "@>", "&&", "->", "#>":
```

plus three hard-coded three-character extensions (`!~*`, `->>`, `#>>`).
Anything not on the list was emitted **one character at a time**.

PostgreSQL does not work this way. `scan.l` matches `{op_chars}+` — a maximal
munch over the operator alphabet — and then *trims* the run. The alphabet is
open, which is what makes `CREATE OPERATOR` possible at all: an operator name
is any run of

```
~ ! @ # ^ & | ` ? + - * / % < > =
```

An allowlist can never model that, so every operator outside it was
unreachable: `@@`, `~~`, `!~~`, `<->`, `@-@`, `|/`, `||/`, `?|`, `?-|`, `<<|`,
`&<|`, the whole jsonb `?` family, and every user-defined operator.

### The failure was structural, not cosmetic

Two distinct symptoms, both traced to the same split:

1. **`a @@ 'x'` reported `operator does not exist: @`.** The run split into two
   `@` tokens, so the grammar reduced `a @ (@ 'x')` — a **prefix** `@` over the
   literal. `prefixOp` (`support.go:1612`) rejected it. goopg was not
   mis-*naming* an infix operator; it was building a different tree.
2. **`a @@ any ('{wr,qh}')` was a syntax error at `"any"`.** The quantifier
   productions (`a_expr subq_op ANY '(' expr_list ')'`, `pg_grammar.y:2554`)
   take exactly one `subq_op`. Two `@` tokens can never reduce to one, so the
   parser died on the `ANY` that followed. This is the `tsearch.sql` shape that
   surfaced the bug.

Characters that are legal op_chars but appeared in no allowlist entry —
`` ` `` and `?` — never reached the operator case at all and died in the
scanner with **`unexpected character '?'`**, which asserts the character is
illegal in SQL. It is not.

## What landed

A faithful port of the `{operator}` rule. Scan `{op_chars}+` greedily, then
trim in upstream's order:

1. **Truncate at an embedded `/*` or `--`**, whichever comes first — those are
   comment starts and the operator must stop there. A run that *begins* with
   either never reaches this code: `skipWhitespaceAndComments` already consumed
   it, which is upstream's "will match a prior rule, not this one".
2. **Strip trailing `+`/`-`** unless some earlier character is one of
   ``~ ! @ # ^ & | ` ? %``. This is the rule that keeps `a=-1` two tokens
   (`=` then `-`) while `a?-1` stays the single operator `?-`, and it is why
   `a@>-1` is one operator but `a>=-1` is two.
3. **Reject at `NAMEDATALEN` (64)** as an error, not notice-and-truncate —
   upstream reasons an over-long operator is a syntactic mistake anyway.

The single-character-`{self}` reduction and the `<= >= <> != =>` remapping that
`scan.l` performs inline were **already present and correct** in
`internal/parser/adapter.go` (`scanSelfChars`, `namedOperator`). The scanner
therefore still returns `TokenOperator` at every width and lets the adapter
choose the terminal — single-character behaviour is bit-for-bit unchanged.

### The one deliberate divergence: `*`-initial runs

`*` is an op_char upstream, but in this scanner it is claimed earlier by the
`{self}` case it shares with `, ; ( ) . [ ]`. Moving it would entangle the
`.`/`..`/`.5` disambiguation that case also owns. So a run **beginning** with
`*` (`**`, `*/`, `*<`) still splits, while every other position absorbs `*`
normally — which is what `~*`, `!~*` and `#>>` depend on. Recorded in the
deferral ledger.

## What this does NOT fix

goopg's AST models operators as a **closed `OpCode` enum** (`op.go:101`,
`ParseBinaryOp`) with no `pg_operator` catalogue lookup. The 36 spellings it
knows are exactly the old allowlist plus the single characters. So `@@`, `~~`,
`?|` and friends now **lex** correctly and reduce through the right grammar
rule, then fail one layer later with `unsupported operator "@@"` instead of
PG's type-aware `operator does not exist: tsvector @@ unknown` (42883, raised
in parse analysis by `oper()`/`LookupOperName`,
`postgres/src/backend/parser/parse_oper.c:99`).

That is a real improvement — the operator is named correctly, the caret lands
in the right place, and the parse tree has the right shape — but it is not
parity. Ledger row for M0134-0179 carries the resume point; it is the same
blocker already recorded for M0134-0158.

## Measurement

`internal/parser/testdata/parity_goldens.txt` (1.5 MB corpus):
**zero diff** — `TestParityGoldensAreCurrent` PASS. This is the review artifact
required by the parser playbook §Rule 6.

20-case regress A/B (operator-densest cases), before → after:

| metric | before | after |
|---|---|---|
| cases byte-identical | — | **18 of 20** |
| `lex error: unexpected character` (jsonb + geometry) | 79 | **0** |
| `-` lines (expected output goopg failed to produce) | 2529 / 4748 | **2529 / 4748 — unchanged** |
| `^+ERROR` count (jsonb / geometry) | 617 / 155 | **617 / 155 — unchanged** |
| `tsearch` bogus prefix-`@` errors | 163 | **0** (now named `@@`) |

**The `-` line count is the regression metric and it did not move**: nothing
that previously succeeded now fails.

### A trap worth naming

`jsonb.diff` grew 6387 → 6517 and `geometry.diff` 5646 → 5674 — the raw diff
line count moved the **wrong** way while fidelity moved the **right** way. The
growth is exactly 65×2 and 14×2: a one-line lex error (`ERROR: lex error at
byte 36: unexpected character '?'`) became PG's three-line parse-error shape
(`ERROR:` + `LINE 1:` + caret). Raw diff lines are a proxy; `^-` lines and
`^+ERROR` counts are the metrics.

## Guard

`internal/parser/operator_maximal_munch_test.go` — 36 token-stream cases
(asserting `parser.Lex` output, not `Parse`, because most of these operators
still have no `OpCode`), the quantified-`ANY` parse shapes, and the
`NAMEDATALEN` boundary at 63/64. Revert-checked against the pre-fix scanner:
**40 failures**, 0 with the fix.

## Gates

- `go test ./internal/parser/` PASS; `TestParityGoldensAreCurrent` PASS (zero golden diff)
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
- `scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=34 — canonical)
- 20-case regress A/B as above

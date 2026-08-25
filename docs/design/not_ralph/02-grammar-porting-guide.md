# 02 — Grammar Porting Guide (gram.y → goyacc)

This is the working convention set for transcribing
`postgres/src/backend/parser/gram.y` (19,728 lines) into goyacc input.
Rules here are binding for all contributors; deviations require updating
this file in the same commit.

## 1. What upstream actually gives us

* **gram.y** — bison grammar, `%pure-parser`, **`%expect 0`** (zero conflicts,
  enforced), `%name-prefix="base_yy"`, `%locations`, `%parse-param`/
  `%lex-param` (:210-216). Actions build parsenodes via
  `makeNode(T_...)` + `list_make*` helpers. Locations via `@n`.
  **Upstream contains ZERO mid-rule actions** (verified: bison output of the
  real file has no `$@N` synthetic rules), which simplifies porting.
* **kwlist.h** — 494 keywords (ground truth: rows matching `^\s*PG_KEYWORD("`,
  comments excluded; also equals the distinct token count of gram.y's
  `%token <keyword>` block :700-795), each `PG_KEYWORD("name", NAME_P,
  category)` with one of four categories: UNRESERVED / COL_NAME /
  TYPE_FUNC_NAME / RESERVED. The scanner turns every keyword into its own
  terminal token.
* **parser.c `base_yylex()`** — a thin lookahead filter that substitutes
  `_LA` variants: `FORMAT→FORMAT_LA` before `JSON`; `NOT→NOT_LA` before
  `BETWEEN/IN_P/LIKE/ILIKE/SIMILAR`; `NULLS_P→NULLS_LA` before
  `FIRST_P/LAST_P`; `WITH→WITH_LA` before `TIME/ORDINALITY`;
  `WITHOUT→WITHOUT_LA` before `TIME`; plus `UIDENT/USCONST → UESCAPE →
  SCONST` triple handling with escape-syntax validation. This table is
  complete (parser.c:138-161, :195-251). base_yylex additionally injects
  `MODE_*` tokens when invoked in non-default RawParseMode — irrelevant for
  SQL-string parsing today, noted for future plpgsql integration.
  **It does NOT do keyword-category downgrading** — that is a common
  misreading; see §4.
* **scan.l** — tokenizer spec: operators (multi-char, greedy, with
  post-processing rules: embedded `/*`/`--` truncation, SQL trailing
  `+`/`-` stripping, NAMEDATALEN operator-length error at scan.l:893-943,
  :987-988), `$n` params, string families (`'...'`, `E'...'`, `U&'...'`,
  `B'...'`, `X'...'`), idents incl. `"quoted"` and unicode forms, comments,
  `::`, etc.

### Validated toolchain probe (2026-08-25)

A directive-adjusted copy of the **full, unmodified-rule-body gram.y** runs
through goyacc (x/tools v0.49.0): **exit 0, 0 shift/reduce + 0 reduce/reduce
conflicts (identical to bison), 6,501 states, 1.27 MB generated parser**.
Typed `%token <field>`, `%union` struct, char-literal precedence
(`%nonassoc '='`), token precedence (`%left Op`), and duplicate-LHS rule
groups all work. The port is empirically feasible; remaining deltas are
cataloged in §6 and 05-risks.md.

## 2. Token & %union design

goyacc supports typed tokens and a union-equivalent struct. Our `yySymType`
mirrors the *used subset* of PG's `%union` with Go-native types:

| PG union field | Go field | Go type |
|---|---|---|
| `ival` | `ival` | `int` |
| `str` | `str` | `string` |
| `keyword` (`const char *keyword`) | `str` | keyword tokens are **typed** `<str>` with the keyword text as value — upstream does the same (`%token <keyword>` gram.y:700, union field :224, and ColId/ColLabel actions `pstrdup($1)` :17632-17660 depend on it) |
| `boolean`, `chr` | folded into `bool`/`str` | as needed per use-site |
| enum-typed fields: `jtype`, `dbehavior`, `oncommit`, `objtype`, `fun_param_mode`, `setquantifier`, `mergematch`, `retoptionkind` (:228-270) | one field each | corresponding existing goopg enum/const types (or `string` where goopg models the enum as string, as it mostly does today) |
| `node` | `node` | `parser.Node`-family interface (the AST root interface already present) |
| `list` | `list` | `[]any` only where heterogeneous; otherwise typed slices (`stmts []parser.Stmt`, `exprs []parser.Expr`, …) — prefer typed slices; fall back to `[]any` only when a production genuinely mixes kinds (mirrors PG's untyped List) |
| `typnam`, `range`, `defelt`, `sortby`, `windef`, `jexpr`, `alias`, `into`, `with`, `infer`, `ielem`, `objwithargs`, `fun_param`, … | one field each | corresponding existing goopg struct pointer |

Terminal typing follows gram.y exactly (`%token <str> IDENT FCONST SCONST
...`, `%token <ival> ICONST PARAM`, `%token <str>` for every keyword).

## 3. Action translation rules

| gram.y idiom | goyacc action equivalent |
|---|---|
| `$$ = makeNode(CreateStmt)` | `$$ = &ast.CreateStmt{pos: @$}` (existing structs; `pos` from location) |
| `n->x = $2` | `$$.x = $2` (struct literal preferred when cheap) |
| `list_make1(x)` / `lappend(l, x)` | `[]T{x}` / `append($<slice>, x)` |
| `NIL` | `nil` |
| `castNode(X, y)` | direct Go type assertion at construction site |
| `@1` / `@$` | **goyacc has no built-in location tracking and no `@$`** — positions ride in the union (`pos int` fields). Rules with default actions inherit `$1`'s pos naturally; rules WITH explicit actions must seed `pos` themselves (typically from the first meaningful member, e.g. `$$.pos = $2.pos`) — mirror what upstream's `@n` reads actually consume |
| `parser_errposition(@n)` inside actions | not needed — positions ride on nodes; error paths use the shim (01-architecture §6) |
| C convenience calls (`makeString`, `makeIntConst`, ...) | tiny helpers in `support.go`, named identically (`makeString(v)`) so actions stay diffable against upstream text |
| `makeNode(X)` (sets unexported pos) | `parser.NewX(pos, ...)` constructors in `internal/parser/yacc_ctors.go` — the sanctioned seam for seeding unexported position fields from the grammar package |

Style rules:

1. Keep nonterminal names byte-identical to gram.y (`opt_partition_spec`,
   `PreparableStmt`, ...). This makes side-by-side review against the oracle
   trivial and greppable.
2. Every rule block carries a comment `// gram.y:Lnnn-Lmmm` citing the
   upstream range it was transcribed from.
3. Inline actions stay ≤ ~10 lines; anything longer becomes a named helper
   in `support.go` (`buildCreateStmt(...)`) so the .y remains reviewable.
4. No logic in actions beyond node construction and position bookkeeping;
   semantic validation belongs to the analyzer (unchanged boundary).

## 4. Keyword categories are GRAMMATICAL, not lexical

Upstream encodes the four kwlist categories through grammar structure —
plus a fifth orthogonal list: nonterminals `unreserved_keyword` (:17685),
`col_name_keyword` (:18028), `type_func_name_keyword` (:18104),
`reserved_keyword` (:18136), and **`bare_label_keyword`** (:18226, feeding
`BareColLabel` :17665 — required for JSON/object bare-label syntax; a
generator that skips it cannot parse those later). All five list-
nonterminals enumerate their terminals and are referenced wherever a bare
identifier-ish name may appear (`ColId`, `type_function_name`, `ColLabel`,
`BareColLabel`). We port all five verbatim, generated from the same data as
the token table (category array + bare-label membership flag).

The ONLY lexical-context machinery to port is the `base_yylex` lookahead
filter from §1 (`_LA` substitutions + UESCAPE triple). It ports cleanly as a
one-token-pushback wrapper in the `yyLexer` adapter (`base_yylex.go`); goyacc
has no problem receiving a substituted token since we own `Lex()`. The
`UESCAPE` case needs two-token lookahead with validation of the escape
string — ported as-is including its error messages.

Consequence for our lexer: today it classifies identifiers against its OWN
304-keyword table with no categories. P0 replaces this path for the new
parser: `keywords_gen.go` provides all 494 tokens + category array; the
legacy `Kw*` constants remain untouched for the legacy parser until cutover.

## 5. Location tracking

goyacc provides NO automatic location facility (its `yyLexer` interface has
no yylloc). Positions are carried manually:

* Lexer adapter returns `Pos int` (byte offset, same units as today).
* Union carries `pos int` (and `endpos int` where a rule spans tokens).
  Default reductions inherit the first member's pos; explicit actions seed
  positions themselves (see §3 table).
* Error reporting uses the lookahead token's pos, preserving today's
  ErrorResponse `Position` fidelity.

## 6. Precedence & conflicts

* Copy the entire precedence block from gram.y verbatim and in order — it
  sits at :824-903 and starts with set-operation levels (`UNION`/`EXCEPT`
  lowest, then `INTERSECT`, then `OR`, `AND`, `%right NOT`, ...). Every
  entry is a plain token or char literal, nothing bison-only.
* **goyacc does not support `%expect`, and on unresolved conflicts it prints
  `conflicts: N shift/reduce ...` to stderr but still EXITS 0 and emits a
  parser** (verified empirically). Upstream's `%expect 0` safety invariant
  therefore does NOT transfer automatically. Mandatory compensation: the
  `gen-parser` build step greps goyacc's stderr AND `y.output` for
  `conflicts:` and FAILS the build if found. Zero-conflict discipline is
  enforced by that gate.
* Known bison→goyacc deltas to watch (probe results recorded where done):
  `%destructor` unsupported (unused upstream), `%parse-param`/`%lex-param`
  unsupported (thread state via the parser struct / lexer closure instead),
  error-recovery productions unused upstream.

## 7. Extension policy (`goopg_ext.y`)

* Anything that is NOT vanilla-PG syntax goes in `grammar/goopg_ext.y`,
  tagged `// GOOPG-EXT: <reason>` at the rule. Today's survey found NO
  custom syntax — current "extensions" (CompatNoopStmt absorption,
  grant/revoke-as-noop) are *statements upstream has but goopg hasn't
  implemented*, which will be expressed as faithful grammar rules producing
  the same compat stubs until their real executors exist.
* Build mechanics: goyacc input must remain a single well-formed grammar,
  so the Makefile concatenates `grammar/header.y` (`%{ package sqlparser …
  %}` prologue with imports) + `pg_grammar.y` + `goopg_ext.y`, where the ext
  file splices its rule groups BEFORE pg_grammar.y's final `%%`/epilogue.
  Duplicate-LHS rule groups across the splice point are legal in goyacc
  (probed), so extensions can add alternatives to extension points defined
  in the main file (e.g. one extra `stmt: | goopg_statement` alternative).
* Rule count in ext file is reviewed at every phase gate: if upstream grew
  the feature meanwhile, prefer deleting the ext rule.

## 8. Statement coverage target

Parity with goopg's CURRENT supported surface (~35 statement families listed
in parseStatement()'s dispatch), not full upstream breadth. Statements
goopg doesn't implement yet keep their current behavior (parse error or
CompatNoopStmt) — expressed either by simply not yet porting those rules
(preferred during migration waves; dispatch sends them to the legacy
parser) or by porting the rule to produce the identical stub. Full-upstream
breadth remains future work outside this rewrite.

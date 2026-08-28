# M0134-0158 — `publication.sql`: the `OPERATOR(schema.op)` spelling and `COLLATE any_name`

Status: **PARKED** (`publication.sql` CSV row `not-tried` → `failed`); the two
parser gaps this sizing surfaced are **FIXED**.
Date: 2026-08-29.
Task: `.ralph/fix_plan.md` M0134-0158.

## 1. What was measured

`scripts/pg-regress-runner.sh --verbose publication` at `ea2529556`:

| | before | after |
|---|---|---|
| diff lines | 2360 | 2233 |
| `^+ERROR` | 171 | 111 |

The single largest contributor was not a publication feature at all. 69 of the
171 goopg-side errors — 40% — were the identical line:

```
ERROR:  syntax error at or near "OPERATOR"
LINE 6: WHERE c.relname OPERATOR(pg_catalog.~) '^(testpub)$' COLLATE pg_...
```

That is not SQL the test file writes. It is SQL **psql generates**:
`processSQLNamePattern` (`postgres/src/fe_utils/string_utils.c:1121-1152`)
appends

```
<namevar> OPERATOR(pg_catalog.~) '^(pattern)$' COLLATE pg_catalog.default
```

to *every* describe meta-command that is given a pattern. So the failure is not
scoped to `\dRp+ testpub_fortable`; it is every `\d name`, `\dt pat`,
`\di pat`, `\dv pat`, … in the entire regress corpus. `publication.sql` merely
happens to call `\dRp+` 23 times.

`OPERATOR(pg_catalog.~)` is fully qualified on purpose: psql must not be
defeated by a `search_path` that shadows `~`.

## 2. Root cause A — `qual_Op` was never ported

Upstream factors the operator position of an expression through

```
qual_Op:	Op
		| OPERATOR '(' any_operator ')'
```

(`gram.y:16658`), used in exactly four places: `a_expr qual_Op a_expr`
(`:15009`), `qual_Op a_expr` (`:15011`), and the `b_expr` pair (`:15488`,
`:15490`). goopg's grammar had only the bare-`Op` half of all four, so the
parenthesised spelling was a hard 42601 anywhere an operator may appear.

Probe at HEAD (`internal/parser`), every line a `syntax error at or near
"OPERATOR"`:

```
SELECT 1 WHERE 'a' OPERATOR(pg_catalog.~) '^a$'
SELECT 1 WHERE 1 OPERATOR(=) 1
SELECT OPERATOR(pg_catalog.-) 1
```

### The fix

A new `qual_op` nonterminal carrying *only* the parenthesised half:

```
qual_op:
		OPERATOR '(' any_operator_name ')'
			{ $$ = qname{parts: $3.parts, pos: $<p>1} }
```

and four new alternatives — `a_expr qual_op a_expr %prec Op`,
`qual_op a_expr %prec Op`, and the `b_expr` pair.

Three deliberate choices:

- **Separate nonterminal, not a widening of the existing rules.** Upstream
  folds the bare `Op` into `qual_Op`; doing that here would have rewritten four
  proven rules (and `'+' '-' '=' '<' '>'` arrive as *char terminals* with
  alternatives of their own, which upstream reaches through `MathOp`/`all_Op`).
  The parenthesised form is purely additive, and the golden corpus confirms it:
  the regeneration changed **only the statement-count header** — 1639 → 1660
  new pins, zero existing pins altered.
- **`%prec Op` is mandatory.** The rule bodies end in a nonterminal, so without
  it yacc takes the rule's precedence from `')'`. Upstream annotates the same
  four rules for the same reason.
- **Reuse `any_operator_name`** (`grammar/goopg_ext.y:3202`), which is already
  gram.y's `any_operator` minus the >1-qualifier recursion, including `op_run`
  — the lexer splits `<=` and `!~~` into per-character `Op` tokens (scan.l's
  `{self}` set is per-character) and `op_run` rejoins them. Its header comment
  claimed it was "reachable only after DROP OPERATOR, where nothing else can
  follow, so it costs no conflict in expression context"; inside
  `OPERATOR '(' … ')'` the run is bounded by the `')'`, so that still holds.
  The conflict pin stayed at **exactly 59**, on the same token allowlist.

### What the qualifier means here

goopg's AST has no schema-qualified operator node — every operator is an
`OpCode` — so `qualOpName` keeps the spelling and **drops the qualifier**, and
`a OPERATOR(pg_catalog.=) b` produces byte-identical AST to `a = b`
(`TestQualifiedOperatorFoldsLikeBareSpelling` asserts this directly, not just
one golden at a time). Upstream instead resolves the name *in the named
schema* (`LookupOperName`, `parse_oper.c:99`), so `OPERATOR(nosuch.=)` is an
error there and an ordinary `=` here. Ledgered; goopg has no user-defined
operators for the distinction to matter yet.

`qualPrefixExpr` handles the prefix rule. `-` and `+` reach a prefix position
*only* through `OPERATOR(...)` (they have their own char-terminal
alternatives), so they are routed to the same constructors those rules use —
`OPERATOR(pg_catalog.-) 1` folds to `IntegerConst{-1}` exactly like `-1` does
(playbook §12.5's `doNegate` note). Everything else goes through `prefixOp`,
preserving its narrow `{~}` set.

## 3. Root cause B — `COLLATE` took `ColId` where upstream takes `any_name`

Fixing A moved the psql query's failure one token to the right:

```
… OPERATOR(pg_catalog.~) '^(x)$' COLLATE pg_catalog.default
                                              ^ syntax error at or near "default"
```

Upstream's COLLATE operand is `any_name` (`gram.y:14867`), and `any_name`'s
second component is `attrs: '.' attr_name` with `attr_name: ColLabel`
(`:9161`, `:17724`) — the *all-keywords* label. goopg spelled it
`ColId '.' ColId`, and `default` is a RESERVED keyword, so it is unreachable
through `ColId`. `pg_catalog.default` is the only qualified collation psql ever
writes, and it is precisely the one whose second component is reserved.

Changed to `ColId '.' as_col_label` — `as_col_label` being this grammar's name
for upstream's `ColLabel` (it is a separate nonterminal because widening
`ColLabel` itself would make `SET ROLE TO x` ambiguous; see its comment). The
qualifier is still discarded: goopg resolves collations by bare name.

Neither fix alone changes anything observable — the psql query needs both.

## 4. Verification

- `go test ./internal/parser/` PASS. Goldens regenerated: **22 insertions, 1
  deletion**, the deletion being the count header. No existing pin moved.
- New guards in `internal/parser/qual_op_test.go`:
  `TestQualifiedOperatorParity` (binary/prefix × qualified/bare × `a_expr`/
  `b_expr`, plus the exact statement psql sends, plus bare-operator guards),
  `TestQualifiedOperatorFoldsLikeBareSpelling`,
  `TestPatternOperatorSpellingsStillUnsupported`, `TestCollateAnyNameParity`.
- `make gen-parser`: 59 shift/reduce, unchanged pin and allowlist.
- **13-file before/after regress sweep** (HEAD worktree vs. working tree),
  net **−1848 diff lines**, zero regressions:

  | file | before | after | | file | before | after |
  |---|---|---|---|---|---|---|
  | alter_table | 4247 | **3776** | | insert | 1212 | **1161** |
  | inherit | 3689 | **3250** | | identity | 1061 | **1035** |
  | constraints | 745 | **167** | | collate | 555 | **536** |
  | publication | 2360 | **2233** | | matview | 378 | **369** |
  | create_index | 3459 | **3340** | | numeric | 3828 | **3825** |
  | domain | 1759 | **1751** | | dependency | 154 | 156 |
  | create_view | 2496 | 2503 | | | | |

  The two files that grew did so because a query that previously died as a
  3-line syntax error now *executes* and prints a longer (still diverging)
  table. Across all 13 files exactly one new `+ERROR` class appears —
  `function pg_catalog.pg_relation_is_publishable does not exist`, reached only
  because `\dRp+` now parses. `constraints` (−578, 745 → 167) is the largest
  single beneficiary and is unrelated to publications.

## 5. Left undone (see `.ralph/deferral_ledger.md` 2026-08-29 M0134-0158)

1. **`publication.sql` itself is still `failed`** (2233 lines). What remains is
   the CREATE/ALTER PUBLICATION grammar, which is a small subset of upstream's:
   `FOR TABLES IN SCHEMA` (28 errors), row filters `FOR TABLE t WHERE (…)`
   (12), column lists `FOR TABLE t (a, b)` (6), and comma-separated publication
   objects (6). Plus `pg_relation_is_publishable` (9) and
   `publication does not exist` cascades (22) downstream of those.
2. **`qual_all_Op` and `subquery_Op` were not ported.** `ORDER BY x USING
   OPERATOR(pg_catalog.<)` and `x OPERATOR(pg_catalog.=) ANY (…)` remain 42601.
   `qual_Op` is used by exactly the four rules changed here; the other two are
   separate upstream nonterminals, and `sortby`'s existing comment records a
   deliberate decision not to accept the form. Nothing psql sends needs them.
3. **`~~`/`!~~`/`~~*`/`!~~*` are not operators in goopg.** `ParseBinaryOp`
   (`internal/parser/op.go:101`) has no case for them. Worse on the bare path:
   the lexer emits `~~` as two `~` tokens, so `'a' ~~ 'b'` parses as
   `'a' ~ (~ 'b')` and reaches the executor as a regex match over a bitwise
   NOT instead of erroring. `op_run` rejoins the run inside `OPERATOR(...)`, so
   only the qualified spelling gets the honest `unsupported operator "!~~"`.
4. **The operator qualifier is ignored** rather than resolved (§2).

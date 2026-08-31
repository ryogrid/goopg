# M0134-0169 — CTAS's source and a view body take `SelectStmt`, not `select_bare`

**Status:** LANDED (engine-wide grammar fix). `sqljson_jsontable.sql` sized live
(`not-tried` → `failed`) and **PARKED** on the SQL/JSON blocker (ledger 0168a).

**Task:** M0134-0169 (`postgres/src/test/regress/sql/sqljson_jsontable.sql`).

---

## 1. What the case turned out to be

`sqljson_jsontable.sql` is 563 lines / 122 statements, and at HEAD it diverged by
**1347 diff lines / 116 `^+ERROR` / 34 `^-ERROR`**. The `^+ERROR` histogram is
almost pure:

| count | error |
|---|---|
| 90 | `syntax error at or near …` |
| 16 | `relation "X" does not exist` |
| 9 | `view "X" does not exist` |
| 1 | `function json_table does not exist` |

and the 90 syntax errors point at only four tokens — `COLUMNS` ×68, `PASSING`
×12, `AS` ×9, `(` ×1. Every one of the 89 `COLUMNS`/`PASSING`/`AS` failures is
the SQL/JSON `JSON_TABLE` construct, which goopg's grammar does not carry at
all; the 25 `relation`/`view` errors are the cascade from the `CREATE TABLE …
AS SELECT … JSON_TABLE(…)` / `CREATE VIEW` statements that failed to parse.

So the case's dominant cause is the same REFACTOR-tier missing subsystem that
parked M0134-0168 (`sqljson.sql`): the SQL/JSON family, ledgered as **0168a**,
which by construction also gates -0170. That is not sliceable in one loop.

**The one token that was NOT JSON_TABLE** is the remaining `(` — and it is a
genuine, engine-wide grammar bug the case merely exposed.

## 2. The bug

`sqljson_jsontable.sql:37` builds the case's fixture table:

```sql
CREATE TEMP TABLE json_table_test (js) AS
	(VALUES
		('1'),
		('[]'),
		('{}'),
		('[1, 1.23, "2", … ]')
	);
```

goopg answered `ERROR: syntax error at or near "("`. Probed directly, the whole
family was rejected:

```
REJECT "CREATE TABLE t AS (SELECT 1)"
REJECT "CREATE TEMP TABLE t (js) AS (VALUES ('1'), ('2'))"
REJECT "CREATE TABLE t AS (SELECT 1 UNION SELECT 2)"
REJECT "CREATE VIEW v AS (SELECT 1)"
REJECT "CREATE MATERIALIZED VIEW mv AS (SELECT 1)"
REJECT "CREATE TABLE test11a AS (SELECT 1::int AS a)"   -- privileges.sql:963
accept "CREATE TABLE t AS SELECT 1"
```

All of these are legal PostgreSQL 18.3. Upstream's rules take `SelectStmt`,
whose second alternative is `select_with_parens`:

- `CreateAsStmt`: `CREATE OptTemp TABLE create_as_target AS SelectStmt opt_with_data`
  (`postgres/src/backend/parser/gram.y:4807`), and its `IF NOT EXISTS` twin at
  `:4821`.
- `ViewStmt`: `CREATE OptTemp VIEW qualified_name opt_column_list opt_reloptions
  AS SelectStmt opt_check_option` (`gram.y:11287`).

goopg's grammar instead routed all three through `select_bare`, the alternative
that deliberately cannot start with `'('`.

## 3. Why it was there — a legacy limit recorded as a PostgreSQL rule

This is the interesting part, and the reason the bug survived the parser
migration untouched. It was not an oversight; it was **written down as
intentional, with the wrong justification**, in three places at once:

1. `grammar/pg_grammar.y:634` carried the comment
   *"select_bare is legacy's parseSelect: NO leading `'('` — CREATE VIEW's body
   and CTAS's source **reject** `AS (SELECT 1)` there, so they take
   select_bare."*
2. `internal/parser/select_layering_test.go` pinned exactly that, listing
   `"CREATE VIEW v AS (SELECT 1)"` and `"CREATE TABLE t AS (SELECT 1)"` under
   `assertBothReject` with the comment *"select_bare: a view body or CTAS source
   may not START with `'('`."*
3. `internal/parser/testdata/parity_goldens.txt` recorded both as
   `!syntax error at or near "(" …`, so the golden corpus agreed.

All three are faithful records of the **legacy hand-written parser's** limit —
`parseSelect` could not take a leading `'('` — promoted to a statement about
PostgreSQL. Nothing in the chain ever checked `gram.y`. This is the failure mode
CLAUDE.md's *"dead code is not a reference implementation"* names, arriving from
the other direction: the legacy parser is not dead, but it is not the oracle
either, and a guard that pins its answer is only as good as the premise that
went in. Playbook §12 states the rule that decides it — *"When you find a NEW
divergence, decide it against `./postgres/`, not against what the code used to
do"* — and against `./postgres/` these are three plainly legal statements.

## 4. The fix

Four productions change `select_bare` → `SelectStmt`:

| file:rule | upstream citation |
|---|---|
| `grammar/goopg_ext.y` `ctas_source` | `gram.y:4807`, `:4821` |
| `grammar/goopg_ext.y` `create_view_stmt` (both arms) | `gram.y:11287` |
| `grammar/goopg_ext.y` `create_matview_stmt` | `gram.y` `CreateMatViewStmt` |

`ctas_source` serves both CTAS spellings (plain and column-alias) and both the
query and `EXECUTE` sources, so one edit covers `CREATE [TEMP|UNLOGGED] TABLE
[IF NOT EXISTS] t [(cols)] AS (query) [WITH [NO] DATA]` in full.

`grammar/pg_grammar.y:634`'s comment is rewritten to say what is actually true:
`select_bare` is right only where upstream *also* refuses a parenthesised query,
and CTAS/CREATE VIEW are not such places.

**No new grammar conflicts.** `make gen-parser` reports exactly 59 shift/reduce
both before and after — the pinned known-conflict count. This is the outcome
worth recording: the layering that `TestParenthesisedSetOpOperands` documents
(`SelectStmt` / `select_with_parens` / `select_no_parens`, with `%prec UMINUS`
breaking the `')'` reduce/reduce) was built precisely so `SelectStmt` could be
used wherever gram.y uses it. The three call sites were the leftovers, not a
constraint.

## 5. Verification

**Parse level** — all ten forms accepted, ASTs correct: `Parenthesized=true`
stamped, `ColumnAliases=["js"]` and `ValuesRows=[…]` carried through the
column-alias + `VALUES` spelling, `WithNoData=true` still read off the tail.

**Execution level** (live server, cgroup-capped, port 5533) — parsing is not
executing, so every form was run end to end:

| statement | result |
|---|---|
| `CREATE TABLE t1 AS (SELECT 1 AS a)` | 1 row, `a = 1` |
| `CREATE TEMP TABLE json_table_test (js) AS (VALUES ('1'),('[]'),('{}'))` | 3 rows, column named `js` |
| `CREATE TABLE t2 AS (SELECT 1 UNION SELECT 2)` | 2 rows |
| `CREATE TABLE t3 AS (SELECT 1 AS a) WITH NO DATA` | 0 rows |
| `CREATE VIEW v1 AS (SELECT 1 AS a)` | 1 row |
| `CREATE MATERIALIZED VIEW mv1 AS (SELECT a FROM tbase)` | 2 rows |
| `CREATE TABLE t4 AS (SELECT 1 AS a ORDER BY 1 LIMIT 1)` | 1 row |

**Regress A/B**, 15 cases, this tree vs a `HEAD` worktree, headers normalised:

| case | before | after |
|---|---|---|
| `sqljson_jsontable` | 1347 | **1335** (`^+ERROR` 116 → 115) |
| `privileges` | 3885 | **3878** |
| `join` | 20906 | 20906 (content: one estimate line) |
| `subselect` | 2840 | 2840 (content: one estimate line) |
| aggregates, alter_table, create_view, matview, portals, rules, select_into, transactions, union, updatable_views, with | — | **byte-identical** |

**Zero regressions.** The `privileges` delta is exactly the removal of the two
`CREATE TABLE test11a/test11b AS (SELECT …)` syntax errors plus the cascading
`table "test11b" does not exist`. The two "changed but same length" files are
*not* a regression and are worth explaining, because they look like one: both
are the line `->  Values (431 rows)` becoming `->  Values (435 rows)` inside a
hunk that is already a `+` divergence in **both** builds — goopg plans a scan of
the virtual `pg_class` as a `Values` node whose row count is the live relation
count. The regress runner shares one database across the run, `privileges` and
`sqljson_jsontable` run before `subselect`/`join`, and four relations that
previously failed to be created now exist. The estimate moving is the fix's
downstream effect, not a plan regression.

**Guard:** `TestCtasAndViewSourceAcceptsParenthesisedQuery`
(`internal/parser/select_layering_test.go`), ten statements pinned with
`assertParity`. Revert-checked: with the grammar reverted and the parser
regenerated, it fails on all ten, twice each — once as AST drift and once via
`assertParity`'s explicit "is REJECTED but pinned" arm. The two stale
`assertBothReject` entries were removed from `TestParenthesisedSetOpOperands`
and their goldens re-recorded; the golden diff is 23 lines (7 new statements,
2 flipped from `!syntax error …` to a real AST) and is the review artifact for
this change per Hard-won Rule #6.

## 6. Deferred

- **The case itself stays `failed`.** Its remaining 1335 lines are entirely the
  SQL/JSON `JSON_TABLE` subsystem — ledger row **0168a** covers the parser,
  transform and executor work for the whole family; -0169 and -0170 re-arm when
  it lands.
- **`pg_get_viewdef` keeps the parens.** goopg stores a view's `RawDef` as raw
  source text, so `CREATE VIEW v AS (SELECT 1)` round-trips through `\sv` as
  `(SELECT 1)` where PG re-deparses from the parse tree and prints ` SELECT 1 AS
  a;`. This is the *pre-existing* raw-text-vs-deparse divergence (it already
  differs in whitespace, casing and the trailing semicolon on every view), but
  the parenthesised spelling is a newly reachable input to it. Filed as
  M0134-0169a.
- **`COPY (query) TO`'s `copy_inner` still uses `select_bare`.** Not touched
  here: upstream reaches it through a different rule shape and it was not
  exercised by this case. Filed as M0134-0169b to be decided against
  `gram.y`'s `CopyStmt`/`PreparableStmt` rather than assumed.

## 7. Files

- `grammar/goopg_ext.y` — `ctas_source`, `create_view_stmt` ×2, `create_matview_stmt`
- `grammar/pg_grammar.y` — the `SelectStmt` comment block (§3)
- `internal/parser/yacc_parser.go`, `tokennums_gen.go`, `tokens_gen.y`,
  `kwlists_gen.y` — regenerated by `make gen-parser`
- `internal/parser/select_layering_test.go` — new guard, two stale rejections removed
- `internal/parser/testdata/parity_goldens.txt` — 1660 → 1667 statements
- `docs/test-port/postgres-oracle-target-inventory.csv` — `sqljson_jsontable.sql`
  `not-tried` → `failed`

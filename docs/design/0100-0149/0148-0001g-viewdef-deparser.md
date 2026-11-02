# M0148-0001g — pg\_get\_viewdef deparses the view as ruleutils prints it

**Parent:** M0148-0001 `aggregates.sql` regress coverage closure · **Status:**
slice 1 landed `cc0d25b7f` (run\-2 loop #28, 2026\-11\-02); slice 2 (ordered\-set
aggregates) landed `025d72204` (run\-2 loop #29); **closed** 2026\-11\-02 —
breadth residue filed as M0148\-0001g1
**Target:** the aggregates `pg_get_viewdef` hunks (the seven `agg_view1`
variants at expected\-lines 1667\-1756, `aggordview1` at 2317) and the same
function's output in other suites (rules, create\_view)

## Why

PG stores a view's analysed Query and deparses it on demand:
`pg_get_viewdef_worker` → `make_viewdef` → `get_query_def` with
`PRETTYFLAG_INDENT` and `WRAP_COLUMN_DEFAULT` (0). goopg captured the view
body's source text at CREATE VIEW and echoed it, so every viewdef line
differed in case, spacing, qualification, output names and constant labels.

## Slice 1 — a subset port of get\_query\_def

`internal/executor/viewdef_deparse.go` re\-parses the stored body and prints
PG's form; anything outside the subset reports `ok=false` and
`pg_get_viewdef` keeps returning the stored text.

- Layout (`appendContextKeyword` under `PRETTYFLAG_INDENT`): ` SELECT` at
  indent level 8; with wrap column 0 every target after the first starts a
  new line at `PRETTYINDENT_VAR`; ` FROM` is printed at indent 2, ` WHERE` /
  ` GROUP BY` / ` ORDER BY` at 1, ` HAVING` / ` LIMIT` / ` OFFSET` at 0; every
  FROM item after the first goes on its own line.
- Column references are qualified when the range table does not hold exactly
  one entry (`get_query_def`'s varprefix rule); a target prints ` AS name`
  unless it is a plain column of the same name; the names come from the
  view's columns (the result descriptor).
- FROM items (`get_from_clause_item`, `get_rte_alias`,
  `get_column_alias_list`): public relations (others fall back —
  `generate_relation_name` qualifies a relation the search path does not
  reach), subqueries with their alias and user\-written column aliases,
  VALUES subqueries as `( VALUES …)`, `generate_series` with its alias and a
  full column\-alias list (`i(i)` — a function RTE always prints both;
  composite\-returning functions list their attributes, so they fall back).
- VALUES (`get_values_def`): rows joined by `, `, columns by `,`, each
  constant labelled by its column's resolved type (int4 bare, numeric,
  `'foo'::text`, `NULL::integer`); `get_simple_values_rte` — a VALUES view
  whose columns were renamed prints `SELECT column1 AS x, column2 FROM
  (VALUES (1,2)) "*VALUES*"`.
- Expressions: columns, non\-negative int4 literals, operators parenthesised
  (non\-pretty), AND/OR chains flattened, function and aggregate calls with
  DISTINCT, ORDER BY (DESC, NULLS, `USING op NULLS LAST` per
  `get_rule_orderby`) and FILTER.
- Pretty mode (`pg_get_viewdef(view, true)`, psql's `\d+`) is
  `PRETTYFLAG_PAREN`, which elides parentheses by precedence (`isSimpleNode`);
  it is served only for queries without operator expressions, where both
  modes print the same.

### Evidence

- Regress runner, HEAD vs change over 35 suites (the 34 EXPLAIN\-heavy suites
  plus rules): aggregates 314 → 266 diff lines / 21 → 14 hunks (the seven
  `agg_view1` hunks match), rules 2323 → 2300 with no new mismatched line; the
  other 33 unchanged. First attempts printed a composite\-returning function
  as `t(t)` and a non\-public relation unqualified; both now fall back.
- Pin: `TestPgGetViewdefDeparse`.

## Slice 2 — ordered\-set aggregates

`get_agg_expr` prints an ordered\-set aggregate as `name(direct args) WITHIN
GROUP (ORDER BY …) [FILTER (WHERE …)]`, its direct arguments as the analyser
coerced them (`get_func_expr` prints arguments with showimplicit):
`percentile_disc` / `percentile_cont` take double precision, so a literal is
an implicit cast shown with its label (`(0.5)::double precision`); a
hypothetical\-set aggregate's direct arguments take the matching ORDER BY
column's type (`rank(5, 'AZZZZ'::name, 50)`). `orderedSetCall` renders these
shapes; relation RTEs carry their columns' catalog types for the labels.

- Regress runner, HEAD vs change over 35 suites: aggregates 266 → 251 diff
  lines / 14 → 13 hunks (`aggordview1` matches); the other 34 unchanged.
- Pin: `TestPgGetViewdefDeparse` (aggordview1 shape).

## Closure (2026\-11\-02, run\-2 loop #29)

Both aggregates hunk groups the task names match. The deparser's breadth —
pretty mode (psql `\d+`), joins, set operations, CTEs, windows, CASE, casts,
sublinks, literal coercions outside VALUES, search\-path qualification — is
filed as M0148\-0001g1.

## Remaining at closure (moved to M0148\-0001g1)

- `aggordview1`: WITHIN GROUP aggregates and argument coercions
  (`percentile_disc((0.5)::double precision)`, `'AZZZZ'::name`) need the
  function's argument types; string/numeric literals outside VALUES.
- Pretty mode's precedence\-based parenthesisation (`isSimpleNode`).
- Joins, set operations, CTEs, window functions, CASE, casts, sublinks,
  schema qualification by search\-path visibility.

## Follow\-up 2026\-11\-03 — search\-path qualification (f3f0d0d02)

The slice\-1 rule above ("public relations; others fall back") was wrong for
public too: `generate_relation_name` qualifies any relation
`RelationIsVisible` rejects, and pg\_dump runs with an empty search\_path, so
PG dumps `FROM public.foo` where goopg printed `FROM foo`
(AI\-20261103\-004134\-003, `TestPort_PgDumpConnectionSetup`).
`relationVisibleFunc` now ports `RelationIsVisible` over the session's
effective search\_path (pg\_catalog searched first unless listed; a relation
is visible when the first path schema holding its name is its own), and an
invisible relation prints `schema.name`. Non\-public relations no longer fall
back. Pin: `TestPgGetViewdefQualifiesInvisibleRelations`; regress matview
gains the `mvtest_mvschema` viewdef.

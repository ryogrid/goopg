# TODO — goyacc parser rewrite

One checkbox ≈ one commit ≈ one push. Check off only after the item's gate
(04-testing-and-gates.md) is green. Doc review findings (2 reviewers,
2026-08-25, both APPROVE-WITH-CHANGES) are folded in below.

## P0 — Foundation

- [ ] **P0.1 Toolchain**: add `golang.org/x/tools` (goyacc) via `tools.go`
  + go.mod with PINNED version; verify `go run golang.org/x/tools/cmd/goyacc`
  executes; record pinned version next to the conflict gate.
- [ ] **P0.2 Keyword generator**: `cmd/gen-kwlist-go` reads
  `postgres/src/include/parser/kwlist.h` and emits
  `internal/sqlparser/keywords_gen.go` (494 token constants — ground truth:
  rows matching `^\s*PG_KEYWORD("`, comments excluded — + category array +
  bare_label_keyword flag + name→token map). Provenance header. Unit test:
  count == upstream, categories spot-checked, no duplicates.
- [ ] **P0.3 Error-helper relocation** (review finding): move
  syntaxErrorMsg/syntaxErrorCode from internal/postmaster into a leaf spot
  in internal/parser; postmaster consumes from there. Behavior-preserving;
  postmaster tests stay green.
- [ ] **P0.4 Package scaffold**: `internal/sqlparser` with `yyLexer`
  adapter over existing lexer (incl. named-operator splitting per 05 #11);
  error shim through the relocated helpers; entry point takes TOKEN SLICES:
  `ParseOne(stmtTokens []Token, baseOffset int)` — no string re-lexing.
- [ ] **P0.5 base_yylex filter port** (`base_yylex.go`): _LA substitutions +
  UESCAPE triple (+ stubbed MODE_* branch), table-driven tests for every
  substitution pair and UESCAPE validation error.
- [ ] **P0.6 Skeleton grammar & build**: `grammar/header.y` (prologue) +
  `grammar/pg_grammar.y` (verbatim precedence block :824-903, minimal
  `stmt:` core) + empty `grammar/goopg_ext.y`; Makefile `gen-parser` target
  with MANDATORY conflict grep (fails on 'conflicts:' in y.output/stderr);
  reproducibility check (`make gen-parser` twice ⇒ zero diff); seeded-conflict
  test proving the gate fires; lexer-conformance checklist vs scan.l
  (05 #4/#12 fixtures).
- [ ] **P0.7 Perf baselines** (review finding): micro-benchmarks for Parse/
  ParseExpr (SELECT-heavy / DDL-heavy / expr-heavy inputs); record ns/op +
  allocs/op as the flip-comparison baseline (04-testing §3).
- [ ] **P0.8 Dispatch plumbing**: token splitter (top-level ';' over opaque
  dollar-quote-safe tokens, base offsets preserved), route() table scaffold
  incl. ident-led case-insensitive matching, '(' entry, WITH
  follower-keyword scanner (03 §2); hook wired at the named production site
  (postmaster/server main), default nil = all-legacy; wrapper-invariant
  dispatch unit test.

## P1 — SELECT family

- [ ] **P1.1 select core**: simple_select_no_parens basics — targets, FROM
  one relation, WHERE. Differential harness lands here (difftest corpus
  extractor).
- [ ] **P1.2 FROM clause**: joins (inner/left/right/full/cross/natural),
  aliases+colaliases, subquery-in-FROM, LATERAL, func tables, ONLY/`*`.
- [ ] **P1.3 grouping & distinct**: GROUP BY, HAVING, DISTINCT [ON].
- [ ] **P1.4 window functions**: WINDOW clause, OVER (partition/order/
  frame specs). Flip SELECT core routing incl. '(' parenthesized queries.
- [ ] **P1.5 order/limit**: ORDER BY, LIMIT/OFFSET, FETCH FIRST, targeting
  (`SELECT ... FOR UPDATE` lock clauses).
- [ ] **P1.6 set operations**: UNION/INTERSECT/EXCEPT (+ALL), parens nesting.
- [ ] **P1.7 CTEs**: WITH [RECURSIVE], materialization hints; TABLE/VALUES
  statement forms. Flip full SELECT family routing (WITH routes by follower
  keyword — SELECT only until P3).

## P2 — Expressions

- [ ] **P2.1 operator expressions**: a_expr operators w/ full precedence,
  IS/ISNULL, BETWEEN, IN, LIKE/ILIKE/SIMILAR family, ANY/ALL/SOME, quantified
  comparisons.
- [ ] **P2.2 conditional & set exprs**: CASE, NULLIF/GREATEST/LEAST,
  sublinks (EXISTS/IN/row compares).
- [ ] **P2.3 func_call**: arg modes, ORDER BY/VARIADIC forms, aggregates
  (DISTINCT, star), FILTER, WITHIN GROUP.
- [ ] **P2.4 casts, constructors, params, indirection**: `::`, CAST(),
  ARRAY[]/ROW(), typed literals, parameters `$n`, array/field/composite
  indirection.
- [ ] **P2.5 type names**: Typename port incl. arrays, SETOF, intervals,
  character variants; flip ParseExpr to new parser; run plpgsql suite.

## P3 — DML writes

- [ ] **P3.1 INSERT**: VALUES/SELECT source, ON CONFLICT (index inference,
  DO NOTHING/UPDATE), RETURNING, OVERRIDING.
- [ ] **P3.2 UPDATE**: FROM joins, WHERE CURRENT OF, SET list forms.
- [ ] **P3.3 DELETE / MERGE**: USING joins; MERGE WHEN [NOT] MATCHED actions.
  Flip DML routing; WITH follower routing extends to INSERT/UPDATE/DELETE.

## P4 — DDL wave 1 (tables)

- [ ] **P4.1 CREATE TABLE columns+constraints**: column defs (types, DEFAULT,
  NOT NULL, GENERATED, collation), column + table-level constraints.
- [ ] **P4.2 CREATE TABLE table options**: PARTITION BY / PARTITION OF,
  INHERITS, USING access method, WITH options, ON COMMIT, TABLESPACE,
  AS query, IF NOT EXISTS.
- [ ] **P4.3 ALTER TABLE**: full action list goopg supports.
- [ ] **P4.4 DROP TABLE / TRUNCATE / INDEXes**: CREATE/DROP INDEX incl.
  partial/expression/concurrent flags. Flip wave-1 routing; initdb replay.

## P5 — DDL wave 2 (everything else CREATE/ALTER/DROP)

- [ ] **P5.1 SEQUENCE / VIEW / MATERIALIZED VIEW / REFRESH**
- [ ] **P5.2 FUNCTION / PROCEDURE / AGGREGATE** (dollar-quoted bodies)
- [ ] **P5.3 OPERATOR family / CAST / COLLATION / CONVERSION / TRANSFORM**
- [ ] **P5.4 TYPE / DOMAIN / ENUM / EXTENSION / STATISTICS /
  ACCESS METHOD / SCHEMA**
- [ ] **P5.5 DATABASE / TABLESPACE / FOREIGN* / PUBLICATION / SUBSCRIPTION**
- [ ] **P5.6 COMMENT / SECURITY LABEL / TRIGGER / RULE / POLICY** (review
  finding: these were unnamed in earlier drafts). Flip wave-2 routing;
  initdb replay.

## P6 — Utility statements

- [ ] **P6.1 Transactions**: BEGIN/START/COMMIT/END/ROLLBACK/ABORT/
  SAVEPOINT/RELEASE/ROLLBACK TO/PREPARE TRANSACTION.
- [ ] **P6.2 SET / SHOW / RESET** (incl. SESSION|LOCAL forms, TIME ZONE,
  ROLE, SESSION AUTHORIZATION paths per M0134-0155 semantics).
- [ ] **P6.3 Cursors & prepared stmts**: DECLARE/FETCH/MOVE/CLOSE,
  PREPARE/EXECUTE/DEALLOCATE.
- [ ] **P6.4 EXPLAIN** (+ANALYZE, option lists).
- [ ] **P6.5 Maintenance**: VACUUM/ANALYZE/CLUSTER/REINDEX/CHECKPOINT/COPY.
- [ ] **P6.6 Session & misc**: DO/CALL/DISCARD/LISTEN/NOTIFY/UNLISTEN/
  LOCK (review finding)/GRANT/REVOKE(noop semantics preserved)/role &
  session statements. Flip utility routing.

## P7 — Cutover & deletion

- [ ] **P7.1 Move generated sources into internal/parser; delete
  internal/sqlparser; external import paths unchanged.**
- [ ] **P7.2 Delete legacy recursive-descent files** (select.go, ddl.go,
  dml.go, expr.go, function.go, copy.go, alter/interval parse helpers),
  prune parser.go drivers (keep Parse/ParseExpr entries, token pool, error
  machinery), remove legacy keyword classification + dead AST sweep.
- [ ] **P7.3 Final gates**: units, tpch-spotcheck, tpcds SF0.5, full
  regress sweep ≥ baseline, docs updated (design index), README status.

## Continuous

- [x] Docs reviewed by agent reviewers; findings folded back (2026-08-25:
  architecture reviewer + grammar/tooling reviewer, both APPROVE-WITH-
  CHANGES; all BLOCKER/MAJOR findings incorporated above).

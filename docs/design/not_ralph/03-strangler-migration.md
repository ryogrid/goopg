# 03 — Strangler Migration Plan

## 1. Why strangler (and not big-bang)

The parser surface is ~35k lines feeding 165 AST types consumed by 500+ files
across the engine. A big-bang swap would freeze the whole project for months
and land as one un-reviewable commit. The strangler approach lets every
statement class move independently, gated by tests, with legacy code deleted
incrementally at cutover.

## 2. Dispatch model — deterministic token-stream routing

`Parse()` keeps its signature but its internals change to **token-stream
dispatch**:

1. Lex the WHOLE input once with the surviving lexer (it folds dollar-quoted
   bodies into opaque single tokens, lexer.go:430-464, so top-level `;`
   symbols are unambiguous split points).
2. Split the token slice at top-level `;` — per-statement subslices keep a
   base byte offset so error positions stay globally correct (a string-based
   `ParseOne(input)` would re-lex and reset offsets, violating the error
   contract in 01 §6 for statements after the first).
3. Route each subslice independently:

```
route(stmtTokens []Token) (handled bool, err error)
```

Routing rules (in order):

* **Leading keyword** (`TokenKeyword`) → table entry. Ident-led statements
  (START/DISCARD/FETCH/MOVE/CLOSE/REFRESH/GRANT/REVOKE/LISTEN/... which
  arrive as `TokenIdent`) match case-insensitively on the ident value.
* **Parenthesized queries**: leading `(` routes to the SELECT family (at
  statement level upstream only allows select-with-parens there) — flipped
  at P1.4.
* **WITH-led statements**: routing scans past the balanced CTE list and
  routes on the FOLLOWER keyword (SELECT→P1; INSERT/UPDATE/DELETE→P3).
  Naive WITH→sqlparser at P1 would break today-working `WITH ... INSERT`
  until P3 (BLOCKER from doc review). The scanner is trivial: track paren
  depth over tokens, stop at first non-consumed keyword.
* **Wrapper invariant**: wrapper statements (EXPLAIN/PREPARE/DECLARE/COPY
  (query)/CREATE TABLE AS) route in a wave ≥ their inner statement's wave;
  inner statements are never routed independently out of an unrouted
  wrapper.

Design points retained:

* **Deterministic**: a class routes to the new parser ONLY when ported and
  gated. No try-new-fallback-on-error (masks bugs, double-parses, makes
  errors depend on which parser won).
* The hook is set explicitly by production code — since nothing imports
  `internal/sqlparser` in server binaries, an init() there never runs;
  P0.6 names the wiring site (postmaster/server main) so flips cannot be
  inert-in-prod while green-in-tests. Default nil = all legacy.

## 3. Wave order and contents

Each wave = one or more TODO.md checkboxes = one or more commits.

| wave | statement classes (leading keywords) | notes |
|---|---|---|
| P1 | SELECT / TABLE / VALUES / WITH (+ set ops, CTEs) | longest pole; sub-phased: core select_no_parens first |
| P2 | expressions & type names (feeds ParseExpr too) | a_expr/c_expr/func_call/case/cast/sublinks/array/$n |
| P3 | INSERT / UPDATE / DELETE / MERGE (+ ON CONFLICT, RETURNING) | |
| P4 | CREATE TABLE / ALTER TABLE / DROP / INDEX / constraints / TRUNCATE | ddl.go wave 1 |
| P5 | SEQUENCE / VIEW+MATVIEW / FUNCTION+PROCEDURE / TYPE+DOMAIN / OPERATOR / AGGREGATE / CAST / COLLATION / CONVERSION / EXTENSION / FOREIGN* / PUBLICATION+SUBSCRIPTION / STATISTICS / ACCESS METHOD / SCHEMA / DATABASE / TABLESPACE / COMMENT / SECURITY LABEL | ddl.go wave 2 + remaining CREATE/DROP/ALTER families goopg supports |
| P6 | BEGIN..PREPARE TRANSACTION / SET+SHOW+RESET / cursors (DECLARE/FETCH/MOVE/CLOSE) / PREPARE+EXECUTE+DEALLOCATE / EXPLAIN / VACUUM+ANALYZE / CLUSTER+REINDEX / CHECKPOINT / COPY / DO / CALL / DISCARD / LISTEN+NOTIFY / GRANT+REVOKE / role & session auth statements | utility statements; GRANT/REVOKE keep producing today's CompatNoopStmt semantics until their milestone lands |

Flipping a wave requires (see 04-testing-and-gates.md):

1. differential AST harness: new-vs-legacy outputs identical on the wave's
   entire existing test corpus + regress-runner SQL for those statements,
2. full `go test ./internal/parser/...` green,
3. units suite green,
4. regress-runner overall pass-rate not lower than pre-flip baseline.

## 4. Cutover and deletion (P7)

Cutover moves generated + support sources into `internal/parser`, deletes the
transitional package, and removes the legacy recursive-descent engine.

Deletion list (verified against imports at execution time):

* `internal/parser` recursive-descent files: `select.go`, `ddl.go`,
  `dml.go`, `expr.go`, `function.go`, `copy.go`, alter/interval parsing
  helpers — i.e. every file whose content is statement/expression parsing.
* `parser.go` is PRUNED, not deleted: the `Parse`/`ParseExpr` entry points,
  token pool, and trailing-token error machinery survive (01 §6/§7); only
  the statement-driver and per-statement functions go.
* Lexer keyword classification path (its own 304-word table) once the
  adapter feeds from keywords_gen; the lexer itself SURVIVES as yyLexer's
  front end.
* Dead AST nodes / helper funcs found via reference sweep afterwards
  (`go vet`, grep-driven; each removal cited in the commit message).

Final gates before declaring done: full units suite, tpch-spotcheck
(canonical Q12/Q13 row counts), tpcds SF0.5 regression gate, full
pg-regress-runner sweep with pass-rate ≥ baseline, pgbench smoke (hook).

## 5. Rollback story

* Every flip is one commit; `git revert <commit>` restores routing to legacy.
* Until P7 the legacy parser stays complete and runnable, so any wave can be
  reverted independently with zero coordination.
* The dispatch hook defaults to nil (= all legacy); infrastructure commits
  before P1 are inert by construction.
* Revert window: post-flip fix commits that touch the same wave land within
  one working session of the flip; after that window, corrections go
  forward as new commits (reverting a settled wave requires re-reading its
  gate status first).

## 6. Non-goals

* No analyzer/planner changes. No new SQL features. No performance tuning
  beyond not regressing (perf watchpoint: TPC-H Q12/Q13 spotcheck unchanged).
* plpgsql's own parser is untouched; we only preserve what it imports.

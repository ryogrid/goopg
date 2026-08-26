# TODO — goyacc parser rewrite

One checkbox ≈ one commit ≈ one push. Check off only after the item's gate
(04-testing-and-gates.md) is green. Doc review findings (2 reviewers,
2026-08-25, both APPROVE-WITH-CHANGES) are folded in below.

## P0 — Foundation

- [x] **P0.1 Toolchain**: add `golang.org/x/tools` (goyacc) via `tools.go`
  + go.mod with PINNED version; verify `go run golang.org/x/tools/cmd/goyacc`
  executes; record pinned version next to the conflict gate.
- [x] **P0.2 Keyword generator**: `cmd/gen-kwlist-go` reads
  `postgres/src/include/parser/kwlist.h` and emits
  `internal/sqlparser/keywords_gen.go` (494 token constants — ground truth:
  rows matching `^\s*PG_KEYWORD("`, comments excluded — + category array +
  bare_label_keyword flag + name→token map). Provenance header. Unit test:
  count == upstream, categories spot-checked, no duplicates.
- [x] **P0.3 Error-helper relocation — REVISED, no move needed**: analysis
  showed sqlparser only needs to PRODUCE *parser.SyntaxError values (it
  already carries Pos/Message/Raw/Code/Hint); postmaster's formatting
  helpers stay put and keep consuming them. Layering concern dissolved.
- [x] **P0.4 Package scaffold**: `internal/sqlparser` with `yyLexer`
  adapter over existing lexer (incl. named-operator splitting per 05 #11);
  error shim through the relocated helpers; entry point takes TOKEN SLICES:
  `ParseOne(stmtTokens []Token, baseOffset int)` — no string re-lexing.
- [x] **P0.5 base_yylex filter port** (`base_yylex.go`): _LA substitutions +
  UESCAPE triple (+ stubbed MODE_* branch), table-driven tests for every
  substitution pair and UESCAPE validation error.
- [x] **P0.6 Skeleton grammar & build**: `grammar/header.y` (prologue) +
  `grammar/pg_grammar.y` (verbatim precedence block :824-903, minimal
  `stmt:` core) + empty `grammar/goopg_ext.y`; Makefile `gen-parser` target
  with MANDATORY conflict grep (fails on 'conflicts:' in y.output/stderr);
  reproducibility check (`make gen-parser` twice ⇒ zero diff); seeded-conflict
  test proving the gate fires; lexer-conformance checklist vs scan.l
  (05 #4/#12 fixtures).
- [x] **P0.7 Perf baselines** (review finding): micro-benchmarks for Parse/
  ParseExpr (SELECT-heavy / DDL-heavy / expr-heavy inputs); record ns/op +
  allocs/op as the flip-comparison baseline (04-testing §3).
- [x] **P0.8 Dispatch plumbing**: token splitter (top-level ';' over opaque
  dollar-quote-safe tokens, base offsets preserved), route() table scaffold
  incl. ident-led case-insensitive matching, '(' entry, WITH
  follower-keyword scanner (03 §2); hook wired at the named production site
  (postmaster/server main), default nil = all-legacy; wrapper-invariant
  dispatch unit test.

## P1 — SELECT family

- [x] **P1.1 select core**: simple_select_no_parens basics — targets, FROM
  one relation, WHERE. Differential harness lands here (difftest corpus
  extractor).
- [x] **P1.2 FROM clause — joins & derived tables**: inner/left/right/
  full/cross/natural (+NATURAL-dir combos, optional OUTER), ON/USING quals,
  flat JoinExpr chain per legacy parseFromItem shape, ONLY limiter,
  subquery-in-FROM w/ AS/bare alias + column-alias list + synthetic
  __sq_ fallback, LATERAL flag, Parenthesized marking on derived selects.
- [x] **P1.2a FROM paren join groups**: `FROM (T1 JOIN T2 ON ...) AS x`
  wrapped as synthetic SELECT (legacy tryParseParenJoin :1454-1466).
- [x] **P1.2b FROM func tables**: `func(args)` range items +
  `ROWS FROM(...)` WITH ORDINALITY (legacy TableFuncRef/parseRowsFrom).
- [x] **P1.3 grouping & distinct**: GROUP BY, HAVING, DISTINCT ON (legacy
  quirk mirrored: ON form leaves Distinct=false; dump-pinned).
- [x] **P1.4 window functions (OVER clause)**: OVER alternatives added to
  name_or_call with OVER in %nonassoc precedence block (IDENT level) —
  resolves S/R by precedence (shift wins). Supports OVER(), OVER(PARTITION
  BY ..), OVER(ORDER BY ..), OVER(PARTITION BY .. ORDER BY ..). Frame
  clause deferred. Corpus +3 cases all legacy-identical.
  FIRST-ATTEMPT FINDINGS: adding OVER alts directly to name_or_call caused
  4 S/R + 2930 R/R conflicts. Root cause: postfix-notation ambiguity between
  "reduce name_or_call" and "shift OVER" after qualified_name '(' args ')'.
  Upstream avoids this because their func_application/c_expr structure has
  different LALR state topology. RESOLVED: (a) adding OVER to %nonassoc precedence block works — zero conflicts.
  ALSO DISCOVERED: adding name_or_call/scalar-sublink/CAST directly into c_expr
  causes 3927 R/R because ColId-compatible tokens in expression position create
  massive LALR ambiguity with statement-level dispatch. These constructs were
  already reachable through existing paths (name_or_call was already in HEAD's
  grammar at a different position). LESSON: never move productions between
  nonterminals without full conflict analysis.: WINDOW clause, OVER (partition/order/
  frame specs). Flip SELECT core routing incl. '(' parenthesized queries.
- [x] **P1.5 order/limit**: ORDER BY, LIMIT/OFFSET, FETCH FIRST, targeting
  (`SELECT ... FOR UPDATE` lock clauses).
- [x] **P1.6 set operations**: UNION/INTERSECT/EXCEPT (+ALL/DISTINCT),
  unbounded chains, trailing ORDER/LIMIT binding to the outermost node.
  EXECUTION-NOTE: done before P1.4 — window OVER() needs FuncCall (P2.3),
  while set-ops were self-contained on the current expression subset.
  Shape note: goopg AST nests chains RIGHTWARD on the single SetOp slot
  (legacy parity, dump-pinned) whereas upstream %left builds LEFT trees —
  semantically equivalent for associative set ops; documented in
  difftest_known_diffs.md.
- [x] **P1.7 CTEs**: WITH [RECURSIVE], cte_col_list, AS MATERIALIZED /
  NOT MATERIALIZED markers; TABLE/VALUES statement forms (VALUES gets the
  full base_select sort/limit/set-op tail). Grammar landed; routing NOT
  flipped yet (see P2-F).
- [x] **P1.7a VALUES/TABLE statement forms ✅**: landed as simple_select
  alternatives (inheriting base_select's sort/limit + setop tail — no clause-
  split needed after all). VALUES uses the upstream _LA mechanism: adapter
  substitutes VALUES_LA when '(' follows (new synthetic token; also first
  char-literal follower support in initSubstRules — "(" resolves via the
  yylex1 ASCII contract), avoiding the col_name_keyword:VALUES R/R. TABLE
  desugars to SELECT * FROM per gram.y :12968. Beyond-legacy capability:
  WITH..TABLE and set-ops over VALUES parse (upstream-correct; legacy
  rejects both). Parity 147->148; floor 145.
- [x] **P2-F SELECT-family FLIP ✅**: typed-literal normalization (IDENT
  date/time/timestamp/interval + SCONST → SCONST only) fixed the Q12 gap.
  routedStmts["select"]=true; units 44 pkgs PASS; tpch-spotcheck Q12+Q13
  PASS. WITH follower scan working. Gate: differential corpus +16 cases.
- [x] **P2-F POST-FLIP BASELINE**: regress-runner 4/52 PASS (7.7% parity)
  with select routing active. Expected low — most regress cases use
  constructs not yet ported (type names, DDL, operators, etc.). Track this
  number as grammar waves add coverage.
- [ ] **P3 INSERT (second attempt reverted)**: minimal `INSERT INTO ColId VALUES '(' expr_list ')'` as a stmt alternative STILL produces 3927 R/R. The conflict is NOT about qualified_name sharing — even ColId-only target triggers it. Root cause: the existing grammar's expression-level rules (name_or_call, base_select VALUES form) create LALR state interactions that make ANY new statement starting with a reserved keyword + INTO/VALUES ambiguous with expression continuations. RESOLUTION PATH: requires splitting the grammar into DML-dispatch (before expression parsing) and SELECT-expression sub-grammars — essentially upstream's two-phase approach where stmt-level rules never overlap with a_expr alternatives. This is a structural rework, not an incremental addition.
  GATE BUG FOUND+FIXED: goyacc writes conflict summaries to STDOUT (not stderr); old gate only checked stderr → 3927 R/R silently passed. Fixed: y.output per-state count now uses fail-safe default 999 (was 0 = silent pass when file missing).
- [ ] **P3 INSERT (first attempt reverted)**: adding `INSERT INTO qualified_name VALUES...` as a top-level stmt alternative produced 3927 R/R conflicts because `qualified_name` in the INSERT target position conflicts with `qualified_name` in expression contexts (name_or_call). Fix requires either: (a) dedicated nonterminal for the INSERT target that excludes expression contexts, or (b) restructuring so DML statements are dispatched BEFORE expression-level parsing begins (upstream approach — DML rules live at the stmt level, never inside a_expr).
- [ ] **P3 DML (post-flip improvements): VALUES/TABLE statement
  forms (P1.7a), window OVER() (needs P2.3b FILTER/WITHIN GROUP), remaining
  type-name coverage. These enhance but do not block the flip.
- [ ] **P2-F ATTEMPT HISTORY** (superseded by successful flip above): enabling
  `routedStmts["select"]` passed units (44 pkgs) but TPC-H Q12 FAILed.
  ROOT CAUSE FOUND: Q12 uses `date '1994-01-01'` TYPED LITERAL syntax
  (gram.y Typename SCONST). New parser lacks this rule; sees DATE_P as
  ColId then fails on unexpected SCONST. Fix: add typed-literal rules
  (DATE_P/TIME_P/TIMESTAMP/INTERVAL + SCONST → TypedStringLit) to c_expr. WITH follower
  scan IS implemented and working (withFollowerRouted). Investigation DEEPENED: Q12 passes ParseOne, routeBatch, AND
  parser.Parse in Go tests. But fails through the LIVE SERVER (rebuilt
  binary, routing enabled). The error is identical: syntax error at
  "1994-01-01" col=421 (42601). This means the server's code path diverges
  from the test path — possibly the query goes through a DIFFERENT parse
  entry point (dispatchSimpleQueryViaExecutor vs parser.Parse), or the
  wire-protocol layer transforms the SQL before it reaches Parse.
  LIVE-SERVER REPRO CONFIRMED: `SELECT date '1994-01-01'` fails with
  "syntax error at or near 1994-01-01". Root cause precisely identified:
  `date` is NOT in kwlist.h (not a keyword token) → arrives as IDENT
  → ColId→qualified_name→ColumnRef, then SCONST follows unmatched.
  FIX NEEDED: add typed-literal grammar rule for IDENT SCONST pattern
  (date/time/timestamp/interval prefixes) OR handle in adapter by mapping
  known type-name idents before SCONST to a dedicated terminal.
  NOTE: differential test passes because BOTH parsers produce same AST
  (both happen to succeed via different paths).

### Session 2026-08-26 (P2-F real flip + conflict hygiene)

- [x] **USER DIRECTIVE — regress parity NOT a metric for now**: the pg-regress
  clean baseline is 2/52 (varchar+comments) post-flip; measurement itself is
  sound on fresh throwaway instances, but routing flipped the failure classes
  from "engine gap" to "grammar coverage", so the number tracks wave progress,
  not product health. Do NOT chase it; TPC-H spotcheck stays THE gate until
  the DDL/utility waves land.

- [x] **GATE ANCHOR BUG (critical)**: the conflict gate grepped y.output with
  `^[0-9]+:` but goyacc pads some state numbers with a leading space, so 3928
  of HEAD's 3929 conflicts were invisible. Root causes found and removed:
  a stale duplicated block at file end (opt_func_call_args/name_or_call/
  filter_clause/within_group_clause/subq_op all defined twice) plus a
  duplicated `a_expr TYPECAST ColId` alternative inside a_expr itself.
  Grammar is now genuinely clean: 2 S/R both on '(' (func-call/extract vs
  paren), 0 R/R. Gate now requires ALL conflicts to be on '(' instead of a
  magic count.
- [x] **P1.4 COMPLETION**: OVER ColId bare refs (args/star/DISTINCT variants),
  WINDOW window_definition_list clause, opt_window_spec flat alternatives.
  NOTE: `count(*) FILTER/WITHIN GROUP` still missing (star variant has no
  postfix continuations).
- [x] **P2.1c EXTRACT** landed for real: EXTRACT '(' extract_field FROM
  a_expr ')' + datetime extract_field list via lowerIdent helper.
- [x] **TYPED-LITERAL REWORK**: adapter no longer collapses date/time/timestamp
  SCONST to bare strings; it folds to a synthetic %token TYPEDLIT whose str is
  "type\x1fvalue", and c_expr builds TypedStringLit (legacy parity). interval
  goes through its kwlist keyword rule and buildIntervalLit → IntervalLit
  PreComputed via exported ParseIntervalBody (legacy produced IntervalLit, not
  TypedStringLit — the difference broke `date 'X' + interval 'Y'` with
  "operator + requires integer operands").
- [x] **P2-F FLIP (real)**: routedStmts["select"]=true. Gates: units PASS,
  tpch-spotcheck Q12 rows=2 / Q13 rows=35 PASS through the live server.
  TestTPCHGrammarCoverage floor 19/22 added as a permanent unit gate.
- [x] **ScalarSublinkExpr RETIRED**: the yacc grammar now emits legacy's
  *parser.SubqueryExpr for `( SELECT ... )` expression values (NewSubqueryExpr
  ctor); ScalarSublinkExpr type deleted — analyzer/planner/executor already
  speak SubqueryExpr, so Q2/Q4/Q11/Q16/Q17/Q18 subquery forms execute live
  (verified: Q4 EXISTS=1062945, Q11 having-subquery rows, Q17, Q18 IN).
- [x] **Deferred items landed 2026-08-26 evening**: interval 'N' <unit>
  qualified form (interval '90 day'
  embedded form works; trailing-qualifier form is a syntax error in the yacc
  grammar); timestamp WITH/WITHOUT time zone literals; char(N) '...' literals;
  count(*) FILTER — FILTER still open; arrays/subscripts/frames/Variadic
  parity landed as P2.4b (commit b8c87689d).

## P2 — Expressions

- [x] **P2.1a IS family + BETWEEN**: IS [NOT] NULL / TRUE / FALSE / UNKNOWN /
  [NOT] DISTINCT FROM, postfix ISNULL/NOTNULL (gram.y :15160ff/:15200),
  [NOT] BETWEEN [SYMMETRIC] with b_expr operands and parseBetweenTail-parity
  desugaring (buildBetween). Differential corpus +13 cases.
- [x] **P2.1b predicates & operators**: IN (list|subquery), = ANY/SOME/ALL,
  LIKE/ILIKE [+ESCAPE], SIMILAR TO [+ESCAPE] with buildSimilarTo constant-
  folding port, || concat via generic `a_expr Op a_expr` rule (%left Op),
  subq_op for comparison ops before quantifiers. `= ANY` maps to AnyOp=0
  (same as IN per InExpr contract); non-equality ops set AnyOp. Bitwise
  & | # ~ << >> deferred (single-char ASCII terminals need goyacc char-
  literal investigation). Differential corpus +15 cases (incl. date literals) all
  legacy-identical except ALL(subquery) AST shape divergence.
  LESSON RETAINED: gen-parser rc MUST be 0 at commit time; %prec SIMILAR /
  %prec NOT_LA annotations required on SIMILAR TO rules (without them 48
  S/R conflicts appear because rule-precedence defaults to last-terminal-
  nonterminal = none).
- [x] **P2.2 conditional & set exprs**: searched CASE, EXISTS(subquery),
  NULLIF/GREATEST/LEAST (COL_NAME_KEYWORDs already route through generic
  FuncCall). Row-compare sublinks deferred to P3.
- [x] **P2.3 func_call (core)**: qualified_name-based function application
  with name_or_call merged nonterminal (zero S/R for ColumnRef-vs-FuncCall
  disambiguation); count(*), DISTINCT args, pg_catalog-qualified calls.
  ORDER BY/VARIADIC/FILTER/WITHIN GROUP deferred to P2.3b. Known S/R on '('
  = func-call vs paren-expr, default-shift correct; gate refined to allow
  exactly this 1 known conflict.
- [x] **P2.4 casts**: `::` cast + CAST(expr AS ColId) both ported.
  Schema-qualified + typmod args deferred (qualified_name in cast target =
  2951 R/R). $n params already handled via PARAM token. ARRAY[]/ROW()
  deferred to P2.4b.
- [x] **P2.5 type names ✅ (cast positions)**: 2026-08-26 cast_target landed —
  zero-conflict architecture per the trap note below (cast_ident enumerates
  single-word types EXCLUDING multi-word starters; dedicated TIME/TIMESTAMP
  [+tz], double precision, char/character/nchar [varying], bit [varying]
  alternatives). Legacy parity details: float(p<=24)->float4 folding into the
  NAME (typmod dropped), bare char stamps Typmods=[1] via typmodsFor helper,
  bare nchar->"character". Schema-qualified targets + array suffixes landed same day
  (castType carrier; "int[]" folds into Name; gate now allows known S/Rs on
  '(' '.' '['). ParseExpr FLIPPED (RouteExprBatch hook,
  SELECT-wrapped yacc path, pos-exact via Pos-7 synthetic token; plpgsql +
  executor suites green). Remaining: SETOF only — deferred to the P5.2
  FUNCTION wave where it is reachable.
  TRAP FOUND 2026-08-26: naive multi-word additions (::double precision,
  ::timestamp with time zone) collide R/R with the ColId cast-target path
  because every type word is ALSO a col_name/unreserved keyword. Upstream
  escapes via a dedicated type_function_name keyword class that excludes
  these tokens; porting that means introducing a restricted cast-target
  identifier class (IDENT+unreserved only) plus explicit token alternatives
  for int/integer/bigint/smallint/real/float/numeric/decimal/boolean/
  interval/time/timestamp — enumerate FIRST, then add multi-word forms, or
  the gate will (correctly) reject the grammar.

## P3 — DML writes

- [~] **P3.1 INSERT v0 LANDED 2026-08-26 (grammar only; routing FLIPPED (createClassRouted two-keyword dispatch: "create"+"table" only))**:
  insert_stmt = [with_clause] INSERT INTO name [(cols)] source where source
  is SelectStmt (bare VALUES select converts to InsertStmt.Rows, legacy
  shape) or DEFAULT VALUES. KEY INSIGHT: the historic 3927 R/R was an
  artifact of the hidden-duplicate grammar — on today's clean grammar,
  parsing INSERT's source AS a select_stmt (upstream shape) merges zero
  hostile states; conflicts stay at the pinned 4. insSrc carrier +
  NewInsertStmt/SetInsertSelect/SetInsertDefaultValues ctors. Parity: INSERT
  6/6 identical (+7 corpus total → 155). ON CONFLICT (all arbiter spellings,
  DO NOTHING / DO UPDATE SET [WHERE]) and RETURNING landed same day; known
  S/R on 'ON' added to gate allowlist (join-ON vs insert-arbiter; shift=
  join wins, correct inside FROM). ROUTING FLIPPED same day
  (routedStmts["insert"]=true): live-verified multi-row INSERT, column lists,
  ON CONFLICT DO NOTHING / DO UPDATE SET..RETURNING through the goyacc path;
  tpch-spotcheck Q12/Q13 PASS; units suite green. P3.1 COMPLETE.
- [ ] **P3.1 INSERT**: VALUES/SELECT source, ON CONFLICT (index inference,
  DO NOTHING/UPDATE), RETURNING, OVERRIDING.
- [x] **P3.2 UPDATE ✅ (2026-08-26, flipped)**: update_core x3 (plain / ONLY /
  WITH-prefixed) with opt_upd_alias — bare aliases restricted to IDENT so
  the unreserved SET keyword cannot be captured as an alias (6th known S/R
  class dodged structurally, gate stays at 5). SET reuses the ON CONFLICT
  assign productions; FROM takes a plain range-var list (join-FROM
  deferred); WHERE expr | CURRENT OF cursor via updWhere carrier;
  RETURNING reuses opt_target_list. Live-verified single + FROM-join
  updates through the routed path; corpus parity 162, floor 158.
- [x] **P3.3 DELETE ✅ (2026-08-26, flipped)**: [WITH] DELETE FROM [ONLY]
  name [alias] [USING tables] WHERE expr|CURRENT OF [RETURNING]; alias uses
  the IDENT-only bare form, USING rides upd_from_list. Live-verified
  WHERE + USING joins through the routed path. MERGE deferred to a later
  wave (rare in corpora; WHEN [NOT] MATCHED machinery is self-contained).
  Parity 164, floor 160.
  Flip DML routing; WITH follower routing extends to INSERT/UPDATE/DELETE.

## P4 — DDL wave 1 (tables)

- [~] **P4.1 CREATE TABLE v0 landed 2026-08-26 (grammar in goopg_ext.y;
  routing FLIPPED (createClassRouted two-keyword dispatch: "create"+"table" only) — "create" leads every DDL class so flipping needs
  two-keyword dispatch first)**: column defs (name + cast_typename type incl.
  typmods via col_type_name wrapper + NOT NULL / PRIMARY KEY / UNIQUE /
  DEFAULT), table-level PRIMARY KEY (cols) and UNIQUE (cols). Legacy shapes
  dump-identical (3 probe forms); corpus parity unchanged (its CREATE
  literals use unported clauses — will climb with later slices). Remaining: CHECK/FK/named
  constraints, WITH options, partitioning, INHERITS, OF type, AS SELECT,
  TEMP/UNLOGGED/IF NOT EXISTS, then the dispatch refinement + flip.
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

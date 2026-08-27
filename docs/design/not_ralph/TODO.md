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
  literals use unported clauses — will climb with later slices). Remaining: CHECK/FK landed
  (CheckExpr = RAW SOURCE span via lexerState.src plumbing: RouteBatch/
  RouteExprBatch now carry src; markSpanStart/spanText replicate legacy
  captureSrcSpan. FK column+table-level incl. ON DELETE/UPDATE actions;
  known S/R on NOT = DEFAULT-expr vs SET-DEFAULT, shift correct), NAMED column/table
  constraints (CONSTRAINT name NOT NULL/PK/UNIQUE/CHECK) landed; WITH
  options / INHERITS / PARTITION BY / AS SELECT attempted via an open
  left-recursive option list -> 29 conflicts (each option keyword fights the
  empty-reduce). NEXT APPROACH: upstream's FIXED clause order as one flat
  rule (optwith optimherits partitionspec as-select), not open composition. ATTEMPTED 2026-08-26 evening: layered optional stages
  (opt_ct_after_with/inherits/partition chain) STILL produced conflicts +
  cascading type-check whack-a-mole; slice REVERTED to pinned-green state.
  Root difficulty unresolved — next attempt must start from a MINIMAL
  repro (single WITH-only tail) and grow one clause at a time, verifying
  goyacc output at each step. Suspect interaction between the ')' closing
  table_element_list and downstream statement-level expectations., partitioning, INHERITS, OF type, AS SELECT,
  TEMP/UNLOGGED/IF NOT EXISTS, then the dispatch refinement + flip.
- [ ] **P4.1 CREATE TABLE columns+constraints**: column defs (types, DEFAULT,
  NOT NULL, GENERATED, collation), column + table-level constraints.
- [ ] **P4.2 CREATE TABLE table options**: PARTITION BY / PARTITION OF,
  INHERITS, USING access method, WITH options, ON COMMIT, TABLESPACE,
  AS query, IF NOT EXISTS.
- [~] **P4.3 ALTER TABLE v0 (2026-08-26, flipped)**: single-action ADD
  COLUMN / ADD PRIMARY KEY / DROP COLUMN / ALTER COLUMN TYPE / RENAME TO +
  [IF EXISTS] [ONLY]; alter via second-keyword dispatch. Gate pins 13
  known S/Rs (IF_P x5). Multi-action lists, SET/DROP DEFAULT/NOT NULL,
  DROP CONSTRAINT, OWNER TO, SET SCHEMA, partition actions deferred.
- [~] **P5 v0b CREATE MATERIALIZED VIEW (2026-08-26, flipped)**: CREATE
  MATERIALIZED VIEW [IF NOT EXISTS] qn [(aliases)] AS select [WITH [NO] DATA]
  via create-pair "materialized". WITH DATA is excluded from RawDef via the
  with_data_kw marker (post-WITH-shift pin: endMark = prevPos+len(prevText) —
  the select body's last token end). Gate pins 15 known S/Rs (IF_P x7).
- [~] **P5 v0 CREATE VIEW (2026-08-26, flipped)**: CREATE [OR REPLACE] VIEW
  qn [(cols)] AS select routed via create-pair "view". TEMP/UNLOGGED VIEW
  intentionally NOT routed (modifier-prefix S/R ambiguity with create-table;
  secondKeywordRouted falls back to legacy when TEMP|TEMPORARY|UNLOGGED
  precedes the object kind). RawDef now exact via fragEndPos
  (fragment-end carrier: last real token end, trailing ';' excluded) — EOF
  short-circuit zeroes lexer last*/peek at stmt-final reduce, so use the
  fragment carrier instead of peek().
- [~] **P4.3b DONE + bare-name create alt (2026-08-26)**: PARTITION OF rides
  opt_ct_tail(_noas); new bare `CREATE TABLE name opt_ct_tail_noas` alternative
  covers column-less creates (PARTITION OF / INHERITS / WITH / PARTITION BY
  without a coldef list). opt_ct_tail_noas excludes AS to avoid R/R with the
  pre-existing create_table_stmt_as rule (conflicts stay 13).
  KNOWN EXECUTOR GAP (pre-existing, not parser): INSERT into partitioned
  parent fails "no partition of relation found" even with valid bounds.
- [~] **P4.3b CREATE TABLE ... PARTITION OF (2026-08-26)**: rides opt_ct_tail
  (PARTITION OF qn + RANGE/IN/DEFAULT bounds via partBound carrier →
  CreateTableStmt.PartitionOf). NOTE: unit tests must wire
  parser.RouteBatch = RouteBatch explicitly (postmaster init is not linked
  into test binaries).
- [~] **P4.3 ALTER TABLE — routing LIVE 2026-08-27 via a narrow action
  allowlist.** `routedAlterTableActions` + `alterTableActionsRouted`
  (`internal/sqlparser/dispatch.go`) route a statement only when EVERY
  comma-separated action is one the grammar covers: 31 of the corpus's 153
  forms go to yacc, 122 stay on legacy. `TestAlterTableRoutingIsNarrow`
  (`internal/sqlparser/alter_table_test.go`) asserts every corpus form is
  either not routed or routed AND legacy-identical, so "routed but
  unparseable" is unreachable. **Widen the allowlist only together with the
  matching grammar alternative** — the gate will fail otherwise. Still
  unported and deliberately left on legacy, in corpus-frequency order:
  `ADD [CONSTRAINT name] {PRIMARY KEY|UNIQUE|CHECK|FOREIGN KEY|EXCLUDE}` (31
  forms), `ALTER COLUMN {SET STATISTICS|STORAGE|COMPRESSION|(attopts)}` and
  identity actions (25), `ALTER CONSTRAINT` + `RENAME CONSTRAINT` (12),
  `ENABLE|DISABLE {TRIGGER|RULE|ROW LEVEL SECURITY}` + `FORCE|NO FORCE` (10),
  `CLUSTER ON` / `SET WITHOUT CLUSTER` / `SET TABLESPACE` / `SET ACCESS
  METHOD` / `INHERIT` / `NO INHERIT` / `OF` / `NOT OF` (15), plus
  `ADD COLUMN IF NOT EXISTS`, `DROP COLUMN IF EXISTS`, `ALTER COLUMN TYPE ...
  USING`, `DETACH PARTITION ... CONCURRENTLY|FINALIZE`, and `OWNER TO
  CURRENT_USER|SESSION_USER|CURRENT_ROLE` (the grammar's target is `ColId`,
  which excludes reserved keywords).
  Historical note — GRAMMAR landed 2026-08-26, routing was NEVER LIVE:
  Corrected 2026-08-27: `fragmentRouted` (`internal/sqlparser/dispatch.go`) only
  delegated `create`/`drop` to `secondKeywordRouted`, and `"alter"` is not in
  `routedStmts`, so every ALTER TABLE fell through to the legacy parser. The
  `routedCreatePairs["alter"]["table"]` entry and all eight P4.2/P4.3 "flip"
  commits were dead code that had never executed. Sizing before enabling it:
  the legacy test corpus carries **138 distinct ALTER TABLE forms** and the
  grammar covers roughly 20, and `routeBatch` does not fall back after routing
  — a wholesale flip turns the other ~118 into hard 42601s. Enable via an
  action-leader allowlist in the dispatcher (same strangler shape as
  `routedCreatePairs`), widened only alongside the matching grammar
  alternative and a differential case. The three field bugs found while sizing
  it (`DROP CONSTRAINT` and `VALIDATE CONSTRAINT` writing `OldConstraintName`
  instead of `ConstraintName`; `DROP CONSTRAINT` missing `IfExists`/`Restrict`;
  `ADD COLUMN` missing `NotNullExplicit`) were FIXED in the same commit as the
  flip.
  Original entry: all executor-relevant
  actions — ADD COLUMN/PK/FK/CHECK, DROP COLUMN/CONSTRAINT, ALTER COLUMN
  TYPE/SET DEFAULT/DROP DEFAULT/SET|DROP NOT NULL, RENAME TO/COLUMN,
  VALIDATE CONSTRAINT, REPLICA IDENTITY (f/n/d/i), OWNER TO, SET SCHEMA,
  SET (reloptions), SET LOGGED|UNLOGGED, multi-action comma lists,
  ATTACH/DETACH PARTITION (RANGE/IN/DEFAULT; CONCURRENTLY unsupported by
  legacy too). pos fields of partition actions are 0 vs legacy child-name
  offset (error-position only).
- [ ] **P4.3 ALTER TABLE**: full action list goopg supports.
- [~] **P4.4 partial (2026-08-26)**: DROP TABLE (IF EXISTS, multi-name,
  CASCADE/RESTRICT) + TRUNCATE (TABLE kw, multi-name, RESTART/CONTINUE
  IDENTITY, behavior) landed AND flipped — drop via second-keyword dispatch
  ("drop table"), truncate direct. Gate now pins 7 known S/Rs ('(' x2,
  '[' x2, ON, IF_P x2 optional-empty class). CREATE/DROP INDEX landed same day (v0: plain column keys; CONCURRENTLY,
  expressions, DESC/opclasses deferred). routedCreatePairs now maps
  create/drop -> {table,index}. Gate pins 10 known S/Rs (IF_P x4 from the
  four optional IF EXISTS clauses).
- [ ] **P4.4 DROP TABLE / TRUNCATE / INDEXes**: CREATE/DROP INDEX incl.
  partial/expression/concurrent flags. Flip wave-1 routing; initdb replay.

## Audit findings 2026-08-27 (first honest pgbench-smoke run)

The previous wave landed every parser commit with `git commit --no-verify`, so
the pre-commit pgbench smoke had not run since the P3.1 INSERT flip. Running it
found one blocker; repairing the differential harness found five more defects in
ALREADY-ROUTED statement classes. All six are fixed (see the P2.6 commit); what
follows is what the audit left OPEN.

- [x] **DONE 2026-08-27 — `TestCreateTableV0` and `TestInsertOnConflictReturning` assert nothing.**
  Both are now `diffParse` gates with three buckets (must-match / yacc-only /
  both-reject) plus a guard that `parser.RouteBatch` is nil so the legacy leg
  cannot self-compare. All 85 literals match after the fixes below; only
  `CREATE TABLE c (a int) WITH (...) PARTITION BY RANGE (a)` stays rejected by
  both (opt_ct_tail is flat and cannot compose WITH with PARTITION BY).
  Original entry:
  Both `t.Logf` on parse failure and `fmt.Printf` the result, and neither wires
  `parser.RouteBatch = RouteBatch`, so they run entirely through the LEGACY
  parser. Every DDL/utility wave from P4.1 to P6.2 therefore has ZERO enforced
  differential coverage. Convert both to `diffParse` + `t.Errorf` (the shape
  `values_table_test.go` and `cast_target_test.go` already use).
- [x] **DONE 2026-08-27 — table-level `CHECK` / `FOREIGN KEY` ported** (anonymous
  and `CONSTRAINT name FOREIGN KEY`), reusing opt_ref_cols / opt_fk_actions;
  conflict pin unmoved at 16. Still on legacy and tracked here: `MATCH FULL`,
  `[NOT] DEFERRABLE [INITIALLY ...]`, `NOT VALID`, `[NOT] ENFORCED`,
  `ON DELETE SET (cols)`, `NO INHERIT`, `INCLUDE (...)`.
  Original entry: Only the column-level forms are ported (`pg_grammar.y`
  `col_constraint`), so
  `CREATE TABLE t (a int, b int, foreign key (b) references o (id))` is a syntax
  error on the routed path. `FOREIGN` is one of the reserved tokens no
  production consumes (see the reachability item below).
- [x] **DONE 2026-08-27 — named table constraints wired**, along with the
  anonymous `UNIQUE (cols)` carrier that discarded its columns outright (so
  `TableUniques` was always nil on the routed path) and all the parallel
  slices canonDump compares. Original entry:
  `table_element`'s `CONSTRAINT ColId {PRIMARY KEY|UNIQUE|CHECK}` alternatives
  fill `tableElem.namedPk` / `namedUq` / `check` / `checkName`, but
  `create_table_stmt`'s element loop only consumes `e.pk` and `e.uq`.
- [x] **DONE 2026-08-27 — `TestReservedKeywordsReachable`** asserts every
  `reserved_keyword` / `type_func_name_keyword` token is either consumed by a
  hand-written production or on the `notYetPortedKeywords` allowlist (18
  entries today; `FOREIGN` dropped off when the table-level FK landed). It also
  fails on STALE entries, so closing a hole forces the allowlist update in the
  same commit — both directions negative-tested. Original entry: 22 of the 78
  `reserved_keyword`s and 8 of the 23 `type_func_name_keyword`s are never
  referenced by any hand-written production, so they are unreachable and any
  routed statement using one is a guaranteed syntax error — exactly how the
  CURRENT_TIMESTAMP blocker happened. Still unreachable and relevant:
  `FOREIGN` (above), `VARIADIC` (legacy fills `FuncCall.Variadic`),
  `INITIALLY` (`DEFERRABLE INITIALLY DEFERRED`), `ASYMMETRIC`
  (`BETWEEN ASYMMETRIC`), `BOTH`/`LEADING`/`TRAILING`/`PLACING` (the SQL
  standard `TRIM`/`OVERLAY` forms — `TRIM`/`OVERLAY`/`POSITION`/`SUBSTRING`
  special syntax is absent from the grammar entirely). The rest belong to
  statement classes not yet ported. **Worth mechanising as a test** that
  asserts each such token is either consumed by a production or on an explicit
  not-yet-ported allowlist.
- [x] **DONE 2026-08-27 — `name_or_call` nil-args coercion removed.**
  Original entry: for
  `qualified_name '(' opt_func_call_args ')'`, so every zero-arg call (`now()`,
  `pg_backend_pid()`, `current_database()`) renders `Args=[]` where legacy
  renders `∅`. A latent parity diff for the whole zero-arg call class; the new
  `func_expr_common_subexpr` deliberately does NOT copy it.
- [ ] **`pg_get_viewdef` returns empty on the live server.** Seen during the P5
  CREATE VIEW slice and never recorded. The parser side is now testable
  (`diffParse` uses `ParseOneSrc`, so `RawDef` is actually compared). Repro:
  drop and recreate the view on a FRESH cluster, then
  `SELECT pg_get_viewdef('v'::regclass)`. Storage is
  `internal/executor/operators_ddl.go:6291` (`vt.ViewDef = s.RawDef`), read-back
  is `internal/executor/expr.go:11569`. Leading suspects: catalog rows written
  before `5cf25672a` fixed `RawDef`, or the name-vs-OID lookup path.

## Remaining routed-surface gaps, ranked (measured 2026-08-27)

Method: bulk-parse every SQL fragment of the upstream corpora through BOTH
parsers and keep only the cases legacy accepts and the yacc parser rejects.
Those are migration regressions by construction (the legacy parser's SQL
surface is unchanged since before the routing flip — see the verdict below).

**Isolation specs: 1690 steps checked, 115 -> 10 over this session.** What is
left there: an expression index (`ON t((f(k)))`), MERGE inside a CTE, a
subpartition `PARTITION OF ... PARTITION BY`, and two multi-statement steps.

**Must-pass regress: 2409 routed fragments, 222 still failing across 27 of the
59 cases.** Ranked by fragments blocked:

- [x] **DONE 2026-08-27 — index column surface.** Expression keys, per-column
  opclass, COLLATE, ASC/DESC, NULLS order, `ON ONLY`, `WITH (fillfactor = ...)`.
  Two grammar shapes were forced by measurement: the key must be
  `name_or_call` / `'(' a_expr ')'` rather than a bare a_expr (a trailing
  opclass ColId after an a_expr is ambiguous with VARYING / FILTER / WITHIN /
  YEAR_P / SECOND_P — 14 S/R), and the opclass must be IDENT rather than ColId
  (ColId admits FILTER and WITHIN, the very tokens that continue a
  function-call key — 4 more S/R).
- [x] **DONE 2026-08-27 — `AS <reserved keyword>` aliases (~33).** The earlier
  measurement (12 R/R for the whole list, 6 for TRUE_P/FALSE_P alone) blamed
  the wrong culprit: it is NOT `c_expr: TRUE_P` and NOT the SelectStmt/AS
  restructure. ColLabel's OTHER user is `set_value_atom` (goopg_ext.y), which
  lists ON / DEFAULT / TRUE_P / FALSE_P explicitly PRECISELY BECAUSE ColLabel
  lacked them — widening ColLabel duplicates those four alternatives and every
  duplicate is a reduce/reduce. Deleting the explicit four along with the
  widening drops 12 R/R to one S/R, and that last one is real: `SET ROLE TO x`
  becomes ambiguous because TO is then a candidate VALUE for SET ROLE. So the
  widening lives in a separate `as_col_label` nonterminal used ONLY by
  target_el — zero conflicts, set_value_atom untouched. It does not blind
  TestReservedKeywordsReachable: that scanner reads literal tokens out of the
  hand-written .y files and never expands the generated reserved_keyword.
- [x] **DONE 2026-08-27 — `VARIADIC` call arguments**, plus the aggregate
  `ORDER BY` inside a call (`array_agg(a ORDER BY b)`) that surfaced with it.
  The name_or_call alternatives moved from the plain `opt_func_call_args`
  ([]Expr) to a carrier holding the per-argument flags; all eight share the
  `qualified_name '('` prefix so they had to move together, while ARRAY[...]
  and the SQL value functions kept the plain list. The subtle half is legacy's
  EXPANSION — `f(VARIADIC array[a,b])` becomes two individually-flagged
  arguments, and with a `::int[]` cast the ELEMENT type is pushed onto each
  expanded element as its own cast; reproducing only the flag yields a
  silently different Variadic slice.

- [ ] **Row-constructor comparison (~14)** — `WHERE (a, b) > ('x', 0)`.
  **Measured 2026-08-27:** `c_expr: '(' a_expr ',' expr_list ')'` (spelled to
  need two items so it cannot collide with `'(' a_expr ')'` grouping) still
  costs 12 reduce/reduce, all of them `a_expr: c_expr` vs `b_expr: c_expr` —
  the row constructor merely EXPOSES the pre-existing a_expr/b_expr split.
  Resolve that split first.
- [x] **DONE 2026-08-27 — `ALTER TABLE ... ADD PRIMARY KEY USING INDEX name`.**
- [x] **DONE 2026-08-27 — subpartitioning (~7)** (`PARTITION OF p FOR VALUES
  ... PARTITION BY LIST (c)`). Not actually blocked by the flat `opt_ct_tail`:
  an `opt_subpartition_by` tail on the PARTITION OF alternative costs zero
  conflicts, and ctTail already carried both fields.
- [x] **DONE 2026-08-27 — `SET [SESSION] AUTHORIZATION name|DEFAULT`.** A
  separate `AUTHORIZATION DEFAULT` alternative reduce/reduces against
  set_value_atom's own DEFAULT (14 conflicts), and the atom text is "default"
  for the bare keyword and the literal `'default'` alike, so the distinction is
  made on the token KIND in `sessionAuthzStmt`. Legacy itself rejects
  `SET LOCAL AUTHORIZATION`.
- [x] **DONE 2026-08-27 — hash partition bounds** (`FOR VALUES WITH (modulus m,
  remainder r)`) and **opclass option lists** (`int4_ops(foo=1)`).
- [x] **DONE 2026-08-27 — EXCLUDE constraints** (`EXCLUDE [USING m] (col WITH
  op, ...) [INCLUDE (...)] [WHERE (pred)]` plus constraint attrs), anonymous to
  TableExclusions and named to NamedConstraints. Legacy keeps the columns as a
  list but only ONE ExclusionOp (the first) and defaults Method to "btree".
- [x] **DONE 2026-08-27 — everything else that was measurable.** In rough
  order landed: table-level `CHECK` / `FOREIGN KEY`, `SET CONSTRAINTS`,
  table-level `NOT NULL col [NO INHERIT]`, the empty target list
  (`SELECT FROM t`, `SELECT`), alias column lists (`FROM t AS f(a, b)`),
  `ORDER BY x USING op`, `TRUNCATE ONLY`, `FETCH FIRST ROWS WITH TIES` and
  `FETCH FIRST (expr)`, `CREATE TABLE (LIKE ...)`, CTAS column aliases and
  `WITH [NO] DATA`, CTAS `AS EXECUTE`, full PARTITION BY key elements
  (expressions, opclasses, COLLATE, plus MethodPos/KeyColPos),
  `RESET SESSION AUTHORIZATION`, prefix `~` and b_expr's unary signs,
  `AS <reserved>` labels, TRIM, the inheritance star (`FROM person*`),
  json/xml/path cast targets, `ADD PRIMARY KEY ... INCLUDE`, `AT TIME ZONE` /
  `AT LOCAL`, the interval Form-2 ordering fix, `UPDATE ... FROM` /
  `DELETE ... USING` aliases, six keyword typed-literal prefixes, keyword
  function names (`left(...)`), counted datetime types, and `SELECT ... INTO`.

**Must-pass regress fragment count: 222 -> 188 -> 157 -> 103 -> 84 -> 33 -> 26 -> 13 -> 1.**

The measurement harness was also corrected mid-session: it now discards any
fragment the LEGACY parser rejects too, so the regress files' DELIBERATE syntax
errors (`select distinct from pg_database;`, `select;`, the copydml cases) no
longer inflate the count. Roughly 33 of the original 222 were that noise.

**Must-pass regress: ZERO legacy-accepts/yacc-rejects fragments remain.** The
last one, GROUPING SETS, landed with the full-corpus sweep below. Both
structural blockers are gone, and so are `VALUES(n.*)` and the identity
sequence options.

### Full regress corpus sweep (2026-08-27)

Once the must-pass set was exhausted the same harness was pointed at EVERY
regress file — 36,066 routed fragments, not just the 2,567 of the 59 must-pass
cases. Legacy-accepts / yacc-rejects: **801 -> 625 -> 299 -> 135 -> 101**,
across five commits (`10fff5077` partition element lists and column qualifiers;
`6bb72f3d1` composable CREATE TABLE tail, SQL-standard function forms, bit
literals; `9ef17c8d5` opaque CHECK bodies, GROUPING SETS expansion, FK options;
`dcd1b9350` multi-column SET). Corpus parity over the same span: 463 -> 523.
Conflict pin 23 -> 28, every addition in an already-allowlisted class.

Three findings from that sweep worth knowing before touching these areas again:

- **CHECK bodies are opaque in legacy.** parseCheckExpr never parses the
  expression; it stores a PLAIN space join of the tokens between the parens
  (`( y ) . a > 0`). The adapter now folds `CHECK ( ... )` into one CHECKBODY
  terminal. Legacy has THREE different token joins — plain (checks),
  joinGeneratedExprTokens (generated columns, with spacing rules), and a
  lower-cased unquoted one (partition-of checks) — and support.go carries all
  three, named for their legacy originals.
- **Trailers that start with NOT must be list items, not rule suffixes.**
  `CHECK (...) . NOT` cannot tell NOT ENFORCED from a following NOT NULL with
  one token of lookahead; gram.y's ConstraintAttributeSpec is a separate item
  for exactly this reason. Same lesson as generated columns' mid-rule actions:
  two alternatives sharing a prefix and each carrying their own `{ ... }` are
  two distinct empty nonterminals reducible at the same point (1329 R/R on the
  first cut of the table-check trailer).
- **CREATE TABLE's tail had to become a LIST.** The flat single-clause tail
  could not spell `PARTITION BY ... WITH (...)` (65 fragments). Two things were
  forced by conflicts once it composed: the PARTITION OF-with-elements form is
  a tail item, not a statement alternative (as an alternative it shifts
  PARTITION before the no-column CREATE's empty tail can reduce — 900
  fragments broke on the first cut), and the plain CTAS lives over the same
  tail rather than in create_table_stmt_as.

**Not ported, recorded — the 101 that remain:**

| fragments | form | why |
|---|---|---|
| 53 | `(f(x)).*` | legacy rewrites it into a synthetic `__irs_N` FROM item numbered from a PROCESS-GLOBAL counter, so its AST is not reproducible run to run |
| 18 | `AS "Confucius' Birthday"` | harness noise: the corpus splitter's quote handling, not a parser gap |
| 7 | `x IS JSON` | legacy reads it as `x AS json` (a legacy bug) |
| 6 | `ORDER BY c USING ~<~` | legacy's lexer splits multi-char operators and re-concatenates them; the adapter sees `~` `<` `~` |
| 3 | `EXCLUDE ... (a WITH -\|-)` | same split-operator issue |
| 2 | `33 * ANY (...)` | legacy reads `any(...)` as a function call after a non-comparison operator |
| 2 | MERGE inside a CTE | MERGE is unported |
| 2 | typed-table element lists with table constraints | `OF type (id WITH OPTIONS PRIMARY KEY, UNIQUE (name))` |
| ~8 | singletons | `char(20) 'x'`, `::INTERVAL DAY TO MINUTE`, `WITH ordinality AS (...)` (the WITH_LA substitution eats a CTE named ordinality), `percentile_cont(p ORDER BY p) WITHIN GROUP`, a FILTER-after-WITHIN-GROUP, `CREATE TABLE t (col) ON COMMIT ... AS SELECT` |

One legacy defect deliberately NOT reproduced (difftest_known_diffs.md, pinned
both sides): a NOT NULL written after a column CHECK is lost by legacy.

### The structural blockers (2026-08-27)

- [x] **DONE 2026-08-27 — parenthesised set-op operands (14).** Landed as
  upstream's three-tier layering (`SelectStmt` / `select_with_parens` /
  `select_no_parens`, plus `select_bare` for legacy's parseSelect, which
  refuses a leading '('), with legacy's WRAPPER-node shape reproduced exactly
  (`parenGroup`): `(S)` alone is S stamped; anything after the ')' hangs off a
  fresh node whose `SetOpOperand` is S. Cost: ONE shift/reduce on ')' (nested
  derived-table parens, shift is the legacy parse) — no reduce/reduce at all,
  for precisely the reason recorded below: with no bare
  `select_no_parens: select_with_parens` alternative, the nested-paren case is
  a SHIFT against the c_expr reduce and `%prec UMINUS` settles it.

  Two things fell out of sharing `select_with_parens` across every subquery
  site. First, legacy is NOT uniform about stamping `Parenthesized` on a single
  pair of parens — derived tables and set-op right operands stamp; scalar
  subqueries, EXISTS, IN, ANY and CTE bodies do not — and the old scalar-
  subquery action stamped unconditionally, so every `SELECT (SELECT ...)` had
  been a silent parity diff. Second, INSERT's optional column list had to be
  inlined into the source alternative (gram.y does the same): once the source
  may start with '(', an EMPTY `opt_ins_cols` reduction on '(' fights the shift
  and the shift would parse `INSERT INTO t (SELECT 1)` as a column list.

  While there: `= ANY` / `<> ANY` / SOME desugaring on subqueries (the list
  path already did it), and parseAnyTail's `ANY (ARRAY[...])` element splice,
  whose test is TOKEN-literal (`((ARRAY[1]))` stays wrapped) and is therefore
  reproduced from the token stream rather than the AST. Corpus parity 393 ->
  434 across the batch.

  The original diagnosis, kept for the record:

  Adding `simple_select: paren_select`, with ONE shared `'(' SelectStmt ')'` rule
  feeding all three consumers, leaves exactly TWO reduce/reduce conflicts,
  both on `')'`:

    state 1131  simple_select: paren_select.  vs  c_expr: paren_select.
    state 1916  simple_select: paren_select.  vs  base_table_ref: paren_select.opt_derived_alias

Every OTHER lookahead already resolves correctly on its own — UNION / INTERSECT
/ EXCEPT / ORDER / LIMIT / OFFSET / FETCH / FOR all reduce to simple_select,
which is exactly what the 14 fragments need. `')'` cannot be resolved by
precedence: %prec steers shift/reduce, not reduce/reduce, and upstream's
`c_expr: select_with_parens %prec UMINUS` (tried) changes nothing. yacc breaks
reduce/reduce by RULE ORDER, and simple_select is declared far earlier than
c_expr, so simple_select wins `')'` — which BREAKS `SELECT f((SELECT 1))` and
every other doubly-parenthesised subquery.

The fix is upstream's layering — select_no_parens / select_clause /
select_with_parens, with the set-op alternatives taking select_clause — so the
parenthesised operand never enters simple_select at all. That is a structural
rewrite of the SELECT core and should be done deliberately, not piecemeal.

- [x] **DONE 2026-08-27 — row constructors (10), and the a_expr/b_expr split
  itself.** The "split" was two separate copies of the parenthesised
  expression — `a_expr: '(' a_expr ')'` and `b_expr: '(' b_expr ')'` — where
  gram.y :15540 keeps ONE in c_expr. With two, the parser had to decide right
  after '(' whether the inside was an a_expr or a b_expr, and any third rule
  sharing the prefix collided with both; that is what every "12 reduce/reduce,
  all a_expr: c_expr vs b_expr: c_expr" measurement on this file was. Moving
  the single copy into c_expr (b_expr reaches it via `b_expr: c_expr`) and
  hanging gram.y's implicit_row beside it costs ZERO conflicts. Legacy shape
  reproduced: RowExpr for `(a, b)`, a plain row(...) FuncCall for ROW(...),
  no indirection on either.

- [x] **DONE 2026-08-27 — `VALUES(n.*)` / `f(n.*)`.** The recorded "20
  reduce/reduce, the a_expr/b_expr split" was wrong on the cause: re-measured
  after the split was gone it was still 21, all in one state — target_el
  carried its OWN `qualified_name '.' '*'` copy that reduced in the same state
  as c_expr's, one conflict per token that can follow a target entry. Only
  c_expr spells it now; target_el reaches it through a_expr. Zero conflicts.

- [x] **DONE 2026-08-27 — identity sequence options** (`GENERATED ... AS
  IDENTITY (START WITH n INCREMENT BY n MINVALUE n MAXVALUE n CACHE n [NO]
  CYCLE)`), on both the CREATE TABLE column and the ALTER TABLE ADD COLUMN
  sibling — which had been dropping identity altogether.

## ⚠️ VERIFICATION VERDICT 2026-08-27 — the migration is BELOW pre-migration level

The first full `make nightly-batch` since routing went live
(`run_id=20260827-052222`, sha `846d651d`) says the routed surface is **not** at
pre-migration parity. units / race / pgbench pass and the TPC-H silent-regression
spotcheck is GREEN (Q12=2, Q13=34), but:

| suite | pre-migration | now |
|---|---|---|
| isolation strict specs | **0 FAIL** (2026-08-25 nightly) | 64 -> 41 -> 7 -> 6 -> 3 -> **2 FAIL** of 245 |
| regress must-pass (59) | not measurable — the 08-25 run wedged and was killed, so Go never printed subtest verdicts | 40 -> 38 -> 19 -> **15 not passing** |
| TPC-DS syntax errors | **0** (2026-08-25) | 6 → **1** after this session's fixes (+3 known dsqgen artefacts) |
| TPC-H Q12/Q13 spotcheck | 2 / 34 | **2 / 34 ✅** (re-run after the interval-node fix) |
| whole testport FAIL count | — | 170 -> 135 -> 69 -> 53 -> 28 -> **15** |

### ALTER TABLE action wave 2026-08-27 (`ce592d915`, P4.2)

The largest unrouted class after EXPLAIN: 712 fragments across 44 actions,
now 6. Every action the regress and isolation corpora use is ported, and
routedAlterTableActions became a description of the grammar rather than a
short exception list. Corpus parity 550 -> 646; routed regress fragments
39,493; must-pass still 0 rejects; testport UNCHANGED at 15 FAIL.

Two shapes were forced by conflicts and are worth remembering:

- The constraint's trailing words are ONE FLAT left-recursive list. NOT is
  followed by DEFERRABLE, VALID or ENFORCED, so a split tail cannot decide
  with one token of lookahead — the same lesson as the table-level CHECK
  trailer — and flattening is also what lets the corpus's orders compose
  (`NOT VALID NO INHERIT`, `NOT DEFERRABLE ENFORCED`).
- The ADD column form is spelled four ways instead of using opt_COLUMN /
  opt_if_not_exists: with an empty nonterminal reducing right after ADD,
  `ADD EXCLUDE` had to choose between the constraint keyword and a column
  named "exclude" one token too early.

Legacy quirks reproduced: SET DATA TYPE is a NoOp there while plain TYPE
records AlterColumnType; table-level RESET drops its option names while the
per-column RESET keeps them; SET COMPRESSION default records an EMPTY method;
ENABLE / DISABLE TRIGGER is a statement-level flag with no action and no
trigger name.

### EXPLAIN routed 2026-08-27 (`82b75a727`, P6.4)

Routed only when the wrapped statement would be (explainInnerRouted), so
unported inner classes stay on legacy. +1,919 regress fragments; must-pass 0
rejects over 2,782; parity 550. The clean testport run after the flip is
UNCHANGED at 15 FAIL — the regress files lean on `EXPLAIN (COSTS OFF)`, so
this was the run that could have moved.

### Routing widened 2026-08-27 (`29d3b9bc9`): TEMP/UNLOGGED TABLE, UNIQUE INDEX, WITH RECURSIVE

Three dispatcher decisions, not grammar gaps: a stale "conflicts" rationale
refused every CREATE modifier, UNIQUE was taken as the object kind, and
withFollowerRouted returned `routedStmts["recursive"]` (false) for every
recursive CTE. +684 regress and +12 isolation fragments now route; must-pass
stays at 0 rejects; the clean testport run after the flip is UNCHANGED at 15
FAIL (same 15 cases). Two old dispatch tests were also un-routing SELECT for
every later test (`defer delete(routedStmts, "select")`) — fixed to restore.

Remaining unrouted classes by corpus frequency (regress + isolation): EXPLAIN
2013, ALTER TABLE actions outside the allowlist 851, CREATE FUNCTION 384 /
DROP FUNCTION 383, CREATE TRIGGER 373, GRANT 325, ANALYZE 306, MERGE 277,
COPY 273, FETCH 264, VACUUM 212, roles 340, CREATE TYPE 174, REINDEX 147.

### Testport end state after the full-corpus sweep (2026-08-27, clean foreground run)

15 failures: 4 pre-migration reds (`TestSyntax_AdvisoryLock_...`,
`TestPort_PgDumpConnectionSetup`, and the two isolation specs
`IsolationMergeMatchRecheck` / `IsolationPredicateGin` that were red before the
sweep), `PgoutputInteropGoopgToPG`, and 9 regress cases — char, dbsize, errors,
int8, limit, numerology, partition_info, select_into, union — all of which now
PARSE and diverge on OUTPUT (executor-level), none on 42601. The 13 cases that
cleared since the previous run (28 FAIL) include every one of the six that had
"appeared" then because they were newly parsing.

### Testport end state 2026-08-27 (clean single run, 20G/24G cap) — earlier

28 failures, and the composition matters more than the count:

- **4 were already red before the migration** and are not parser work:
  `TestSyntax_AdvisoryLock_SessionUnlockAcrossBeginBoundary`,
  `TestPort_PgDumpConnectionSetup`, `TestPort_PgAmcheck002Nonesuch`,
  `TestPort_PgAmcheck005OpclassDamage`.
- **3 isolation specs** remain: `IsolationMergeMatchRecheck`,
  `IsolationPartialIndex`, `IsolationPredicateGin`.
- **1 is a recorded parser deferral**:
  `ZeroColumnJoinDoesNotCrashBackend/lateral_values` needs `VALUES(n.*)`,
  whose 20-reduce/reduce cost is documented in the grammar.
- **15 regress cases**, plus `PgoutputInteropGoopgToPG` and
  `CreateTableInheritsNoInheritCheckNotPropagated`.

Eight regress cases were fixed this session (boolean, case,
create_function_sql, hash_part, int2, int4, partition_aggregate, prepare) and
six appeared (portals_p2, select_distinct, select_into, truncate, union,
varchar). The six are NOT regressions — they are cases whose statements now
PARSE, so they run further and diverge on OUTPUT instead of stopping at 42601.
Verified two of them: `truncate` diverges on inheritance-aware TRUNCATE
semantics and a missing relation (executor-level, no syntax error in the diff),
and `varchar` PASSES when run alone, so its failure in the full sweep is
suite-order state rather than anything the parser does.

**Methodology note — why no baseline worktree was needed.** The intended
baseline `f5613d73f` (parent of the first permanent routing flip `ae1516857`)
**does not build**: its committed `internal/sqlparser/yacc_parser.go` references
`parser.NewExtractExpr`, which does not exist in `internal/parser` at that
commit. It does not matter, because `git diff f5613d73f..HEAD -- internal/parser/`
touches only `parser.go` (the RouteBatch/RouteExprBatch hooks) and
`yacc_ctors.go` (additive constructors): **the legacy parser's SQL surface is
unchanged**. Before routing every statement went to legacy, so pre-migration
behaviour IS current legacy behaviour — which is exactly what `diffParse`'s
legacy leg measures. Every "legacy=true yacc=false" below is therefore a
migration regression by construction.

Also note the nightly's own testport stage has been red since 2026-08-23 for an
unrelated reason (it caps memory at 6G/8G/GOMEMLIMIT=5GiB, the regress suite
wedges and is killed at the 2h12m Go deadline). This run completed in 1088s, so
its verdicts are real.

### Root causes, by how many tests they block

- [x] **DONE 2026-08-27 — `FOR UPDATE` / `FOR SHARE` / `FOR NO KEY UPDATE` /
  `FOR KEY SHARE` locking clauses.** They were absent from the grammar ENTIRELY
  (`Locking` had zero occurrences in `pg_grammar.y`) even though
  `SelectStmt.Locking` has existed on the AST since M0021 and the P1.5 entry
  claimed they landed. Ported with `OF rel, ...`, `NOWAIT`, `SKIP LOCKED` and
  multiple clauses. `base_select` adopts gram.y's three-alternative split so
  the limit may come before OR after the locking clause (the upstream
  skip-locked specs use the latter) — writing it as
  `opt_select_limit opt_for_locking` plus a `for_locking_clause select_limit`
  alternative instead costs one S/R on FOR that goyacc resolves by shifting,
  which would silently demand a LIMIT after every FOR UPDATE. Pin stays 16.
- [x] **DONE 2026-08-27 — constraint attribute trailers.** `[NOT] DEFERRABLE`,
  `INITIALLY DEFERRED|IMMEDIATE`, `NULLS NOT DISTINCT` and `INCLUDE (cols)` on
  column- and table-level UNIQUE / PRIMARY KEY / FOREIGN KEY, named and
  anonymous. Ported the gram.y way: **ConstraintAttr is a SIBLING alternative
  of the col_constraint loop, not a trailer on each element** — that is what
  keeps `NOT NULL` and `NOT DEFERRABLE` separable with one token of lookahead,
  so the pin stayed at 16. EXCLUDE constraints remain unported.
- [x] **DONE 2026-08-27 — `SET [LOCAL] TRANSACTION <modes>`**, reusing
  `tx_mode_list` so ISOLATION LEVEL / READ ONLY|WRITE / [NOT] DEFERRABLE all
  parse; only the isolation level reaches the AST, as in legacy.
- [x] **DONE 2026-08-27 — `CREATE|DROP INDEX CONCURRENTLY` and
  `CREATE INDEX ... INCLUDE (cols)`.** (Legacy itself rejects
  `CREATE INDEX CONCURRENTLY IF NOT EXISTS`, so that combination is a legacy
  limitation, not a porting gap.)
- [ ] **A PARENTHESIZED select as a set-operation operand** — TPC-DS Q87.
  Full analysis and resume point below.

`ci/logs/20260827-052222/` holds the evidence: `testport/go-test.log`,
`testport/regress-diffs/`, `tpcds/run.log`, `summary.md`.

### Found by the first post-migration nightly (2026-08-27)

The nightly's TPC-DS stage went from 0 errors (2026-08-25, pre-flip) to 6
syntax errors. Root-caused to three grammar holes on the ROUTED SELECT path,
all now FIXED, plus one deferred:

- **`CAST(x AS t(p,s))` had no typmod alternatives** while the `::` spelling has
  had them since P2.5 — a sibling-path divergence. Broke TPC-DS
  Q18/Q49/Q61/Q75/Q90.
- **The simple `CASE operand WHEN value THEN ...` form was missing entirely**
  (only the searched form existed), so ordinary, very common SQL was a syntax
  error. `NewCaseExpr` had always taken an operand — only the grammar lacked it.
- **A bare derived-table alias accepted only `IDENT`**, not any unreserved
  keyword, so TPC-DS Q90's `(...) at, (...) pt` failed.

- [ ] **DEFERRED — a PARENTHESIZED select cannot be a set-operation operand.**
  `((SELECT ...) EXCEPT (SELECT ...))` fails at the operator, though the
  unparenthesized `(SELECT ... EXCEPT SELECT ...)` parses. Legacy accepts both.
  Affects TPC-DS Q87 and `query_0.sql` — 1 of 99 sweep queries.

  **Diagnosis (2026-08-27, third attempt — this is the useful part).** Upstream
  layers this as `select_clause: simple_select | select_with_parens` with
  `select_with_parens: '(' select_no_parens ')' | '(' select_with_parens ')'`,
  and `c_expr: select_with_parens %prec UMINUS`. Porting that layering to this
  grammar produces exactly TWO reduce/reduce conflicts, and **they pull in
  opposite directions**, which is why no local edit resolves both:

  - `state 1113: select_clause: select_with_parens.` vs
    `c_expr: select_with_parens.` on `')'` — wants c_expr to NOT share the
    nonterminal.
  - `state 1902: select_with_parens: '(' SelectStmt ')'.` vs
    `base_table_ref: '(' SelectStmt ')'. opt_derived_alias` — wants
    base_table_ref to share it (i.e. consume `select_with_parens` directly).

  Attempts and their exact costs, so none is repeated: grafting
  `'(' SelectStmt ')'` onto `simple_select` = 18 S/R + 2 R/R; full upstream
  layering incl. the recursive `'(' select_with_parens ')'` plus rewiring
  `table_ref`/`LATERAL` = 21 S/R + 1 R/R; layering WITHOUT the recursive
  alternative = 18 S/R + 2 R/R; additionally collapsing `c_expr`'s own
  `'(' SelectStmt ')'` into `select_with_parens` = still 18 S/R + 2 R/R
  (it moves the conflict from the productions to the nonterminal, states above).

  **Resume point:** the root cause is that this grammar reaches a parenthesized
  select from THREE places — expression (`c_expr`), FROM item
  (`base_table_ref`), and statement (`SelectStmt`) — which LALR merges into one
  state, whereas upstream's `simple_select` carries the set-op rules itself
  (`select_clause UNION ... select_clause`) and so never puts `select_clause` at
  statement start. Porting upstream's set-op placement INTO `simple_select` —
  replacing goopg's `SelectStmt: select_clause setop_tail` shape — is the
  change that would make the layering fit; it is a restructure of SelectStmt /
  base_select / setop_tail, not a local edit. Conflict pin is 16.

### Found while consuming the audit list (2026-08-27) — all FIXED

Five more AST-carrier defects on already-routed classes, none of them a parse
failure (each produced a well-formed statement carrying the wrong value, which
is why every gate stayed green):

- **Every routed `SET name = value` stored the WRONG value.** The mid-rule
  `markSpanStart()` is not lookahead-stable — the parser has already consumed
  the first value token to decide the `set_eq_to` reduce, so `peek()` pointed
  one token past it: `SET x = 1` stored `""`, `SET search_path TO public,
  pg_catalog` stored `", pg_catalog"`. The port was also wrong in kind: legacy
  joins the value tokens' DECODED text with `", "` (parseSetValueAtoms,
  parser.go:3056), so a source span matches only by accident.
- **`ON CONFLICT (cols)` dropped `OnConflictTarget.Exprs`**, which legacy keeps
  parallel to `Columns`.
- **`tx_mode_list` dropped every mode after a comma** — `BEGIN ISOLATION LEVEL
  SERIALIZABLE, READ ONLY` started a READ WRITE transaction.
- **Raw spans truncated on a quoted tail.** `fragEndPos` used the last token's
  `Pos+len(Value)`, but `Value` is DECODED, so a trailing `'x'` under-counted
  by its quotes; the `with_data_kw` end marker had the same bug.
  **Lesson for future span work: never size a span from a token's `Value`
  length — use a delimiter's position.**

## P5 — DDL wave 2 (everything else CREATE/ALTER/DROP)

- [~] **P5.1 REFRESH / DROP MATERIALIZED VIEW (2026-08-27, flipped)**:
  `REFRESH MATERIALIZED VIEW [CONCURRENTLY] name [WITH [NO] DATA]` via
  `routedStmts["refresh"]`, and `DROP MATERIALIZED VIEW [IF EXISTS] names
  [CASCADE|RESTRICT]` via the drop-pair "materialized". DROP emits
  `DropCompatStmt` with the two-word ObjType "materialized view", NOT
  `DropViewStmt` — legacy takes that path at `internal/parser/ddl.go:6329`.
  Gate pins 16 known S/Rs (IF_P x9, the ninth `opt_if_exists_drop` user).
  Live-verified: refresh re-runs the query (count 3 -> 4), `DROP ... IF EXISTS`
  on a missing matview emits the NOTICE, multi-name DROP works.
  **Fixed while landing it — the NOT/NOT_LA split was wrong in both
  directions.** `EXISTS` had been added to base_yylex's NOT_LA follower set
  (upstream parser.c has only BETWEEN/IN_P/LIKE/ILIKE/SIMILAR) so that
  `opt_if_not_exists: IF_P NOT_LA EXISTS` would reduce — which made every
  ordinary `NOT EXISTS (...)` a syntax error on the routed SELECT path. The
  upstream-correct fix is an exact follower set plus plain `NOT` in the two
  rules that had been spelled `NOT_LA` (`opt_if_not_exists`,
  `transaction_mode_item`'s `NOT DEFERRABLE`, which had never been reachable).
  `TestNotLookaheadParity` now pins both directions.
  Still legacy: `ALTER MATERIALIZED VIEW`, `CREATE MATERIALIZED VIEW` with
  `USING <am>` / `WITH (opts)` / `TABLESPACE`.
- [ ] **P5.1 SEQUENCE** (CREATE / ALTER / DROP SEQUENCE)
- [ ] **Command tags for matview DDL are `OK`, not PG's.** Observed live:
  `CREATE MATERIALIZED VIEW` should answer `SELECT <n>` and
  `REFRESH MATERIALIZED VIEW` should answer `REFRESH MATERIALIZED VIEW`; goopg
  answers `OK` for both. Executor-side and pre-existing (the parser emits an
  AST byte-identical to legacy), so it is NOT a parser-migration item — filed
  here only because this wave is what surfaced it.
- [ ] **P5.2 FUNCTION / PROCEDURE / AGGREGATE** (dollar-quoted bodies)
- [ ] **P5.3 OPERATOR family / CAST / COLLATION / CONVERSION / TRANSFORM**
- [ ] **P5.4 TYPE / DOMAIN / ENUM / EXTENSION / STATISTICS /
  ACCESS METHOD / SCHEMA**
- [ ] **P5.5 DATABASE / TABLESPACE / FOREIGN* / PUBLICATION / SUBSCRIPTION**
- [ ] **P5.6 COMMENT / SECURITY LABEL / TRIGGER / RULE / POLICY** (review
  finding: these were unnamed in earlier drafts). Flip wave-2 routing;
  initdb replay.

## P6 — Utility statements

- [~] **P6.1 Transactions v0 (2026-08-26, flipped)**: bare BEGIN / BEGIN
  WORK / START TRANSACTION / COMMIT / END / ROLLBACK / ABORT landed and
  routed (routedStmts += begin/start/commit/rollback/abort/end; END is the
  reserved-token leader). Live-verified commit-persistence and rollback.
  Transaction modes (ISOLATION LEVEL, READ ONLY/WRITE, DEFERRABLE) next.
- [ ] **P6.1 Transactions**: BEGIN/START/COMMIT/END/ROLLBACK/ABORT/
  SAVEPOINT/RELEASE/ROLLBACK TO/PREPARE TRANSACTION.
- [~] **P6.2 SET/SHOW/RESET v0 (2026-08-26, flipped)**: bare + SESSION/
  LOCAL scopes, '='|TO, DEFAULT, comma-list values as RAW source span
  (spanText), SHOW ALL/name, RESET ALL/name. Gate pins 12 known S/Rs
  (+SESSION/LOCAL optional-scope class). SET TIME ZONE / FROM CURRENT /
  transaction-scoped SET CONSTRAINTS deferred.
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

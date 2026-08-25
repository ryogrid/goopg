/* grammar/pg_grammar.y — goyacc port of postgres/src/backend/parser/gram.y */
/* (PostgreSQL 18.3, READ-ONLY oracle). Conventions: */
/* docs/design/not_ralph/02-grammar-porting-guide.md. Every rule block cites */
/* its upstream line range. */
/*  */
/* Keyword tokens come from grammar/tokens_gen.y and the five keyword-list */
/* nonterminals from grammar/kwlists_gen.y (both generated from kwlist.h; */
/* replaces gram.y :700-795 and :17685-18330). */
/*  */
/* WAVE STATE (TODO.md): statement surface = SELECT core subset (P1.1). */
/* Everything not yet ported raises a clean syntax error here while the */
/* dispatch router keeps those classes on the legacy parser. */

%start root

/* Non-keyword terminals (gram.y :692-699 base set; named operator terminals */
/* per scan.l :968-977 — the adapter splits them by string value, */
/* 05-risks #11). */
%token <str> IDENT UIDENT FCONST SCONST USCONST BCONST XCONST Op
%token <ival> ICONST PARAM
%token TYPECAST DOT_DOT COLON_EQUALS EQUALS_GREATER
%token LESS_EQUALS GREATER_EQUALS NOT_EQUALS
%token NOT_LA NULLS_LA WITH_LA WITHOUT_LA FORMAT_LA

/* Precedence: lowest to highest — port of gram.y :824-903 WITH ONE DEVIATION:
   the %left UNION/INTERSECT/EXCEPT entries are REMOVED because goopg's AST
   is right-recursive single-slot (legacy parity) and set-op chaining is
   expressed structurally (setop_tail), not via precedence. Everything else
   is verbatim. */

%left		OR
%left		AND
%right		NOT
%nonassoc	IS ISNULL NOTNULL	/* IS sets precedence for IS NULL, etc */
%nonassoc	'<' '>' '=' LESS_EQUALS GREATER_EQUALS NOT_EQUALS
%nonassoc	BETWEEN IN_P LIKE ILIKE SIMILAR NOT_LA
%nonassoc	ESCAPE			/* ESCAPE must be just above LIKE/ILIKE/SIMILAR */
%nonassoc	UNBOUNDED NESTED /* ideally would have same precedence as IDENT */
%nonassoc	IDENT PARTITION RANGE ROWS GROUPS PRECEDING FOLLOWING CUBE ROLLUP
			SET KEYS OBJECT_P SCALAR VALUE_P WITH WITHOUT PATH
%left		Op OPERATOR		/* multi-character ops and user-defined operators */
%left		'+' '-'
%left		'*' '/' '%'
%left		'^'
/* Unary Operators */
%left		AT				/* sets precedence for AT TIME ZONE, AT LOCAL */
%left		COLLATE
%right		UMINUS
%left		'[' ']'
%left		'(' ')'
%left		TYPECAST
%left		'.'
/* JOIN operators get high precedence so they may also serve as function
 * names (gram.y :886-895). */
%left		JOIN CROSS LEFT FULL RIGHT INNER_P NATURAL

%type <stmts>	root stmt_list
%type <stmt>	stmt SelectStmt simple_select base_select
%type <node>	setop_tail setop_op
%type <b>	opt_recursive
%type <ctes>	cte_list
%type <node>	cte_item
%type <str>	opt_materialized
%type <withc>	with_clause
%type <node>	opt_with_clause
%type <b>	set_quantifier
%type <p>	select_pos
%type <node>	opt_all_distinct
%type <targets>	opt_target_list target_list
%type <rt>	target_el
%type <fexprs>	from_list opt_from_clause
%type <fexpr>	table_ref
%type <rvar>	base_table_ref
%type <jspec>	join_outer
%type <node>	join_qual_opt opt_derived_alias group_clause opt_with_ordinality
%type <node>	func_table_expr
%type <rfes>	row_from_list
%type <rfe>	row_from_entry_one

%type <strs>	col_alias_list cte_col_list
%type <exprs>	opt_func_arg_list func_arg_list
%type <rvar>	relation_expr_opt_alias
%type <qn>	qualified_name
%type <expr>	a_expr c_expr where_clause having_clause
%type <exprs>	expr_list group_by_list
%type <vrows>	VALUES_rows_tail
%type <exprs>	VALUES_row
%type <expr>	group_by_item
%type <node>	opt_select_limit select_limit limit_clause offset_clause
%type <sortbys>	opt_sort_clause sort_by_list sort_clause
%type <sortby>	SortBy
%type <expr>	select_limit_value select_offset_value select_fetch_first_value
%type <str>		first_or_next
%type <str>	ColId ColLabel BareColLabel first_or_next opt_alias_ident
%type <node>	row_or_rows_opt row_or_rows

%%

/* root / stmt_list — gram.y :961 stmtmulti (statement-per-';' batch), */
/* adapted: the start production hands the finished []parser.Stmt to the */
/* lexer state instead of returning a node list. */
root:
		/* empty */
		{
			yylex.(*lexerState).out = nil
		}
	| stmt_list
		{
			yylex.(*lexerState).out = $1
		}

stmt_list:
		stmt
			{
				$$ = []parser.Stmt{$1}
			}
	| stmt_list ';' stmt
			{
				$$ = append($1, $3)
			}
	| stmt_list ';'
			{
				$$ = $1 // trailing semicolon(s), gram.y stmtmulti ';' alt
			}

stmt:
		SelectStmt
			{
				$$ = $1
			}

/* SelectStmt — gram.y :12823 (P1.1 routes the parenthesised-select wrapper */
/* through the same path later; TODO P1.6). */
// SelectStmt — legacy recursive-descent shape: a base SELECT (with its own
// optional sort/limit), then an optional right-recursive set-op tail whose
// RHS is a FULL SelectStmt. Trailing ORDER BY/LIMIT/OFFSET therefore land
// on the innermost RHS first; foldSetOps lifts them outward when that RHS
// is not explicitly parenthesized (M0097-0024/M0097-0042).
SelectStmt:
		/* TERMINAL-ESCAPE RULE: this alternative must exist so the
		   cte_item -> SelectStmt -> with_clause cycle has a terminal-only
		   derivation; without it goyacc reports never-derives for the whole
		   chain. */
		base_select setop_tail
			{
				base, ok := $1.(*parser.SelectStmt)
				if !ok || base == nil {
					base = parser.NewSelectStmt(0)
				}
				tail := $2.(*setopChain)
				$$ = foldSetOps(base, tail.pairs)
			}
	| with_clause base_select setop_tail
			{
				base, ok := $2.(*parser.SelectStmt)
				if !ok || base == nil {
					base = parser.NewSelectStmt(0)
				}
				if wc := $1; wc != nil {
					// Legacy: With attaches to the OUTERMOST select only.
					base.With = wc
				}
				tail := $3.(*setopChain)
				$$ = foldSetOps(base, tail.pairs)
			}

// base_select — simple_select plus its trailing sort/limit (gram.y
// select_clause sort_clause / select_limit combinations, subset).
base_select:
		simple_select opt_sort_clause opt_select_limit
			{
				m := $1.(*parser.SelectStmt)
				m.OrderBy = $2
				if sl, ok := $3.(*selectLimit); ok && sl != nil {
					m.Limit = sl.count
					m.Offset = sl.offset
					m.WithTies = sl.withTies
				}
				$$ = m
			}
	| VALUES VALUES_row VALUES_rows_tail opt_sort_clause opt_select_limit
			{
				rows := append([][]parser.Expr{$2}, $3...)
				m := newValuesSelect(0, rows)
				if sl, ok := $5.(*selectLimit); ok && sl != nil {
					m.Limit = sl.count
					m.Offset = sl.offset
					m.WithTies = sl.withTies
				}
				m.OrderBy = $4
				$$ = m
			}
	| TABLE qualified_name opt_sort_clause opt_select_limit
			{
				s := tableSelect(0, $2)
				s.OrderBy = $3
				if sl, ok := $4.(*selectLimit); ok && sl != nil {
					s.Limit = sl.count
					s.Offset = sl.offset
					s.WithTies = sl.withTies
				}
				$$ = s
			}

VALUES_row:
		'(' expr_list ')'
			{
				$$ = $2
			}

VALUES_rows_tail:
		/* empty */
			{ $$ = nil }
	| VALUES_rows_tail ',' VALUES_row
			{
				$$ = append($1, $3)
			}


setop_tail:
		/* empty */
			{
				$$ = &setopChain{}
			}
	| setop_op SelectStmt
			{
				op := $1.(*opSpec)
				rt, _ := $2.(*parser.SelectStmt)
				$$ = &setopChain{pairs: []setopPair{{op: op, right: rt}}}
			}

setop_op:
		UNION
			{
				$$ = &opSpec{typ: parser.SetOpUnion, pos: yylex.(*lexerState).lastConsumedPos()}
			}
	| UNION ALL
			{
				$$ = &opSpec{typ: parser.SetOpUnion, all: true, pos: yylex.(*lexerState).lastConsumedPos()}
			}
	| UNION DISTINCT
			{
				$$ = &opSpec{typ: parser.SetOpUnion, pos: yylex.(*lexerState).lastConsumedPos()}
			}
	| INTERSECT
			{
				$$ = &opSpec{typ: parser.SetOpIntersect, pos: yylex.(*lexerState).lastConsumedPos()}
			}
	| INTERSECT ALL
			{
				$$ = &opSpec{typ: parser.SetOpIntersect, all: true, pos: yylex.(*lexerState).lastConsumedPos()}
			}
	| EXCEPT
			{
				$$ = &opSpec{typ: parser.SetOpExcept, pos: yylex.(*lexerState).lastConsumedPos()}
			}
	| EXCEPT ALL
			{
				$$ = &opSpec{typ: parser.SetOpExcept, all: true, pos: yylex.(*lexerState).lastConsumedPos()}
			}

// select_with_parens — gram.y :12828-12831. goopg addition: mark
// Parenthesized=true (legacy parseParenthesisedSelectStmt stamps it;
// planner stops flattening there, M0097-0042).
// select_clause — gram.y :12922-12926.
// select_no_parens — gram.y :12837-12920 subset (locking clauses arrive
// with P6; with_clause with P1.7). Set-op alternatives resolve via the
// UNION/INTERSECT/EXCEPT %left declarations exactly like upstream, and
// makeSetOp builds legacy's single-SetOp-slot shape.
// set_quantifier — gram.y :13459ff companion (empty = DISTINCT default).
set_quantifier:
		/* empty */ { $$ = false }
	| ALL             { $$ = true }
	| DISTINCT        { $$ = false }

/* select_pos captures the SELECT token's byte position via the adapter's */
/* prev-token tracking (goyacc has no @$; see 02 §5). */
select_pos:
		/* empty */
			{
				$$ = yylex.(*lexerState).lastConsumedPos()
			}


/* simple_select — gram.y :12935, P1.1 subset: SELECT [ALL|DISTINCT] */
/* targets [FROM ...] [WHERE ...]. into/group/having/window clauses arrive */
/* with later P1 sub-phases (TODO P1.3); their absence here is what keeps */
/* unrouted inputs failing cleanly toward the legacy parser. */
simple_select:
		SELECT select_pos opt_all_distinct opt_target_list opt_from_clause where_clause group_clause having_clause
			{
				s := parser.NewSelectStmt($2)
				if di, ok := $3.(*distinctInfo); ok && di != nil {
					s.Distinct = di.distinct
					s.DistinctOn = di.on
				}
				s.Targets = $4
				// Flatten each comma-item into legacy's dual representation:
				// s.From carries Base plus every JoinExpr.Right; s.FromExprs
				// preserves the JOIN chains (planner reads structure here).
				for _, fe := range $5 {
					s.FromExprs = append(s.FromExprs, fe)
					s.From = append(s.From, fe.Base)
					for _, jn := range fe.Joins {
						s.From = append(s.From, jn.Right)
					}
				}
				s.Where = $6
				if gc, ok := $7.(*groupClause); ok && gc != nil {
					s.GroupBy = gc.list
				}
				s.Having = $8
				$$ = s
				// NOTE: ORDER BY / LIMIT / OFFSET live one level up, in
				// select_no_parens (gram.y :12916 comment) — a set-op RHS
				// must not swallow them.
			}

// opt_sort_clause / sort_by_list / SortBy — gram.y :13196-13220.
opt_sort_clause:
		/* empty */
			{
				$$ = nil
			}
	| ORDER BY sort_by_list
			{
				$$ = $3
			}

sort_by_list:
		SortBy
			{
				$$ = []parser.SortBy{$1}
			}
	| sort_by_list ',' SortBy
			{
				$$ = append($1, $3)
			}

SortBy:
		a_expr
			{
				$$ = parser.NewSortBy($1.Pos(), $1, false, "")
			}
	| a_expr ASC
			{
				$$ = parser.NewSortBy($1.Pos(), $1, false, "")
			}
	| a_expr DESC
			{
				$$ = parser.NewSortBy($1.Pos(), $1, true, "")
			}
	| a_expr ASC NULLS_LA FIRST_P
			{
				$$ = parser.NewSortBy($1.Pos(), $1, false, "")
				v := true
				$$.NullsFirst = &v
			}
	| a_expr ASC NULLS_LA LAST_P
			{
				$$ = parser.NewSortBy($1.Pos(), $1, false, "")
				v := false
				$$.NullsFirst = &v
			}
	| a_expr DESC NULLS_LA FIRST_P
			{
				$$ = parser.NewSortBy($1.Pos(), $1, true, "")
				v := true
				$$.NullsFirst = &v
			}
	| a_expr DESC NULLS_LA LAST_P
			{
				$$ = parser.NewSortBy($1.Pos(), $1, true, "")
				v := false
				$$.NullsFirst = &v
			}

// opt_select_limit/select_limit/limit_clause/offset_clause — gram.y
// :13261-13360. The LIMIT #,# form reproduces upstream's in-action error.
opt_select_limit:
		/* empty */
			{
				$$ = nil
			}
	| select_limit
			{
				$$ = $1
			}

select_limit:
		limit_clause offset_clause
			{
				lc := $1.(*selectLimit)
				lc.offset = $2.(*selectLimit).offset
				lc.set = true
				$$ = lc
			}
	| offset_clause limit_clause
			{
				lc := $2.(*selectLimit)
				lc.offset = $1.(*selectLimit).offset
				lc.set = true
				$$ = lc
			}
	| limit_clause
			{
				$$ = $1
			}
	| offset_clause
			{
				$$ = $1
			}

limit_clause:
		LIMIT select_limit_value
			{
				$$ = &selectLimit{count: $2, set: true}
			}
	| LIMIT select_limit_value ',' select_offset_value
			{
				gateSyntaxError(yylex.(*lexerState),
					"LIMIT #,# syntax is not supported",
					"Use separate LIMIT and OFFSET clauses.")
				$$ = &selectLimit{set: true}
			}
	| FETCH first_or_next select_fetch_first_value row_or_rows ONLY
			{
				$$ = &selectLimit{count: $3, set: true}
			}
	| FETCH first_or_next row_or_rows ONLY
			{
				// Omitted count defaults to 1 (gram.y :13346 alt).
				$$ = &selectLimit{count: parser.NewIntegerConst(0, 1), set: true}
			}
	| FETCH first_or_next select_fetch_first_value row_or_rows WITH TIES
			{
				$$ = &selectLimit{count: $3, withTies: true, set: true}
			}

offset_clause:
		OFFSET select_offset_value row_or_rows_opt
			{
				$$ = &selectLimit{offset: $2, set: true}
			}

row_or_rows_opt:
		/* empty */ { $$ = nil }
	| ROW         { $$ = nil }
	| ROWS        { $$ = nil }

row_or_rows:
		ROW  { $$ = nil }
	| ROWS { $$ = nil }

first_or_next:
		FIRST_P { $$ = "" }
	| NEXT     { $$ = "" }

select_limit_value:
		a_expr
			{
				$$ = $1
			}

select_offset_value:
		a_expr
			{
				$$ = $1
			}

// select_fetch_first_value — gram.y :13346ff: c_expr or signed ICONST/FCONST.
select_fetch_first_value:
		c_expr
			{
				$$ = $1
			}
	| '-' ICONST
			{
				e := parser.NewIntegerConst(yylex.(*lexerState).lastConsumedPos(), int64(-$2))
				$$ = e
			}
	| '-' FCONST
			{
				$$ = parser.NewNumericConst(yylex.(*lexerState).lastConsumedPos(), "-"+$2)
			}

/* opt_all_clause/distinct_clause collapsed into one carrier — upstream */
/* splits them (gram.y :13221 distinct_clause) because they sit in different */
/* positions of the two simple_select alternatives; our merged alternative */
/* needs one slot. Documented deviation, 02 §3 note. */
opt_all_distinct:
		/* empty */
			{
				$$ = nil
			}
	| ALL
			{
				$$ = nil
			}
	| DISTINCT
			{
				$$ = &distinctInfo{distinct: true}
			}
	| DISTINCT ON '(' expr_list ')'
			{
				// distinct_clause ON form, gram.y :13213. LEGACY QUIRK:
				// parseSelect leaves Distinct=false when DistinctOn is set
				// (dump-verified) even though the ast.go comment claims
				// otherwise — mirror legacy until cutover.
				$$ = &distinctInfo{on: $4}
			}

/* opt_target_list / target_list — gram.y :13505ish / :17246. */
opt_target_list:
		target_list
			{
				$$ = $1
			}

target_list:
		target_el
			{
				$$ = []parser.ResTarget{$1}
			}
	| target_list ',' target_el
			{
				$$ = append($1, $3)
			}

/* target_el — gram.y :17251-17287: AS alias, bare-label alias, bare expr, */
/* and '*'. */
target_el:
		a_expr AS ColLabel
			{
				$$ = parser.NewResTarget($1.Pos(), $3, $1)
			}
	| a_expr BareColLabel
			{
				$$ = parser.NewResTarget($1.Pos(), $2, $1)
			}
	| a_expr
			{
				$$ = parser.NewResTarget($1.Pos(), "", $1)
			}
	| '*'
			{
				p := yylex.(*lexerState).lastConsumedPos()
				$$ = parser.NewResTarget(p, "", parser.NewStarExpr(p, "", ""))
			}
	| qualified_name '.' '*'
			{
				// gram.y :17287 target_el qualified star (table.*).
				parts := $1.parts
				schema, table := "", ""
				switch n := len(parts); {
				case n == 1:
					table = parts[0]
				case n >= 2:
					schema, table = parts[n-2], parts[n-1]
				}
				$$ = parser.NewResTarget($1.pos, "", parser.NewStarExpr($1.pos, schema, table))
			}

/* from_clause — gram.y :13598. */
opt_from_clause:
		/* empty */
			{
				$$ = nil
			}
	| FROM from_list
			{
				$$ = $2
			}

from_list:
		table_ref
			{
				$$ = []parser.FromExpr{$1}
			}
	| from_list ',' table_ref
			{
				$$ = append($1, $3)
			}

/* table_ref — gram.y :13600 area. Shape note: upstream nests joined tables
   as a tree; goopg's AST flattens each comma-item into ONE FromExpr with an
   ordered Joins chain (parseFromItem, select.go:1250-1269), so this port
   reduces left-recursively and appends JoinExpr entries instead of building
   nested nodes. JOIN's high precedence (block above) preserves upstream
   associativity and delimits ON expressions. */
table_ref:
		base_table_ref
			{
				$$ = parser.NewFromExpr($1.Pos(), $1, nil)
			}
	| table_ref join_outer base_table_ref join_qual_opt
			{
				spec := $2
				q := joinQual{}
				if jq, ok := $4.(*joinQual); ok && jq != nil {
					q = *jq
				}
				j := buildJoin(yylex.(*lexerState), spec, $3, q)
				$$.Joins = append($1.Joins, j)
			}

/* join_outer — gram.y :13840-13975 prefix alternatives collapsed into one
   carrier. Reduction happens after the prefix's LAST keyword, so
   lastConsumedPos lands on the JOIN token (differs from upstream @1 only
   for NATURAL-prefixed spellings; content dumps unaffected). */
join_outer:
		JOIN
			{
				$$ = newJoinSpec(false, "inner")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| INNER_P JOIN
			{
				$$ = newJoinSpec(false, "inner")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| NATURAL JOIN
			{
				$$ = newJoinSpec(true, "inner")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| LEFT OUTER_P JOIN
			{
				$$ = newJoinSpec(false, "left")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| LEFT JOIN
			{
				$$ = newJoinSpec(false, "left")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| RIGHT OUTER_P JOIN
			{
				$$ = newJoinSpec(false, "right")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| RIGHT JOIN
			{
				$$ = newJoinSpec(false, "right")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| FULL OUTER_P JOIN
			{
				$$ = newJoinSpec(false, "full")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| FULL JOIN
			{
				$$ = newJoinSpec(false, "full")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| NATURAL LEFT OUTER_P JOIN
			{
				$$ = newJoinSpec(true, "left")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| NATURAL LEFT JOIN
			{
				$$ = newJoinSpec(true, "left")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| NATURAL RIGHT OUTER_P JOIN
			{
				$$ = newJoinSpec(true, "right")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| NATURAL RIGHT JOIN
			{
				$$ = newJoinSpec(true, "right")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| NATURAL FULL OUTER_P JOIN
			{
				$$ = newJoinSpec(true, "full")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| NATURAL FULL JOIN
			{
				$$ = newJoinSpec(true, "full")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}
	| CROSS JOIN
			{
				$$ = newJoinSpec(false, "cross")
				$$.pos = yylex.(*lexerState).lastConsumedPos()
			}

/* join_qual_opt — ON a_expr | USING '(' cols ')' | none (NATURAL/CROSS). */
join_qual_opt:
		/* empty */
			{
				$$ = nil
			}
	| ON a_expr
			{
				$$ = &joinQual{on: $2}
			}
	| USING '(' col_alias_list ')'
			{
				$$ = &joinQual{using: $3}
			}

col_alias_list:
		ColId
			{
				$$ = []string{$1}
			}
	| col_alias_list ',' ColId
			{
				$$ = append($1, $3)
			}

relation_expr_opt_alias:
		qualified_name %prec UMINUS
			{
				$$ = rangeVarFromName($1, "")
			}
	| qualified_name ColId
			{
				$$ = rangeVarFromName($1, $2)
			}
	| qualified_name AS ColId
			{
				$$ = rangeVarFromName($1, $3)
			}

/* base_table_ref — gram.y :13600 alternatives, P1.2 subset: plain relation
   (+ONLY inheritance limiter) and parenthesised subquery with the
   mandatory-in-practice alias (:1416-1452). Parenthesised join groups and
   function tables remain explicit P1.2 TODO sub-items. */
base_table_ref:
		relation_expr_opt_alias
			{
				$$ = $1
			}
	| ONLY qualified_name opt_alias_ident
			{
				rv := rangeVarFromName($2, $3)
				rv.Only = true
				$$ = rv
			}
	| '(' table_ref ')' opt_derived_alias
			{
				fe := $2
				lateral := false
				alias := ""
				var cols []string
				if da, ok2 := $4.(*derivedAlias); ok2 && da != nil {
					alias, cols = da.alias, da.cols
					lateral = da.lateral
				}
				// Group-start position approximated by the base item's own
				// position (legacy uses the '(' offset; a paren_pos mid-rule
				// here created an unresolvable S/R against nested groups).
				pos := fe.Base.Pos()
				sub := syntheticParenSelect(pos, fe)
				$$ = derivedRangeVar(yylex.(*lexerState), pos, sub, alias, cols, lateral)
			}
	| '(' SelectStmt ')' opt_derived_alias
			{
				sub, ok := $2.(*parser.SelectStmt)
				if !ok {
					lerr(yylex, "subquery in FROM did not produce SELECT", yylex.(*lexerState).lastConsumedPos())
					sub = parser.NewSelectStmt(0)
				}
				sub.Parenthesized = true
				pos := sub.Pos()
				lateral := false
				alias := ""
				var cols []string
				if da, ok2 := $4.(*derivedAlias); ok2 && da != nil {
					alias, cols = da.alias, da.cols
					lateral = da.lateral
				}
				$$ = derivedRangeVar(yylex.(*lexerState), pos, sub, alias, cols, lateral)
			}
	| LATERAL_P '(' SelectStmt ')' opt_derived_alias
			{
				sub, ok := $3.(*parser.SelectStmt)
				if !ok {
					lerr(yylex, "subquery in FROM did not produce SELECT", yylex.(*lexerState).lastConsumedPos())
					sub = parser.NewSelectStmt(0)
				}
				// Legacy parseParenthesisedSelectStmt marks every
				// paren-wrapped select Parenthesized=true (:1396-1402);
				// planner stops flattening at such branches.
				sub.Parenthesized = true
				pos := sub.Pos()
				lateral := true
				alias := ""
				var cols []string
				if da, ok2 := $5.(*derivedAlias); ok2 && da != nil {
					alias, cols = da.alias, da.cols
					lateral = lateral || da.lateral
				}
				$$ = derivedRangeVar(yylex.(*lexerState), pos, sub, alias, cols, lateral)
			}

	| func_table_expr opt_derived_alias
			{
				lateral := false
				alias := ""
				var cols []string
				if da, ok := $2.(*derivedAlias); ok && da != nil {
					alias, cols = da.alias, da.cols
					lateral = da.lateral
				}
				ft, _ := $1.(*funcTable)
				if ft == nil {
					ft = &funcTable{ref: parser.NewTableFuncRef(0, "__missing__", nil, false, nil)}
				}
				rv := parser.NewRangeVar(ft.ref.Pos(), ft.schema, "", alias)
				rv.TableFunc = ft.ref
				rv.Lateral = lateral
				rv.Columns = cols
				$$ = rv
			}
	| LATERAL_P func_table_expr opt_derived_alias
			{
				lateral := false
				alias := ""
				var cols []string
				if da, ok := $3.(*derivedAlias); ok && da != nil {
					alias, cols = da.alias, da.cols
					lateral = da.lateral
				}
				ft, _ := $2.(*funcTable)
				if ft == nil {
					ft = &funcTable{ref: parser.NewTableFuncRef(0, "__missing__", nil, false, nil)}
				}
				rv := parser.NewRangeVar(ft.ref.Pos(), ft.schema, "", alias)
				rv.TableFunc = ft.ref
				rv.Lateral = lateral
				rv.Columns = cols
				$$ = rv
			}

/* func_table_expr — gram.y func_table :13930ish subset: name(args)
   [WITH ORDINALITY] and ROWS FROM(name(args), ...) [WITH ORDINALITY].
   Name normalization per legacy select.go:1499-1528. */
func_table_expr:
		qualified_name '(' opt_func_arg_list ')'
			{
				ft := splitFuncName($1)
				ft.ref = newTableFuncRef($1.pos, funcTableName(ft.schema, ft.name), $3, false, nil)
				$$ = ft
			}
	| qualified_name '(' opt_func_arg_list ')' WITH_LA ORDINALITY
			{
				ft := splitFuncName($1)
				ft.ref = newTableFuncRef($1.pos, funcTableName(ft.schema, ft.name), $3, true, nil)
				$$ = ft
			}
	| ROWS FROM '(' row_from_list ')' opt_with_ordinality
			{
				ord := $6 == ordYes
				$$ = &funcTable{ref: newTableFuncRef(0, "", nil, ord, $4)}
			}

/* opt_ordinality — gram.y :14069: WITH_LA (base_yylex substitutes
   WITH->WITH_LA before ORDINALITY), keeping this optional clause
   conflict-free against WITH-led continuations. */
opt_with_ordinality:
		/* empty */ { $$ = ordNo }
	| WITH_LA ORDINALITY { $$ = ordYes }

row_from_list:
		row_from_entry_one
			{
				$$ = []parser.RowsFromEntry{$1}
			}
	| row_from_list ',' row_from_entry_one
			{
				$$ = append($1, $3)
			}

row_from_entry_one:
		qualified_name '(' opt_func_arg_list ')'
			{
				$$ = parser.RowsFromEntry{Name: rowsFromName($1.parts), Args: $3}
			}

opt_func_arg_list:
		/* empty */ { $$ = nil }
	| func_arg_list { $$ = $1 }

func_arg_list:
		a_expr
			{
				$$ = []parser.Expr{$1}
			}
	| func_arg_list ',' a_expr
			{
				$$ = append($1, $3)
			}

/* opt_derived_alias — AS alias / bare IDENT / +column list; missing alias
   triggers the synthetic __sq_<pos> fallback (legacy :1427-1432 mirrors
   PG16). Bare-ident form accepts plain IDENT only (isAliasStart subset;
   unreserved-keyword aliases arrive with generated BareColLabel wiring at
   the next sub-phase if any corpus case needs them). */
opt_derived_alias:
		/* empty */
			{
				$$ = &derivedAlias{}
			}
	| AS ColId
			{
				$$ = &derivedAlias{alias: $2}
			}
	| IDENT
			{
				$$ = &derivedAlias{alias: $1}
			}
	| AS ColId '(' col_alias_list ')'
			{
				$$ = &derivedAlias{alias: $2, cols: $4}
			}
	| ColId '(' col_alias_list ')'
			{
				$$ = &derivedAlias{alias: $1, cols: $3}
			}
	| IDENT '(' col_alias_list ')'
			{
				$$ = &derivedAlias{alias: $1, cols: $3}
			}

opt_alias_ident:
		/* empty */ { $$ = "" }
	| AS ColId        { $$ = $2 }
	| ColId           { $$ = $1 }

/* group_clause / group_by_list / group_by_item — gram.y :13456-13484.
   P1.3 subset: plain a_expr items. empty_grouping_set / CUBE / ROLLUP /
   GROUPING SETS carry legacy expansion machinery and are tracked as TODO
   P1.3a; set_quantifier (GROUP BY DISTINCT/ALL, PG18) is a documented
   legacy divergence (legacy parser rejects it) — see difftest_known_diffs. */
group_clause:
		/* empty */
			{
				$$ = nil
			}
	| GROUP_P BY group_by_list
			{
				$$ = &groupClause{list: $3}
			}

group_by_list:
		group_by_item
			{
				$$ = []parser.Expr{$1}
			}
	| group_by_list ',' group_by_item
			{
				$$ = append($1, $3)
			}

group_by_item:
		a_expr
			{
				$$ = $1
			}

/* sort_clause — gram.y :13196 (mandatory ORDER BY variant). */
sort_clause:
		ORDER BY sort_by_list
			{
				$$ = $3
			}

/* having_clause — gram.y :13522. */
opt_with_clause:
		/* empty */ { $$ = nil }
	| with_clause     { $$ = $1 }

with_clause:
		WITH opt_recursive cte_list
			{
				$$ = parser.NewWithClause(0, $2, $3)
			}

opt_recursive:
		/* empty */ { $$ = false }
	| RECURSIVE      { $$ = true }

cte_list:
		cte_item
			{
				ci, _ := $1.(*cteItem)
				if ci == nil || ci.cte == nil {
					ci = &cteItem{cte: parser.NewCommonTableExpr(0, "", nil, nil)}
				}
				$$ = []*parser.CommonTableExpr{ci.cte}
			}
	| cte_list ',' cte_item
			{
				ci, _ := $3.(*cteItem)
				if ci == nil || ci.cte == nil {
					ci = &cteItem{cte: parser.NewCommonTableExpr(0, "", nil, nil)}
				}
				$$ = append($1, ci.cte)
			}

/* cte_item — gram.y :13030ish subset: SELECT body only (DML bodies arrive
   with P3). MATERIALIZED markers per M0097-0047. */
cte_item:
		ColId cte_col_list AS opt_materialized '(' SelectStmt ')'
			{
				sub, ok := $6.(*parser.SelectStmt)
				if !ok || sub == nil {
					sub = parser.NewSelectStmt(0)
				}
				mat := $4
				cte := parser.NewCommonTableExpr(0, $1, $2, sub)
				cte.Materialized = mat
				$$ = &cteItem{cte: cte}
			}

cte_col_list:
		/* empty */           { $$ = nil }
	| '(' col_alias_list ')'  { $$ = $2 }

opt_materialized:
		/* empty */    { $$ = "" }
	| MATERIALIZED         { $$ = "materialized" }
	| NOT MATERIALIZED     { $$ = "not materialized" }

having_clause:
		/* empty */
			{
				$$ = nil
			}
	| HAVING a_expr
			{
				$$ = $2
			}

/* expr_list — gram.y :13944ish; reused by DISTINCT ON and later waves. */
expr_list:
		a_expr
			{
				$$ = []parser.Expr{$1}
			}
	| expr_list ',' a_expr
			{
				$$ = append($1, $3)
			}

/* where_clause — gram.y :14074. */
where_clause:
		/* empty */
			{
				$$ = nil
			}
	| WHERE a_expr
			{
				$$ = $2
			}

/* qualified_name — gram.y :14024ish ColId('.'attr_name)*; parts kept as */
/* strings; interpretation (schema/table/column vs schema/table) happens at */
/* the consumption sites. */
qualified_name:
		ColId
			{
				$$ = qname{parts: []string{$1}, pos: yylex.(*lexerState).lastConsumedPos()}
			}
	| qualified_name '.' ColId
			{
				$$ = qname{parts: append($1.parts, $3), pos: $1.pos}
			}

/* a_expr — gram.y :15464ff, P1.1 operator subset. Precedence comes from the */
/* verbatim block above, so these rules stay flat exactly like upstream's. */
a_expr:
		c_expr
			{
				$$ = $1
			}
	| a_expr '+' a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpAdd, $1, $3)
			}
	| a_expr '-' a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpSub, $1, $3)
			}
	| a_expr '*' a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpMul, $1, $3)
			}
	| a_expr '/' a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpDiv, $1, $3)
			}
	| a_expr '%' a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpMod, $1, $3)
			}
	| a_expr '<' a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpLt, $1, $3)
			}
	| a_expr '>' a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpGt, $1, $3)
			}
	| a_expr '=' a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpEq, $1, $3)
			}
	| a_expr LESS_EQUALS a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpLe, $1, $3)
			}
	| a_expr GREATER_EQUALS a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpGe, $1, $3)
			}
	| a_expr NOT_EQUALS a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpNe, $1, $3)
			}
	| a_expr AND a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpAnd, $1, $3)
			}
	| a_expr OR a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpOr, $1, $3)
			}
	| NOT a_expr
			{
				$$ = parser.NewUnaryOp(yylex.(*lexerState).lastConsumedPos(), parser.OpNot, $2)
			}
	| '-' a_expr %prec UMINUS
			{
				$$ = foldNegate($2)
			}
	| '+' a_expr %prec UMINUS
			{
				$$ = parser.NewUnaryOp(yylex.(*lexerState).lastConsumedPos(), parser.OpUnaryPos, $2)
			}
	| '(' a_expr ')'
			{
				$$ = $2
			}

/* c_expr — gram.y :15640ff, P1.1 subset: literals, parameters, column refs. */
c_expr:
		ICONST
			{
				/* $1 is already the parsed integer (adapter fills ival). */
				$$ = parser.NewIntegerConst(yylex.(*lexerState).lastConsumedPos(), int64($1))
			}
	| FCONST
			{
				$$ = parser.NewNumericConst(yylex.(*lexerState).lastConsumedPos(), $1)
			}
	| SCONST
			{
				$$ = parser.NewStringConst(yylex.(*lexerState).lastConsumedPos(), $1)
			}
	| TRUE_P
			{
				$$ = parser.NewBooleanConst(yylex.(*lexerState).lastConsumedPos(), true)
			}
	| FALSE_P
			{
				$$ = parser.NewBooleanConst(yylex.(*lexerState).lastConsumedPos(), false)
			}
	| NULL_P
			{
				$$ = parser.NewNullConst(yylex.(*lexerState).lastConsumedPos())
			}
	| PARAM
			{
				$$ = parser.NewParamRef(yylex.(*lexerState).lastConsumedPos(), $1)
			}
	| qualified_name
			{
				$$ = columnRefFromParts($1)
			}

/* Identifier context aliases — gram.y :17632-17720. Generated lists come */
/* from kwlists_gen.y. */
ColId:
		IDENT
			{
				$$ = $1
			}
	| unreserved_keyword
			{
				$$ = $1
			}
	| col_name_keyword
			{
				$$ = $1
			}

ColLabel:
		IDENT
			{
				$$ = $1
			}
	| unreserved_keyword
			{
				$$ = $1
			}
	| col_name_keyword
			{
				$$ = $1
			}
	| type_func_name_keyword
			{
				$$ = $1
			}

BareColLabel:
		IDENT
			{
				$$ = $1
			}
	| bare_label_keyword
			{
				$$ = $1
			}

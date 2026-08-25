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

/* Precedence: lowest to highest — VERBATIM port of gram.y :824-903 */
%left		UNION EXCEPT
%left		INTERSECT
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
%type <stmt>	stmt SelectStmt simple_select
%type <p>	select_pos
%type <node>	opt_all_distinct
%type <targets>	opt_target_list target_list
%type <rt>	target_el
%type <fexprs>	from_list opt_from_clause
%type <fexpr>	table_ref
%type <rvar>	base_table_ref
%type <jspec>	join_outer
%type <node>	opt_lateral join_qual_opt opt_derived_alias
%type <strs>	col_alias_list
%type <rvar>	relation_expr_opt_alias
%type <qn>	qualified_name
%type <expr>	a_expr c_expr where_clause
%type <node>	opt_select_limit select_limit limit_clause offset_clause
%type <sortbys>	opt_sort_clause sort_by_list
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
SelectStmt:
		simple_select
			{
				$$ = $1
			}

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
		SELECT select_pos opt_all_distinct opt_target_list opt_from_clause where_clause opt_sort_clause opt_select_limit
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
				s.OrderBy = $7
				if sl, ok := $8.(*selectLimit); ok && sl != nil {
					s.Limit = sl.count
					s.Offset = sl.offset
					s.WithTies = sl.withTies
				}
				$$ = s
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
	| opt_lateral '(' SelectStmt ')' opt_derived_alias
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
				lateral := false
				alias := ""
				var cols []string
				if da, ok2 := $5.(*derivedAlias); ok2 && da != nil {
					alias, cols = da.alias, da.cols
					lateral = da.lateral || $1 == latYes
				}
				$$ = derivedRangeVar(yylex.(*lexerState), pos, sub, alias, cols, lateral)
			}

/* opt_lateral — LATERAL_P presence marker. */
opt_lateral:
		/* empty */ { $$ = latNo }
	| LATERAL_P        { $$ = latYes }

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

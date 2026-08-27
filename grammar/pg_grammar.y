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
			SET KEYS OBJECT_P SCALAR VALUE_P WITH WITHOUT PATH OVER
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
%type <stmt>	stmt SelectStmt simple_select base_select select_with_parens select_no_parens select_bare
%type <node>	setop_tail setop_op
%type <ctes>	cte_list
%type <node>	cte_item
%type <stmt>	cte_dml_body
%type <str>	opt_materialized
%type <withc>	with_clause
%type <node>	opt_with_clause
%type <b>	set_quantifier opt_if_exists_drop opt_restart opt_if_not_exists opt_unique opt_drop_if_exists
%type <p>	select_pos begin_pos
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

%type <strs>	pk_cols uq_cols col_alias_list cte_col_list opt_name_list_p
%type <onames>	drop_name_list
%type <node>	at_constraint at_constr_tail fn_type fn_return fn_attrs fn_attr func_arg fn_drop_item opt_call_named_args call_named_arg_list call_named_arg
%type <fargs>	opt_func_args func_arg_list_p
%type <fitems>	fn_drop_extras
%type <str>	fn_param_name fn_number fn_lang_name fn_set_values fn_body_list fn_config_name fn_config_value
%type <ival>	fn_arg_mode
%type <expr>	opt_arg_default
%type <expr>	opt_at_using
%type <b>	opt_detach_tail
%type <ival>	rule_state
%type <exprs>	arbiter_elem_list
%type <expr>	arbiter_elem
%type <node>	explain_opt_list explain_opt
%type <str>	explain_opt_name explain_opt_value
%type <node>	group_by_item group_by_list gs_list gs_elem fk_set_act opt_check_option tbl_check_tail
%type <strs>	opt_fk_set_cols
%type <b>	opt_fk_match opt_enforced opt_gen_storage opt_idx_nnd
%type <strs>	opt_view_with
%type <ctt>	ct_tail_list ct_tail_item
%type <str>	opt_tablespace opt_idx_tablespace check_body opt_ins_alias
%type <expr>	func_arg_expr
%type <exprs>	array_expr array_expr_list substr_list overlay_list position_list
%type <exprs>	trim_list
%type <str>	sort_using_op constr_name func_name_keyword opt_part_opclass
%type <node>	part_elem part_elem_list opt_subpartition_by ctas_source into_clause paren_tail partof_elem partof_elem_list partof_col_opts opt_of_col_opts of_col_opt of_col_opt_list opt_identity_seq_opts identity_seq_opt_list identity_seq_opt
%type <i64>	signed_iconst
%type <expr>	opt_paren_limit opt_paren_offset
%type <exprs>	opt_execute_params

%type <strs>	constr_name_list
%type <b>	constraints_set_mode opt_no_inherit opt_ctas_with_data
%type <node>	trunc_name_list trunc_name
%type <exprs>	opt_func_call_args
%type <node>	opt_call_args call_arg_list call_arg
%type <exprs>	opt_func_arg_list func_arg_list
%type <rvar>	relation_expr_opt_alias
%type <qn>	qualified_name
%type <expr>	a_expr c_expr where_clause having_clause b_expr name_or_call
%type <expr>	func_expr_common_subexpr
%type <str>	sql_value_func_name
%type <node>	cse_wl when_then filter_clause within_group_clause
%type <exprs>	opt_func_call_args
%type <str>	subq_op extract_field
%type <str>	opt_tzmark double_tail cast_ident character_word opt_upd_alias
%type <vrows>	values_rows
%type <node>	opt_create_modifier opt_TRUNCATE_kw alter_table_action alter_action_list part_bound_spec2
%type <b>	opt_ONLY_kw opt_or_replace opt_with_data
%type <node>	opt_COLUMN
%type <node>	index_col_list index_col opt_index_nulls
%type <str>	opt_index_collate
%type <node>	opt_index_opclass
%type <ival>	opclass_opt_list opclass_opt
%type <b>	opt_index_dir
%type <node>	opt_index_with
%type <str>	opt_drop_behavior
%type <b>	opt_if_not_exists
%type <str>	opt_index_name
%type <str>	opt_like_options like_option
%type <node>	exclude_elem_list exclude_elem opt_exclude_where
%type <str>	exclude_op
%type <node>	opt_table_element_list opt_index_where
%type <node>	opt_constr_attrs
%type <str>	constr_attr
%type <strs>	opt_include
%type <b>	opt_unique_nnd
%type <node>	opt_for_locking for_locking_clause for_locking_item for_locking_strength opt_lock_wait_policy
%type <strs>	opt_locked_rels
%type <exprs>	values_item_list
%type <expr>	values_item
%type <str>	set_guc_name set_value_list set_value_atom
%type <b>	opt_transaction opt_savepoint_kw
%type <stmt>	set_transaction_stmt
%type <stmt>	refresh_matview_stmt drop_matview_stmt
%type <b>	opt_concurrently
%type <stmt>	create_function_stmt drop_function_stmt call_stmt tx_begin tx_commit tx_rollback alter_table_stmt create_index_stmt drop_index_stmt create_table_stmt_as drop_table_stmt truncate_stmt create_table_stmt delete_stmt delete_core update_stmt update_core insert_stmt insert_core set_stmt show_stmt reset_stmt create_view_stmt drop_view_stmt create_matview_stmt

%type <node>	table_element_list table_element col_type_name col_constraints col_constraint
%type <strs>	str_pair_list
%type <str>	str_pair
%type <isrc>	insert_source
%type <oct>	opt_arbiter
%type <ualist> update_set_list
%type <ua>	update_assign
%type <strs>	opt_ref_cols
%type <node>	opt_fk_actions fk_actions fk_action fk_kw
%type <expr>	opt_update_where
%type <ctt>
%type <strs>	str_pair_list
%type <str>	str_pair with_value opt_using_method
%type <node>	upd_where del_where
%type <b>	set_scope
%type <str>	set_eq_to
%type <strs>
%type <rvars>	opt_upd_from opt_using_list
%type <rvars>	upd_from_list
%type <oc>	opt_on_conflict
%type <targets>	opt_returning
%type <strs>	colid_list
%type <node>	insert_rest
%type <ct>	cast_target cast_typename
%type <ivq>	interval_qual
%type <str>	iv_field
%type <ival>	opt_array_tail
%type <exprs>	expr_list
%type <expr>
%type <node>	opt_select_limit select_limit limit_clause offset_clause
%type <sortbys>	opt_sort_clause sort_by_list sort_clause
%type <sortby>	SortBy
%type <expr>	select_limit_value select_offset_value select_fetch_first_value
%type <str>		first_or_next
%type <str>	ColId ColLabel BareColLabel as_col_label first_or_next opt_alias_ident
%type <wd>	opt_window_spec
%type <fr>	opt_frame_tail
%type <node>	part_frame_extent part_frame_bound part_frame_excl
%type <nwd>	window_definition
%type <nwds>	opt_window_clause window_definition_list
%type <node>	row_or_rows_opt row_or_rows opt_tx_modes tx_mode_list tx_mode iso_level

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
	/* EXPLAIN — gram.y ExplainStmt. The inner statement is any routed `stmt`;
	   the dispatcher only routes an EXPLAIN whose inner statement it would
	   route on its own (explainInnerRouted), so an unported inner class falls
	   to legacy instead of surfacing a 42601. Bare ANALYZE / VERBOSE words and
	   the parenthesised option list are both legacy-accepted; the list's
	   names and values are validated by applyExplainOpts exactly as legacy's
	   parseExplainOneOption does. */
		EXPLAIN stmt
			{
				$$ = parser.NewExplainStmt($<p>1, parser.ExplainOptions{}, $2)
			}
	| EXPLAIN ANALYZE stmt
			{
				o := parser.ExplainOptions{Analyze: true}
				o.Set.Analyze = true
				$$ = parser.NewExplainStmt($<p>1, o, $3)
			}
	| EXPLAIN VERBOSE stmt
			{
				o := parser.ExplainOptions{Verbose: true}
				o.Set.Verbose = true
				$$ = parser.NewExplainStmt($<p>1, o, $3)
			}
	| EXPLAIN ANALYZE VERBOSE stmt
			{
				o := parser.ExplainOptions{Analyze: true, Verbose: true}
				o.Set.Analyze, o.Set.Verbose = true, true
				$$ = parser.NewExplainStmt($<p>1, o, $4)
			}
	| EXPLAIN '(' explain_opt_list ')' stmt
			{
				$$ = parser.NewExplainStmt($<p>1, applyExplainOpts(yylex, $<p>1, $3.([]*explainOpt)), $5)
			}
	| SelectStmt
			{
				// `SELECT ... INTO name` becomes a CreateTableStmt. The wrap
				// happens HERE and not at the SelectStmt rule, because every
				// consumer of SelectStmt — CREATE VIEW's body, a derived
				// table, a CTE — asserts the concrete *parser.SelectStmt, and
				// handing one of them a CreateTableStmt panics. INTO is only
				// legal on a top-level statement anyway.
				$$ = intoWrap(yylex, $1)
			}
	| insert_stmt
			{
				$$ = $1
			}
	| update_stmt
			{
				$$ = $1
			}
	| delete_stmt
			{
				$$ = $1
			}
	| create_table_stmt
	| create_table_stmt_as
			{
				$$ = $1
			}
	| drop_table_stmt
			{
				$$ = $1
			}
	| truncate_stmt
			{
				$$ = $1
			}
	| create_index_stmt
			{
				$$ = $1
			}
	| drop_index_stmt
			{
				$$ = $1
			}
	| tx_begin
			{
				$$ = $1
			}
	| tx_commit
			{
				$$ = $1
			}
	| tx_rollback
			{
				$$ = $1
			}
	| set_stmt
			{
				$$ = $1
			}
	| show_stmt
			{
				$$ = $1
			}
	| reset_stmt
			{
				$$ = $1
			}
	| set_transaction_stmt
			{
				$$ = $1
			}
	| alter_table_stmt
			{
				$$ = $1
			}
	| create_view_stmt
			{
				$$ = $1
			}
	| create_function_stmt
			{
				$$ = $1
			}
	| drop_function_stmt
			{
				$$ = $1
			}
	| call_stmt
			{
				$$ = $1
			}
	| drop_view_stmt
			{
				$$ = $1
			}
	| create_matview_stmt
			{
				$$ = $1
			}
	| refresh_matview_stmt
			{
				$$ = $1
			}
	| drop_matview_stmt
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
/* The three tiers mirror gram.y :12823-12900 (SelectStmt / select_with_parens
   / select_no_parens) and exist for ONE reason: a parenthesised select must
   not be reducible to a complete statement while the parser still sits inside
   the parentheses. When it was (`simple_select: paren_select`, measured
   2026-08-27), `( (SELECT 1) . )` could reduce EITHER to a statement (for the
   outer select-parens) OR to c_expr (for the outer `'(' a_expr ')'`) — two
   reduce/reduce conflicts on ')' that rule order breaks the wrong way,
   killing `f((SELECT 1))`. In this layering the nested-paren case is a SHIFT
   (`'(' select_with_parens ')'`) against the c_expr reduce, and
   `%prec UMINUS` on the c_expr alternative lets ')' win it.

   Legacy shapes reproduced (parseParenthesisedSelectStmt, select.go:1007):
     (S)                 -> S, Parenthesized=true, no wrapper
     ((S))               -> same (stamping is idempotent)
     (S) UNION T / (S) ORDER BY / LIMIT / OFFSET
                         -> a fresh WRAPPER stmt {SetOpOperand: S(paren)} that
                            carries the set-op and the trailing clauses, with
                            ORDER BY / LIMIT / OFFSET lifted off a BARE right
                            branch exactly as foldSetOps lifts them.
   select_bare is legacy's parseSelect: NO leading '(' — CREATE VIEW's body and
   CTAS's source reject `AS (SELECT 1)` there, so they take select_bare. */
SelectStmt:
		select_no_parens %prec UMINUS
			{
				$$ = $1
			}
	| select_with_parens %prec UMINUS
			{
				s := $1.(*parser.SelectStmt)
				s.Parenthesized = true
				$$ = s
			}

select_with_parens:
		'(' select_no_parens ')'
			{
				$$ = $2
			}
	| '(' select_with_parens ')'
			{
				s := $2.(*parser.SelectStmt)
				s.Parenthesized = true
				$$ = s
			}

select_no_parens:
		select_bare
			{
				$$ = $1
			}
	| select_with_parens paren_tail
			{
				$$ = parenGroup($<p>1, $1.(*parser.SelectStmt), $2.(*parenTail))
			}

/* What may follow a parenthesised left operand: legacy's
   trailingClauseFollowsParens (select.go:982) admits exactly UNION /
   INTERSECT / EXCEPT / ORDER / LIMIT / OFFSET, in that fixed order and with
   plain a_expr limits — no FETCH, no FOR, no LIMIT ALL, no OFFSET-then-LIMIT.
   Wider than that would ACCEPT statements the legacy parser rejects. */
paren_tail:
		setop_op SelectStmt
			{
				rt, _ := $2.(*parser.SelectStmt)
				$$ = &parenTail{op: $1.(*opSpec), right: rt}
			}
	| ORDER BY sort_by_list opt_paren_limit opt_paren_offset
			{
				$$ = &parenTail{orderBy: $3, limit: $4, offset: $5}
			}
	| LIMIT a_expr opt_paren_offset
			{
				$$ = &parenTail{limit: $2, offset: $3}
			}
	| OFFSET a_expr
			{
				$$ = &parenTail{offset: $2}
			}

opt_paren_limit:
		/* empty */     { $$ = (parser.Expr)(nil) }
	| LIMIT a_expr      { $$ = $2 }

opt_paren_offset:
		/* empty */     { $$ = (parser.Expr)(nil) }
	| OFFSET a_expr     { $$ = $2 }

select_bare:
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
		simple_select opt_sort_clause
			{
				m := $1.(*parser.SelectStmt)
				m.OrderBy = $2
				$$ = m
			}
	/* gram.y select_no_parens splits the limit/locking tail into two
	   alternatives whose LEADING part is non-optional, so the decision after
	   opt_sort_clause is on distinct terminals (FOR vs LIMIT/OFFSET/FETCH).
	   Writing it as `opt_select_limit opt_for_locking` plus a second
	   `for_locking_clause select_limit` instead costs one S/R on FOR, which
	   goyacc silently resolves by shifting — i.e. it would demand a LIMIT after
	   every FOR UPDATE. */
	| simple_select opt_sort_clause select_limit opt_for_locking
			{
				m := $1.(*parser.SelectStmt)
				m.OrderBy = $2
				if sl, ok := $3.(*selectLimit); ok && sl != nil {
					m.Limit = sl.count
					m.Offset = sl.offset
					m.WithTies = sl.withTies
				}
				m.Locking, _ = $4.([]*parser.LockingClause)
				$$ = m
			}
	| simple_select opt_sort_clause for_locking_clause opt_select_limit
			{
				m := $1.(*parser.SelectStmt)
				m.OrderBy = $2
				m.Locking, _ = $3.([]*parser.LockingClause)
				if sl, ok := $4.(*selectLimit); ok && sl != nil {
					m.Limit = sl.count
					m.Offset = sl.offset
					m.WithTies = sl.withTies
				}
				$$ = m
			}

/* Table-level constraint trailers — gram.y ConstraintAttributeSpec,
   opt_unique_null_treatment and opt_c_include. The AST has carried every
   field since DU-002 (TableUniqueDeferrable, TableUniqueIncludes,
   PrimaryKeyInclude, TableConstraintDef.NullsNotDistinct, ...); only the
   grammar was missing, so `UNIQUE (a) INCLUDE (b)` and
   `... DEFERRABLE INITIALLY DEFERRED` were syntax errors on a routed
   CREATE TABLE. */
opt_constr_attrs:
		/* empty */                    { $$ = (*constrAttrs)(nil) }
	| opt_constr_attrs constr_attr     { a, _ := $1.(*constrAttrs); $$ = mergeConstrAttr(a, $2) }

constr_attr:
		DEFERRABLE          { $$ = "deferrable" }
	| NOT DEFERRABLE        { $$ = "not_deferrable" }
	| INITIALLY DEFERRED    { $$ = "initially_deferred" }
	| INITIALLY IMMEDIATE   { $$ = "initially_immediate" }

opt_include:
		/* empty */                   { $$ = nil }
	| INCLUDE '(' colid_list ')'      { $$ = $3 }

opt_unique_nnd:
		/* empty */                   { $$ = false }
	| NULLS_P NOT DISTINCT            { $$ = true }

/* for_locking_clause — gram.y :13300ff. `FOR UPDATE` and friends were absent
   from this grammar entirely (SelectStmt.Locking existed on the AST but no
   production ever built one), so every row-locking SELECT was a syntax error
   on the routed path: 23 upstream isolation specs. */
opt_for_locking:
		/* empty */          { $$ = nil }
	| for_locking_clause     { $$ = $1 }

for_locking_clause:
		for_locking_item
			{
				$$ = []*parser.LockingClause{$1.(*parser.LockingClause)}
			}
	| for_locking_clause for_locking_item
			{
				$$ = append($1.([]*parser.LockingClause), $2.(*parser.LockingClause))
			}

for_locking_item:
		for_locking_strength opt_locked_rels opt_lock_wait_policy
			{
				$$ = parser.NewLockingClause(yylex.(*lexerState).lastConsumedPos(),
					$1.(parser.LockStrength), $2, $3.(parser.LockWaitPolicy))
			}

for_locking_strength:
		FOR UPDATE            { $$ = parser.LockStrengthForUpdate }
	| FOR SHARE               { $$ = parser.LockStrengthForShare }
	| FOR NO KEY UPDATE       { $$ = parser.LockStrengthForNoKeyUpdate }
	| FOR KEY SHARE           { $$ = parser.LockStrengthForKeyShare }

opt_locked_rels:
		/* empty */           { $$ = nil }
	| OF colid_list           { $$ = $2 }

opt_lock_wait_policy:
		/* empty */           { $$ = parser.LockWaitBlock }
	| NOWAIT                  { $$ = parser.LockWaitNoWait }
	| SKIP LOCKED             { $$ = parser.LockWaitSkipLocked }
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
//
// NOT PORTED: a parenthesised SET-OP OPERAND, `(SELECT ...) UNION ALL
// (SELECT ...)`. 14 must-pass regress fragments need it (select_distinct,
// union). Measured 2026-08-27, and the diagnosis is now exact:
//
//   Adding `simple_select: paren_select` — with ONE shared `'(' SelectStmt ')'`
//   rule feeding all three consumers, which is the least-conflict spelling —
//   leaves exactly TWO reduce/reduce conflicts, both on ')' :
//     state 1131  simple_select: paren_select.  vs  c_expr: paren_select.
//     state 1916  simple_select: paren_select.  vs  base_table_ref: paren_select.opt_derived_alias
//   Every OTHER lookahead resolves correctly on its own: UNION / INTERSECT /
//   EXCEPT / ORDER / LIMIT / OFFSET / FETCH / FOR all reduce to simple_select,
//   which is what the 14 fragments need.
//
//   ')' cannot be resolved by precedence — %prec steers shift/reduce, not
//   reduce/reduce, and upstream's `c_expr: select_with_parens %prec UMINUS`
//   (which was tried) changes nothing here. yacc resolves reduce/reduce by RULE
//   ORDER, and simple_select is declared far earlier than c_expr, so
//   simple_select wins ')' — which BREAKS `SELECT f((SELECT 1))` and every
//   other doubly-parenthesised subquery. That is why this stays unported rather
//   than shipping with the conflicts allowlisted.
//
//   The real fix is upstream's layering — select_no_parens / select_clause /
//   select_with_parens, with the set-op alternatives taking select_clause — so
//   the parenthesised operand never enters simple_select at all. That is a
//   structural rewrite of the SELECT core, tracked in
//   docs/design/not_ralph/TODO.md rather than attempted piecemeal.
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
		SELECT select_pos opt_all_distinct opt_target_list into_clause opt_from_clause where_clause group_clause having_clause opt_window_clause
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
				for _, fe := range $6 {
					s.FromExprs = append(s.FromExprs, fe)
					s.From = append(s.From, fe.Base)
					for _, jn := range fe.Joins {
						s.From = append(s.From, jn.Right)
					}
				}
				s.Where = $7
				if gc, ok := $8.(*groupClause); ok && gc != nil {
					s.GroupBy = gc.list
					s.GroupingSets = gc.sets
				}
				s.Having = $9
				if len($10) > 0 {
					s.WindowClause = $10
				}
				// SELECT ... INTO is CREATE TABLE ... AS in disguise; the
				// wrap happens at the SelectStmt rule, once ORDER BY / LIMIT
				// have been attached, so only the target is recorded here.
				if tgt, ok := $5.(*parser.ObjectName); ok && tgt != nil {
					l := yylex.(*lexerState)
					if l.intoFor == nil {
						l.intoFor = map[*parser.SelectStmt]parser.ObjectName{}
					}
					l.intoFor[s] = *tgt
				}
				$$ = s
				// NOTE: ORDER BY / LIMIT / OFFSET live one level up, in
				// select_no_parens (gram.y :12916 comment) — a set-op RHS
				// must not swallow them.
			}
	| VALUES_LA values_rows
			{
				// lastConsumedPos is the VALUES keyword itself (select_pos
				// equivalent without the mid-rule empty that would merge
				// this state with col_name_keyword: VALUES).
				s := parser.NewSelectStmt(yylex.(*lexerState).lastConsumedPos())
				s.ValuesRows = $2
				$$ = s
			}
	| TABLE qualified_name
			{
				// gram.y :12968 desugars TABLE <rel> to SELECT * FROM <rel>.
				s := parser.NewSelectStmt(yylex.(*lexerState).lastConsumedPos())
				rv := rangeVarFromName($2, "")
				s.Targets = []parser.ResTarget{parser.NewResTarget(0, "", parser.NewStarExpr(0, "", ""))}
				s.From = []parser.RangeVar{rv}
				s.FromExprs = []parser.FromExpr{{Base: rv}}
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

/* The operator after USING: gram.y spells this `any_operator |
   OPERATOR '(' any_operator ')'`, but legacy's parseSortUsingOperator
   (select.go:1771) never consumed the OPERATOR(...) form — it returns the bare
   keyword and then chokes on '(' — so porting that alternative would ACCEPT
   MORE than legacy. Pinned to what legacy takes: symbol operators (shared with
   exclude_op) plus a plain or schema-qualified operator name. */
func_name_keyword:
		AUTHORIZATION  { $$ = "authorization" }
	| BINARY           { $$ = "binary" }
	| COLLATION        { $$ = "collation" }
	| CONCURRENTLY     { $$ = "concurrently" }
	| CROSS            { $$ = "cross" }
	| FREEZE           { $$ = "freeze" }
	| FULL             { $$ = "full" }
	| ILIKE            { $$ = "ilike" }
	| INNER_P          { $$ = "inner" }
	| IS               { $$ = "is" }
	| ISNULL           { $$ = "isnull" }
	| JOIN             { $$ = "join" }
	| LEFT             { $$ = "left" }
	| LIKE             { $$ = "like" }
	| NATURAL          { $$ = "natural" }
	| NOTNULL          { $$ = "notnull" }
	| OUTER_P          { $$ = "outer" }
	| OVERLAPS         { $$ = "overlaps" }
	| RIGHT            { $$ = "right" }
	| SIMILAR          { $$ = "similar" }
	| TABLESAMPLE      { $$ = "tablesample" }
	| VERBOSE          { $$ = "verbose" }

/* into_clause — gram.y :12986. Legacy takes only `INTO [TABLE] name`; its
   TEMP / UNLOGGED / TABLESPACE variants are NOT accepted there
   (`SELECT a INTO TEMP x` is a syntax error), so they stay out. */
into_clause:
		/* empty */                      { $$ = (*parser.ObjectName)(nil) }
	| INTO opt_into_table qualified_name
			{
				n := objectNameFromQn($3)
				$$ = &n
			}

opt_into_table:
		/* empty */   { _ = 0 }
	| TABLE           { _ = 0 }

/* Identity sequence options — gram.y OptParenthesizedSeqOptList / SeqOptElem,
   restricted to what legacy's identity parser takes (ddl.go:4715-4755):
   START [WITH] n, INCREMENT [BY] n, MINVALUE n, MAXVALUE n, CACHE n, CYCLE,
   and NO MINVALUE / NO MAXVALUE / NO CYCLE, in any order, signed integers
   only. (Legacy also swallows a bare NO; not reproduced — it is a typo.) */
opt_identity_seq_opts:
		/* empty */                  { $$ = (*identityOpts)(nil) }
	| '(' identity_seq_opt_list ')'  { $$ = $2 }

identity_seq_opt_list:
		identity_seq_opt
			{
				o := &identityOpts{}
				$1.(func(*identityOpts))(o)
				$$ = o
			}
	| identity_seq_opt_list identity_seq_opt
			{
				o := $1.(*identityOpts)
				$2.(func(*identityOpts))(o)
				$$ = o
			}

identity_seq_opt:
		START opt_with_kw signed_iconst   { n := $3; $$ = func(o *identityOpts) { o.start = n } }
	| INCREMENT opt_by_kw signed_iconst   { n := $3; $$ = func(o *identityOpts) { o.inc = &n } }
	| MINVALUE signed_iconst              { n := $2; $$ = func(o *identityOpts) { o.min = &n } }
	| MAXVALUE signed_iconst              { n := $2; $$ = func(o *identityOpts) { o.max = &n } }
	| CACHE signed_iconst                 { n := $2; $$ = func(o *identityOpts) { o.cache = &n } }
	| CYCLE                               { $$ = func(o *identityOpts) { o.cycle = true } }
	| NO MINVALUE                         { $$ = func(o *identityOpts) {} }
	| NO MAXVALUE                         { $$ = func(o *identityOpts) {} }
	| NO CYCLE                            { $$ = func(o *identityOpts) { o.cycle = false } }

opt_with_kw:
		/* empty */   { _ = 0 }
	| WITH            { _ = 0 }

opt_by_kw:
		/* empty */   { _ = 0 }
	| BY              { _ = 0 }

signed_iconst:
		ICONST          { $$ = int64($1) }
	| '-' ICONST        { $$ = -int64($2) }
	| '+' ICONST        { $$ = int64($2) }

/* check_body — `CHECK ( a_expr )` with its raw source span captured at the
   reduce, before any trailer (NO INHERIT) is consumed. */
check_body:
		CHECKBODY
			{ $$ = joinCheckTokens(yylex, $<p>1, $1) }

array_expr:
		'[' expr_list ']'          { $$ = $2 }
	| '[' array_expr_list ']'      { $$ = $2 }
	| '[' ']'                      { $$ = nil }

array_expr_list:
		array_expr
			{ $$ = []parser.Expr{parser.NewArrayConstructorExpr($<p>1, $1)} }
	| array_expr_list ',' array_expr
			{ $$ = append($1, parser.NewArrayConstructorExpr($<p>3, $3)) }

/* SQL-standard argument spellings of SUBSTRING / OVERLAY / POSITION —
   gram.y substr_list / overlay_list / position_list. Legacy rewrites each into
   an ordinary call (position's operands REVERSED: position(hay, needle)) built
   as a special form, so Variadic stays nil — hence specialFormCall. The
   comma-spelled calls `substring(x, 1, 2)` are the SAME special form there,
   so the keyword rules take them too (the keyword shifts '(' ahead of the
   ColId call path, which would otherwise carry Variadic flags). */
substr_list:
		a_expr FROM a_expr                 { $$ = []parser.Expr{$1, $3} }
	| a_expr FROM a_expr FOR a_expr        { $$ = []parser.Expr{$1, $3, $5} }
	| expr_list                            { $$ = $1 }

overlay_list:
		a_expr PLACING a_expr FROM a_expr              { $$ = []parser.Expr{$1, $3, $5} }
	| a_expr PLACING a_expr FROM a_expr FOR a_expr     { $$ = []parser.Expr{$1, $3, $5, $7} }
	| expr_list                                        { $$ = $1 }

position_list:
		b_expr IN_P b_expr                 { $$ = []parser.Expr{$3, $1} }
	/* b_expr first in both alternatives, so the reduce after the first
	   argument is the same either way; an a_expr here would reduce/reduce. */
	| b_expr ',' expr_list                 { $$ = append([]parser.Expr{$1}, $3...) }

explain_opt_list:
		explain_opt                        { $$ = []*explainOpt{$1.(*explainOpt)} }
	| explain_opt_list ',' explain_opt     { $$ = append($1.([]*explainOpt), $3.(*explainOpt)) }

/* An option is a NAME with an optional VALUE; ANALYZE / VERBOSE are keyword
   tokens, the rest are identifiers. Values: identifiers (on, off, json, ...),
   TRUE / FALSE, or a number. */
explain_opt:
		explain_opt_name                     { $$ = &explainOpt{name: $1} }
	| explain_opt_name explain_opt_value   { $$ = &explainOpt{name: $1, value: $2, has: true} }

explain_opt_name:
		ColId         { $$ = $1 }
	| ANALYZE         { $$ = "analyze" }
	| ANALYSE         { $$ = "analyse" }
	| VERBOSE         { $$ = "verbose" }
	/* FORMAT itself is a ColId; only the adapter's FORMAT-before-JSON
	   substitution (FORMAT_LA) needs its own alternative. json / text / xml
	   values are ColIds too. */
	| FORMAT_LA       { $$ = "format" }

explain_opt_value:
		ColId         { $$ = $1 }
	| TRUE_P          { $$ = "true" }
	| FALSE_P         { $$ = "false" }
	| ON              { $$ = "on" }
	| ICONST          { $$ = yylex.(*lexerState).lastText }
	| SCONST          { $$ = $1 }

/* Arbiter elements — gram.y index_elem again: an expression with an optional
   COLLATE and operator class. Legacy DROPS both (the opclass here, the
   COLLATE in arbiterFromExprs), so only the expression survives. */
arbiter_elem_list:
		arbiter_elem                        { $$ = []parser.Expr{$1} }
	| arbiter_elem_list ',' arbiter_elem    { $$ = append($1, $3) }

arbiter_elem:
		a_expr            { $$ = $1 }
	| a_expr IDENT        { $$ = $1 } /* opclass: parsed and dropped */

sort_using_op:
		exclude_op        { $$ = $1 }
	| ColId               { $$ = $1 }
	| ColId '.' ColId     { $$ = $1 + "." + $3 }

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
	/* NULLS FIRST|LAST without an explicit ASC/DESC — gram.y's sortby is
	   `a_expr opt_asc_desc opt_nulls_order`, so both parts are optional
	   independently. */
	/* ORDER BY x USING op — gram.y sortby's first alternative. */
	| a_expr USING sort_using_op
			{
				$$ = parser.NewSortBy($1.Pos(), $1, sortUsingIsDesc($3), $3)
			}
	| a_expr USING sort_using_op NULLS_LA FIRST_P
			{
				$$ = parser.NewSortBy($1.Pos(), $1, sortUsingIsDesc($3), $3)
				v := true
				$$.NullsFirst = &v
			}
	| a_expr USING sort_using_op NULLS_LA LAST_P
			{
				$$ = parser.NewSortBy($1.Pos(), $1, sortUsingIsDesc($3), $3)
				v := false
				$$.NullsFirst = &v
			}
	| a_expr NULLS_LA FIRST_P
			{
				$$ = parser.NewSortBy($1.Pos(), $1, false, "")
				v := true
				$$.NullsFirst = &v
			}
	| a_expr NULLS_LA LAST_P
			{
				$$ = parser.NewSortBy($1.Pos(), $1, false, "")
				v := false
				$$.NullsFirst = &v
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
	| FETCH first_or_next row_or_rows WITH TIES
			{
				/* Countless form: the row count defaults to one
				   (gram.y makeIntConst(1, -1)). */
				$$ = &selectLimit{count: parser.NewIntegerConst(0, 1), withTies: true, set: true}
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
	/* Genuinely empty: `SELECT FROM t` and `SELECT;` are both legal upstream
	   (gram.y opt_target_list's empty alternative) and produce a
	   zero-column result. Only target_list was ported, so a zero-column join
	   — the shape TestPort_ZeroColumnJoinDoesNotCrashBackend exercises —
	   raised 42601 instead of running. */
	| /* empty */
			{
				$$ = nil
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
		a_expr AS as_col_label
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
	| NATURAL INNER_P JOIN
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
	/* `person*` — gram.y relation_expr's inheritance-star form. It asks for
	   descendants explicitly, which is already the default, so PG and legacy
	   both parse it and record nothing; RangeVar.Only stays false. */
	| qualified_name '*'
			{
				$$ = rangeVarFromName($1, "")
			}
	| qualified_name '*' ColId
			{
				$$ = rangeVarFromName($1, $3)
			}
	| qualified_name '*' AS ColId
			{
				$$ = rangeVarFromName($1, $4)
			}
	/* Alias with a COLUMN-alias list — gram.y alias_clause's
	   `AS ColId '(' name_list ')'` / `ColId '(' name_list ')'`. RangeVar.Columns
	   already carries them; only the productions were missing. */
	| qualified_name ColId '(' col_alias_list ')'
			{
				rv := rangeVarFromName($1, $2)
				rv.Columns = $4
				$$ = rv
			}
	| qualified_name AS ColId '(' col_alias_list ')'
			{
				rv := rangeVarFromName($1, $3)
				rv.Columns = $5
				$$ = rv
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
	| select_with_parens opt_derived_alias
			{
				sub, ok := $1.(*parser.SelectStmt)
				if !ok {
					lerr(yylex, "subquery in FROM did not produce SELECT", yylex.(*lexerState).lastConsumedPos())
					sub = parser.NewSelectStmt(0)
				}
				sub.Parenthesized = true
				pos := sub.Pos()
				lateral := false
				alias := ""
				var cols []string
				if da, ok2 := $2.(*derivedAlias); ok2 && da != nil {
					alias, cols = da.alias, da.cols
					lateral = da.lateral
				}
				$$ = derivedRangeVar(yylex.(*lexerState), pos, sub, alias, cols, lateral)
			}
	| LATERAL_P select_with_parens opt_derived_alias
			{
				sub, ok := $2.(*parser.SelectStmt)
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
				if da, ok2 := $3.(*derivedAlias); ok2 && da != nil {
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

/* func_arg_expr — gram.y :16160: a positional argument or a named one
   (`name => value`, `name := value`). Legacy DROPS the name and keeps the
   value, as call_arg does for expression-position calls. */
func_arg_expr:
		a_expr                          { $$ = $1 }
	| a_expr EQUALS_GREATER a_expr      { $$ = $3 }
	| a_expr COLON_EQUALS a_expr        { $$ = $3 }
	| VARIADIC a_expr                   { $$ = $2 } /* legacy keeps the array unflagged on a table function */

func_arg_list:
		func_arg_expr
			{
				$$ = []parser.Expr{$1}
			}
	| func_arg_list ',' func_arg_expr
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
	| ColId
			{
				// ColId, not IDENT: a bare derived-table alias may be any
				// UNRESERVED keyword, and those lex as TokenKeyword rather than
				// IDENT. TPC-DS Q90 aliases its subqueries `at` and `pt`.
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
				$$ = buildGroupClause($<p>3, $3.([]*groupItem))
			}

group_by_list:
		group_by_item
			{
				$$ = []*groupItem{$1.(*groupItem)}
			}
	| group_by_list ',' group_by_item
			{
				$$ = append($1.([]*groupItem), $3.(*groupItem))
			}

/* group_by_item — gram.y group_by_item: plain expression, ROLLUP / CUBE
   / GROUPING SETS constructs, and the empty grouping set. Legacy expands the
   constructs at PARSE time (parseGroupByElems): the flat GroupBy list keeps
   every expression in order, duplicates included, and GroupingSets holds the
   cartesian product of each element's alternatives. A parenthesised operand
   `(a, b)` arrives from the expression grammar as a RowExpr and is one unit,
   which is what legacy's own operand loop produces. */
group_by_item:
		a_expr
			{
				$$ = plainGroupItem($1)
			}
	| '(' ')'
			{
				$$ = &groupItem{alts: [][]parser.Expr{{}}, construct: true}
			}
	| ROLLUP '(' expr_list ')'
			{
				u := groupingUnits($3)
				$$ = &groupItem{flat: flattenUnits(u), alts: rollupAlternatives(u), construct: true}
			}
	| ROLLUP '(' ')'
			{
				$$ = &groupItem{alts: rollupAlternatives(nil), construct: true}
			}
	| CUBE '(' expr_list ')'
			{
				u := groupingUnits($3)
				$$ = &groupItem{flat: flattenUnits(u), alts: cubeAlternatives(u), construct: true}
			}
	| CUBE '(' ')'
			{
				$$ = &groupItem{alts: cubeAlternatives(nil), construct: true}
			}
	| GROUPING SETS '(' gs_list ')'
			{
				alts := $4.([][]parser.Expr)
				$$ = &groupItem{flat: flattenUnits(alts), alts: alts, construct: true}
			}

/* One GROUPING SETS operand contributes one or more sets: nested ROLLUP /
   CUBE expand in place, `()` is the empty set, `(a, b)` is a RowExpr. */
gs_list:
		gs_elem                  { $$ = $1 }
	| gs_list ',' gs_elem        { $$ = append($1.([][]parser.Expr), $3.([][]parser.Expr)...) }

gs_elem:
		a_expr
			{
				if r, ok := $1.(*parser.RowExpr); ok {
					$$ = [][]parser.Expr{r.Elems}
				} else {
					$$ = [][]parser.Expr{{$1}}
				}
			}
	| '(' ')'                          { $$ = [][]parser.Expr{{}} }
	| ROLLUP '(' expr_list ')'         { $$ = rollupAlternatives(groupingUnits($3)) }
	| CUBE '(' expr_list ')'           { $$ = cubeAlternatives(groupingUnits($3)) }

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

/* Two flat alternatives instead of an optional opt_recursive prefix: the
   optional form creates an S/R on RECURSIVE (it is an UNRESERVED keyword,
   so it also starts cte_item's ColId); splitting lets LALR decide purely
   from the lookahead with zero conflicts (upstream parity: gram.y :13005
   uses the same two-alternative spelling). */
with_clause:
		WITH cte_list
			{
				$$ = parser.NewWithClause(0, false, $2)
			}
	| WITH RECURSIVE cte_list
			{
				$$ = parser.NewWithClause(0, true, $3)
			}

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
		ColId cte_col_list AS opt_materialized select_with_parens
			{
				sub, ok := $5.(*parser.SelectStmt)
				if !ok || sub == nil {
					sub = parser.NewSelectStmt(0)
				}
				mat := $4
				cte := parser.NewCommonTableExpr(0, $1, $2, sub)
				cte.Materialized = mat
				$$ = &cteItem{cte: cte}
			}
	/* Data-modifying CTEs — gram.y common_table_expr's PreparableStmt body.
	   CommonTableExpr.DMLBody has existed on the AST all along; only the
	   grammar restricted the body to SelectStmt, so
	   `WITH u AS (UPDATE ... RETURNING ...) INSERT ...` was a syntax error on
	   the routed path. INSERT/UPDATE/DELETE start with distinct reserved
	   keywords, so this costs no conflict. */
	| ColId cte_col_list AS opt_materialized '(' cte_dml_body ')'
			{
				cte := parser.NewCommonTableExpr(0, $1, $2, nil)
				cte.Materialized = $4
				cte.DMLBody = $6.(parser.Stmt)
				$$ = &cteItem{cte: cte}
			}

cte_dml_body:
		insert_stmt   { $$ = $1 }
	| update_stmt     { $$ = $1 }
	| delete_stmt     { $$ = $1 }

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

/* opt_window_clause / window_definition — gram.y :13470ff subset. Flat
   alternatives mirror the inline-OVER variants in name_or_call; frame
   clauses (ROWS/RANGE ...) arrive with a later wave. */
opt_window_clause:
		/* empty */
			{
				$$ = nil
			}
	| WINDOW window_definition_list
			{
				$$ = $2
			}

window_definition_list:
		window_definition
			{
				$$ = []parser.NamedWindowDef{$1}
			}
	| window_definition_list ',' window_definition
			{
				$$ = append($1, $3)
			}

window_definition:
		ColId AS '(' opt_window_spec ')'
			{
				$$ = parser.NamedWindowDef{Name: $1, Def: $4}
			}

opt_window_spec:
	/* An existing window name first — gram.y opt_existing_window_name — with
	   its own frame or ORDER BY (`OVER (w RANGE BETWEEN ...)`). */
		ColId opt_frame_tail
			{
				wd := parser.NewWindowDef(yylex.(*lexerState).lastConsumedPos())
				wd.RefName = $1
				if fr := $2; fr != nil {
					wd.Frame = fr
				}
				$$ = wd
			}
	| ColId ORDER BY sort_by_list opt_frame_tail
			{
				wd := parser.NewWindowDef(yylex.(*lexerState).lastConsumedPos())
				wd.RefName = $1
				wd.OrderBy = $4
				if fr := $5; fr != nil {
					wd.Frame = fr
				}
				$$ = wd
			}
	| opt_frame_tail
			{
				wd := parser.NewWindowDef(yylex.(*lexerState).lastConsumedPos())
				if fr := $1; fr != nil {
					wd.Frame = fr
				}
				$$ = wd
			}
	| PARTITION BY expr_list opt_frame_tail
			{
				wd := parser.NewWindowDef(yylex.(*lexerState).lastConsumedPos())
				wd.PartitionBy = $3
				if fr := $4; fr != nil {
					wd.Frame = fr
				}
				$$ = wd
			}
	| ORDER BY sort_by_list opt_frame_tail
			{
				wd := parser.NewWindowDef(yylex.(*lexerState).lastConsumedPos())
				wd.OrderBy = $3
				if fr := $4; fr != nil {
					wd.Frame = fr
				}
				$$ = wd
			}
	| PARTITION BY expr_list ORDER BY sort_by_list opt_frame_tail
			{
				wd := parser.NewWindowDef(yylex.(*lexerState).lastConsumedPos())
				wd.PartitionBy = $3
				wd.OrderBy = $6
				if fr := $7; fr != nil {
					wd.Frame = fr
				}
				$$ = wd
			}

/* opt_frame_tail / frame extent+bound+exclusion — gram.y :13560ff subset.
   Carriers live in support.go (partFrame*); only FrameModeRows reaches the
   executor today — RANGE/GROUPS parse structurally and the analyzer rejects
   them upstream-parity (0A000). */
opt_frame_tail:
		/* empty */
			{
				$$ = nil
			}
	| ROWS part_frame_extent part_frame_excl
			{
				$$ = finishFrame(parser.FrameModeRows, $2.(*partFrameExtent), $3.(*partFrameExcl))
			}
	| RANGE part_frame_extent part_frame_excl
			{
				$$ = finishFrame(parser.FrameModeRange, $2.(*partFrameExtent), $3.(*partFrameExcl))
			}
	| GROUPS part_frame_extent part_frame_excl
			{
				$$ = finishFrame(parser.FrameModeGroups, $2.(*partFrameExtent), $3.(*partFrameExcl))
			}

part_frame_extent:
		part_frame_bound
			{
				fp := $1.(*partFrameBound)
				$$ = &partFrameExtent{start: fp.k, startOff: fp.off, end: parser.FrameBoundCurrentRow, hasBetween: false}
			}
	| BETWEEN part_frame_bound AND part_frame_bound
			{
				s := $2.(*partFrameBound)
				e := $4.(*partFrameBound)
				$$ = &partFrameExtent{start: s.k, startOff: s.off, end: e.k, endOff: e.off, hasBetween: true}
			}

part_frame_bound:
		UNBOUNDED PRECEDING
			{ $$ = &partFrameBound{k: parser.FrameBoundUnboundedPreceding} }
	| UNBOUNDED FOLLOWING
			{ $$ = &partFrameBound{k: parser.FrameBoundUnboundedFollowing} }
	| CURRENT_P ROW
			{ $$ = &partFrameBound{k: parser.FrameBoundCurrentRow} }
	| a_expr PRECEDING
			{ $$ = &partFrameBound{k: parser.FrameBoundOffsetPreceding, off: $1} }
	| a_expr FOLLOWING
			{ $$ = &partFrameBound{k: parser.FrameBoundOffsetFollowing, off: $1} }

part_frame_excl:
		/* empty */
			{ $$ = &partFrameExcl{} }
	| EXCLUDE CURRENT_P ROW
			{ $$ = &partFrameExcl{x: parser.FrameExcludeCurrentRow} }
	| EXCLUDE GROUP_P
			{ $$ = &partFrameExcl{x: parser.FrameExcludeGroup} }
	| EXCLUDE TIES
			{ $$ = &partFrameExcl{x: parser.FrameExcludeTies} }
	| EXCLUDE NO OTHERS
			{ $$ = &partFrameExcl{} }

/* CASE WHEN ... THEN ... [ELSE ...] END — gram.y :15464 case_expr subset.
   Both forms are supported: the searched form and the simple form
   (CASE operand WHEN val THEN ...). Historical note — the simple form was
   deferred until
   P2.3 func_call lands; searched form covers TPC-H Q12/Q13. */cse_wl:
		when_then { $$ = $1 }
	| cse_wl when_then
			{
				prev := $1.(*whenList)
				nxt := $2.(*whenList)
				$$ = &whenList{items: append(prev.items, nxt.items[0])}
			}

when_then:
		WHEN a_expr THEN a_expr
			{
				$$ = &whenList{items: []parser.CaseWhen{parser.NewCaseWhen($2, $4)}}
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
	/* Exponentiation. '^' is in scan.l's {self} set and has a %left entry, but
	   no production ever consumed it, so `a ^ b` was a syntax error. */
	| a_expr '^' a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), binOp(yylex, "^"), $1, $3)
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
	/* IS [NOT] NULL / TRUE / FALSE / UNKNOWN / DISTINCT FROM — gram.y
	   :15160ff IS NULL_P etc; DISTINCT FROM uses %prec IS like upstream.
	   Positions ride on the LEFT operand (content dumps strip them). */
	| a_expr IS NULL_P
			{
				$$ = parser.NewIsNullExpr($1.Pos(), $1, false)
			}
	| a_expr IS NOT NULL_P
			{
				$$ = parser.NewIsNullExpr($1.Pos(), $1, true)
			}
	| a_expr IS TRUE_P
			{
				$$ = parser.NewIsBoolExpr($1.Pos(), $1, true, false, false)
			}
	| a_expr IS NOT TRUE_P
			{
				$$ = parser.NewIsBoolExpr($1.Pos(), $1, true, false, true)
			}
	| a_expr IS FALSE_P
			{
				$$ = parser.NewIsBoolExpr($1.Pos(), $1, false, true, false)
			}
	| a_expr IS NOT FALSE_P
			{
				$$ = parser.NewIsBoolExpr($1.Pos(), $1, false, true, true)
			}
	| a_expr IS UNKNOWN
			{
				$$ = parser.NewIsBoolExpr($1.Pos(), $1, false, false, false)
			}
	| a_expr IS NOT UNKNOWN
			{
				$$ = parser.NewIsBoolExpr($1.Pos(), $1, false, false, true)
			}
	| a_expr IS DISTINCT FROM b_expr %prec IS
			{
				$$ = parser.NewIsDistinctFromExpr($1.Pos(), $1, $5, false)
			}
	| a_expr IS NOT DISTINCT FROM b_expr %prec IS
			{
				$$ = parser.NewIsDistinctFromExpr($1.Pos(), $1, $6, true)
			}
	/* Postfix spellings — gram.y :15200 ISNULL / NOTNULL. */
	| a_expr ISNULL
			{
				$$ = parser.NewIsNullExpr($1.Pos(), $1, false)
			}
	| a_expr NOTNULL
			{
				$$ = parser.NewIsNullExpr($1.Pos(), $1, true)
			}
	| EXISTS select_with_parens
			{
				sub, _ := $2.(*parser.SelectStmt)
				if sub == nil {
					sub = parser.NewSelectStmt(0)
				}
				$$ = parser.NewExistsExpr(yylex.(*lexerState).lastConsumedPos(), false, sub)
			}
	| CASE cse_wl END_P
			{
				wl := $2.(*whenList)
				$$ = parser.NewCaseExpr(0, nil, wl.items, nil)
			}
	| CASE cse_wl ELSE a_expr END_P
			{
				wl := $2.(*whenList)
				$$ = parser.NewCaseExpr(0, nil, wl.items, $4)
			}
	/* SIMPLE form: CASE operand WHEN value THEN ... (gram.y case_expr's
	   case_arg). NewCaseExpr has always taken an operand — only the grammar
	   was missing, so `CASE x WHEN 1 THEN 2 END` was a syntax error on the
	   routed SELECT path in every shape. WHEN is a reserved keyword and cannot
	   start an a_expr, so the operand is LALR(1)-unambiguous against the
	   searched form above. */
	| CASE a_expr cse_wl END_P
			{
				wl := $3.(*whenList)
				$$ = parser.NewCaseExpr(0, $2, wl.items, nil)
			}
	| CASE a_expr cse_wl ELSE a_expr END_P
			{
				wl := $3.(*whenList)
				$$ = parser.NewCaseExpr(0, $2, wl.items, $5)
			}

	/* Generic operator terminal — gram.y `a_expr Op a_expr` (%left Op).
	   Covers || << >> ~* !~ <@ @> && -> ->> #> #>> and any future
	   multi-character spelling routed here by the adapter. */
	| a_expr COLLATE ColId
			{
				$$ = parser.NewCollateExpr($1.Pos(), $1, $3)
			}
	/* AT TIME ZONE / AT LOCAL — gram.y :15540ff. Both are rewritten into a
	   timezone() call with the ZONE argument FIRST (gram.y builds
	   makeFuncCall(timezone, list_make2($5, $1))); AT LOCAL passes the value
	   alone. Only the productions were missing — the %left AT precedence
	   declaration was already there from the verbatim block — so every
	   spelling was a hard 42601. Like TRIM these are SYNTHESISED calls, so
	   Variadic stays nil.

	   %prec Op, NOT %prec AT: legacy parses the zone with
	   parseExprPrec(precCompare+1) (select.go:2455), which binds LOOSER than
	   '+' and '*', so `x AT TIME ZONE 'UTC' + interval '1 day'` means
	   timezone('UTC' + interval, x) there and timezone('UTC', x) + interval
	   under upstream's %left AT. Upstream is the saner reading, but this is a
	   migration and the routed parser must not disagree with the parser it
	   replaces; Op sits below '+' and above the comparisons, which reproduces
	   legacy's binding exactly. */
	| a_expr AT TIME ZONE a_expr %prec Op
			{
				$$ = specialFormCall($1.Pos(), "timezone", []parser.Expr{tzZone(yylex, $5), $1})
			}
	| a_expr AT LOCAL %prec Op
			{
				$$ = specialFormCall($1.Pos(), "timezone", []parser.Expr{$1})
			}
	| a_expr Op a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), binOp(yylex, $2), $1, $3)
			}
	/* Prefix operator — gram.y `qual_Op a_expr %prec Op`. '-' and '+' arrive
	   as char terminals and have their own alternatives above, so the only
	   spelling that reaches here and that legacy also takes (select.go:3025)
	   is '~'; prefixOp rejects the rest rather than silently widening. */
	| Op a_expr %prec Op
			{
				$$ = parser.NewUnaryOp(yylex.(*lexerState).lastConsumedPos(), prefixOp(yylex, $1), $2)
			}
	/* op ANY/SOME/ALL — gram.y :15150ff quantified comparisons via
	   subquery_Op subset. */
	/* LIKE / ILIKE quantified over a list — legacy folds these into the IN
	   shape with the pattern operator as AnyOp (OpLike / OpNotLike / OpILike),
	   exactly like `op ANY`. */
	| a_expr LIKE ANY '(' expr_list ')'
			{ $$ = quantifiedAny(yylex, $1.Pos(), $1, parser.OpLike, nil, $5, $<p>5) }
	| a_expr NOT_LA LIKE ANY '(' expr_list ')'
			{ $$ = quantifiedAny(yylex, $1.Pos(), $1, parser.OpNotLike, nil, $6, $<p>6) }
	| a_expr ILIKE ANY '(' expr_list ')'
			{ $$ = quantifiedAny(yylex, $1.Pos(), $1, parser.OpILike, nil, $5, $<p>5) }
	| a_expr LIKE ALL '(' expr_list ')'
			{ $$ = parser.NewInExpr($1.Pos(), $1, false, parser.OpLike, true, nil, unwrapAnyArray(yylex, $5, $<p>5)) }
	| a_expr NOT_LA LIKE ALL '(' expr_list ')'
			{ $$ = parser.NewInExpr($1.Pos(), $1, false, parser.OpNotLike, true, nil, unwrapAnyArray(yylex, $6, $<p>6)) }
	| a_expr ILIKE ALL '(' expr_list ')'
			{ $$ = parser.NewInExpr($1.Pos(), $1, false, parser.OpILike, true, nil, unwrapAnyArray(yylex, $5, $<p>5)) }
	| a_expr subq_op ANY '(' expr_list ')'
			{
				$$ = quantifiedAny(yylex, $1.Pos(), $1, binOp(yylex, $2), nil, $5, $<p>5)
			}
	| a_expr subq_op SOME '(' expr_list ')'
			{
				$$ = quantifiedAny(yylex, $1.Pos(), $1, binOp(yylex, $2), nil, $5, $<p>5)
			}
	| a_expr subq_op ALL '(' expr_list ')'
			{
				$$ = parser.NewInExpr($1.Pos(), $1, false, binOp(yylex, $2), true, nil, unwrapAnyArray(yylex, $5, $<p>5))
			}
	| a_expr subq_op ANY select_with_parens
			{
				sub, _ := $4.(*parser.SelectStmt)
				if sub == nil {
					sub = parser.NewSelectStmt(0)
				}
				$$ = quantifiedAny(yylex, $1.Pos(), $1, binOp(yylex, $2), sub, nil, 0)
			}
	| a_expr subq_op SOME select_with_parens
			{
				sub, _ := $4.(*parser.SelectStmt)
				if sub == nil {
					sub = parser.NewSelectStmt(0)
				}
				$$ = quantifiedAny(yylex, $1.Pos(), $1, binOp(yylex, $2), sub, nil, 0)
			}
	| a_expr subq_op ALL select_with_parens
			{
				sub, _ := $4.(*parser.SelectStmt)
				if sub == nil {
					sub = parser.NewSelectStmt(0)
				}
				$$ = parser.NewInExpr($1.Pos(), $1, false, binOp(yylex, $2), true, sub, nil)
			}

	| a_expr TYPECAST cast_typename
			{
				nm := $3.name
				if nm == "float" {
					nm = "float8"
				}
				// $3.args is the datetime targets' INLINE typmod
				// (`timestamp(3) with time zone`), which cannot ride the
				// trailing `'(' ICONST ')'` alternatives below.
				tm := typmodsFor(nm, $3.args, len($3.args))
				$$ = parser.NewCastExpr($1.Pos(), $1, parser.ObjectName{Schema: $3.schema, Name: nm}, tm)
			}
	| a_expr TYPECAST cast_typename '(' ICONST ')'
			{
				nm := $3.name
				tm := []int64{int64($5)}
				if nm == "float" && len($3.schema) == 0 {
					if $5 <= 24 {
						nm, tm = "float4", nil // legacy folds the precision into the type name
					} else {
						nm, tm = "float8", nil
					}
				}
				tm = typmodsFor(nm, tm, 1)
				$$ = parser.NewCastExpr($1.Pos(), $1, parser.ObjectName{Schema: $3.schema, Name: nm}, tm)
			}
	| a_expr TYPECAST cast_typename '(' ICONST ',' ICONST ')'
			{
				tm := typmodsFor($3.name, []int64{int64($5), int64($7)}, 2)
				$$ = parser.NewCastExpr($1.Pos(), $1, parser.ObjectName{Schema: $3.schema, Name: $3.name}, tm)
			}

	/* Subscripts — gram.y :15040ff opt_slice_bound forms: base[i],
	   base[l:u], base[:u], base[l:], base[:] (M0097 array slice parity). */
	| a_expr '[' a_expr ']'
			{
				$$ = parser.NewArraySubscriptExpr($1.Pos(), $1, false, $3, nil)
			}
	| a_expr '[' a_expr ':' a_expr ']'
			{
				$$ = parser.NewArraySubscriptExpr($1.Pos(), $1, true, $3, $5)
			}
	| a_expr '[' ':' a_expr ']'
			{
				$$ = parser.NewArraySubscriptExpr($1.Pos(), $1, true, nil, $4)
			}
	| a_expr '[' a_expr ':' ']'
			{
				$$ = parser.NewArraySubscriptExpr($1.Pos(), $1, true, $3, nil)
			}
	| a_expr '[' ':' ']'
			{
				$$ = parser.NewArraySubscriptExpr($1.Pos(), $1, true, nil, nil)
			}

	/* [NOT] SIMILAR TO [+ ESCAPE] — gram.y :15080-15115; constant folding
	   via buildSimilarTo (legacy buildSimilarTo parity). */
	| a_expr SIMILAR TO a_expr %prec SIMILAR
			{
				$$ = buildSimilarTo(yylex, $1, $4, nil, $1.Pos(), false)
			}
	| a_expr SIMILAR TO a_expr ESCAPE a_expr %prec SIMILAR
			{
				$$ = buildSimilarTo(yylex, $1, $4, $6, $1.Pos(), false)
			}
	| a_expr NOT_LA SIMILAR TO a_expr %prec NOT_LA
			{
				$$ = buildSimilarTo(yylex, $1, $5, nil, $1.Pos(), true)
			}
	| a_expr NOT_LA SIMILAR TO a_expr ESCAPE a_expr %prec NOT_LA
			{
				$$ = buildSimilarTo(yylex, $1, $5, $7, $1.Pos(), true)
			}

	/* [NOT] LIKE / ILIKE [+ ESCAPE] — gram.y :15080ff. ESCAPE wraps the
	   pattern in LikeEscapePattern (legacy parity). */
	| a_expr LIKE a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpLike, $1, $3)
			}
	| a_expr NOT_LA LIKE a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpNotLike, $1, $4)
			}
	| a_expr ILIKE a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpILike, $1, $3)
			}
	| a_expr NOT_LA ILIKE a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpNotILike, $1, $4)
			}
	| a_expr LIKE a_expr ESCAPE a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpLike, $1, parser.NewLikeEscapePattern($3.Pos(), $3, $5))
			}
	| a_expr NOT_LA LIKE a_expr ESCAPE a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpNotLike, $1, parser.NewLikeEscapePattern($4.Pos(), $4, $6))
			}
	| a_expr ILIKE a_expr ESCAPE a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpILike, $1, parser.NewLikeEscapePattern($3.Pos(), $3, $5))
			}
	| a_expr NOT_LA ILIKE a_expr ESCAPE a_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpNotILike, $1, parser.NewLikeEscapePattern($4.Pos(), $4, $6))
			}

	/* [NOT] IN — gram.y :15130ff in_expr (list and subquery forms). */
	| a_expr IN_P '(' expr_list ')'
			{
				$$ = parser.NewInExpr($1.Pos(), $1, false, 0, false, nil, $4)
			}
	| a_expr NOT_LA IN_P '(' expr_list ')'
			{
				$$ = parser.NewInExpr($1.Pos(), $1, true, 0, false, nil, $5)
			}
	| a_expr IN_P select_with_parens
			{
				sub, _ := $3.(*parser.SelectStmt)
				if sub == nil {
					sub = parser.NewSelectStmt(0)
				}
				$$ = parser.NewInExpr($1.Pos(), $1, false, 0, false, sub, nil)
			}
	| a_expr NOT_LA IN_P select_with_parens
			{
				sub, _ := $4.(*parser.SelectStmt)
				if sub == nil {
					sub = parser.NewSelectStmt(0)
				}
				$$ = parser.NewInExpr($1.Pos(), $1, true, 0, false, sub, nil)
			}

	/* [NOT] BETWEEN [SYMMETRIC] — gram.y :15190ff with b_expr operands;
	   desugars via buildBetween (legacy parseBetweenTail parity). */
	| a_expr BETWEEN b_expr AND b_expr %prec BETWEEN
			{
				$$ = buildBetween($1, $3, $5, false, false)
			}
	| a_expr BETWEEN SYMMETRIC b_expr AND b_expr %prec BETWEEN
			{
				$$ = buildBetween($1, $4, $6, false, true)
			}
	| a_expr NOT_LA BETWEEN b_expr AND b_expr %prec BETWEEN
			{
				$$ = buildBetween($1, $4, $6, true, false)
			}
	| a_expr NOT_LA BETWEEN SYMMETRIC b_expr AND b_expr %prec BETWEEN
			{
				$$ = buildBetween($1, $5, $7, true, true)
			}

/* c_expr — gram.y :15640ff, P1.1 subset: literals, parameters, column refs. */
c_expr:
	/* NOT PORTED: `tbl.*` as an EXPRESSION (whole-row expansion — VALUES(n.*),
	   f(n.*)). gram.y reaches it through columnref's indirection; grafting
	   `qualified_name '.' '*'` onto c_expr here costs 20 reduce/reduce
	   conflicts (measured 2026-08-27), all of them the pre-existing
	   a_expr/b_expr c_expr split rather than anything about the star itself.
	   Target-list `tbl.*` is unaffected — it has its own target_el
	   alternative (:839). Cost recorded in docs/design/not_ralph/TODO.md. */
	/* The parenthesised expression lives HERE, in c_expr, as gram.y :15540 has
	   it — not in a_expr and b_expr as two separate copies, which is where
	   this grammar kept it until 2026-08-27. That split was the "a_expr/b_expr
	   c_expr split" behind every reduce/reduce measurement on this file: with
	   two paren rules the parser had to decide, right after '(', whether the
	   inside was an a_expr or a b_expr, and any third rule sharing the prefix
	   (a row constructor, `tbl.*`) collided with both. With one rule here the
	   row constructor beside it merely shares a prefix state and the decision
	   is made on ',' versus ')'. b_expr reaches this through `b_expr: c_expr`,
	   so `BETWEEN (x) AND y` still parses. */
		'(' a_expr ')'
			{
				$$ = $2
			}
	/* Implicit row constructor — gram.y implicit_row (:16632), spelled with a
	   mandatory second element so it cannot collide with grouping parens.
	   Legacy builds RowExpr for this form and a plain `row(...)` FuncCall for
	   the explicit ROW(...) spelling (name_or_call already does that); it
	   rejects indirection on either, so none is offered. */
	| '(' expr_list ',' a_expr ')'
			{
				$$ = parser.NewRowExpr($<p>1, append($2, $4))
			}
	/* `tbl.*` as an EXPRESSION — whole-row expansion: VALUES(n.*), f(n.*).
	   gram.y reaches it through columnref's indirection. Only ONE rule may
	   spell it: target_el used to carry its own copy, and the two reduced in
	   the same state — 21 reduce/reduce, one per token that can follow a
	   target entry. target_el now reaches it through a_expr like everything
	   else, and builds the same StarExpr it always did. */
	| qualified_name '.' '*'
			{
				parts := $1.parts
				schema, table := "", parts[len(parts)-1]
				if len(parts) > 1 {
					schema = parts[len(parts)-2]
				}
				$$ = parser.NewStarExpr($1.pos, schema, table)
			}
	/* B'...' / X'...' — the adapter delivers both as BCONST with legacy's
	   marker byte in the value; bitStringConst reproduces decodeBitStringLit
	   (a plain StringConst, hex expanded to bits). */
	| BCONST
			{
				$$ = bitStringConst($<p>1, $1)
			}
	| ICONST
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
	/* interval(p) 'body' — a precision typmod BEFORE the literal. Legacy
	   records it as the Qualified form with unit "second" and the precision. */
	| INTERVAL '(' ICONST ')' SCONST
			{
				$$ = parser.NewIntervalLitQualified($<p>1, $5, "second", true, $3)
			}
	| INTERVAL SCONST
			{
				l := yylex.(*lexerState)
				e := buildIntervalLit(l.lastConsumedPos(), $2)
				// Remember the raw body for AT TIME ZONE — see lastIntervalNode.
				l.lastIntervalNode, l.lastIntervalRaw = e, $2
				$$ = e
			}
	| TYPEDLIT
			{
				typ, val := typedLitParts($1)
				$$ = parser.NewTypedStringLit(yylex.(*lexerState).lastConsumedPos(), typ, val)
			}
	/* Multi-word typed literals — gram.y ConstDatetime Sconst. The lexer's
	   TYPEDLIT fold needs the SCONST to follow the type name IMMEDIATELY, so
	   `TIMESTAMP WITH TIME ZONE '...'` cannot fold and needs real productions.
	   WITH/WITHOUT arrive as WITH_LA/WITHOUT_LA because base_yylex substitutes
	   them when TIME follows. */
	| TIMESTAMP WITH_LA TIME ZONE SCONST
			{ $$ = parser.NewTypedStringLit(yylex.(*lexerState).lastConsumedPos(), "timestamptz", $5) }
	| TIMESTAMP WITHOUT_LA TIME ZONE SCONST
			{ $$ = parser.NewTypedStringLit(yylex.(*lexerState).lastConsumedPos(), "timestamp", $5) }
	| TIME WITH_LA TIME ZONE SCONST
			{ $$ = parser.NewTypedStringLit(yylex.(*lexerState).lastConsumedPos(), "timetz", $5) }
	| TIME WITHOUT_LA TIME ZONE SCONST
			{ $$ = parser.NewTypedStringLit(yylex.(*lexerState).lastConsumedPos(), "time", $5) }
	/* ARRAY[...] — gram.y array_expr: the inner brackets nest WITHOUT the
	   keyword (`array[[1,2],[3,4]]`), and legacy builds nested
	   ArrayConstructorExpr for them. */
	| ARRAY array_expr
			{
				// An empty ARRAY[] carries a NIL element list, as legacy does.
				$$ = parser.NewArrayConstructorExpr(yylex.(*lexerState).lastConsumedPos(), $2)
			}
	/* ARRAY(select) — legacy parses the body with parseSelect inside its own
	   parens, so it is neither Parenthesized nor allowed to start with '('. */
	| ARRAY '(' select_bare ')'
			{
				sub, _ := $3.(*parser.SelectStmt)
				if sub == nil {
					sub = parser.NewSelectStmt(0)
				}
				$$ = parser.NewArraySubqueryExpr(yylex.(*lexerState).lastConsumedPos(), sub)
			}
	| INTERVAL SCONST YEAR_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "year", "", -1) }
	| INTERVAL SCONST MONTH_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "month", "", -1) }
	| INTERVAL SCONST DAY_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "day", "", -1) }
	| INTERVAL SCONST HOUR_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "hour", "", -1) }
	| INTERVAL SCONST MINUTE_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "minute", "", -1) }
	| INTERVAL SCONST SECOND_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "second", "", -1) }
	| INTERVAL SCONST YEAR_P TO MONTH_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "year", "month", -1) }
	| INTERVAL SCONST DAY_P TO HOUR_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "day", "hour", -1) }
	| INTERVAL SCONST DAY_P TO MINUTE_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "day", "minute", -1) }
	| INTERVAL SCONST DAY_P TO SECOND_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "day", "second", -1) }
	| INTERVAL SCONST HOUR_P TO MINUTE_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "hour", "minute", -1) }
	| INTERVAL SCONST HOUR_P TO SECOND_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "hour", "second", -1) }
	| INTERVAL SCONST MINUTE_P TO SECOND_P
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "minute", "second", -1) }
	| INTERVAL SCONST SECOND_P '(' ICONST ')'
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "second", "", $5) }
	/* `<field> TO SECOND(p)` — the range form with a fractional-seconds
	   precision on its trailing field (interval.sql writes it 11 times). */
	| INTERVAL SCONST DAY_P TO SECOND_P '(' ICONST ')'
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "day", "second", $7) }
	| INTERVAL SCONST HOUR_P TO SECOND_P '(' ICONST ')'
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "hour", "second", $7) }
	| INTERVAL SCONST MINUTE_P TO SECOND_P '(' ICONST ')'
			{ $$ = buildIntervalQualified(yylex.(*lexerState).lastConsumedPos(), $2, "minute", "second", $7) }
	| EXTRACT '(' extract_field FROM a_expr ')'
			{
				$$ = parser.NewExtractExpr(yylex.(*lexerState).lastConsumedPos(), $3, $5)
			}
	/* Scalar subquery. %prec UMINUS is what lets ')' SHIFT (nested parens,
	   `((SELECT 1))`) over reducing to c_expr — gram.y :16220 does the same.
	   No stamp: legacy's scalar-subquery path calls parseSelect inside its own
	   parens, so `SELECT (SELECT 1)` is Parenthesized=false there and only a
	   NESTED paren marks it; the previous action stamped unconditionally and
	   every scalar subquery was a silent parity diff. */
	| select_with_parens %prec UMINUS
			{
				sub, _ := $1.(*parser.SelectStmt)
				if sub == nil {
					sub = parser.NewSelectStmt(0)
				}
				$$ = parser.NewSubqueryExpr(yylex.(*lexerState).lastConsumedPos(), sub)
			}

	| name_or_call
			{
				$$ = $1
			}
	| CAST '(' a_expr AS cast_typename ')'
			{
				$$ = parser.NewCastExpr($3.Pos(), $3, parser.ObjectName{Schema: $5.schema, Name: $5.name}, typmodsFor($5.name, $5.args, len($5.args)))
			}
	/* CAST(x AS t(n)) / CAST(x AS t(p,s)) — SIBLING of the `a_expr TYPECAST
	   cast_typename '(' ... ')'` alternatives, which have carried typmods since
	   P2.5. Only the '::' spelling did: `CAST(x AS decimal(15,4))` was a syntax
	   error, and SELECT is routed, so it broke five TPC-DS queries
	   (Q18/Q49/Q61/Q75/Q90). Float-precision folding is kept identical to the
	   TYPECAST arm — legacy collapses float(p) into float4/float8. */
	| CAST '(' a_expr AS cast_typename '(' ICONST ')' ')'
			{
				nm := $5.name
				tm := []int64{int64($7)}
				if nm == "float" && len($5.schema) == 0 {
					if $7 <= 24 {
						nm, tm = "float4", nil
					} else {
						nm, tm = "float8", nil
					}
				}
				$$ = parser.NewCastExpr($3.Pos(), $3, parser.ObjectName{Schema: $5.schema, Name: nm}, typmodsFor(nm, tm, 1))
			}
	| CAST '(' a_expr AS cast_typename '(' ICONST ',' ICONST ')' ')'
			{
				tm := typmodsFor($5.name, []int64{int64($7), int64($9)}, 2)
				$$ = parser.NewCastExpr($3.Pos(), $3, parser.ObjectName{Schema: $5.schema, Name: $5.name}, tm)
			}
	| func_expr_common_subexpr
			{
				$$ = $1
			}

/* func_expr_common_subexpr — gram.y :15812ff, restricted to the SQL value
   functions (the niladic "no-parens" family). goopg's AST has no
   SQLValueFunction node: the legacy parser emits a plain zero-arg FuncCall
   carrying the lower-cased name (internal/parser/select.go:4729-4731,
   classified by IsNoParenFuncName at :4753), so the actions below must
   reproduce that shape byte-for-byte or the differential harness diffs.

   These tokens are RESERVED, so ColId/ColLabel exclude them and nothing
   else in the grammar consumed them — before this rule every one of them
   was unreachable, which is what broke `INSERT INTO pgbench_history
   (..., mtime) VALUES (..., CURRENT_TIMESTAMP)` once INSERT was routed
   (P3.1). SYSTEM_USER is deliberately ABSENT: IsNoParenFuncName does not
   list it, so legacy treats it as a bare identifier and adding it here
   would create a parity diff, not fix one.

   The call form covers both `current_timestamp()` (legacy: parseFuncCallTail
   returns immediately on ')' leaving Args and Variadic nil) and the
   precision form `current_timestamp(3)` (Args=[IntegerConst],
   Variadic=[false] — what NewFuncCall appends for a non-star call). Passing
   $3 straight through preserves the nil, which name_or_call deliberately
   does NOT do; matching legacy is what matters here. */
func_expr_common_subexpr:
		SUBSTRING '(' substr_list ')'
			{ $$ = specialFormCall($<p>1, "substring", $3) }
	| SUBSTRING '(' a_expr SIMILAR a_expr ESCAPE a_expr ')'
			{ $$ = substringSimilar(yylex, $<p>1, $3, $5, $7) }
	| OVERLAY '(' overlay_list ')'
			{ $$ = specialFormCall($<p>1, "overlay", $3) }
	| POSITION '(' position_list ')'
			{ $$ = specialFormCall($<p>1, "position", $3) }
	/* TRIM — gram.y :15900ff. The direction keyword picks the target function
	   (btrim / ltrim / rtrim) and trim_list REVERSES the operands: the string
	   to trim ends up first and the trim characters after it, because gram.y
	   builds it with lappend($3, $1). Legacy reproduces that ordering, so the
	   port has to as well. Note `TRIM(x)` and `TRIM(x, y)` are NOT accepted by
	   legacy — trim_list's bare expr_list alternative is what covers the
	   FROM-less spelling upstream, and legacy simply requires the FROM. */
	| TRIM '(' BOTH trim_list ')'
			{
				$$ = specialFormCall($<p>1, "btrim", $4)
			}
	| TRIM '(' LEADING trim_list ')'
			{
				$$ = specialFormCall($<p>1, "ltrim", $4)
			}
	| TRIM '(' TRAILING trim_list ')'
			{
				$$ = specialFormCall($<p>1, "rtrim", $4)
			}
	| TRIM '(' trim_list ')'
			{
				$$ = specialFormCall($<p>1, "btrim", $3)
			}
	| sql_value_func_name
			{
				$$ = parser.NewFuncCall(yylex.(*lexerState).lastConsumedPos(), parser.ObjectName{Name: $1}, nil, false)
			}
	| sql_value_func_name '(' opt_func_call_args ')'
			{
				$$ = parser.NewFuncCall(yylex.(*lexerState).lastConsumedPos(), parser.ObjectName{Name: $1}, $3, false)
			}

/* sql_value_func_name — the exact eleven names IsNoParenFuncName accepts.
   Keep this list and internal/parser/select.go:4753-4762 in sync: they are
   sibling paths (encode/decode-style) and a name present in only one of
   them is a silent parity diff. */
/* gram.y's trim_list has a third, FROM-less alternative (`expr_list`) that
   makes `TRIM(x)` and `TRIM(x, y)` legal upstream. It is deliberately NOT
   ported: legacy rejects both, so admitting them would widen the routed parser
   past the parser it replaces — and it costs a shift/reduce conflict, because
   `TRIM(` then has to choose between starting an expr_list and starting the
   `a_expr FROM ...` alternative. */
trim_list:
		a_expr FROM expr_list   { $$ = append($3, $1) }
	| FROM expr_list            { $$ = $2 }

sql_value_func_name:
		CURRENT_TIMESTAMP	{ $$ = "current_timestamp" }
	| CURRENT_DATE		{ $$ = "current_date" }
	| CURRENT_TIME		{ $$ = "current_time" }
	| LOCALTIMESTAMP	{ $$ = "localtimestamp" }
	| LOCALTIME		{ $$ = "localtime" }
	| CURRENT_USER		{ $$ = "current_user" }
	| SESSION_USER		{ $$ = "session_user" }
	| USER			{ $$ = "user" }
	| CURRENT_ROLE		{ $$ = "current_role" }
	| CURRENT_CATALOG	{ $$ = "current_catalog" }
	| CURRENT_SCHEMA	{ $$ = "current_schema" }

/* b_expr — gram.y :15040ff subset: the operand grammar for predicates that
   must not swallow AND/BETWEEN/IN/LIKE keywords. Kept name-identical to
   upstream for greppability; grows alongside a_expr waves. */
b_expr:
		c_expr
			{
				$$ = $1
			}
	| b_expr '+' b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpAdd, $1, $3)
			}
	| b_expr '-' b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpSub, $1, $3)
			}
	| b_expr '*' b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpMul, $1, $3)
			}
	| b_expr '/' b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpDiv, $1, $3)
			}
	| b_expr '%' b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpMod, $1, $3)
			}
	| b_expr '<' b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpLt, $1, $3)
			}
	| b_expr '>' b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpGt, $1, $3)
			}
	| b_expr '=' b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpEq, $1, $3)
			}
	| b_expr LESS_EQUALS b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpLe, $1, $3)
			}
	| b_expr GREATER_EQUALS b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpGe, $1, $3)
			}
	| b_expr NOT_EQUALS b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpNe, $1, $3)
			}
	/* b_expr's own unary signs and generic operator — gram.y gives b_expr the
	   same four (`'+' b_expr`, `'-' b_expr`, `b_expr qual_Op b_expr`,
	   `qual_Op b_expr`). Without them BETWEEN, whose operands are b_expr,
	   rejected every signed bound: `BETWEEN -1e6 AND 1e6`. */
	| '-' b_expr %prec UMINUS
			{
				$$ = foldNegate($2)
			}
	| '+' b_expr %prec UMINUS
			{
				$$ = parser.NewUnaryOp(yylex.(*lexerState).lastConsumedPos(), parser.OpUnaryPos, $2)
			}
	/* gram.y b_expr also has TYPECAST; POSITION's operands are b_expr, and
	   strings.sql writes POSITION('x'::bytea IN ''::bytea). */
	| b_expr TYPECAST cast_typename
			{
				nm := $3.name
				if nm == "float" {
					nm = "float8"
				}
				$$ = parser.NewCastExpr($1.Pos(), $1, parser.ObjectName{Schema: $3.schema, Name: nm}, typmodsFor(nm, $3.args, len($3.args)))
			}
	| b_expr '^' b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), parser.OpPow, $1, $3)
			}
	| b_expr Op b_expr
			{
				$$ = parser.NewBinaryOp($1.Pos(), binOp(yylex, $2), $1, $3)
			}
	| Op b_expr %prec Op
			{
				$$ = parser.NewUnaryOp(yylex.(*lexerState).lastConsumedPos(), prefixOp(yylex, $1), $2)
			}

opt_func_call_args:
		/* empty */ { $$ = nil }
	| expr_list  { $$ = $1 }

/* opt_call_args — the name_or_call argument list, which unlike
   opt_func_call_args may carry VARIADIC per argument (gram.y func_arg_expr /
   func_arg_list). ARRAY[...] and the SQL value functions keep the plain list. */
opt_call_args:
		/* empty */                    { $$ = (*callArgs)(nil) }
	| call_arg_list                    { $$ = $1 }

call_arg_list:
		call_arg                       { $$ = appendCallArg(nil, $1.(callArg)) }
	| call_arg_list ',' call_arg       { $$ = appendCallArg($1.(*callArgs), $3.(callArg)) }

call_arg:
		a_expr                         { $$ = callArg{expr: $1} }
	| VARIADIC a_expr                  { $$ = callArg{expr: $2, variadic: true} }
	/* Named arguments `name => value` / `name := value`. Legacy DROPS the name
	   and keeps only the value (PostgreSQL maps named arguments positionally
	   for built-ins; internal/parser/select.go parseFuncCallTail). Written with
	   an a_expr on the left rather than ColId so it cannot be ambiguous with
	   the plain-argument alternative above. */
	| a_expr EQUALS_GREATER a_expr     { $$ = callArg{expr: $3} }
	| a_expr COLON_EQUALS a_expr       { $$ = callArg{expr: $3} }

/* name_or_call — merged ColumnRef/FuncCall disambiguation: after
   qualified_name, seeing '(' shifts into FuncCall; anything else reduces
   ColumnRef. Single nonterminal = zero S/R conflicts. */
name_or_call:
	/* gram.y's func_name is type_function_name, which ADMITS
	   type_func_name_keyword; ColId does not, so `left('ahoj', 2)` and
	   `right(...)` were hard 42601s. Only the CALL form is added: legacy reads
	   a BARE `left` as a column reference, so routing the bare spelling here
	   too would change its node rather than fix an error.

	   The list is spelled out rather than reusing the generated
	   type_func_name_keyword nonterminal, and omits exactly one token from it:
	   CURRENT_SCHEMA, which is ALSO a sql_value_func_name. Reusing the
	   generated list makes that token reducible two ways and costs a
	   reduce/reduce conflict — resolved correctly by rule order, but the
	   conflict gate does not admit reduce/reduce. TestFuncNameKeywordListInSync
	   pins this list against kwlists_gen.y so it cannot drift. */
		func_name_keyword '(' opt_call_args ')'
			{
				$$ = callFuncExpr($<p>1, parser.ObjectName{Name: lowerIdent($1)}, $3.(*callArgs))
			}
	| qualified_name
			{
				$$ = columnRefFromParts($1)
			}
	| qualified_name '(' opt_call_args ')'
			{
				ft := splitFuncName($1)
				// Pass $3 through NIL. Legacy's parseFuncCallTail returns on the
				// empty-parens path without touching fc.Args (select.go:4778), so
				// `now()` is Args=∅; coercing to []parser.Expr{} made every
				// non-OVER zero-arg call a parity diff. The OVER alternatives
				// below already pass $3 straight through.
				$$ = callFuncExpr($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $3.(*callArgs))
			}
	| qualified_name '(' '*' ')'
			{
				ft := splitFuncName($1)
				_ = ft
				$$ = parser.NewFuncCall($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, nil, true)
			}
	| qualified_name '(' '*' ')' filter_clause
			{
				ft := splitFuncName($1)
				fc := parser.NewFuncCall($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, nil, true)
				fc.Filter = $5.(parser.Expr)
				$$ = fc
			}
	| qualified_name '(' '*' ')' filter_clause OVER ColId
			{
				ft := splitFuncName($1)
				fc := parser.NewFuncCall($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, nil, true)
				fc.Filter = $5.(parser.Expr)
				fc.Over = parser.NewBareWindowRef(yylex.(*lexerState).lastConsumedPos(), $7)
				$$ = fc
			}
	| qualified_name '(' '*' ')' filter_clause OVER '(' opt_window_spec ')'
			{
				ft := splitFuncName($1)
				fc := parser.NewFuncCall($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, nil, true)
				fc.Filter = $5.(parser.Expr)
				fc.Over = $8
				$$ = fc
			}
	| qualified_name '(' '*' ')' OVER ColId
			{
				ft := splitFuncName($1)
				fc := parser.NewFuncCall($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, nil, true)
				fc.Over = parser.NewBareWindowRef(yylex.(*lexerState).lastConsumedPos(), $6)
				$$ = fc
			}
	| qualified_name '(' '*' ')' OVER '(' opt_window_spec ')'
			{
				ft := splitFuncName($1)
				fc := parser.NewFuncCall($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, nil, true)
				fc.Over = $7
				$$ = fc
			}
	| qualified_name '(' DISTINCT expr_list ')' OVER '(' opt_window_spec ')'
			{
				ft := splitFuncName($1)
				fc := parser.NewFuncCall($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $4, false)
				fc.Distinct = true
				fc.Over = $8
				$$ = fc
			}
	| qualified_name '(' DISTINCT expr_list ')'
			{
				ft := splitFuncName($1)
				_ = ft
				fc := parser.NewFuncCall($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $4, false)
				fc.Distinct = true
				$$ = fc
			}
	| qualified_name '(' DISTINCT expr_list ')' filter_clause
			{
				ft := splitFuncName($1)
				fc := parser.NewFuncCall($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $4, false)
				fc.Distinct = true
				fc.Filter, _ = $6.(parser.Expr)
				$$ = fc
			}
	| qualified_name '(' DISTINCT expr_list ')' OVER ColId
			{
				ft := splitFuncName($1)
				fc := parser.NewFuncCall($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $4, false)
				fc.Distinct = true
				fc.Over = parser.NewBareWindowRef(yylex.(*lexerState).lastConsumedPos(), $7)
				$$ = fc
			}
	| qualified_name '(' opt_call_args ')' OVER ColId
			{
				ft := splitFuncName($1)
				fc := callFuncExpr($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $3.(*callArgs))
				fc.Over = parser.NewBareWindowRef(yylex.(*lexerState).lastConsumedPos(), $6)
				$$ = fc
			}
	| qualified_name '(' opt_call_args ')' OVER '(' opt_frame_tail ')'
			{
				fc := callFuncExpr(yylex.(*lexerState).lastConsumedPos(), parser.ObjectName{Name: splitFuncName($1).name}, $3.(*callArgs))
				wd := parser.NewWindowDef(0)
				if fr := $7; fr != nil {
					wd.Frame = fr
				}
				fc.Over = wd
				$$ = fc
			}
	| qualified_name '(' opt_call_args ')' OVER '(' PARTITION BY expr_list opt_frame_tail ')'
			{
				fc := callFuncExpr(yylex.(*lexerState).lastConsumedPos(), parser.ObjectName{Name: splitFuncName($1).name}, $3.(*callArgs))
				wd := parser.NewWindowDef(0)
				wd.PartitionBy = $9
				if fr := $10; fr != nil {
					wd.Frame = fr
				}
				fc.Over = wd
				$$ = fc
			}
	| qualified_name '(' opt_call_args ')' OVER '(' ORDER BY sort_by_list opt_frame_tail ')'
			{
				fc := callFuncExpr(yylex.(*lexerState).lastConsumedPos(), parser.ObjectName{Name: splitFuncName($1).name}, $3.(*callArgs))
				wd := parser.NewWindowDef(0)
				wd.OrderBy = $9
				if fr := $10; fr != nil {
					wd.Frame = fr
				}
				fc.Over = wd
				$$ = fc
			}
	/* An existing window name first — gram.y opt_existing_window_name — with
	   its own frame: `OVER (w RANGE BETWEEN ...)`. These alternatives spell
	   the spec inline, so opt_window_spec's ColId forms do not reach them. */
	| qualified_name '(' opt_call_args ')' OVER '(' ColId opt_frame_tail ')'
			{
				fc := callFuncExpr(yylex.(*lexerState).lastConsumedPos(), parser.ObjectName{Name: splitFuncName($1).name}, $3.(*callArgs))
				wd := parser.NewWindowDef(0)
				wd.RefName = $7
				if fr := $8; fr != nil {
					wd.Frame = fr
				}
				fc.Over = wd
				$$ = fc
			}
	| qualified_name '(' opt_call_args ')' OVER '(' ColId ORDER BY sort_by_list opt_frame_tail ')'
			{
				fc := callFuncExpr(yylex.(*lexerState).lastConsumedPos(), parser.ObjectName{Name: splitFuncName($1).name}, $3.(*callArgs))
				wd := parser.NewWindowDef(0)
				wd.RefName = $7
				wd.OrderBy = $10
				if fr := $11; fr != nil {
					wd.Frame = fr
				}
				fc.Over = wd
				$$ = fc
			}
	| qualified_name '(' opt_call_args ')' OVER '(' PARTITION BY expr_list ORDER BY sort_by_list opt_frame_tail ')'
			{
				fc := callFuncExpr(yylex.(*lexerState).lastConsumedPos(), parser.ObjectName{Name: splitFuncName($1).name}, $3.(*callArgs))
				wd := parser.NewWindowDef(0)
				wd.PartitionBy = $9
				wd.OrderBy = $12
				if fr := $13; fr != nil {
					wd.Frame = fr
				}
				fc.Over = wd
				$$ = fc
			}
	/* Aggregate ORDER BY inside the call — gram.y's func_application
	   `func_name '(' func_arg_list opt_sort_clause ')'`. Legacy records it as
	   FuncCall.OrderBy (select.go:4878-4893). */
	| qualified_name '(' call_arg_list ORDER BY sort_by_list ')'
			{
				ft := splitFuncName($1)
				fc := callFuncExpr($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $3.(*callArgs))
				fc.OrderBy = $6
				$$ = fc
			}
	/* expr_list, like the other DISTINCT alternatives — a call_arg_list here
	   reduce/reduces against them on ','. */
	| qualified_name '(' DISTINCT expr_list ORDER BY sort_by_list ')'
			{
				ft := splitFuncName($1)
				fc := parser.NewFuncCall($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $4, false)
				fc.Distinct = true
				fc.OrderBy = $7
				$$ = fc
			}
	| qualified_name '(' opt_call_args ')' filter_clause
			{
				ft := splitFuncName($1)
				_ = ft
				fc := callFuncExpr($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $3.(*callArgs))
				fc.Filter = $5.(parser.Expr)
				$$ = fc
			}
	| qualified_name '(' opt_call_args ')' filter_clause OVER ColId
			{
				ft := splitFuncName($1)
				_ = ft
				fc := callFuncExpr($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $3.(*callArgs))
				fc.Filter = $5.(parser.Expr)
				fc.Over = parser.NewBareWindowRef(0, $7)
				$$ = fc
			}
	| qualified_name '(' opt_call_args ')' filter_clause OVER '(' opt_window_spec ')'
			{
				ft := splitFuncName($1)
				_ = ft
				fc := callFuncExpr($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $3.(*callArgs))
				fc.Filter = $5.(parser.Expr)
				fc.Over = $8
				$$ = fc
			}
	| qualified_name '(' opt_call_args ')' within_group_clause
			{
				ft := splitFuncName($1)
				_ = ft
				fc := callFuncExpr($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $3.(*callArgs))
				if wg, ok := $5.([]parser.SortBy); ok {
					fc.WithinGroup = wg
				}
				$$ = fc
			}
	| qualified_name '(' DISTINCT expr_list ')' within_group_clause
			{
				ft := splitFuncName($1)
				_ = ft
				fc := parser.NewFuncCall($1.pos, parser.ObjectName{Schema: ft.schema, Name: ft.name}, $4, false)
				fc.Distinct = true
				if wg, ok := $6.([]parser.SortBy); ok {
					fc.WithinGroup = wg
				}
				$$ = fc
			}

/* filter_clause / within_group_clause — gram.y :15230ff */
filter_clause:
		FILTER '(' WHERE a_expr ')'
			{
				$$ = $4
			}

within_group_clause:
		WITHIN GROUP_P '(' ORDER BY sort_by_list ')'
			{
				$$ = $5
			}

/* subq_op — gram.y :15150 subquery_Op subset: comparison operators legal
   before ANY/SOME/ALL. Char literals + named terminals. */
subq_op:
		Op        { $$ = $1 }
	| '='           { $$ = "=" }
	| '<'           { $$ = "<" }
	| '>'           { $$ = ">" }
	| LESS_EQUALS    { $$ = "<=" }
	| GREATER_EQUALS { $$ = ">=" }
	| NOT_EQUALS     { $$ = "<>" }

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

/* as_col_label is upstream's ColLabel: everything this grammar's ColLabel takes
   PLUS the reserved keywords. `SELECT true AS true` and `SELECT f1 AS five` are
   both ordinary regress spellings.

   It is a SEPARATE nonterminal rather than a widening of ColLabel because
   ColLabel's other user is set_value_atom (goopg_ext.y:951), and admitting
   reserved words there makes `SET ROLE TO x` ambiguous — TO becomes a candidate
   VALUE for `SET ROLE`, and the shift that wins would swallow it. */
as_col_label:
		ColLabel          { $$ = $1 }
	| reserved_keyword    { $$ = $1 }

BareColLabel:
		IDENT
			{
				$$ = $1
			}
	| bare_label_keyword
			{
				$$ = $1
			}

/* extract_field — gram.y :14085 extract_list subset (the datetime fields
   TPC-H uses; full list arrives with P2.5). Lowercased via helper so the
   grammar prologue needs no extra import. */
extract_field:
		IDENT            { $$ = lowerIdent($1) }
	| YEAR_P           { $$ = "year" }
	| MONTH_P          { $$ = "month" }
	| DAY_P            { $$ = "day" }
	| HOUR_P           { $$ = "hour" }
	| MINUTE_P         { $$ = "minute" }
	| SECOND_P         { $$ = "second" }

/* cast_target — P2.5 slice of gram.y Typename for cast positions (:: and CAST).
   Zero-conflict architecture: tokens starting multi-word forms are EXCLUDED
   from cast_ident so each input has exactly one derivation. Canonicalization
   mirrors legacy parseMultiWordTypeName (ddl.go:5278). Schema-qualified and
   array-typmod forms stay deferred (TODO P2.5). */
/* interval_qual is deliberately NON-empty: an empty alternative here would
   reduce right after INTERVAL and decide one token before the field keyword is
   visible, the mid-rule trap documented in the playbook. A bare INTERVAL keeps
   reaching cast_ident instead. */
interval_qual:
		iv_field                              { $$ = ivQual{hi: $1, prec: -1} }
	| iv_field TO iv_field                    { $$ = ivQual{hi: $1, lo: $3, prec: -1} }
	| iv_field TO iv_field '(' ICONST ')'     { $$ = ivQual{hi: $1, lo: $3, prec: $5} }

iv_field:
		YEAR_P     { $$ = "year" }
	| MONTH_P      { $$ = "month" }
	| DAY_P        { $$ = "day" }
	| HOUR_P       { $$ = "hour" }
	| MINUTE_P     { $$ = "minute" }
	| SECOND_P     { $$ = "second" }

cast_target:
		cast_ident
			{ $$ = castType{name: $1} }
	| DOUBLE_P double_tail
			{ $$ = castType{name: "float8"} }
	| character_word VARYING
			{ $$ = castType{name: "varchar"} }
	| NATIONAL character_word VARYING
			{ $$ = castType{name: "varchar"} }
	| character_word
			{ $$ = castType{name: lowerIdent($1)} }
	| NATIONAL character_word
			{ $$ = castType{name: "character"} }
	| BIT VARYING
			{ $$ = castType{name: "varbit"} }
	| BIT
			{ $$ = castType{name: "bit"} }
	| TIME opt_tzmark
			{ $$ = castType{name: tzJoin("time", $2)} }
	| TIMESTAMP opt_tzmark
			{ $$ = castType{name: tzJoin("timestamp", $2)} }
	/* gram.y ConstDatetime's counted forms. The precision sits BEFORE the tz
	   mark, so col_type_name's trailing typmod suffix cannot express them and
	   `time(2) with time zone` was a hard 42601. */
	| TIME '(' ICONST ')' opt_tzmark
			{ $$ = castType{name: tzJoin("time", $5), args: []int64{int64($3)}} }
	| TIMESTAMP '(' ICONST ')' opt_tzmark
			{ $$ = castType{name: tzJoin("timestamp", $5), args: []int64{int64($3)}} }
	| IDENT '.' ColId
			{ $$ = castType{schema: $1, name: $3} }
	/* INTERVAL <hi> [TO <lo>] [(p)] — gram.y ConstInterval opt_interval. The
	   fields cannot ride col_type_name's trailing typmod suffix (they are bare
	   keywords, not a parenthesised number), so `interval year to month` was a
	   hard 42601 in EVERY routed position: column definitions and casts alike. */
	| INTERVAL interval_qual
			{
				c, cl, ok := parser.IntervalQualTypmods($2.hi, $2.lo, $2.prec)
				if !ok {
					yylex.Error("invalid interval field combination")
					return 1
				}
				$$ = castType{name: "interval", args: []int64{c}, ivCol: []int64{cl}}
			}

/* cast_typename — cast_target plus optional array suffixes ("int[]" folds
   into Name, legacy parity). */
cast_typename:
		cast_target opt_array_tail
			{
				$$ = $1.withArrays($2)
			}

opt_array_tail:
		/* empty */          { $$ = 0 }
	| '[' ']' opt_array_tail { $$ = $3 + 1 }

character_word:
		CHARACTER  { $$ = "character" }
	| CHAR_P      { $$ = "char" }
	| NCHAR       { $$ = "character" } /* legacy: bare nchar aliases character */

double_tail:
		/* empty */ { $$ = "double" }
	| PRECISION    { $$ = "precision" }

opt_tzmark:
		/* empty */             { $$ = "" }
	| WITH_LA TIME ZONE       { $$ = "tz" }
	| WITHOUT_LA TIME ZONE    { $$ = "" }

/* cast_ident — single-word type names legal bare in cast position. Enumerates
   col_name type tokens + unreserved ones legacy accepts; EXCLUDES every
   starter of the multi-word alternatives above so lookahead never has two
   derivations (the P2.5 trap documented in TODO). */
cast_ident:
		IDENT        { $$ = $1 }
	| VARCHAR       { $$ = "varchar" }
	| TEXT_P        { $$ = "text" }
	| NAME_P        { $$ = "name" }
	| BIGINT        { $$ = "bigint" }
	| INT_P         { $$ = "int" }
	| INTEGER       { $$ = "integer" }
	| SMALLINT      { $$ = "smallint" }
	| FLOAT_P       { $$ = "float" }
	| REAL          { $$ = "real" }
	| NUMERIC       { $$ = "numeric" }
	| DECIMAL_P     { $$ = "decimal" }
	| BOOLEAN_P     { $$ = "boolean" }
	| INTERVAL      { $$ = "interval" }
	| UNKNOWN       { $$ = "unknown" }
	/* Keyword-tokenised type names. Measured against legacy, json / xml / path
	   are the ONLY three names it accepts in cast position that do not already
	   arrive as IDENT — every other candidate (jsonb, bytea, uuid, inet,
	   money, the geometric types, ...) is a plain identifier and reaches
	   cast_ident's IDENT alternative unchanged. */
	| JSON          { $$ = "json" }
	| XML_P         { $$ = "xml" }
	| PATH          { $$ = "path" }


/* values_rows — gram.y :13035 values_clause LIST subset: rows are '(' expr_list ')'
   comma-separated; single-row VALUES and multi-type rows inherit the same shape. */
values_rows:
		'(' values_item_list ')'
			{
				$$ = [][]parser.Expr{$2}
			}
	| values_rows ',' '(' values_item_list ')'
			{
				$$ = append($1, $4)
			}

/* values_item — a VALUES row may carry the DEFAULT placeholder
   (`INSERT INTO t VALUES (1, DEFAULT)`), which is not an a_expr. */
values_item_list:
		values_item                          { $$ = []parser.Expr{$1} }
	| values_item_list ',' values_item       { $$ = append($1, $3) }

values_item:
		a_expr    { $$ = $1 }
	| DEFAULT     { $$ = parser.NewDefaultMarker(yylex.(*lexerState).lastConsumedPos()) }

/* insert_stmt — P3.1 v0 (gram.y :17213 insert_rest subset): [WITH ctes]
   INSERT INTO name [(cols)] source where source is VALUES_LA rows / SelectStmt /
   DEFAULT VALUES. ON CONFLICT + RETURNING arrive with later P3 stages.
   VALUES rides VALUES_LA so the DML site shares the expression-site's
   conflict-free treatment of the col_name keyword. */
insert_stmt:
		insert_core opt_on_conflict opt_returning
			{
				is := $1.(*parser.InsertStmt)
				is.OnConflict = $2
				if len($3) > 0 {
					parser.NewInsertReturning(is, $3)
				}
				$$ = is
			}

/* insert_rest folds the optional column list INTO the source alternative
   instead of an empty opt_ins_cols nonterminal — gram.y :12210 does the same,
   and for the same reason: once the source may be a parenthesised select
   (`INSERT INTO t (SELECT 1)`), an EMPTY column-list reduction on '(' fights
   the shift of '(' for `(a, b)`, and the shift that wins would parse the
   select as a column list. With both spelled out, '(' is shifted either way
   and the SECOND token (ColId vs SELECT) decides. */
insert_core:
		INSERT INTO qualified_name opt_ins_alias insert_rest
			{
				src := $5.(*insRest)
				rv, cols := insertTarget($3, $4, src.cols)
				is := parser.NewInsertStmt(0, rv, cols, src.src.rows)
				if src.src.sel != nil {
					parser.SetInsertSelect(is, src.src.sel)
				}
				if src.src.def {
					parser.SetInsertDefaultValues(is)
				}
				$$ = is
			}
	| with_clause INSERT INTO qualified_name opt_ins_alias insert_rest
			{
				src := $6.(*insRest)
				rv, cols := insertTarget($4, $5, src.cols)
				is := parser.NewInsertStmt(0, rv, cols, src.src.rows)
				if src.src.sel != nil {
					parser.SetInsertSelect(is, src.src.sel)
				}
				if src.src.def {
					parser.SetInsertDefaultValues(is)
				}
				is.With = $1
				$$ = is
			}

/* opt_on_conflict — gram.y :17275 subset: all arbiter spellings + both
   actions; DO UPDATE SET supports single- and table-qualified columns. */
opt_on_conflict:
		/* empty */
			{ $$ = nil }
	| ON CONFLICT opt_arbiter DO NOTHING
			{ $$ = parser.NewOnConflictClause($3, parser.OnConflictNothing, nil, nil) }
	| ON CONFLICT opt_arbiter DO UPDATE SET update_set_list opt_update_where
			{ $$ = parser.NewOnConflictClause($3, parser.OnConflictUpdate, $7, $8) }

opt_arbiter:
		/* empty */               { $$ = nil }
	| '(' arbiter_elem_list ')'           { $$ = arbiterFromExprs($2) } /* items may be expressions, not just names */
	| '(' arbiter_elem_list ')' WHERE a_expr
			{
				t := arbiterFromExprs($2)
				t.Where = $5
				$$ = t
			}
	| ON CONSTRAINT ColId { $$ = parser.NewOnConflictTarget(nil, $3, nil) }

update_set_list:
		update_assign                   { $$ = []parser.UpdateAssign{$1} }
	| update_set_list ',' update_assign { $$ = append($1, $3) }

update_assign:
		ColId '=' a_expr
			{ $$ = *parser.NewUpdateAssign($1, "", nil, $3) }
	/* `SET col = DEFAULT` — the DEFAULT placeholder is not an a_expr. */
	| ColId '=' DEFAULT
			{ $$ = *parser.NewUpdateAssign($1, "", nil, parser.NewDefaultMarker(yylex.(*lexerState).lastConsumedPos())) }
	| ColId '.' ColId '=' a_expr
			{ $$ = *parser.NewUpdateAssign($3, $1, nil, $5) }
	/* Multi-column form `(c1, c2) = (e1, e2)` / `= (subquery)` / `= ROW(...)`
	   — gram.y set_clause's `'(' set_target_list ')' '=' a_expr`. The RHS is
	   whatever the expression grammar makes of it: a RowExpr for `(1, 2)`, a
	   SubqueryExpr for a select, a FuncCall for ROW(...). One legacy detail:
	   a single parenthesised value `(a) = (1)` is a one-element RowExpr there,
	   which the expression grammar cannot tell from `1` — multiSetRHS checks
	   the token stream for the '('. */
	| '(' colid_list ')' '=' a_expr
			{ $$ = *parser.NewUpdateAssign("", "", $2, multiSetRHS(yylex, $5, $<p>5)) }

opt_update_where:
		/* empty */   { $$ = nil }
	| WHERE a_expr    { $$ = $2 }

/* opt_returning — gram.y :17340 returning_clause. */
opt_returning:
		/* empty */               { $$ = nil }
	/* target_list, NOT opt_target_list: gram.y's returning_clause (:12377)
	   requires at least one item, and so does legacy — sharing the optional
	   form here would make the yacc parser accept a bare RETURNING. */
	| RETURNING target_list   { $$ = $2 }

/* gram.y insert_target: the alias needs AS, so it cannot be mistaken for a
   column list or a parenthesised source. */
opt_ins_alias:
		/* empty */   { $$ = "" }
	| AS ColId        { $$ = $2 }

insert_rest:
		insert_source                       { $$ = &insRest{src: $1} }
	| '(' colid_list ')' insert_source     { $$ = &insRest{cols: $2, src: $4} }

colid_list:
		ColId                    { $$ = []string{$1} }
	| colid_list ',' ColId       { $$ = append($1, $3) }

insert_source:
		SelectStmt
			{
				// Upstream parses INSERT's source as a full select_stmt; a
				// bare VALUES select converts to Rows here so analyzer/
				// executor see legacy's InsertStmt.Rows shape.
				sel, _ := $1.(*parser.SelectStmt)
				i := &insSrc{}
				if sel != nil && len(sel.ValuesRows) > 0 {
					i.rows = sel.ValuesRows
					sel.ValuesRows = nil
				} else if sel != nil {
					i.sel = sel
				}
				$$ = i
			}
	| DEFAULT VALUES
			{ i := &insSrc{def: true}; $$ = i }

/* update_stmt — P3.2 (gram.y :17400 subset): [WITH] UPDATE [ONLY] name
   [alias] SET assignments [FROM tables] WHERE expr|CURRENT OF cursor
   [RETURNING]. SET reuses the ON CONFLICT assign productions; FROM is a
   plain range-var list (joins in UPDATE FROM stay deferred). */
update_stmt:
		update_core opt_returning
			{
				u := $1.(*parser.UpdateStmt)
				if len($2) > 0 {
					u.Returning = $2
				}
				$$ = u
			}

update_core:
		UPDATE qualified_name opt_upd_alias SET update_set_list opt_upd_from upd_where
			{
				rv := rangeVarFromName($2, $3)
				w := $7.(*updWhere)
				if w.currentOf != "" {
					u := parser.NewUpdateStmt(0, rv, $5, $6, nil)
					parser.SetUpdateWhereCurrentOf(u, w.currentOf)
					$$ = u
				} else {
					$$ = parser.NewUpdateStmt(0, rv, $5, $6, w.expr)
				}
			}
	/* The leading UPDATE keyword was MISSING from this alternative, so
	   `UPDATE ONLY t SET ...` could never reduce. */
	| UPDATE ONLY qualified_name opt_upd_alias SET update_set_list opt_upd_from upd_where
			{
				rv := rangeVarFromName($3, $4)
				rv.Only = true
				w := $8.(*updWhere)
				if w.currentOf != "" {
					u := parser.NewUpdateStmt(0, rv, $6, $7, nil)
					parser.SetUpdateWhereCurrentOf(u, w.currentOf)
					$$ = u
				} else {
					$$ = parser.NewUpdateStmt(0, rv, $6, $7, w.expr)
				}
			}
	| with_clause UPDATE qualified_name opt_upd_alias SET update_set_list opt_upd_from upd_where
			{
				rv := rangeVarFromName($3, $4)
				w := $8.(*updWhere)
				var u *parser.UpdateStmt
				if w.currentOf != "" {
					u = parser.NewUpdateStmt(0, rv, $6, $7, nil)
					parser.SetUpdateWhereCurrentOf(u, w.currentOf)
				} else {
					u = parser.NewUpdateStmt(0, rv, $6, $7, w.expr)
				}
				u.With = $1
				$$ = u
			}

/* opt_upd_alias — bare aliases restricted to plain IDENT so the SET keyword
   cannot be captured as an alias (unreserved-keyword aliases need AS). */
opt_upd_alias:
		/* empty */   { $$ = "" }
	| AS ColId         { $$ = $2 }
	| IDENT            { $$ = $1 }

opt_upd_from:
		/* empty */                       { $$ = nil }
	| FROM upd_from_list               { $$ = $2 }

/* base_table_ref, not qualified_name: legacy accepts EVERYTHING that
   nonterminal covers in `UPDATE ... FROM` and `DELETE ... USING` — aliases,
   AS aliases, ONLY, the inheritance star, derived tables, function tables and
   LATERAL — and rejects only JOIN, which base_table_ref also excludes (joins
   live in table_ref). The bare-name list dropped every alias, so
   `UPDATE t SET ... FROM u b WHERE ...` was a hard 42601. Shared by both
   statements, exactly as before. */
upd_from_list:
		base_table_ref                     { $$ = []parser.RangeVar{$1} }
	| upd_from_list ',' base_table_ref    { $$ = append($1, $3) }

/* upd_where / del_where — the empty alternative was MISSING, so an
   unqualified `UPDATE t SET x = 1` or `DELETE FROM t` was a syntax error on
   the routed path even though both are everyday SQL. */
upd_where:
		/* empty */                 { $$ = &updWhere{} }
	| WHERE a_expr                  { $$ = &updWhere{expr: $2} }
	| WHERE CURRENT_P OF ColId     { $$ = &updWhere{currentOf: $4} }

/* delete_stmt — P3.3 (gram.y :17560 subset): [WITH] DELETE FROM [ONLY] name
   [alias] [USING tables] WHERE expr|CURRENT OF cursor [RETURNING]. Alias
   uses the IDENT-only bare form (USING is unreserved — same dodge as
   UPDATE's SET). */
delete_stmt:
		delete_core opt_returning
			{
				d := $1.(*parser.DeleteStmt)
				if len($2) > 0 {
					d.Returning = $2
				}
				$$ = d
			}

delete_core:
		DELETE_P FROM qualified_name opt_upd_alias opt_using_list del_where
			{
				rv := rangeVarFromName($3, $4)
				w := $6.(*updWhere)
				if w.currentOf != "" {
					d := parser.NewDeleteStmt(0, rv, $5, nil)
					parser.SetDeleteWhereCurrentOf(d, w.currentOf)
					$$ = d
				} else {
					$$ = parser.NewDeleteStmt(0, rv, $5, w.expr)
				}
			}
	| DELETE_P FROM ONLY qualified_name opt_upd_alias opt_using_list del_where
			{
				rv := rangeVarFromName($4, $5)
				rv.Only = true
				w := $7.(*updWhere)
				if w.currentOf != "" {
					d := parser.NewDeleteStmt(0, rv, $6, nil)
					parser.SetDeleteWhereCurrentOf(d, w.currentOf)
					$$ = d
				} else {
					$$ = parser.NewDeleteStmt(0, rv, $6, w.expr)
				}
			}
	| with_clause DELETE_P FROM qualified_name opt_upd_alias opt_using_list del_where
			{
				rv := rangeVarFromName($4, $5)
				w := $7.(*updWhere)
				var d *parser.DeleteStmt
				if w.currentOf != "" {
					d = parser.NewDeleteStmt(0, rv, $6, nil)
					parser.SetDeleteWhereCurrentOf(d, w.currentOf)
				} else {
					d = parser.NewDeleteStmt(0, rv, $6, w.expr)
				}
				d.With = $1
				$$ = d
			}

opt_using_list:
		/* empty */            { $$ = nil }
	| USING upd_from_list    { $$ = $2 }

del_where:
		/* empty */                 { $$ = &updWhere{} }
	| WHERE a_expr                  { $$ = &updWhere{expr: $2} }
	| WHERE CURRENT_P OF ColId     { $$ = &updWhere{currentOf: $4} }


col_constraints:
		/* empty */                    { $$ = &colConstraints{} }
	| col_constraints col_constraint
			{
				cc := $1.(*colConstraints)
				switch k := $2.(*colConstraint); k.kind {
				case "nn":
					cc.notNull, cc.nnName = true, k.name
				case "nn_noinh":
					cc.notNull, cc.nnNoInherit, cc.nnName = true, true, k.name
				case "pk":
					cc.primary = true // legacy drops a CONSTRAINT name here
				case "uq":
					cc.unique, cc.uqName = true, k.name
				case "def":
					// Legacy DROPS a named default outright
					// (`CONSTRAINT df DEFAULT 1` leaves DefaultExpr nil).
					if k.name == "" {
						cc.defExpr = k.expr
					}
				case "check":
					cc.checkText, cc.checkName = k.text, k.name
				case "check_noinh":
					cc.checkText, cc.checkName, cc.checkNoInherit = k.text, k.name, true
				case "attr_not_enforced":
					cc.checkNotEnforced = true
				case "attr_enforced":
					cc.checkNotEnforced = false
				case "null":
					// nothing to record
				case "storage":
					cc.storage = k.text
				case "collate":
					cc.collation = k.text
				case "compression":
					cc.compression = k.text
				case "fk":
					cc.fk = k.fk
				case "uq_nnd":
					cc.unique, cc.nullsNotDistinct = true, true
				case "attr_deferrable":
					cc.deferrable = true
				case "attr_not_deferrable":
					cc.deferrable, cc.initiallyDeferred = false, false
				case "attr_initially_deferred":
					// INITIALLY DEFERRED implies DEFERRABLE (legacy
					// parseConstraintDeferrable, internal/parser/ddl.go).
					cc.deferrable, cc.initiallyDeferred = true, true
				case "attr_initially_immediate":
					cc.initiallyDeferred = false
				case "gen":
					cc.genAlways, cc.genExpr = true, k.text
				case "gen_virtual":
					cc.genAlways, cc.genExpr, cc.genVirtual = true, k.text, true
				case "identity_always":
					cc.identity, cc.identityAlways, cc.identitySeq = true, true, k.seq
				case "identity_default":
					cc.identity, cc.identityAlways, cc.identitySeq = true, false, k.seq
				}
				$$ = cc
			}

col_constraint:
		NOT NULL_P        { $$ = &colConstraint{kind: "nn"} }
	| NOT NULL_P NO INHERIT { $$ = &colConstraint{kind: "nn_noinh"} }
	/* `CONSTRAINT name` prefix — gram.y ColConstraint. Legacy records the name
	   only for NOT NULL, UNIQUE and CHECK; on PRIMARY KEY, DEFAULT and
	   REFERENCES it is parsed and dropped, which the merge above mirrors. */
	| CONSTRAINT ColId col_constraint
			{
				c := $3.(*colConstraint)
				c.name = $2
				$$ = c
			}
	/* COLLATE and COMPRESSION ride the same qualifier loop as the constraints
	   (gram.y puts COLLATE in ColQualList; COMPRESSION sits just before it).
	   ColId, not a_expr, so `DEFAULT x COLLATE "C"` keeps binding the COLLATE
	   into the DEFAULT expression exactly as it did before. */
	| COLLATE ColId       { $$ = &colConstraint{kind: "collate", text: $2} }
	| COMPRESSION ColId   { $$ = &colConstraint{kind: "compression", text: $2} }
	/* PRIMARY KEY / UNIQUE / DEFAULT used to sit at the END of fk_kw below:
	   the FK rules (opt_ref_cols … fk_kw) were inserted between REFERENCES
	   and these three alternatives, so yacc read them as fk_kw alternatives.
	   Effects, all live because CREATE TABLE is routed: `a int primary key`,
	   `a int unique` and `a int default 0` were syntax errors, and
	   `ON DELETE PRIMARY KEY` PANICKED applyFkAction with an interface
	   conversion (*colConstraint is not parser.FKAction). Keep new
	   col_constraint alternatives ABOVE the FK helper rules. */
	| PRIMARY KEY         { $$ = &colConstraint{kind: "pk"} }
	| UNIQUE              { $$ = &colConstraint{kind: "uq"} }
	| UNIQUE NULLS_P NOT DISTINCT { $$ = &colConstraint{kind: "uq_nnd"} }
	/* ConstraintAttr — gram.y ColConstraint's own alternatives, NOT a trailer
	   on each constraint element. Making them siblings in this loop is what
	   keeps `NOT NULL` and `NOT DEFERRABLE` separable with one token of
	   lookahead: after shifting NOT, NULL_P and DEFERRABLE pick the arm. */
	/* Generated columns — gram.y ColConstraintElem's GENERATED arms. The
	   expression is a RAW SOURCE SPAN in legacy (like CHECK), so it uses the
	   same markSpanStart / spanTextCloseParen pair. IDENTITY option lists
	   (START WITH / INCREMENT BY / ...) are not ported yet; see TODO.md. */
	/* The expression text is legacy's TOKEN JOIN (joinGeneratedExprTokens),
	   not a source span, and the storage word is optional — PG 18 defaults a
	   generated column to VIRTUAL, and so does legacy. No mid-rule action any
	   more: the join takes the two paren positions. */
	| GENERATED ALWAYS AS '(' a_expr ')' opt_gen_storage
			{
				k := "gen"
				if $7 {
					k = "gen_virtual"
				}
				$$ = &colConstraint{kind: k, text: joinLegacyTokens(yylex, $<p>4, $<p>6)}
			}
	/* GENERATED BY DEFAULT AS (expr): legacy records it exactly like ALWAYS. */
	| GENERATED BY DEFAULT AS '(' a_expr ')' opt_gen_storage
			{
				k := "gen"
				if $8 {
					k = "gen_virtual"
				}
				$$ = &colConstraint{kind: k, text: joinLegacyTokens(yylex, $<p>5, $<p>7)}
			}
	| GENERATED ALWAYS AS IDENTITY_P opt_identity_seq_opts
			{ $$ = &colConstraint{kind: "identity_always", seq: $5.(*identityOpts)} }
	| GENERATED BY DEFAULT AS IDENTITY_P opt_identity_seq_opts
			{ $$ = &colConstraint{kind: "identity_default", seq: $6.(*identityOpts)} }
	| DEFERRABLE          { $$ = &colConstraint{kind: "attr_deferrable"} }
	| NOT DEFERRABLE      { $$ = &colConstraint{kind: "attr_not_deferrable"} }
	| INITIALLY DEFERRED  { $$ = &colConstraint{kind: "attr_initially_deferred"} }
	| INITIALLY IMMEDIATE { $$ = &colConstraint{kind: "attr_initially_immediate"} }
	/* %prec Op (below COLLATE): `DEFAULT 'x' COLLATE "C"` binds the COLLATE
	   into the default expression, which is legacy's greedy parseExpr
	   behaviour; a column-level COLLATE must precede DEFAULT to be one. */
	| DEFAULT a_expr %prec Op { $$ = &colConstraint{kind: "def", expr: $2} }
	/* ONE check_body for both spellings: two alternatives sharing the prefix
	   and each carrying its own mid-rule markSpanStart() would be two
	   distinct empty nonterminals reducible at the same point — the 420-
	   reduce/reduce trap generated columns hit, and 1329 here. */
	| check_body opt_no_inherit
			{
				k := "check"
				if $2 {
					k = "check_noinh"
				}
				$$ = &colConstraint{kind: k, text: $1}
			}
	/* [NOT] ENFORCED is its OWN list item, as gram.y's ConstraintAttributeSpec
	   is: written as a trailer on the CHECK rule, `CHECK (...) . NOT` cannot
	   tell NOT ENFORCED from a following NOT NULL constraint with one token
	   of lookahead, and the shift that wins breaks `CHECK (x > 0) NOT NULL`. */
	| NOT ENFORCED        { $$ = &colConstraint{kind: "attr_not_enforced"} }
	| ENFORCED            { $$ = &colConstraint{kind: "attr_enforced"} }
	| NULL_P              { $$ = &colConstraint{kind: "null"} }   /* explicit nullability: a no-op, as legacy */
	| STORAGE ColId       { $$ = &colConstraint{kind: "storage", text: $2} }
	| REFERENCES qualified_name opt_ref_cols opt_fk_match opt_fk_actions
			{
				i := &colConstraint{kind: "fk"}
				acts := $5.(*fkActs)
				i.fk = &fkInfo{refTable: objectNameFromQn($2), refCols: $3, onDel: acts.del, onUp: acts.up, delSetCols: acts.delSetCols, matchFull: $4}
				$$ = i
			}

opt_ref_cols:
		/* empty */       { $$ = nil }
	| '(' colid_list ')'  { $$ = $2 }

opt_fk_actions:
		/* empty */      { $$ = &fkActs{} }
	| fk_actions         { $$ = $1 }

fk_actions:
		fk_action                  { i := &fkActs{}; $$ = applyFkAction(i, $1.(*namedFkAct)) }
	| fk_actions fk_action         { $$ = applyFkAction($1.(*fkActs), $2.(*namedFkAct)) }

fk_action:
		ON DELETE_P fk_kw    { $$ = &namedFkAct{del: true, act: $3.(parser.FKAction)} }
	| ON DELETE_P fk_set_act
			{
				sa := $3.(*namedFkAct)
				sa.del = true
				$$ = sa
			}
	| ON UPDATE SET fk_kw    { $$ = &namedFkAct{up: true, act: $4.(parser.FKAction)} }
	| ON UPDATE fk_kw        { $$ = &namedFkAct{up: true, act: $3.(parser.FKAction)} }
	| ON UPDATE fk_set_act
			{
				sa := $3.(*namedFkAct)
				sa.up = true
				$$ = sa
			}

/* SET NULL / SET DEFAULT live outside fk_kw so their optional column list
   (`ON DELETE SET NULL (cols)`, PG 15) does not shift/reduce against the
   keyword-only reduce. Legacy keeps the list for ON DELETE only. */
fk_set_act:
		SET NULL_P opt_fk_set_cols       { $$ = &namedFkAct{act: parser.FKActionSetNull, setCols: $3} }
	| SET DEFAULT opt_fk_set_cols        { $$ = &namedFkAct{act: parser.FKActionSetDefault, setCols: $3} }

opt_fk_set_cols:
		/* empty */         { $$ = nil }
	| '(' colid_list ')'    { $$ = $2 }

opt_fk_match:
		/* empty */      { $$ = false }
	| MATCH FULL         { $$ = true }
	| MATCH SIMPLE       { $$ = false }
	| MATCH PARTIAL      { $$ = false }

opt_enforced:
		/* empty */      { $$ = false }
	| ENFORCED           { $$ = false }
	| NOT ENFORCED       { $$ = true }

opt_gen_storage:
		/* empty */   { $$ = true }
	| STORED          { $$ = false }
	| VIRTUAL         { $$ = true }

fk_kw:
		CASCADE              { $$ = parser.FKActionCascade }
	| RESTRICT               { $$ = parser.FKActionRestrict }
	| NO ACTION              { $$ = parser.FKActionNoAction }

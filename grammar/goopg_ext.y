/* grammar/goopg_ext.y — goopg-only grammar extensions. */
/*  */
/* Policy (docs/design/not_ralph/02-grammar-porting-guide.md §7): every rule */
/* here must carry a `// GOOPG-EXT: <reason>` tag. Rules splice BEFORE */
/* pg_grammar.y's final "%%" (the Makefile appends the closing %% itself), so */
/* alternatives may extend extension points defined in the main file. */
/*  */
/* Currently EMPTY: the survey found no goopg-specific syntax. Statements */
/* upstream has but goopg has not implemented are expressed as faithful */
/* pg_grammar rules producing the existing compat stubs, not as extensions. */

/* create_table_stmt — P4.1 v0 (gram.y CREATE TABLE subset): column defs with
   NOT NULL / PRIMARY KEY / UNIQUE / DEFAULT plus table-level PRIMARY KEY and
   UNIQUE lists. Types reuse cast_typename (P2.5) so multi-word/tz/array forms
   match cast behaviour; parenthesised typmods fold into ColumnType.Args.
   CHECK / FK / named constraints / WITH / partitioning arrive in later P4
   slices. */
create_table_stmt:
		CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name '(' table_element_list ')' opt_ct_tail
			{
				elems := $7.([]*tableElem)
				var cols []parser.ColumnDef
				var pk []string
				var uqs [][]string
				for _, e := range elems {
					switch {
					case e.col != nil:
						c := e.col
						cd := parser.NewColumnDef(c.name, parser.NewColumnType(c.schema, c.typ, c.args, c.isArray))
						cd.NotNull = c.notNull
						cd.NotNullExplicit = c.notNull
						cd.Primary = c.primary
						cd.Unique = c.unique
						cd.DefaultExpr = c.defExpr
						if c.checkText != "" {
							cd.CheckExpr = c.checkText
						}
						if c.fkInfo != nil {
							cd.RefTable = c.fkInfo.refTable
							cd.RefColumns = c.fkInfo.refCols
							cd.OnDelete = c.fkInfo.onDel
							cd.OnUpdate = c.fkInfo.onUp
						}
						cols = append(cols, *cd)
					default:
						pk = append(pk, e.pk...)
						uqs = append(uqs, e.uq...)
					}
				}
				nm := $5.parts
	tbl := parser.ObjectName{Name: nm[len(nm)-1]}
	if len(nm) > 1 {
		tbl.Schema = nm[len(nm)-2]
	}
 	ct := parser.NewCreateTableStmt(0, tbl, cols, pk)
				for _, c := range cols {
					ct.BodyOrder = append(ct.BodyOrder, c.Name)
				}
				if pfx := $2.(*createPrefix); pfx != nil {
					ct.Temporary = pfx.temporary
					ct.Unlogged = pfx.unlogged
				}
				ct.IfNotExists = $4
				ct.TableUniques = uqs
				tail := $9
				_ = tail
				for _, kv := range tail.withKv {
					if ct.With == nil {
						ct.With = map[string]string{}
					}
					parts := splitKV(kv)
					if len(parts) == 2 {
						ct.With[parts[0]] = parts[1]
					}
				}
				ct.Inherits = tail.inherits
				ct.PartitionBy = tail.partition
				ct.SelectSource = tail.asSelect
				$$ = ct
			}

/* opt_create_modifier — TEMP/TEMPORARY/UNLOGGED between CREATE and TABLE. */
opt_create_modifier:
		/* empty */   { $$ = (*createPrefix)(nil) }
	| TEMP            { $$ = &createPrefix{temporary: true} }
	| TEMPORARY       { $$ = &createPrefix{temporary: true} }
	| UNLOGGED        { $$ = &createPrefix{unlogged: true} }

opt_if_not_exists:
		/* empty */       { $$ = false }
	| IF_P NOT_LA EXISTS  { $$ = true }

table_element_list:
		table_element                           { $$ = []*tableElem{$1.(*tableElem)} }
	| table_element_list ',' table_element     { $$ = append($1.([]*tableElem), $3.(*tableElem)) }

table_element:
		ColId col_type_name col_constraints
			{
				cs := &colSpec{name: $1}
				tw := $2.(*typeWithArgs)
				cs.schema, cs.typ = tw.ct.schema, tw.ct.name
				cs.isArray = len(cs.typ) >= 2 && cs.typ[len(cs.typ)-2:] == "[]"
				cc := $3.(*colConstraints)
				cs.args, cs.notNull, cs.primary, cs.unique, cs.defExpr =
					cc.args, cc.notNull, cc.primary, cc.unique, cc.defExpr
				$$ = &tableElem{col: cs}
			}
	| PRIMARY KEY pk_cols   { $$ = &tableElem{pk: $3} }
	| UNIQUE uq_cols        { $$ = &tableElem{} }
	| CONSTRAINT ColId PRIMARY KEY pk_cols
			{
				d := parser.NewTableConstraintDef($2, $5, true)
				$$ = &tableElem{pk: $5, namedPk: d}
			}
	| CONSTRAINT ColId UNIQUE uq_cols
			{ $$ = &tableElem{namedUq: parser.NewTableConstraintDef($2, $4, false)} }
	| CONSTRAINT ColId CHECK '(' { yylex.(*lexerState).markSpanStart() } a_expr ')'
			{ $$ = &tableElem{check: yylex.(*lexerState).spanText(), checkName: $2} }

pk_cols:
		'(' colid_list ')'   { $$ = $2 }

uq_cols:
		'(' colid_list ')'   { $$ = $2 }

/* col_type_name — cast_typename plus optional typmod args; arrays ride
   cast_typename's own suffix and are re-detected in the action. */
col_type_name:
		cast_typename                            { $$ = &typeWithArgs{ct: $1} }
	| col_type_name '(' ICONST ')'               { $1.(*typeWithArgs).args = []int64{int64($3)}; $$ = $1 }
	| col_type_name '(' ICONST ',' ICONST ')'    { $1.(*typeWithArgs).args = []int64{int64($3), int64($5)}; $$ = $1 }


/* drop_table_stmt / truncate_stmt — P4.4 (gram.y DropStmt / TruncateStmt
   subsets). DROP uses the two-keyword dispatch ("drop table"); TRUNCATE
   routes on its own leading keyword. ONLY-per-table and RESTART IDENTITY
   forms arrive with a later slice. */
create_table_stmt_as:
		CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name AS SelectStmt
			{
				sel, _ := $7.(*parser.SelectStmt)
				nm := $5.parts
				tbl := parser.ObjectName{Name: nm[len(nm)-1]}
				if len(nm) > 1 {
					tbl.Schema = nm[len(nm)-2]
				}
				ct := parser.NewCreateTableStmt(0, tbl, nil, nil)
				if pfx := $2.(*createPrefix); pfx != nil {
					ct.Temporary = pfx.temporary
					ct.Unlogged = pfx.unlogged
				}
				ct.IfNotExists = $4
				ct.SelectSource = sel
				$$ = ct
			}

drop_table_stmt:
		DROP TABLE opt_if_exists_drop drop_name_list opt_drop_behavior
			{
				$$ = parser.NewDropTableStmt(0, $3, $4, dropBehavior($5))
			}

opt_if_exists_drop:
		/* empty */   { $$ = false }
	| IF_P EXISTS     { $$ = true }

drop_name_list:
		qualified_name                         { $$ = []parser.ObjectName{objectNameFromQn($1)} }
	| drop_name_list ',' qualified_name        { $$ = append($1, objectNameFromQn($3)) }

opt_drop_behavior:
		/* empty */  { $$ = "" }
	| CASCADE       { $$ = "cascade" }
	| RESTRICT      { $$ = "restrict" }

truncate_stmt:
		TRUNCATE opt_TRUNCATE_kw drop_name_list opt_restart opt_drop_behavior
			{
				onl := make([]bool, len($3))
				for i := range onl {
					onl[i] = false
				}
				$$ = parser.NewTruncateStmt(0, $3, onl, dropBehavior($5), $4)
			}

opt_TRUNCATE_kw:
		/* empty */  { _ = 0 }
	| TABLE          { _ = 0 }

opt_restart:
		/* empty */            { $$ = false }
	| RESTART IDENTITY_P       { $$ = true }
	| CONTINUE_P IDENTITY_P   { $$ = false }


/* opt_ct_tail — trailing options v2: flat keyword-distinct alternatives. */
opt_ct_tail:
		/* empty */     { $$ = &ctTail{} }
	| WITH '(' str_pair_list ')'
			{ i := &ctTail{}; i.withKv = $3; $$ = i }
	| INHERITS '(' drop_name_list ')'
			{ i := &ctTail{}; i.inherits = $3; $$ = i }
	| AS SelectStmt
			{
				sel, _ := $2.(*parser.SelectStmt)
				i := &ctTail{}; i.asSelect = sel; $$ = i
			}
	| PARTITION BY ColId '(' colid_list ')'
			{
				pb := parser.NewPartitionByClause(upperIdent($3), nil)
				for _, c := range $5 {
					pb.KeyCols = append(pb.KeyCols, c)
					pb.OpClasses = append(pb.OpClasses, "")
					pb.Collations = append(pb.Collations, "")
				}
				i := &ctTail{}; i.partition = pb; $$ = i
			}

str_pair_list:
		str_pair                    { $$ = []string{$1} }
	| str_pair_list ',' str_pair    { $$ = append($1, $3) }

str_pair:
		ColId '=' with_value        { $$ = $1 + "=" + $3 }

with_value:
		SCONST   { $$ = $1 }
	| ICONST     { $$ = yylex.(*lexerState).lastText }

	| FCONST     { $$ = yylex.(*lexerState).lastText }

	| TRUE_P     { $$ = "true" }
	| FALSE_P    { $$ = "false" }
	| ON         { $$ = "on" }
	| OFF        { $$ = "off" }

/* create_index_stmt / drop_index_stmt — P4.4 (gram.y IndexStmt / DropStmt
   subsets). v0: plain column keys (expressions, DESC/NULLS, opclasses and
   CONCURRENTLY arrive later); ColOrders/ColExprs filled with per-column
   defaults for legacy dump parity. */
create_index_stmt:
		CREATE opt_unique INDEX opt_if_not_exists ColId ON qualified_name opt_using_method '(' index_col_list ')'
			{
				nm := $7.parts
				tbl := parser.ObjectName{Name: nm[len(nm)-1]}
				if len(nm) > 1 {
					tbl.Schema = nm[len(nm)-2]
				}
				$$ = parser.NewCreateIndexStmt(0, $2, $4, $5, tbl, $8, $10)
			}

opt_unique:
		/* empty */  { $$ = false }
	| UNIQUE        { $$ = true }

opt_using_method:
		/* empty */      { $$ = "" }
	| USING ColId        { $$ = $2 }

index_col_list:
		index_col                     { $$ = []string{$1} }
	| index_col_list ',' index_col   { $$ = append($1, $3) }

index_col:
		ColId                         { $$ = $1 }

drop_index_stmt:
		DROP INDEX opt_drop_if_exists drop_name_list opt_drop_behavior
			{
				$$ = parser.NewDropIndexStmt(0, false, $3, $4, dropBehavior($5))
			}

opt_drop_if_exists:
		/* empty */    { $$ = false }
	| IF_P EXISTS      { $$ = true }

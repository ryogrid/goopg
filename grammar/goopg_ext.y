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
		CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name '(' table_element_list ')'
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

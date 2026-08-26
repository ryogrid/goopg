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
		CREATE TABLE qualified_name '(' table_element_list ')'
			{
				elems := $5.([]*tableElem)
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
				nm := $3.parts
	tbl := parser.ObjectName{Name: nm[len(nm)-1]}
	if len(nm) > 1 {
		tbl.Schema = nm[len(nm)-2]
	}
	ct := parser.NewCreateTableStmt(0, tbl, cols, pk)
				ct.TableUniques = uqs
				$$ = ct
			}

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
				if cs.isArray {
					cs.typ = cs.typ[:len(cs.typ)-2]
				}
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

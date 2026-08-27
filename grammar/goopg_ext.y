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
		CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name '(' opt_table_element_list ')' ct_tail_list opt_tablespace
			{
				elems := $7.([]*tableElem)
				var cols []parser.ColumnDef
				var pk []string
				var uqs [][]string
				var uqIncludes [][]string
				var uqNullsNotDistinct, uqDeferrable, uqInitiallyDeferred []bool
				var named []parser.TableConstraintDef
				var exclusions []parser.TableConstraintDef
				var likes []parser.ObjectName
				var bodyOrder []string
				var namedChecks []parser.PartitionCheckConstraint
				var fks []parser.TableForeignKeyDef
				var pkIncl []string
				var pkDeferrable, pkInitiallyDeferred bool
				var checks []string
				var checkNoInherit, checkNotEnforced []bool
				var nnNames, nnCols []string
				hasNoInheritCheck := false
				var nnNoInherit []bool
				for _, e := range elems {
					switch {
					case e.col != nil:
						c := e.col
						cd := parser.NewColumnDef(c.name, parser.NewColumnType(c.schema, c.typ, c.args, c.isArray))
						cd.NotNull = c.notNull || c.primary
						cd.NotNullExplicit = c.notNull
						cd.Primary = c.primary
						cd.Unique = c.unique
						cd.UniqueNullsNotDistinct = c.nullsNotDistinct
						cd.GeneratedAlways = c.genAlways
						cd.GeneratedExpr = c.genExpr
						cd.GeneratedVirtual = c.genVirtual
						cd.IdentityColumn = c.identity
						cd.IdentityAlways = c.identityAlways
						applyIdentityOpts(cd, c.identitySeq)
						copyColConstraints(cd, c.collation, c.compression, c.nnName, c.uqName, c.checkName, c.nnNoInherit, c.checkNoInherit)
						// The attrs attach to whichever constraint the column
						// actually declared (legacy threads pointers to the
						// specific flag pair).
						if c.unique {
							cd.UniqueDeferrable = c.deferrable
							cd.UniqueInitiallyDeferred = c.initiallyDeferred
						}
						if c.primary {
							cd.PrimaryDeferrable = c.deferrable
							cd.PrimaryInitiallyDeferred = c.initiallyDeferred
						}
						if c.fkInfo != nil {
							cd.FKDeferrable = c.deferrable
							cd.FKInitiallyDeferred = c.initiallyDeferred
						}
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
						bodyOrder = append(bodyOrder, c.name)
						if c.primary {
							// Legacy records a column-level PRIMARY KEY in the
							// statement's PrimaryKey list as well as on the
							// ColumnDef; downstream DDL reads the list.
							pk = append(pk, c.name)
						}
					default:
						pk = append(pk, e.pk...)
						if len(e.pk) > 0 {
							pkIncl = e.pkIncl
							if a := e.pkAttrs; a != nil {
								pkDeferrable, pkInitiallyDeferred = a.deferrable, a.initiallyDeferred
							}
						}
						for _, u := range e.uq {
							// TableUniques has four PARALLEL slices; legacy
							// appends to all five per anonymous UNIQUE
							// (internal/parser/ddl.go:3958-4016), and canonDump
							// distinguishes ∅ from [false].
							uqs = append(uqs, u)
							uqIncludes = append(uqIncludes, e.uqIncl)
							uqNullsNotDistinct = append(uqNullsNotDistinct, e.uqNND)
							def, initDef := false, false
							if a := e.uqAttrs; a != nil {
								def, initDef = a.deferrable, a.initiallyDeferred
							}
							uqDeferrable = append(uqDeferrable, def)
							uqInitiallyDeferred = append(uqInitiallyDeferred, initDef)
						}
						if e.fkDef != nil {
							fks = append(fks, *e.fkDef)
						}
						if e.namedPk != nil {
							named = append(named, *e.namedPk)
						}
						if e.namedUq != nil {
							named = append(named, *e.namedUq)
						}
						if e.exclusion != nil {
							exclusions = append(exclusions, *e.exclusion)
						}
						if e.notNull != nil {
							nnNames = append(nnNames, e.notNull.name)
							nnCols = append(nnCols, e.notNull.col)
							nnNoInherit = append(nnNoInherit, e.notNull.noInherit)
						}
						if e.like != nil {
							likes = append(likes, *e.like)
							// Legacy interleaves a "@@LIKE:<dotted name>"
							// marker into BodyOrder at the element's position,
							// so BodyOrder has to be built in the loop rather
							// than from the column list afterwards.
							mk := e.like.Name
							if e.like.Schema != "" {
								mk = e.like.Schema + "." + mk
							}
							bodyOrder = append(bodyOrder, "@@LIKE:"+mk+e.likeOpts)
						}
						if e.check != "" {
							if e.checkName != "" {
								// CONSTRAINT c CHECK (...) -> TableNamedChecks,
								// and legacy does NOT touch the anonymous
								// parallel slices for it (ddl.go:4191-4238).
								namedChecks = append(namedChecks, parser.PartitionCheckConstraint{Name: e.checkName, Expr: e.check, NoInherit: e.checkNoInh})
							} else {
								checks = append(checks, e.check)
								checkNoInherit = append(checkNoInherit, e.checkNoInh)
								checkNotEnforced = append(checkNotEnforced, false)
							}
							if e.checkNoInh {
								hasNoInheritCheck = true
							}
						}
					}
				}
				nm := $5.parts
	tbl := parser.ObjectName{Name: nm[len(nm)-1]}
	if len(nm) > 1 {
		tbl.Schema = nm[len(nm)-2]
	}
 	ct := parser.NewCreateTableStmt(0, tbl, cols, pk)
				ct.BodyOrder = bodyOrder
				if pfx := $2.(*createPrefix); pfx != nil {
					ct.Temporary = pfx.temporary
					ct.Unlogged = pfx.unlogged
				}
				ct.IfNotExists = $4
				ct.TableUniques = uqs
				ct.TableUniqueIncludes = uqIncludes
				ct.TableUniqueNullsNotDistinct = uqNullsNotDistinct
				ct.TableUniqueDeferrable = uqDeferrable
				ct.TableUniqueInitiallyDeferred = uqInitiallyDeferred
				ct.NamedConstraints = named
				ct.TableExclusions = exclusions
				ct.LikeTables = likes
				ct.TableNamedChecks = namedChecks
				ct.TableForeignKeys = fks
				ct.PrimaryKeyInclude = pkIncl
				ct.PrimaryKeyDeferrable = pkDeferrable
				ct.PrimaryKeyInitiallyDeferred = pkInitiallyDeferred
				ct.TableNotNullNames = nnNames
				ct.TableNotNullCols = nnCols
				ct.TableNotNullNoInherit = nnNoInherit
				ct.TableChecks = checks
				ct.TableHasNoInheritCheck = hasNoInheritCheck
				ct.TableCheckNoInherit = checkNoInherit
				ct.TableCheckNotEnforced = checkNotEnforced
				tail := $9
				ct.Tablespace = $10
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
				if tail.partOf.Name != "" {
					ct.PartitionOf = parser.NewPartitionOfClause(tail.partOf, tail.fromVals, tail.toVals, tail.inVals, tail.bDefault)
					parser.SetPartitionOfHashBound(ct.PartitionOf, tail.modulus, tail.remainder, tail.isHash)
					applyPartOfElems(ct.PartitionOf, tail.partOfElems)
				}
				ct.SelectSource = tail.asSelect
				$$ = ct
			}
	| CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name ct_tail_list opt_tablespace
			{
				nm := $5.parts
				tbl := parser.ObjectName{Name: nm[len(nm)-1]}
				if len(nm) > 1 {
					tbl.Schema = nm[len(nm)-2]
				}
				ct := parser.NewCreateTableStmt(0, tbl, nil, nil)
				ct.IfNotExists = $4
				if pfx := $2.(*createPrefix); pfx != nil {
					ct.Temporary = pfx.temporary
					ct.Unlogged = pfx.unlogged
				}
				tail := $6
				ct.Tablespace = $7
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
				if tail.partOf.Name != "" {
					ct.PartitionOf = parser.NewPartitionOfClause(tail.partOf, tail.fromVals, tail.toVals, tail.inVals, tail.bDefault)
					parser.SetPartitionOfHashBound(ct.PartitionOf, tail.modulus, tail.remainder, tail.isHash)
					applyPartOfElems(ct.PartitionOf, tail.partOfElems)
				}
				ct.SelectSource = tail.asSelect
				$$ = ct
			}

	/* Plain CTAS, `CREATE TABLE t [USING am] [WITH (...)] AS query`, lives HERE
	   over ct_tail_list rather than in create_table_stmt_as: its optional
	   USING / WITH prefix and the no-column CREATE's tail start in the same
	   state, and two separate rules for them reduce/reduce on WITH. */
	| CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name ct_tail_list AS ctas_source opt_ctas_with_data
			{
				src := $8.(*ctasSrc)
				ct := parser.NewCreateTableStmt(0, objectNameFromQn($5), nil, nil)
				if pfx := $2.(*createPrefix); pfx != nil {
					ct.Temporary = pfx.temporary
					ct.Unlogged = pfx.unlogged
				}
				ct.IfNotExists = $4
				ct.SelectSource, ct.ExecuteSource = src.sel, src.exec
				ct.WithNoData = $9
				$$ = ct
			}
	/* Typed table — `CREATE TABLE t OF type [( col WITH OPTIONS ... )]`
	   (gram.y :4020). The options are ColumnDefs with an EMPTY type. */
	| CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name OF qualified_name opt_of_col_opts
			{
				ct := parser.NewCreateTableStmt(0, objectNameFromQn($5), nil, nil)
				if pfx := $2.(*createPrefix); pfx != nil {
					ct.Temporary = pfx.temporary
					ct.Unlogged = pfx.unlogged
				}
				ct.IfNotExists = $4
				ot := objectNameFromQn($7)
				ct.OfType = &ot
				ct.OfTypeColumnOptions, _ = $8.([]parser.ColumnDef)
				$$ = ct
			}

part_bound_spec2:
		FOR VALUES IN_P '(' expr_list ')'
			{
				$$ = &partBound{inVals: $5}
			}
	| FOR VALUES FROM '(' expr_list ')' TO '(' expr_list ')'
			{
				$$ = &partBound{from: $5, to: $9}
			}
	/* MODULUS / REMAINDER are not kwlist keywords in this build, so they arrive
	   as plain IDENTs; the action validates the spelling. */
	| FOR VALUES WITH '(' IDENT ICONST ',' IDENT ICONST ')'
			{
				m, r := int64($6), int64($9)
				if !eqFold($5, "modulus") || !eqFold($8, "remainder") {
					lerr(yylex, "expected MODULUS m, REMAINDER r", yylex.(*lexerState).lastConsumedPos())
				}
				$$ = &partBound{modulus: m, remainder: r, isHash: true}
			}
	| DEFAULT
			{
				$$ = &partBound{isDefault: true}
			}

/* opt_create_modifier — TEMP/TEMPORARY/UNLOGGED between CREATE and TABLE. */
opt_create_modifier:
		/* empty */   { $$ = (*createPrefix)(nil) }
	| TEMP            { $$ = &createPrefix{temporary: true} }
	| TEMPORARY       { $$ = &createPrefix{temporary: true} }
	| UNLOGGED        { $$ = &createPrefix{unlogged: true} }

opt_if_not_exists:
		/* empty */       { $$ = false }
	| IF_P NOT EXISTS  { $$ = true } /* gram.y opt_if_not_exists: plain NOT */

/* opt_table_element_list — `CREATE TABLE c () INHERITS (p)` is legal and the
   isolation specs use it; the list used to be mandatory. */
opt_table_element_list:
		/* empty */                 { $$ = []*tableElem(nil) }
	| table_element_list            { $$ = $1 }

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
				// The typmod lives on the TYPE carrier, not the constraint
				// carrier: `col_type_name '(' ICONST ')'` stashes it in
				// tw.args. Reading it from cc dropped every column typmod
				// (char(22) -> char, numeric(10,2) -> numeric) while the two
				// sibling ALTER TABLE sites below (:541, :565) read tw.args
				// correctly — a classic sibling-path divergence. CREATE TABLE
				// is routed, so this silently created unconstrained columns.
				cs.args = tw.args
				cs.notNull, cs.primary, cs.unique, cs.defExpr =
					cc.notNull, cc.primary, cc.unique, cc.defExpr
				// create_table_stmt (:33-42) already consumes these two, but
				// nothing ever produced them: column-level CHECK and
				// REFERENCES parsed cleanly and were then silently dropped.
				cs.checkText, cs.fkInfo = cc.checkText, cc.fk
				cs.nullsNotDistinct = cc.nullsNotDistinct
				cs.deferrable, cs.initiallyDeferred = cc.deferrable, cc.initiallyDeferred
				cs.genExpr, cs.genAlways, cs.genVirtual = cc.genExpr, cc.genAlways, cc.genVirtual
				cs.identity, cs.identityAlways, cs.identitySeq = cc.identity, cc.identityAlways, cc.identitySeq
				cs.checkName, cs.nnName, cs.uqName = cc.checkName, cc.nnName, cc.uqName
				cs.collation, cs.compression = cc.collation, cc.compression
				cs.nnNoInherit, cs.checkNoInherit = cc.nnNoInherit, cc.checkNoInherit
				$$ = &tableElem{col: cs}
			}
	| PRIMARY KEY pk_cols opt_include opt_constr_attrs
			{ a, _ := $5.(*constrAttrs); $$ = &tableElem{pk: $3, pkIncl: $4, pkAttrs: a} }
	| UNIQUE opt_unique_nnd uq_cols opt_include opt_constr_attrs
			{ a, _ := $5.(*constrAttrs); $$ = &tableElem{uq: [][]string{$3}, uqNND: $2, uqIncl: $4, uqAttrs: a} }
	| CONSTRAINT ColId PRIMARY KEY pk_cols opt_include opt_constr_attrs
			{
				a, _ := $7.(*constrAttrs)
				$$ = &tableElem{pk: $5, namedPk: namedTableConstraint($2, $5, true, $6, false, a)}
			}
	| CONSTRAINT ColId UNIQUE opt_unique_nnd uq_cols opt_include opt_constr_attrs
			{
				a, _ := $7.(*constrAttrs)
				$$ = &tableElem{namedUq: namedTableConstraint($2, $5, false, $6, $4, a)}
			}
	/* LIKE source_table [INCLUDING|EXCLUDING option ...] — gram.y TableLikeClause.
	   Legacy keeps only the table NAME (CreateTableStmt.LikeTables) and
	   discards the option list, so the options are parsed and dropped here too.
	   LIKE is reserved, so this cannot collide with a column definition. */
	| LIKE qualified_name opt_like_options
			{
				n := objectNameFromQn($2)
				$$ = &tableElem{like: &n, likeOpts: $3}
			}

	/* EXCLUDE constraints — gram.y ExclusionConstraintElem. Anonymous ones go
	   to TableExclusions, named ones to NamedConstraints, both as a
	   TableConstraintDef with IsExclusion set. */
	| EXCLUDE opt_using_method '(' exclude_elem_list ')' opt_include opt_exclude_where opt_constr_attrs
			{
				a, _ := $8.(*constrAttrs)
				w, _ := $7.(parser.Expr) // opt_exclude_where boxes an untyped nil
				$$ = &tableElem{exclusion: newExclusionConstraint("", $2, $4.([]excludeElem), $6, w, a)}
			}
	| CONSTRAINT ColId EXCLUDE opt_using_method '(' exclude_elem_list ')' opt_include opt_exclude_where opt_constr_attrs
			{
				a, _ := $10.(*constrAttrs)
				w, _ := $9.(parser.Expr)
				$$ = &tableElem{namedUq: newExclusionConstraint($2, $4, $6.([]excludeElem), $8, w, a)}
			}
	/* Table-level NOT NULL (PG 18's TableConstraint `NOT NULL columnname
	   ConstraintAttributeSpec`, gram.y :4183). Distinct from the column
	   constraint of the same spelling: it names a column that appears
	   elsewhere in the element list, so it lands in the three parallel
	   TableNotNull* slices and contributes NOTHING to BodyOrder. NOT is
	   reserved and NULL_P cannot start a column definition, so the
	   element-start decision stays unambiguous. */
	| NOT NULL_P ColId opt_no_inherit
			{ $$ = &tableElem{notNull: &tableNotNull{col: $3, noInherit: $4}} }
	| CONSTRAINT ColId NOT NULL_P ColId opt_no_inherit
			{ $$ = &tableElem{notNull: &tableNotNull{name: $2, col: $5, noInherit: $6}} }
	| CONSTRAINT ColId check_body opt_no_inherit
			{ $$ = &tableElem{check: $3, checkName: $2, checkNoInh: $4} }
	/* Anonymous table-level CHECK and FOREIGN KEY (gram.y TableConstraint).
	   CHECK and FOREIGN are reserved keywords, so they cannot start the
	   `ColId col_type_name …` column alternative and the element-start decision
	   stays on distinct terminals. Only the plain forms are ported: MATCH FULL,
	   [NOT] DEFERRABLE [INITIALLY …], NOT VALID, [NOT] ENFORCED, ON DELETE SET
	   (cols) and NO INHERIT still fall to legacy — see TODO.md P4.1. */
	| check_body opt_no_inherit
			{ $$ = &tableElem{check: $1, checkNoInh: $2} }
	/* Legacy accepts NOT VALID only BEHIND NO INHERIT on a CREATE TABLE check
	   (and drops it); a bare `CHECK (...) NOT VALID` is a syntax error there. */
	| check_body NO INHERIT NOT VALID
			{ $$ = &tableElem{check: $1, checkNoInh: true} }
	| FOREIGN KEY '(' colid_list ')' REFERENCES qualified_name opt_ref_cols opt_fk_actions opt_constr_attrs
			{
				fk := &parser.TableForeignKeyDef{
					Columns:    $4,
					RefTable:   objectNameFromQn($7),
					RefColumns: $8,
					OnDelete:   $9.(*fkActs).del,
					OnUpdate:   $9.(*fkActs).up,
				}
				if a, _ := $10.(*constrAttrs); a != nil {
					fk.Deferrable, fk.InitiallyDeferred = a.deferrable, a.initiallyDeferred
				}
				$$ = &tableElem{fkDef: fk}
			}
	| CONSTRAINT ColId FOREIGN KEY '(' colid_list ')' REFERENCES qualified_name opt_ref_cols opt_fk_actions opt_constr_attrs
			{
				fk := &parser.TableForeignKeyDef{
					Name:       $2,
					Columns:    $6,
					RefTable:   objectNameFromQn($9),
					RefColumns: $10,
					OnDelete:   $11.(*fkActs).del,
					OnUpdate:   $11.(*fkActs).up,
				}
				if a, _ := $12.(*constrAttrs); a != nil {
					fk.Deferrable, fk.InitiallyDeferred = a.deferrable, a.initiallyDeferred
				}
				$$ = &tableElem{fkDef: fk}
			}

/* The LIKE options are not a separate AST field: legacy ENCODES them into the
   BodyOrder marker as `:+name` per INCLUDING, in source order, dropping every
   EXCLUDING, with INCLUDING ALL expanding to a fixed nine.
   ALL is reserved and therefore not a ColId; every other option name
   (DEFAULTS, CONSTRAINTS, INDEXES, STORAGE, COMMENTS, STATISTICS,
   COMPRESSION, IDENTITY, GENERATED) is unreserved. */
opt_like_options:
		/* empty */                    { $$ = "" }
	| opt_like_options like_option     { $$ = $1 + $2 }

like_option:
		INCLUDING ALL                  { $$ = likeAllOpts }
	| INCLUDING ColId                  { $$ = ":+" + lowerIdent($2) }
	| EXCLUDING ALL                    { $$ = "" }
	| EXCLUDING ColId                  { $$ = "" }

exclude_elem_list:
		exclude_elem                          { $$ = []excludeElem{$1.(excludeElem)} }
	| exclude_elem_list ',' exclude_elem      { $$ = append($1.([]excludeElem), $3.(excludeElem)) }

exclude_elem:
		ColId WITH exclude_op                 { $$ = excludeElem{col: $1, op: $3} }
	| '(' a_expr ')' WITH exclude_op          { $$ = parenExcludeElem(yylex, $<p>2, $<p>3, $5) }

/* The exclusion operator is a bare operator token: single-char comparisons are
   char terminals, everything else (&&, @>, ...) arrives as Op. */
exclude_op:
		Op              { $$ = $1 }
	| '='             { $$ = "=" }
	| '<'             { $$ = "<" }
	| '>'             { $$ = ">" }
	| LESS_EQUALS     { $$ = "<=" }
	| GREATER_EQUALS  { $$ = ">=" }
	| NOT_EQUALS      { $$ = "<>" }

opt_exclude_where:
		/* empty */        { $$ = (parser.Expr)(nil) }
	| WHERE '(' a_expr ')' { $$ = $3 }

pk_cols:
		'(' colid_list ')'   { $$ = $2 }

uq_cols:
		'(' colid_list ')'   { $$ = $2 }
	/* `UNIQUE (valid_at WITHOUT OVERLAPS)` — legacy's column-list loop takes
	   the two keywords as two more COLUMN NAMES. Reproduced as-is; the AST is
	   the contract. */
	| '(' colid_list WITHOUT OVERLAPS ')'   { $$ = append($2, "without", "overlaps") }

/* col_type_name — cast_typename plus optional typmod args; arrays ride
   cast_typename's own suffix and are re-detected in the action. */
col_type_name:
		cast_typename                            { $$ = &typeWithArgs{ct: $1, args: $1.args} }
	| col_type_name '(' ICONST ')'               { $1.(*typeWithArgs).args = []int64{int64($3)}; $$ = $1 }
	| col_type_name '(' ICONST ',' ICONST ')'    { $1.(*typeWithArgs).args = []int64{int64($3), int64($5)}; $$ = $1 }


/* drop_table_stmt / truncate_stmt — P4.4 (gram.y DropStmt / TruncateStmt
   subsets). DROP uses the two-keyword dispatch ("drop table"); TRUNCATE
   routes on its own leading keyword. ONLY-per-table and RESTART IDENTITY
   forms arrive with a later slice. */
create_table_stmt_as:
	/* CTAS with an explicit COLUMN-ALIAS list: `CREATE TABLE t (a, b) AS
	   SELECT ...`. These are aliases, not column definitions — they carry no
	   type — so they land in ColumnAliases, and the parenthesised
	   opt_table_element_list rule (:19) cannot take them. */
		CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name '(' colid_list ')' opt_table_am opt_ctas_with AS ctas_source opt_ctas_with_data
			{
				src := $12.(*ctasSrc)
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
				ct.ColumnAliases = $7
				ct.SelectSource, ct.ExecuteSource = src.sel, src.exec
				ct.WithNoData = $13
				$$ = ct
			}

/* CTAS's source: a query, or a PREPARED STATEMENT. gram.y keeps the latter in
   a separate CreateTableAsStmt-producing rule (ExecuteStmt with into set);
   goopg's AST carries both on CreateTableStmt, so one nonterminal serves both
   the plain and the column-alias spellings instead of doubling them. */
ctas_source:
		select_bare
			{
				sel, _ := $1.(*parser.SelectStmt)
				$$ = &ctasSrc{sel: sel}
			}
	| EXECUTE ColId opt_execute_params
			{
				$$ = &ctasSrc{exec: parser.NewExecuteStmt($<p>1, $2, $3)}
			}

opt_execute_params:
		/* empty */         { $$ = nil }
	| '(' expr_list ')'     { $$ = $2 }

/* CTAS's own `WITH (reloptions)` before AS — distinct from WITH [NO] DATA
   after the query. Legacy parses and keeps the pairs (the AST's With map is
   not part of the parity contract). */
opt_ctas_with:
		/* empty */                    { _ = 0 }
	| WITH '(' str_pair_list ')'       { _ = 0 }

/* Plain WITH [NO] DATA for CTAS. Deliberately NOT opt_with_data, whose
   with_data_kw carries a span-end side effect that exists only to pin a
   materialized view's RawDef; CreateTableStmt keeps no raw body. */
opt_ctas_with_data:
		/* empty */      { $$ = false }
	| WITH DATA_P        { $$ = false }
	| WITH NO DATA_P     { $$ = true }

partof_elem_list:
		partof_elem                        { $$ = []*partOfElem{$1.(*partOfElem)} }
	| partof_elem_list ',' partof_elem     { $$ = append($1.([]*partOfElem), $3.(*partOfElem)) }

partof_elem:
		CONSTRAINT ColId CHECK '(' a_expr ')'
			{
				// Legacy stores the check as a TOKEN JOIN, not a source span.
				$$ = &partOfElem{hasCheck: true, checkName: $2, checkExpr: tokenJoinLower(yylex, $<p>5, $<p>6)}
			}
	| CHECK '(' a_expr ')'
			{ $$ = &partOfElem{hasCheck: true} } /* anonymous: accepted and dropped, as legacy does */
	| ColId opt_with_options partof_col_opts
			{
				el := $3.(*partOfElem)
				el.col = $1
				$$ = el
			}

opt_with_options:
		/* empty */      { _ = 0 }
	| WITH OPTIONS       { _ = 0 }

partof_col_opts:
		/* empty */                                  { $$ = &partOfElem{} }
	| partof_col_opts NOT NULL_P                     { el := $1.(*partOfElem); el.notNull = true; $$ = el }
	| partof_col_opts UNIQUE                         { el := $1.(*partOfElem); el.unique = true; $$ = el }
	| partof_col_opts DEFAULT a_expr                 { el := $1.(*partOfElem); el.def, el.hasDef = $3, true; $$ = el }
	| partof_col_opts GENERATED ALWAYS AS '(' a_expr ')' STORED
			{
				el := $1.(*partOfElem)
				el.genExpr, el.hasGen = tokenJoinLower(yylex, $<p>6, $<p>7), true
				$$ = el
			}

opt_of_col_opts:
		/* empty */                    { $$ = []parser.ColumnDef(nil) }
	| '(' of_col_opt_list ')'          { $$ = $2 }

of_col_opt_list:
		of_col_opt                       { $$ = []parser.ColumnDef{$1.(parser.ColumnDef)} }
	| of_col_opt_list ',' of_col_opt     { $$ = append($1.([]parser.ColumnDef), $3.(parser.ColumnDef)) }

of_col_opt:
		ColId WITH OPTIONS col_constraints
			{
				cc := $4.(*colConstraints)
				cd := parser.NewColumnDef($1, parser.NewColumnType("", "", nil, false))
				cd.NotNull = cc.notNull
				cd.NotNullExplicit = cc.notNull
				cd.DefaultExpr = cc.defExpr
				$$ = *cd
			}

/* Trailing clauses legacy accepts on CREATE TABLE. USING <am> and WITHOUT
   OIDS are parsed and DROPPED there (no AST field); TABLESPACE is kept. */
opt_table_am:
		/* empty */   { _ = 0 }
	| USING ColId     { _ = 0 }

opt_tablespace:
		/* empty */         { $$ = "" }
	| TABLESPACE ColId      { $$ = $2 }

opt_idx_tablespace:
		/* empty */         { $$ = "" }
	| TABLESPACE ColId      { $$ = $2 }

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
		TRUNCATE opt_TRUNCATE_kw trunc_name_list opt_restart opt_drop_behavior
			{
				tt := $3.(*truncTargets)
				$$ = parser.NewTruncateStmt(0, tt.names, tt.only, dropBehavior($5), $4)
			}

/* TRUNCATE's relation list is NOT drop_name_list: each entry carries its own
   ONLY flag (gram.y relation_expr, reached through TruncateStmt's
   relation_expr_list). TruncateStmt.Only is parallel to .Names. */
trunc_name_list:
		trunc_name
			{
				$$ = $1
			}
	| trunc_name_list ',' trunc_name
			{
				a, b := $1.(*truncTargets), $3.(*truncTargets)
				a.names = append(a.names, b.names...)
				a.only = append(a.only, b.only...)
				$$ = a
			}

trunc_name:
		qualified_name
			{
				$$ = &truncTargets{names: []parser.ObjectName{objectNameFromQn($1)}, only: []bool{false}}
			}
	| ONLY qualified_name
			{
				$$ = &truncTargets{names: []parser.ObjectName{objectNameFromQn($2)}, only: []bool{true}}
			}

/* A partition may itself be partitioned: `... PARTITION OF p FOR VALUES ...
   PARTITION BY RANGE (c)`. gram.y hangs OptPartitionSpec off the same
   CreateStmt as the bound, and ctTail already carries both fields. */
opt_subpartition_by:
		/* empty */   { $$ = (*parser.PartitionByClause)(nil) }
	| PARTITION BY ColId '(' part_elem_list ')'
			{ $$ = partitionByFrom($3, $<p>3, $5.([]partKey)) }

/* Partition keys — gram.y part_elem. A key is a column name, a function call,
   or a parenthesised expression, each with an optional COLLATE and an optional
   operator class. The shape mirrors index_col (:739) deliberately, for the same
   reason: with a bare a_expr key, a trailing opclass ColId is indistinguishable
   from a continuation of the expression. Only colid_list was ported, so both
   expression keys and opclasses were hard 42601s, and MethodPos / KeyColPos —
   which M0134-0016b errposition reporting reads — were left at zero even for
   plain column keys.

   Positions come from $<p>N, NOT from lastConsumedPos(). The lexer stamps
   lval.p on every token (adapter.go:302) and goyacc's default action copies the
   whole symbol struct from $1, so p propagates up through ColId and
   qualified_name untouched and $<p>N is the exact offset of symbol N's first
   terminal. lastConsumedPos() is prevPos, which is only correct when the parser
   happened to read a lookahead before the reduce — here it did not, and both
   MethodPos and KeyColPos came out one token early. */
part_elem:
		name_or_call opt_index_collate opt_part_opclass
			{ $$ = newPartKey($1, $<p>1, $2, $3) }
	| '(' a_expr ')' opt_index_collate opt_part_opclass
			{ $$ = newPartKey($2, 0, $4, $5) }

part_elem_list:
		part_elem                       { $$ = []partKey{$1.(partKey)} }
	| part_elem_list ',' part_elem      { $$ = append($1.([]partKey), $3.(partKey)) }

/* IDENT rather than ColId, for the reason spelled out at opt_index_opclass. */
opt_part_opclass:
		/* empty */   { $$ = "" }
	| IDENT           { $$ = $1 }

opt_no_inherit:
		/* empty */   { $$ = false }
	| NO INHERIT      { $$ = true }

constraints_set_mode:
		DEFERRED   { $$ = true }
	| IMMEDIATE    { $$ = false }

constr_name_list:
		constr_name                        { $$ = []string{$1} }
	| constr_name_list ',' constr_name     { $$ = append($1, $3) }

constr_name:
		ColId              { $$ = $1 }
	| ColId '.' ColId      { $$ = $3 }

opt_TRUNCATE_kw:
		/* empty */  { _ = 0 }
	| TABLE          { _ = 0 }

opt_restart:
		/* empty */            { $$ = false }
	| RESTART IDENTITY_P       { $$ = true }
	| CONTINUE_P IDENTITY_P   { $$ = false }


/* opt_ct_tail — trailing options v2: flat keyword-distinct alternatives. */
/* ct_tail_list — CREATE TABLE's trailing clauses, COMPOSABLE: gram.y lets
   OptInherit, OptPartitionSpec, OptWith and the rest follow one another in any
   order, and the regress corpus writes `PARTITION BY LIST (a) WITH
   (fillfactor=100)` 65 times. The old single-clause tail could not. Items
   append (WITH, INHERITS) or replace (PARTITION ...). A PARTITION OF item no
   longer carries its own sub-PARTITION BY: the following list item is it. */
ct_tail_list:
		/* empty */                 { $$ = &ctTail{} }
	| ct_tail_list ct_tail_item     { $$ = mergeCtTail($1, $2) }

ct_tail_item:
		WITH '(' str_pair_list ')'
			{ i := &ctTail{}; i.withKv = $3; $$ = i }
	| INHERITS '(' drop_name_list ')'
			{ i := &ctTail{}; i.inherits = $3; $$ = i }
	| WITHOUT OIDS
			{ $$ = &ctTail{} }
	| USING ColId
			{ $$ = &ctTail{} } /* access method: parsed and dropped, as legacy */
	| PARTITION BY ColId '(' part_elem_list ')'
			{
				i := &ctTail{}; i.partition = partitionByFrom($3, $<p>3, $5.([]partKey)); $$ = i
			}
	| PARTITION OF qualified_name part_bound_spec2
			{
				nm := $3.parts
				par := parser.ObjectName{Name: nm[len(nm)-1]}
				if len(nm) > 1 {
					par.Schema = nm[len(nm)-2]
				}
				i := &ctTail{}
				i.partOf = par
				b := $4.(*partBound)
				i.fromVals, i.toVals, i.inVals, i.bDefault = b.from, b.to, b.inVals, b.isDefault
				i.modulus, i.remainder, i.isHash = b.modulus, b.remainder, b.isHash
				$$ = i
			}
	/* `PARTITION OF p ( elements ) bound` — gram.y's OptTypedTableElementList
	   on a partition. Legacy keeps the elements on the PartitionOfClause, not
	   as columns (ddl.go:3290-3410). A tail ITEM, not its own statement
	   alternative: as an alternative it shifts PARTITION before the no-column
	   CREATE's empty tail can reduce, and every plain `PARTITION OF p FOR
	   VALUES ...` then dies on FOR. */
	| PARTITION OF qualified_name '(' partof_elem_list ')' part_bound_spec2
			{
				i := &ctTail{}
				i.partOf = objectNameFromQn($3)
				b := $7.(*partBound)
				i.fromVals, i.toVals, i.inVals, i.bDefault = b.from, b.to, b.inVals, b.isDefault
				i.modulus, i.remainder, i.isHash = b.modulus, b.remainder, b.isHash
				i.partOfElems = $5.([]*partOfElem)
				$$ = i
			}

str_pair_list:
		str_pair                    { $$ = []string{$1} }
	| str_pair_list ',' str_pair    { $$ = append($1, $3) }

str_pair:
		ColId '=' with_value               { $$ = $1 + "=" + $3 }
	| ColId '.' ColId '=' with_value       { $$ = $1 + "." + $3 + "=" + $5 }   /* toast.autovacuum_enabled = off */
	| ColId                                { $$ = $1 }                           /* WITH (security_barrier) */

with_value:
		SCONST   { $$ = $1 }
	| ICONST     { $$ = yylex.(*lexerState).lastText }

	| FCONST     { $$ = yylex.(*lexerState).lastText }

	| TRUE_P     { $$ = "true" }
	| FALSE_P    { $$ = "false" }
	| ON         { $$ = "on" }
	| ColId      { $$ = $1 } /* covers off, heap, ... */

/* create_index_stmt / drop_index_stmt — P4.4 (gram.y IndexStmt / DropStmt
   subsets). v0: plain column keys (expressions, DESC/NULLS, opclasses and
   CONCURRENTLY arrive later); ColOrders/ColExprs filled with per-column
   defaults for legacy dump parity. */
create_index_stmt:
		CREATE opt_unique INDEX opt_concurrently opt_if_not_exists opt_index_name ON opt_ONLY_kw qualified_name opt_using_method '(' index_col_list ')' opt_include opt_index_with opt_idx_tablespace opt_index_where
			{
				nm := $9.parts
				tbl := parser.ObjectName{Name: nm[len(nm)-1]}
				if len(nm) > 1 {
					tbl.Schema = nm[len(nm)-2]
				}
				elems := $12.([]indexElem)
				cols := make([]string, len(elems))
				exprs := make([]parser.Expr, len(elems))
				orders := make([]parser.IndexColOrder, len(elems))
				withOpts := ""
				for i, e := range elems {
					cols[i], exprs[i], orders[i] = e.name, e.expr, e.order
					if withOpts == "" {
						withOpts = e.optsOpClass
					}
				}
				ix := parser.NewCreateIndexStmt(0, $2, $5, $6, tbl, $10, cols)
				ix.ColExprs = exprs
				ix.ColOrders = orders
				ix.OpClassWithOptions = withOpts
				ix.Concurrently = $4
				ix.OnOnly = $8
				ix.IncludeColumns = $14
				ix.Fillfactor = $15
				ix.Tablespace = $16
				if p, _ := $17.(parser.Expr); p != nil {
					ix.HasPredicate = true
					ix.Predicate = p
				}
				$$ = ix
			}

/* opt_index_with — only fillfactor reaches the AST, as in legacy. */
opt_index_with:
		/* empty */                   { $$ = 0 }
	| WITH '(' str_pair_list ')'      { $$ = fillfactorFrom($3) }

/* opt_index_name — `CREATE INDEX ON t (a)` lets the server pick the name. ON
   is reserved and therefore never a ColId, so the empty case is unambiguous. */
opt_index_name:
		/* empty */        { $$ = "" }
	| ColId                { $$ = $1 }

opt_index_where:
		/* empty */        { $$ = (parser.Expr)(nil) }
	| WHERE a_expr         { $$ = $2 }

opt_unique:
		/* empty */  { $$ = false }
	| UNIQUE        { $$ = true }

opt_using_method:
		/* empty */      { $$ = "" }
	| USING ColId        { $$ = $2 }

index_col_list:
		index_col                     { $$ = []indexElem{$1.(indexElem)} }
	| index_col_list ',' index_col   { $$ = append($1.([]indexElem), $3.(indexElem)) }

/* index_col — gram.y index_elem. A key may be a NAME, a bare function call or
   a parenthesised expression, each with an optional opclass / ASC|DESC /
   NULLS order. Parsing the key as an a_expr and classifying in Go avoids the
   ColId-vs-expression ambiguity that three explicit alternatives would carry. */
index_col:
		name_or_call opt_index_collate opt_index_opclass opt_index_dir opt_index_nulls
			{
				$$ = newIndexElem($1, $2, $3.(opClassRef), $4, $5.(*bool))
			}
	| '(' a_expr ')' opt_index_collate opt_index_opclass opt_index_dir opt_index_nulls
			{
				$$ = newIndexElem($2, $4, $5.(opClassRef), $6, $7.(*bool))
			}

/* COLLATE is handled here, not via `a_expr COLLATE ColId`: legacy records a
   collated key as Columns[i]=name with ColOrders[i].Collation set, not as a
   CollateExpr, and the key form below is name_or_call rather than a_expr
   precisely so a trailing opclass ColId cannot be mistaken for a continuation
   of an expression (VARYING / FILTER / YEAR ... all did). */
/* gen_storage — STORED | VIRTUAL. Also pins the generated expression's span
   end, because by the time the outer action runs the ')' is no longer the last
   consumed token. */
gen_storage:
		STORED    { yylex.(*lexerState).genSpanEnd = yylex.(*lexerState).prevPos; $$ = false }
	| VIRTUAL     { yylex.(*lexerState).genSpanEnd = yylex.(*lexerState).prevPos; $$ = true }

opt_index_collate:
		/* empty */   { $$ = "" }
	| COLLATE ColId   { $$ = $2 }

/* IDENT, not ColId: an opclass name is always a real identifier
   (float8_ops, text_pattern_ops), and ColId also admits FILTER and WITHIN,
   which are exactly the tokens that continue a function-call key
   (`f() FILTER (...)`, `f() WITHIN GROUP (...)`) — 4 shift/reduce conflicts. */
opt_index_opclass:
		/* empty */                        { $$ = opClassRef{} }
	| IDENT                                { $$ = opClassRef{name: $1} }
	/* `int4_ops(foo=1)` — the option list is accepted and discarded; legacy
	   records only that the opclass HAD options, in OpClassWithOptions. */
	| IDENT '(' opclass_opt_list ')'       { $$ = opClassRef{name: $1, withOptions: true} }

opclass_opt_list:
		opclass_opt                        { $$ = 0 }
	| opclass_opt_list ',' opclass_opt     { $$ = 0 }

opclass_opt:
		ColId '=' set_value_atom           { $$ = 0 }
	| ColId                                { $$ = 0 }

opt_index_dir:
		/* empty */   { $$ = false }
	| ASC             { $$ = false }
	| DESC            { $$ = true }

opt_index_nulls:
		/* empty */              { $$ = (*bool)(nil) }
	| NULLS_LA FIRST_P           { v := true; $$ = &v }
	| NULLS_LA LAST_P            { v := false; $$ = &v }

drop_index_stmt:
		DROP INDEX opt_concurrently opt_drop_if_exists drop_name_list opt_drop_behavior
			{
				$$ = parser.NewDropIndexStmt(0, $3, $4, $5, dropBehavior($6))
			}

opt_drop_if_exists:
		/* empty */    { $$ = false }
	| IF_P EXISTS      { $$ = true }

/* tx_stmts — P6.1 v0: bare BEGIN/START TRANSACTION/COMMIT/END/ROLLBACK/
   ABORT. Transaction modes (ISOLATION LEVEL, READ ONLY/WRITE, DEFERRABLE)
   arrive with the next slice. END is a reserved keyword; the rest route via
   routedStmts keys. */
tx_begin:
		BEGIN_P opt_transaction begin_pos opt_tx_modes
			{
				m := $4.(*txModes)
				b := parser.NewBeginStmt($3)
				b.IsolationLevel, b.ReadOnly, b.Deferrable = m.iso, m.ro, m.def
				$$ = b
			}
	| START TRANSACTION begin_pos opt_tx_modes
			{
				m := $4.(*txModes)
				b := parser.NewBeginStmt($3)
				b.IsolationLevel, b.ReadOnly, b.Deferrable = m.iso, m.ro, m.def
				$$ = b
			}

begin_pos:
		/* empty */   { $$ = yylex.(*lexerState).lastConsumedPos() }

opt_tx_modes:
		/* empty */      { $$ = &txModes{} }
	| tx_mode_list       { $$ = $1 }

tx_mode_list:
		tx_mode                  { $$ = $1 }
	| tx_mode_list ',' tx_mode   { $$ = mergeTxModes($1.(*txModes), $3.(*txModes)) } /* $$ = $1 dropped every mode after the first */
	/* gram.y's transaction_mode_list makes the comma OPTIONAL:
	   `BEGIN TRANSACTION ISOLATION LEVEL SERIALIZABLE READ ONLY DEFERRABLE`. */
	| tx_mode_list tx_mode       { $$ = mergeTxModes($1.(*txModes), $2.(*txModes)) }

tx_mode:
		ISOLATION LEVEL iso_level     { i := &txModes{}; i.iso = $3.(string); $$ = i }
	| READ ONLY                         { i := &txModes{}; i.ro = true; $$ = i }
	| READ WRITE                       { $$ = &txModes{} }
	| DEFERRABLE                       { i := &txModes{}; i.def = true; $$ = i }
	| NOT DEFERRABLE                   { $$ = &txModes{} } /* gram.y transaction_mode_item */

iso_level:
		SERIALIZABLE                    { $$ = "serializable" }
	| REPEATABLE READ                   { $$ = "repeatable read" }
	| READ COMMITTED                    { $$ = "read committed" }
	| READ UNCOMMITTED                  { $$ = "read uncommitted" }

tx_commit:
		COMMIT opt_transaction  { $$ = parser.NewCommitStmt(yylex.(*lexerState).lastConsumedPos()) }
	| END_P opt_transaction     { $$ = parser.NewCommitStmt(yylex.(*lexerState).lastConsumedPos()) }
	| COMMIT PREPARED SCONST    { $$ = parser.NewCommitPreparedStmt(yylex.(*lexerState).lastConsumedPos(), $3) }

tx_rollback:
		ROLLBACK opt_transaction { $$ = parser.NewRollbackStmt(yylex.(*lexerState).lastConsumedPos()) }
	| ABORT_P opt_transaction    { $$ = parser.NewRollbackStmt(yylex.(*lexerState).lastConsumedPos()) }
	| ROLLBACK PREPARED SCONST   { $$ = parser.NewRollbackPreparedStmt(yylex.(*lexerState).lastConsumedPos(), $3) }
	| ROLLBACK opt_transaction TO opt_savepoint_kw ColId
			{ $$ = parser.NewRollbackToSavepointStmt(yylex.(*lexerState).lastConsumedPos(), $5) }

/* opt_transaction — gram.y's `opt_transaction: WORK | TRANSACTION | empty`.
   TRANSACTION used to be a tx_mode instead, which made `BEGIN TRANSACTION
   ISOLATION LEVEL ...` unparseable: TRANSACTION was consumed as the first
   mode and the parser then wanted a comma before ISOLATION. 22 isolation
   spec steps. */
opt_transaction:
		/* empty */   { $$ = false }
	| WORK            { $$ = false }
	| TRANSACTION     { $$ = false }

opt_savepoint_kw:
		/* empty */   { $$ = false }
	| SAVEPOINT       { $$ = false }

/* set/show/reset — P6.2 (gram.y VariableSetStmt / VariableShowStmt /
   VariableResetStmt subsets). Value is the legacy token-atom join, NOT a
   source span: each value token's decoded text joined with ", " (see
   setValueAtoms / internal/parser/parser.go:3056), so quotes are stripped. DEFAULT and RESET/SHOW ALL are
   separate shapes. SET TIME ZONE / FROM CURRENT arrive later. */
set_stmt:
	/* SET CONSTRAINTS — gram.y's ConstraintsSetStmt, a sibling of
	   VariableSetStmt rather than one of its set_rest alternatives, so no
	   set_scope: `SET LOCAL CONSTRAINTS` is not accepted upstream and legacy
	   (parser.go:2496) does not take it either. A dotted name keeps only its
	   LAST component, matching parseQualifiedConstraintName (parser.go:3028),
	   which overwrites rather than appends. */
		SET CONSTRAINTS ALL constraints_set_mode
			{ $$ = parser.NewSetConstraintsStmt(0, true, nil, $4) }
	| SET CONSTRAINTS constr_name_list constraints_set_mode
			{ $$ = parser.NewSetConstraintsStmt(0, false, $3, $4) }
	| SET set_scope set_guc_name set_eq_to set_value_list
			{
				// One alternative, not two: `SET x = DEFAULT` differs from
				// `SET x = 'default'` only by token KIND, which the grammar
				// cannot see — a separate DEFAULT alternative would reduce/reduce
				// against the permissive value list below. setValueIsDefault
				// inspects the token instead.
				l := yylex.(*lexerState)
				$$ = parser.NewSetStmt(0, $2, $3, l.setValueAtoms(), l.setValueIsDefault())
			}

	/* SET [SESSION|LOCAL] AUTHORIZATION name|DEFAULT. SESSION is consumed by
	   set_scope, so only AUTHORIZATION remains here. Legacy records it as a
	   plain SetStmt named "session_authorization". */
	| SET set_scope AUTHORIZATION set_value_atom
			{ $$ = sessionAuthzStmt(yylex.(*lexerState), $2, $4) }
	/* `SET LOCAL SESSION AUTHORIZATION x` — LOCAL scope plus the SESSION that
	   is part of the GUC's own spelling. Legacy: SetStmt{Local, session_authorization}. */
	| SET LOCAL SESSION AUTHORIZATION set_value_atom
			{ $$ = sessionAuthzStmt(yylex.(*lexerState), true, $5) }

	/* SET [LOCAL] ROLE name — legacy records it as a plain SetStmt named
	   "role" with no '='/TO, so setValueAtoms (which scans for the separator)
	   cannot recover the value; it comes from the item directly. */
	| SET set_scope ROLE set_value_atom
			{ $$ = parser.NewSetStmt(0, $2, "role", $4, false) }

/* set_guc_name — GUCs may be dotted (`SET spec.session = 1`), which plain
   ColId could not express. */
set_guc_name:
		ColId                 { $$ = $1 }
	| ColId '.' ColId         { $$ = $1 + "." + $3 }

/* set_value_list — legacy's parseSetValueAtoms accepts ANY keyword or literal
   as a value atom (`SET debug_parallel_query = on`, `SET x = off`), so this
   deliberately does NOT reuse expr_list: ON and friends are reserved and can
   never be an a_expr. Only the parse shape matters here; the VALUE itself is
   rebuilt from the tokens by setValueAtoms. */
set_value_list:
		set_value_atom                        { $$ = $1 }
	| set_value_list ',' set_value_atom       { $$ = $1 }

set_value_atom:
		ColLabel        { $$ = $1 }
	/* This grammar's ColLabel omits reserved_keyword (upstream's includes it),
	   so the reserved words that show up as GUC VALUES are listed explicitly.
	   Widening ColLabel instead would mark every reserved keyword "reachable"
	   and blind TestReservedKeywordsReachable. */
	| ON            { $$ = "on" }
	| DEFAULT       { $$ = "default" }
	| TRUE_P        { $$ = "true" }
	| FALSE_P       { $$ = "false" }
	| SCONST            { $$ = $1 }
	| ICONST            { $$ = "" }
	| FCONST            { $$ = $1 }
	| '-' ICONST        { $$ = "" }
	| '-' FCONST        { $$ = $2 }

/* SET [SESSION|LOCAL] TRANSACTION <modes> — gram.y TransactionStmt. Reuses
   tx_mode_list, so ISOLATION LEVEL / READ ONLY|WRITE / [NOT] DEFERRABLE all
   parse; only the isolation level reaches the AST, as in legacy. */
set_transaction_stmt:
		SET set_scope TRANSACTION tx_mode_list
			{
				m := $4.(*txModes)
				$$ = parser.NewSetTransactionStmt(0, m.iso, $2)
			}

set_scope:
		/* empty */   { $$ = false }
	| SESSION          { $$ = false }
	| LOCAL            { $$ = true }

set_eq_to:
		'='           { $$ = "=" }
	| TO              { $$ = "to" }

show_stmt:
		SHOW ALL       { $$ = parser.NewShowStmt(0, true, "") }
	| SHOW ColId       { $$ = parser.NewShowStmt(0, false, $2) }

reset_stmt:
		RESET ALL      { $$ = parser.NewResetStmt(0, true, "") }
	| RESET ColId      { $$ = parser.NewResetStmt(0, false, $2) }
	/* RESET SESSION AUTHORIZATION — gram.y's dedicated VariableResetStmt
	   alternative. AUTHORIZATION is a reserved keyword, so `RESET ColId`
	   cannot reach it; legacy normalises the pair to the GUC's real name. */
	| RESET SESSION AUTHORIZATION
			{ $$ = parser.NewResetStmt(0, false, "session_authorization") }

/* alter_table_stmt — P4.2 v0 (gram.y AlterTableStmt subset): single action
   of ADD COLUMN / ADD PRIMARY KEY / DROP COLUMN / ALTER COLUMN TYPE /
   RENAME TO. Multi-action lists, DROP DEFAULT/NOT NULL, SET forms and
   partition actions arrive in later slices. */
alter_table_stmt:
		ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name alter_action_list
			{
				acts := $6.([]parser.AlterTableAction)
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.Only = $4
				st.Actions = acts
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name OWNER TO ColId
			{
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.Only = $4
				st.OwnerTo = $8
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name SET SCHEMA ColId
			{
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.Only = $4
				st.SetSchema = $8
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name SET LOGGED
			{
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.SetLogged = "logged"
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name SET UNLOGGED
			{
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.SetLogged = "unlogged"
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name SET '(' str_pair_list ')'
			{
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				a := parser.NewATAction(parser.AlterTableSetReloptions)
				m := map[string]string{}
				for _, kv := range $8 {
					parts := splitKV(kv)
					if len(parts) == 2 {
						m[parts[0]] = parts[1]
					}
				}
				a.With = m
				st.Actions = []parser.AlterTableAction{*a}
				$$ = st
			}

alter_action_list:
		alter_table_action
			{
				$$ = []parser.AlterTableAction{*($1.(*parser.AlterTableAction))}
			}
	| alter_action_list ',' alter_table_action
			{
				$$ = append($$.([]parser.AlterTableAction), *($3.(*parser.AlterTableAction)))
			}

opt_ONLY_kw:
		/* empty */  { $$ = false }
	| ONLY           { $$ = true }

alter_table_action:
		ADD_P opt_COLUMN ColId col_type_name col_constraints
			{
				cc := $5.(*colConstraints)
				ct := $4.(*typeWithArgs)
				cd := parser.NewColumnDef($3, parser.NewColumnType(ct.ct.schema, ct.ct.name, ct.args, false))
				cd.NotNull = cc.notNull
				cd.NotNullExplicit = cc.notNull // legacy parseColumnDef, ddl.go:5104
				cd.DefaultExpr = cc.defExpr
				// Identity was silently DROPPED here while CREATE TABLE kept
				// it — the classic sibling-path divergence, and `add column`
				// is routed.
				cd.IdentityColumn, cd.IdentityAlways = cc.identity, cc.identityAlways
				applyIdentityOpts(cd, cc.identitySeq)
				copyColConstraints(cd, cc.collation, cc.compression, cc.nnName, cc.uqName, cc.checkName, cc.nnNoInherit, cc.checkNoInherit)
				a := parser.NewATAction(parser.AlterTableAddColumn)
				a.Column = *cd
				$$ = a
			}
	/* `ADD PRIMARY KEY USING INDEX i` promotes an existing unique index. */
	| ADD_P PRIMARY KEY USING INDEX ColId
			{
				a := parser.NewATAction(parser.AlterTableAddPrimaryKey)
				a.UsingIndexName = $6
				$$ = a
			}
	| ADD_P PRIMARY KEY pk_cols opt_include
			{
				a := parser.NewATAction(parser.AlterTableAddPrimaryKey)
				a.Columns = $4
				a.IncludeColumns = $5
				$$ = a
			}
	| DROP COLUMN ColId
			{
				a := parser.NewATAction(parser.AlterTableDropColumn)
				a.ColumnName = $3
				$$ = a
			}
	| ALTER opt_COLUMN ColId TYPE_P col_type_name
			{
				ct := $5.(*typeWithArgs)
				a := parser.NewATAction(parser.AlterTableAlterColumnType)
				a.ColumnName = $3
				a.NewType = parser.NewColumnType(ct.ct.schema, ct.ct.name, ct.args, false)
				$$ = a
			}
	| RENAME TO ColId
			{
				a := parser.NewATAction(parser.AlterTableRenameTable)
				a.NewName = $3
				$$ = a
			}
	| DROP CONSTRAINT opt_if_exists_drop ColId opt_drop_behavior
			{
				// ConstraintName, NOT OldConstraintName — the latter is
				// RENAME CONSTRAINT's field and is the only one the executor
				// reads there (operators_ddl.go:10035), so this action used
				// to drop nothing. Restrict defaults to TRUE and is cleared
				// only by CASCADE (legacy ddl.go:9851-9862).
				a := parser.NewATAction(parser.AlterTableDropConstraint)
				a.ConstraintName = $4
				a.IfExists = $3
				a.Restrict = $5 != "cascade"
				$$ = a
			}
	| ALTER opt_COLUMN ColId SET DEFAULT a_expr
			{
				a := parser.NewATAction(parser.AlterTableSetDefault)
				a.ColumnName = $3
				a.DefaultExpr = $6
				$$ = a
			}
	| ALTER opt_COLUMN ColId DROP DEFAULT
			{
				a := parser.NewATAction(parser.AlterTableDropDefault)
				a.ColumnName = $3
				$$ = a
			}
	| ALTER opt_COLUMN ColId SET NOT NULL_P
			{
				a := parser.NewATAction(parser.AlterTableSetNotNull)
				a.ColumnName = $3
				$$ = a
			}
	| ALTER opt_COLUMN ColId DROP NOT NULL_P
			{
				a := parser.NewATAction(parser.AlterTableDropNotNull)
				a.ColumnName = $3
				$$ = a
			}
	| RENAME opt_COLUMN ColId TO ColId
			{
				a := parser.NewATAction(parser.AlterTableRenameColumn)
				a.OldColumnName = $3
				a.NewName = $5
				$$ = a
			}
	| VALIDATE CONSTRAINT ColId
			{
				// ConstraintName, not OldConstraintName (legacy ddl.go:9960).
				a := parser.NewATAction(parser.AlterTableValidateConstraint)
				a.ConstraintName = $3
				$$ = a
			}
	| REPLICA IDENTITY_P FULL
			{
				a := parser.NewATAction(parser.AlterTableReplicaIdentity)
				a.ReplicaIdentityMode = "f"
				$$ = a
			}
	| REPLICA IDENTITY_P NOTHING
			{
				a := parser.NewATAction(parser.AlterTableReplicaIdentity)
				a.ReplicaIdentityMode = "n"
				$$ = a
			}
	| REPLICA IDENTITY_P DEFAULT
			{
				a := parser.NewATAction(parser.AlterTableReplicaIdentity)
				a.ReplicaIdentityMode = "d"
				$$ = a
			}
	| REPLICA IDENTITY_P USING INDEX ColId
			{
				a := parser.NewATAction(parser.AlterTableReplicaIdentity)
				a.ReplicaIdentityMode = "i"
				a.ReplicaIdentityIndex = $5
				$$ = a
			}
	/* ATTACH PARTITION takes the SAME bound spec as CREATE TABLE ... PARTITION
	   OF, hash bounds included — three hand-spelled copies had left
	   `FOR VALUES WITH (MODULUS m, REMAINDER r)` unreachable here. */
	| ATTACH PARTITION qualified_name part_bound_spec2
			{
				b := $4.(*partBound)
				a := parser.NewATAttachPartition(0, objectNameFromQn($3), b.from, b.to, b.inVals, b.isDefault)
				parser.SetPartitionOfHashBound(a.AttachPartitionOf, b.modulus, b.remainder, b.isHash)
				$$ = a
			}
	| DETACH PARTITION qualified_name
			{
				$$ = parser.NewATDetachPartition(0, objectNameFromQn($3))
			}

opt_COLUMN:
		/* empty */  { _ = 0 }
	| COLUMN         { _ = 0 }

/* create_view_stmt — P5 v0: CREATE [OR REPLACE] [TEMP] VIEW name [(cols)] AS select */
create_view_stmt:
		CREATE opt_or_replace VIEW qualified_name opt_name_list_p opt_view_with AS { yylex.(*lexerState).markSpanStart() } select_bare
			{
				nm := $4.parts
				v := parser.ObjectName{Name: nm[len(nm)-1]}
				if len(nm) > 1 {
					v.Schema = nm[len(nm)-2]
				}
				sel := $9.(*parser.SelectStmt)
				cv := parser.NewCreateViewStmt(v, $5, sel)
				cv.OrReplace = $2
				viewReloptions(cv, $6)
				cv.RawDef = yylex.(*lexerState).spanTextUpTo(yylex.(*lexerState).fragEnd)
				$$ = cv
			}

opt_view_with:
		/* empty */                    { $$ = []string(nil) }
	| WITH '(' str_pair_list ')'       { $$ = $3 }

opt_or_replace:
		/* empty */  { $$ = false }
	| OR REPLACE    { $$ = true }

opt_name_list_p:
		/* empty */          { $$ = []string(nil) }
	| '(' colid_list ')'     { $$ = $2 }

/* drop_view_stmt — P5 v0: DROP VIEW [IF EXISTS] name [, …] [CASCADE|RESTRICT] */
drop_view_stmt:
		DROP VIEW opt_if_exists_drop drop_name_list opt_drop_behavior
			{
				$$ = parser.NewDropViewStmt(0, $3, $4, dropBehavior($5))
			}

/* create_matview_stmt — P5 v0: CREATE MATERIALIZED VIEW [IF NOT EXISTS] qn
   [(aliases)] AS select [WITH [NO] DATA]. USING/WITH (opts)/TABLESPACE
   deferred (legacy fallback covers them via the modifier-fallback rule). */
create_matview_stmt:
		CREATE MATERIALIZED VIEW opt_if_not_exists qualified_name opt_name_list_p opt_table_am AS { yylex.(*lexerState).markSpanStart() } select_bare opt_with_data
			{
				nm := $5.parts
				v := parser.ObjectName{Name: nm[len(nm)-1]}
				if len(nm) > 1 {
					v.Schema = nm[len(nm)-2]
				}
				sel := $10.(*parser.SelectStmt)
				cv := parser.NewCreateMatViewStmt(v, $6, sel)
				cv.IfNotExists = $4
				cv.RawDef = yylex.(*lexerState).spanTextUpTo(yylex.(*lexerState).spanEnd())
				cv.WithNoData = $11
				$$ = cv
			}

/* refresh_matview_stmt / drop_matview_stmt — P5.1 (gram.y RefreshMatViewStmt
   and the DropStmt MATERIALIZED VIEW arm).

   Legacy (internal/parser/ddl.go:2847) treats MATERIALIZED and VIEW as
   optional after REFRESH; we require both, which is upstream-correct —
   `REFRESH` leads no other statement in PostgreSQL.

   DROP MATERIALIZED VIEW does NOT produce a DropViewStmt: legacy
   (ddl.go:6329-6369) emits DropCompatStmt with the two-word ObjType
   "materialized view". Verified by probing the legacy parser before writing
   these actions. */
refresh_matview_stmt:
		REFRESH MATERIALIZED VIEW opt_concurrently qualified_name opt_with_data
			{
				st := parser.NewRefreshMatViewStmt(0, objectNameFromQn($5))
				st.Concurrently = $4
				st.WithNoData = $6
				$$ = st
			}

opt_concurrently:
		/* empty */    { $$ = false }
	| CONCURRENTLY     { $$ = true }

drop_matview_stmt:
		DROP MATERIALIZED VIEW opt_if_exists_drop drop_name_list opt_drop_behavior
			{
				$$ = parser.NewDropCompatStmt(0, "materialized view", $4, $5, dropBehavior($6))
			}

opt_with_data:
		/* empty */      { $$ = false }
	| with_data_kw DATA_P            { $$ = false }
	| with_data_kw NO DATA_P         { $$ = true }

/* with_data_kw shifts WITH then pins the span end to the END of the token
   preceding WITH (the select body's last token). */
with_data_kw:
		WITH
			{
				// The WITH token's own START is the body's exclusive end, and
				// unlike prevPos+len(prevText) it is quote-safe: prevText is the
				// DECODED token text, so a body ending in a literal ('x') cut
				// inside the quotes and produced RawDef="SELECT '".
				l := yylex.(*lexerState)
				l.endMark = l.lastPos
				_ = 0
			}

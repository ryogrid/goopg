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
						copyColConstraints(cd, c.collation, c.compression, c.nnName, c.uqName, c.checkName, c.nnNoInherit, c.checkNoInherit, c.checkNotEnforced, c.storage)
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
							cd.OnDeleteSetCols = c.fkInfo.delSetCols
							cd.FKMatchFull = c.fkInfo.matchFull
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
								checkNotEnforced = append(checkNotEnforced, e.checkNotEnf)
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
				ct.OnCommit = tail.onCommit
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
				ct.OnCommit = tail.onCommit
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
				// cast_typename folds the array brackets INTO the name
				// (castType.withArrays); ColumnType keeps them in a separate
				// flag with the ELEMENT name, so they must be split back out.
				// Detecting the suffix without stripping it left `text[]` as a
				// type literally named "text[]" on 36 regress fragments.
				ct := colTypeOf(tw)
				cs.schema, cs.typ, cs.isArray = ct.Schema, ct.Name, ct.IsArray
				cc := $3.(*colConstraints)
				// The typmod lives on the TYPE carrier, not the constraint
				// carrier: `col_type_name '(' ICONST ')'` stashes it in
				// tw.args. Reading it from cc dropped every column typmod
				// (char(22) -> char, numeric(10,2) -> numeric) while the two
				// sibling ALTER TABLE sites below (:541, :565) read tw.args
				// correctly — a classic sibling-path divergence. CREATE TABLE
				// is routed, so this silently created unconstrained columns.
				cs.args = ct.Args
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
				cs.checkNotEnforced, cs.storage = cc.checkNotEnforced, cc.storage
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
	| CONSTRAINT ColId check_body tbl_check_tail
			{ t := $4.(*tblCheckTail); $$ = &tableElem{check: $3, checkName: $2, checkNoInh: t.noInherit, checkNotEnf: t.notEnforced} }
	/* Anonymous table-level CHECK and FOREIGN KEY (gram.y TableConstraint).
	   CHECK and FOREIGN are reserved keywords, so they cannot start the
	   `ColId col_type_name …` column alternative and the element-start decision
	   stays on distinct terminals. Only the plain forms are ported: MATCH FULL,
	   [NOT] DEFERRABLE [INITIALLY …], NOT VALID, [NOT] ENFORCED, ON DELETE SET
	   (cols) and NO INHERIT still fall to legacy — see TODO.md P4.1. */
	| check_body tbl_check_tail
			{ t := $2.(*tblCheckTail); $$ = &tableElem{check: $1, checkNoInh: t.noInherit, checkNotEnf: t.notEnforced} }
	/* Legacy accepts NOT VALID only BEHIND NO INHERIT on a CREATE TABLE check
	   (and drops it); a bare `CHECK (...) NOT VALID` is a syntax error there. */
	| FOREIGN KEY '(' colid_list ')' REFERENCES qualified_name opt_ref_cols opt_fk_match opt_fk_actions opt_constr_attrs
			{
				fk := &parser.TableForeignKeyDef{
					Columns:         $4,
					RefTable:        objectNameFromQn($7),
					RefColumns:      $8,
					MatchFull:       $9,
					OnDelete:        $10.(*fkActs).del,
					OnUpdate:        $10.(*fkActs).up,
					OnDeleteSetCols: $10.(*fkActs).delSetCols,
				}
				if a, _ := $11.(*constrAttrs); a != nil {
					fk.Deferrable, fk.InitiallyDeferred = a.deferrable, a.initiallyDeferred
				}
				$$ = &tableElem{fkDef: fk}
			}
	| CONSTRAINT ColId FOREIGN KEY '(' colid_list ')' REFERENCES qualified_name opt_ref_cols opt_fk_match opt_fk_actions opt_constr_attrs
			{
				fk := &parser.TableForeignKeyDef{
					Name:            $2,
					Columns:         $6,
					RefTable:        objectNameFromQn($9),
					RefColumns:      $10,
					MatchFull:       $11,
					OnDelete:        $12.(*fkActs).del,
					OnUpdate:        $12.(*fkActs).up,
					OnDeleteSetCols: $12.(*fkActs).delSetCols,
				}
				if a, _ := $13.(*constrAttrs); a != nil {
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
		cast_typename
			{
				/* Bare char / character / nchar / national character default to
				   an implicit length of 1 (gram.y CharacterWithoutLength ->
				   bpchar typmod 1); ddl.go:5196 stamps it in the column-type
				   path. `bpchar` spelled directly stays unbounded. A following
				   typmod alternative simply overwrites Args. */
				ct := $1
				if len(ct.ivCol) > 0 {
					$$ = &typeWithArgs{ct: ct, args: ct.ivCol}
				} else {
					$$ = &typeWithArgs{ct: ct, args: implicitCharLen(yylex, $<p>1, ct)}
				}
			}
	/* `interval(3)` is not a plain typmod: legacy packs the precision together
	   with the full range mask (packIntervalColumnTypmod). */
	| col_type_name '(' ICONST ')' opt_array_tail               { tw := $1.(*typeWithArgs); tw.args = colTypmodArgs(tw, int64($3)); tw.ct = tw.ct.withArrays($5); $$ = tw }
	| col_type_name '(' ICONST ',' ICONST ')' opt_array_tail    { tw := $1.(*typeWithArgs); tw.args = []int64{int64($3), int64($5)}; tw.ct = tw.ct.withArrays($7); $$ = tw }
	/* The array brackets come AFTER the typmod (`char(10)[]`), which
	   cast_typename's own opt_array_tail cannot reach — it has already been
	   reduced by the time the typmod is seen. */
	| col_type_name '(' ICONST ',' ICONST ',' ICONST ')' opt_array_tail    { tw := $1.(*typeWithArgs); tw.args = []int64{int64($3), int64($5), int64($7)}; tw.ct = tw.ct.withArrays($9); $$ = tw }


/* drop_table_stmt / truncate_stmt — P4.4 (gram.y DropStmt / TruncateStmt
   subsets). DROP uses the two-keyword dispatch ("drop table"); TRUNCATE
   routes on its own leading keyword. ONLY-per-table and RESTART IDENTITY
   forms arrive with a later slice. */
create_table_stmt_as:
	/* CTAS with an explicit COLUMN-ALIAS list: `CREATE TABLE t (a, b) AS
	   SELECT ...`. These are aliases, not column definitions — they carry no
	   type — so they land in ColumnAliases, and the parenthesised
	   opt_table_element_list rule (:19) cannot take them. */
		CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name '(' colid_list ')' ct_tail_list AS ctas_source opt_ctas_with_data
			{
				src := $11.(*ctasSrc)
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
				ct.WithNoData = $12
				ct.OnCommit = $9.onCommit
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
		CONSTRAINT ColId CHECKBODY
			{
				// Legacy stores the check as a LOWER-CASED token join here (unlike
				// a column check's join, which keeps string quotes).
				$$ = &partOfElem{hasCheck: true, checkName: $2, checkExpr: tokenJoinLower(yylex, $<p>3+1, $3)}
			}
	| CHECKBODY
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
	| partof_col_opts DEFAULT a_expr %prec Op        { el := $1.(*partOfElem); el.def, el.hasDef = $3, true; $$ = el }
	| partof_col_opts GENERATED ALWAYS AS '(' a_expr ')' STORED
			{
				el := $1.(*partOfElem)
				el.genExpr, el.hasGen = tokenJoinLower(yylex, $<p>6, $<p>7), true
				$$ = el
			}
	/* Accepted and DROPPED by legacy on a partition's element list. */
	| partof_col_opts GENERATED ALWAYS AS '(' a_expr ')' VIRTUAL          { $$ = $1 }
	| partof_col_opts GENERATED ALWAYS AS '(' a_expr ')'                  { $$ = $1 }
	| partof_col_opts GENERATED ALWAYS AS IDENTITY_P opt_identity_seq_opts { $$ = $1 }
	| partof_col_opts COLLATE ColId                                       { $$ = $1 }
	| partof_col_opts PRIMARY KEY                                         { $$ = $1 }
	| partof_col_opts CHECKBODY                                           { $$ = $1 }

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

/* NULLS [NOT] DISTINCT on a unique index — gram.y opt_unique_null_treatment. */
opt_idx_nnd:
		/* empty */               { $$ = false }
	| NULLS_P DISTINCT            { $$ = false }
	| NULLS_P NOT DISTINCT        { $$ = true }

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

/* A table-level CHECK's trailer, FLAT: every spelling in one nonterminal so
   `NO INHERIT . NOT` shifts once for both NOT VALID and NOT ENFORCED instead
   of reducing an optional NO INHERIT first. Legacy accepts NOT VALID only
   behind NO INHERIT on CREATE TABLE, and drops it. */
tbl_check_tail:
		/* empty */                       { $$ = &tblCheckTail{} }
	| NO INHERIT                          { $$ = &tblCheckTail{noInherit: true} }
	| NO INHERIT NOT VALID                { $$ = &tblCheckTail{noInherit: true} }
	| NO INHERIT NOT ENFORCED             { $$ = &tblCheckTail{noInherit: true, notEnforced: true} }
	| NO INHERIT ENFORCED                 { $$ = &tblCheckTail{noInherit: true} }
	| NOT ENFORCED                        { $$ = &tblCheckTail{notEnforced: true} }
	| ENFORCED                            { $$ = &tblCheckTail{} }

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
	| ON COMMIT DELETE_P ROWS
			{ i := &ctTail{}; i.onCommit = "delete rows"; $$ = i }
	| ON COMMIT DROP
			{ i := &ctTail{}; i.onCommit = "drop"; $$ = i }
	| ON COMMIT PRESERVE ROWS
			{ i := &ctTail{}; i.onCommit = "preserve rows"; $$ = i }
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
	| '-' with_value { $$ = "-" + $2 } /* n_distinct = -0.5 */

/* create_index_stmt / drop_index_stmt — P4.4 (gram.y IndexStmt / DropStmt
   subsets). v0: plain column keys (expressions, DESC/NULLS, opclasses and
   CONCURRENTLY arrive later); ColOrders/ColExprs filled with per-column
   defaults for legacy dump parity. */
create_index_stmt:
		CREATE opt_unique INDEX opt_concurrently opt_if_not_exists opt_index_name ON opt_ONLY_kw qualified_name opt_using_method '(' index_col_list ')' opt_include opt_idx_nnd opt_index_with opt_idx_tablespace opt_index_where
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
				ix.NullsNotDistinct = $15
				applyIndexOpts(ix, $16)
				ix.Tablespace = $17
				if p, _ := $18.(parser.Expr); p != nil {
					ix.HasPredicate = true
					ix.Predicate = p
				}
				$$ = ix
			}

/* opt_index_with — the six storage parameters legacy records (ddl.go:5590ff:
   fillfactor, deduplicate_items, fastupdate, gin_pending_list_limit,
   pages_per_range, buffering, autosummarize); everything else is accepted and
   discarded, as in legacy. Recording only fillfactor here silently dropped the
   other five on 59 regress fragments. */
opt_index_with:
		/* empty */                   { $$ = (*indexOpts)(nil) }
	| WITH '(' str_pair_list ')'      { $$ = indexOptsFrom($3) }

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

	/* SET [LOCAL|SESSION] TIME ZONE v — gram.y's own set_rest alternative,
	   normalised by legacy to the GUC "timezone". TIME is a col_name_keyword
	   and therefore a ColId, so this cannot ride set_guc_name. */
	| SET set_scope TIME ZONE set_value_atom
			{ $$ = tzSetStmt($2, $5) }

/* set_guc_name — GUCs may be dotted (`SET spec.session = 1`), which plain
   ColId could not express. */
/* Left-recursive, not a fixed two-part form: extension GUCs may carry any
   number of dots (`SET custom.my.qualified.guc = 'foo'`). */
set_guc_name:
		ColId                     { $$ = $1 }
	| set_guc_name '.' ColId      { $$ = $1 + "." + $3 }

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
	/* The number, not "": the generic SET path rebuilds its value from the
	   token stream (setValueAtoms) and ignores this string, but SET TIME ZONE
	   and SET ROLE read the atom directly and `SET TIME ZONE 1` is legal. */
	| ICONST            { $$ = strconv.FormatInt(int64($1), 10) }
	| FCONST            { $$ = $1 }
	| '-' ICONST        { $$ = "-" + strconv.FormatInt(int64($2), 10) }
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
	| SHOW set_guc_name { $$ = parser.NewShowStmt(0, false, $2) }
	| SHOW TIME ZONE   { $$ = parser.NewShowStmt(0, false, "timezone") }

reset_stmt:
		RESET ALL      { $$ = parser.NewResetStmt(0, true, "") }
	| RESET set_guc_name { $$ = parser.NewResetStmt(0, false, $2) }
	| RESET TIME ZONE    { $$ = parser.NewResetStmt(0, false, "timezone") }
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
		ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star alter_action_list
			{
				acts := $7.([]parser.AlterTableAction)
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.Only = $4
				st.Actions = acts
				$$ = st
			}
	/* ENABLE / DISABLE [ALWAYS|REPLICA] TRIGGER — legacy records only a
	   statement-level flag, with no action and no trigger name. */
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star trigger_toggle
			{
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.Only = $4
				st.EnableDisableTrigger = true
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star OWNER TO ColId
			{
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.Only = $4
				st.OwnerTo = $9
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star SET SCHEMA ColId
			{
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.Only = $4
				st.SetSchema = $9
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star SET LOGGED
			{
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.SetLogged = "logged"
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star SET UNLOGGED
			{
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.SetLogged = "unlogged"
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star SET '(' str_pair_list ')'
			{
				st := parser.NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				a := parser.NewATAction(parser.AlterTableSetReloptions)
				m := map[string]string{}
				for _, kv := range $9 {
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
	/* The column form is spelled FOUR ways rather than with opt_COLUMN /
	   opt_if_not_exists, so no EMPTY nonterminal reduces right after ADD: with
	   one, `ADD EXCLUDE` had to choose between the constraint keyword and a
	   column named "exclude" a token too early (1 shift/reduce). Shifting the
	   name and deciding on the NEXT token keeps both spellings. */
		ADD_P ColId col_type_name col_constraints
			{
				cc := $4.(*colConstraints)
				ct := $3.(*typeWithArgs)
				cd := parser.NewColumnDef($2, colTypeOf(ct))
				cd.NotNull = cc.notNull
				cd.NotNullExplicit = cc.notNull // legacy parseColumnDef, ddl.go:5104
				cd.DefaultExpr = cc.defExpr
				// CHECK and REFERENCES were dropped here as well — the same
				// sibling-path divergence as identity below.
				cd.CheckExpr = cc.checkText
				if cc.fk != nil {
					cd.RefTable = cc.fk.refTable
					cd.RefColumns = cc.fk.refCols
					cd.OnDelete = cc.fk.onDel
					cd.OnUpdate = cc.fk.onUp
					cd.OnDeleteSetCols = cc.fk.delSetCols
					cd.FKMatchFull = cc.fk.matchFull
				}
				// Identity was silently DROPPED here while CREATE TABLE kept
				// it — the classic sibling-path divergence, and `add column`
				// is routed.
				cd.IdentityColumn, cd.IdentityAlways = cc.identity, cc.identityAlways
				applyIdentityOpts(cd, cc.identitySeq)
				copyColConstraints(cd, cc.collation, cc.compression, cc.nnName, cc.uqName, cc.checkName, cc.nnNoInherit, cc.checkNoInherit, cc.checkNotEnforced, cc.storage)
				a := parser.NewATAction(parser.AlterTableAddColumn)
				a.Column = *cd
				$$ = a
			}
	/* ADD [CONSTRAINT name] <table constraint> — gram.y AlterTableCmd's
	   `ADD_P TableConstraint`. One at_constraint nonterminal serves the named
	   and unnamed spellings; legacy keeps the name in ConstraintName for every
	   kind. Spelled as two ADD alternatives rather than an optional name so no
	   EMPTY nonterminal has to reduce right after ADD, which would collide
	   with the column form's own leading ColId. */
	| ADD_P COLUMN ColId col_type_name col_constraints
			{
				cc := $5.(*colConstraints)
				ct := $4.(*typeWithArgs)
				cd := parser.NewColumnDef($3, colTypeOf(ct))
				cd.NotNull = cc.notNull
				cd.NotNullExplicit = cc.notNull // legacy parseColumnDef, ddl.go:5104
				cd.DefaultExpr = cc.defExpr
				// CHECK and REFERENCES were dropped here as well — the same
				// sibling-path divergence as identity below.
				cd.CheckExpr = cc.checkText
				if cc.fk != nil {
					cd.RefTable = cc.fk.refTable
					cd.RefColumns = cc.fk.refCols
					cd.OnDelete = cc.fk.onDel
					cd.OnUpdate = cc.fk.onUp
					cd.OnDeleteSetCols = cc.fk.delSetCols
					cd.FKMatchFull = cc.fk.matchFull
				}
				// Identity was silently DROPPED here while CREATE TABLE kept
				// it — the classic sibling-path divergence, and `add column`
				// is routed.
				cd.IdentityColumn, cd.IdentityAlways = cc.identity, cc.identityAlways
				applyIdentityOpts(cd, cc.identitySeq)
				copyColConstraints(cd, cc.collation, cc.compression, cc.nnName, cc.uqName, cc.checkName, cc.nnNoInherit, cc.checkNoInherit, cc.checkNotEnforced, cc.storage)
				a := parser.NewATAction(parser.AlterTableAddColumn)
				a.Column = *cd
				$$ = a
			}
	/* ADD [CONSTRAINT name] <table constraint> — gram.y AlterTableCmd's
	   `ADD_P TableConstraint`. One at_constraint nonterminal serves the named
	   and unnamed spellings; legacy keeps the name in ConstraintName for every
	   kind. Spelled as two ADD alternatives rather than an optional name so no
	   EMPTY nonterminal has to reduce right after ADD, which would collide
	   with the column form's own leading ColId. */
	| ADD_P IF_P NOT EXISTS ColId col_type_name col_constraints
			{
				cc := $7.(*colConstraints)
				ct := $6.(*typeWithArgs)
				cd := parser.NewColumnDef($5, colTypeOf(ct))
				cd.NotNull = cc.notNull
				cd.NotNullExplicit = cc.notNull // legacy parseColumnDef, ddl.go:5104
				cd.DefaultExpr = cc.defExpr
				// CHECK and REFERENCES were dropped here as well — the same
				// sibling-path divergence as identity below.
				cd.CheckExpr = cc.checkText
				if cc.fk != nil {
					cd.RefTable = cc.fk.refTable
					cd.RefColumns = cc.fk.refCols
					cd.OnDelete = cc.fk.onDel
					cd.OnUpdate = cc.fk.onUp
					cd.OnDeleteSetCols = cc.fk.delSetCols
					cd.FKMatchFull = cc.fk.matchFull
				}
				// Identity was silently DROPPED here while CREATE TABLE kept
				// it — the classic sibling-path divergence, and `add column`
				// is routed.
				cd.IdentityColumn, cd.IdentityAlways = cc.identity, cc.identityAlways
				applyIdentityOpts(cd, cc.identitySeq)
				copyColConstraints(cd, cc.collation, cc.compression, cc.nnName, cc.uqName, cc.checkName, cc.nnNoInherit, cc.checkNoInherit, cc.checkNotEnforced, cc.storage)
				a := parser.NewATAction(parser.AlterTableAddColumn)
				a.IfExists = true
				a.Column = *cd
				$$ = a
			}
	/* ADD [CONSTRAINT name] <table constraint> — gram.y AlterTableCmd's
	   `ADD_P TableConstraint`. One at_constraint nonterminal serves the named
	   and unnamed spellings; legacy keeps the name in ConstraintName for every
	   kind. Spelled as two ADD alternatives rather than an optional name so no
	   EMPTY nonterminal has to reduce right after ADD, which would collide
	   with the column form's own leading ColId. */
	| ADD_P COLUMN IF_P NOT EXISTS ColId col_type_name col_constraints
			{
				cc := $8.(*colConstraints)
				ct := $7.(*typeWithArgs)
				cd := parser.NewColumnDef($6, colTypeOf(ct))
				cd.NotNull = cc.notNull
				cd.NotNullExplicit = cc.notNull // legacy parseColumnDef, ddl.go:5104
				cd.DefaultExpr = cc.defExpr
				// CHECK and REFERENCES were dropped here as well — the same
				// sibling-path divergence as identity below.
				cd.CheckExpr = cc.checkText
				if cc.fk != nil {
					cd.RefTable = cc.fk.refTable
					cd.RefColumns = cc.fk.refCols
					cd.OnDelete = cc.fk.onDel
					cd.OnUpdate = cc.fk.onUp
					cd.OnDeleteSetCols = cc.fk.delSetCols
					cd.FKMatchFull = cc.fk.matchFull
				}
				// Identity was silently DROPPED here while CREATE TABLE kept
				// it — the classic sibling-path divergence, and `add column`
				// is routed.
				cd.IdentityColumn, cd.IdentityAlways = cc.identity, cc.identityAlways
				applyIdentityOpts(cd, cc.identitySeq)
				copyColConstraints(cd, cc.collation, cc.compression, cc.nnName, cc.uqName, cc.checkName, cc.nnNoInherit, cc.checkNoInherit, cc.checkNotEnforced, cc.storage)
				a := parser.NewATAction(parser.AlterTableAddColumn)
				a.IfExists = true
				a.Column = *cd
				$$ = a
			}
	/* ADD [CONSTRAINT name] <table constraint> — gram.y AlterTableCmd's
	   `ADD_P TableConstraint`. One at_constraint nonterminal serves the named
	   and unnamed spellings; legacy keeps the name in ConstraintName for every
	   kind. Spelled as two ADD alternatives rather than an optional name so no
	   EMPTY nonterminal has to reduce right after ADD, which would collide
	   with the column form's own leading ColId. */
	| ADD_P at_constraint
			{ $$ = $2 }
	| ADD_P CONSTRAINT ColId at_constraint
			{
				a := $4.(*parser.AlterTableAction)
				a.ConstraintName = $3
				$$ = a
			}
	| DROP opt_COLUMN ColId opt_drop_behavior
			{
				a := parser.NewATAction(parser.AlterTableDropColumn)
				a.ColumnName = $3 // CASCADE / RESTRICT: parsed and dropped, as legacy
				$$ = a
			}
	| DROP opt_COLUMN IF_P EXISTS ColId opt_drop_behavior
			{
				a := parser.NewATAction(parser.AlterTableDropColumn)
				a.ColumnName = $5
				a.IfExists = true
				$$ = a
			}
	| ALTER opt_COLUMN ColId TYPE_P col_type_name opt_at_using
			{
				ct := $5.(*typeWithArgs)
				a := parser.NewATAction(parser.AlterTableAlterColumnType)
				a.ColumnName = $3
				a.NewType = colTypeOf(ct)
				a.UsingExpr, _ = $6.(parser.Expr)
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
	/* ALTER [COLUMN] name <sub-action> — the remaining gram.y
	   AlterTableCmd column forms. DROP EXPRESSION / DROP IDENTITY / SET
	   GENERATED / ADD GENERATED are accepted and recorded as NoOp, which is
	   what legacy does with them. */
	| ALTER opt_COLUMN ColId SET STATISTICS signed_iconst
			{
				a := parser.NewATAction(parser.AlterTableSetStatistics)
				a.ColumnName = $3
				a.CheckExpr = atStatValue($6) // legacy stores the number as TEXT
				$$ = a
			}
	| ALTER opt_COLUMN ColId SET STORAGE ColId
			{
				a := parser.NewATAction(parser.AlterTableSetStorage)
				a.ColumnName, a.StorageType = $3, $6
				$$ = a
			}
	| ALTER opt_COLUMN ColId SET COMPRESSION ColId
			{
				a := parser.NewATAction(parser.AlterTableSetCompression)
				a.ColumnName, a.CompressionType = $3, $6
				$$ = a
			}
	/* `SET COMPRESSION default` — legacy records an EMPTY method for it. */
	| ALTER opt_COLUMN ColId SET COMPRESSION DEFAULT
			{
				a := parser.NewATAction(parser.AlterTableSetCompression)
				a.ColumnName = $3
				$$ = a
			}
	| ALTER opt_COLUMN ColId SET '(' str_pair_list ')'
			{
				a := parser.NewATAction(parser.AlterTableAlterColumnSet)
				a.ColumnName, a.SetOptions = $3, $6
				$$ = a
			}
	| ALTER opt_COLUMN ColId RESET '(' str_pair_list ')'
			{
				a := parser.NewATAction(parser.AlterTableAlterColumnReset)
				a.ColumnName, a.SetOptions = $3, $6
				$$ = a
			}
	| ALTER opt_COLUMN ColId DROP EXPRESSION opt_if_exists_drop
			{ $$ = parser.NewATAction(parser.AlterTableNoOp) }
	/* SET EXPRESSION / SET DATA TYPE / the identity-sequence tweaks are all
	   NoOp in legacy — note SET DATA TYPE differs from plain TYPE, which does
	   record AlterColumnType. Reproduced as legacy has it. */
	| ALTER opt_COLUMN ColId SET EXPRESSION AS '(' a_expr ')'
			{ $$ = parser.NewATAction(parser.AlterTableNoOp) }
	| ALTER opt_COLUMN ColId SET DATA_P TYPE_P col_type_name opt_at_using
			{ $$ = parser.NewATAction(parser.AlterTableNoOp) }
	| ALTER opt_COLUMN ColId RESTART opt_restart_with
			{ $$ = parser.NewATAction(parser.AlterTableNoOp) }
	| ALTER opt_COLUMN ColId SET identity_seq_opt
			{ $$ = parser.NewATAction(parser.AlterTableNoOp) }
	| ALTER opt_COLUMN ColId SET GENERATED ALWAYS seq_tweaks
			{ $$ = parser.NewATAction(parser.AlterTableNoOp) }
	| ALTER opt_COLUMN ColId SET GENERATED BY DEFAULT seq_tweaks
			{ $$ = parser.NewATAction(parser.AlterTableNoOp) }
	| ALTER opt_COLUMN ColId DROP IDENTITY_P opt_if_exists_drop
			{ $$ = parser.NewATAction(parser.AlterTableNoOp) }
	| ALTER opt_COLUMN ColId ADD_P GENERATED ALWAYS AS IDENTITY_P opt_identity_seq_opts
			{ $$ = parser.NewATAction(parser.AlterTableNoOp) }
	| ALTER opt_COLUMN ColId ADD_P GENERATED BY DEFAULT AS IDENTITY_P opt_identity_seq_opts
			{ $$ = parser.NewATAction(parser.AlterTableNoOp) }
	/* ALTER CONSTRAINT name [NOT] DEFERRABLE [INITIALLY ...] — legacy records
	   the three flags plus a marker that a deferrability clause was written. */
	| ALTER CONSTRAINT ColId at_constr_tail
			{
				a := parser.NewATAction(parser.AlterTableAlterConstraint)
				a.ConstraintName = $3
				$$ = applyAlterConstraint(a, mustATTail($4))
			}
	/* Table-level SET / RESET / CLUSTER. RESET's option names are DROPPED by
	   legacy at table level, though the per-column RESET above keeps them. */
	| RESET '(' str_pair_list ')'
			{ $$ = parser.NewATAction(parser.AlterTableResetReloptions) }
	| SET ACCESS METHOD ColId
			{
				a := parser.NewATAction(parser.AlterTableSetAccessMethod)
				a.AccessMethodName = $4
				$$ = a
			}
	| SET TABLESPACE ColId
			{
				a := parser.NewATAction(parser.AlterTableSetTablespace)
				a.TablespaceName = $3
				$$ = a
			}
	| SET WITHOUT OIDS
			{ $$ = parser.NewATAction(parser.AlterTableNoOp) }
	| SET WITHOUT CLUSTER
			{ $$ = parser.NewATAction(parser.AlterTableSetWithoutCluster) }
	| CLUSTER ON ColId
			{
				a := parser.NewATAction(parser.AlterTableClusterOn)
				a.ClusterIndexName = $3
				$$ = a
			}
	| RENAME CONSTRAINT ColId TO ColId
			{
				a := parser.NewATAction(parser.AlterTableRenameConstraint)
				a.OldConstraintName, a.NewName = $3, $5
				$$ = a
			}
	| INHERIT qualified_name
			{
				a := parser.NewATAction(parser.AlterTableInherit)
				a.InheritParent = objectNameFromQn($2)
				$$ = a
			}
	| NO INHERIT qualified_name
			{
				a := parser.NewATAction(parser.AlterTableNoInherit)
				a.InheritParent = objectNameFromQn($3)
				$$ = a
			}
	| OF qualified_name
			{
				a := parser.NewATAction(parser.AlterTableAddOf)
				a.OfType = objectNameFromQn($2)
				$$ = a
			}
	| NOT OF
			{ $$ = parser.NewATAction(parser.AlterTableDropOf) }
	| ENABLE_P rule_state RULE ColId
			{
				a := parser.NewATAction(parser.AlterTableEnableDisableRule)
				a.RuleName, a.RuleEnabledState = $4, byte($2)
				$$ = a
			}
	| DISABLE_P RULE ColId
			{
				a := parser.NewATAction(parser.AlterTableEnableDisableRule)
				a.RuleName, a.RuleEnabledState = $3, byte('D')
				$$ = a
			}
	| ENABLE_P ROW LEVEL SECURITY
			{ $$ = parser.NewATAction(parser.AlterTableEnableRowSecurity) }
	| DISABLE_P ROW LEVEL SECURITY
			{ $$ = parser.NewATAction(parser.AlterTableDisableRowSecurity) }
	| FORCE ROW LEVEL SECURITY
			{ $$ = parser.NewATAction(parser.AlterTableForceRowSecurity) }
	| NO FORCE ROW LEVEL SECURITY
			{ $$ = parser.NewATAction(parser.AlterTableNoForceRowSecurity) }
	| ATTACH PARTITION qualified_name part_bound_spec2
			{
				b := $4.(*partBound)
				a := parser.NewATAttachPartition(0, objectNameFromQn($3), b.from, b.to, b.inVals, b.isDefault)
				parser.SetPartitionOfHashBound(a.AttachPartitionOf, b.modulus, b.remainder, b.isHash)
				$$ = a
			}
	| DETACH PARTITION qualified_name opt_detach_tail
			{
				a := parser.NewATDetachPartition(0, objectNameFromQn($3))
				a.DetachConcurrently = $4
				$$ = a
			}

/* One table constraint as ALTER TABLE ADD takes it. Every alternative ends in
   at_constr_tail, the FLAT trailing-word list (see atConstrTail). */
at_constraint:
		PRIMARY KEY pk_cols opt_include at_constr_tail
			{
				a := parser.NewATAction(parser.AlterTableAddPrimaryKey)
				a.Columns, a.IncludeColumns = $3, $4
				$$ = applyATTail(a, mustATTail($5))
			}
	| PRIMARY KEY USING INDEX ColId at_constr_tail
			{
				a := parser.NewATAction(parser.AlterTableAddPrimaryKey)
				a.UsingIndexName = $5
				$$ = applyATTail(a, mustATTail($6))
			}
	| UNIQUE uq_cols opt_include at_constr_tail
			{
				a := parser.NewATAction(parser.AlterTableAddUnique)
				a.Columns, a.IncludeColumns = $2, $3
				$$ = applyATTail(a, mustATTail($4))
			}
	| UNIQUE USING INDEX ColId at_constr_tail
			{
				a := parser.NewATAction(parser.AlterTableAddUnique)
				a.UsingIndexName = $4
				$$ = applyATTail(a, mustATTail($5))
			}
	| check_body at_constr_tail
			{
				a := parser.NewATAction(parser.AlterTableAddCheck)
				a.CheckExpr = $1
				$$ = applyATCheckTail(a, mustATTail($2))
			}
	| FOREIGN KEY '(' colid_list ')' REFERENCES qualified_name opt_ref_cols opt_fk_match opt_fk_actions at_constr_tail
			{
				a := parser.NewATAction(parser.AlterTableAddForeignKey)
				a.Columns = $4
				a.RefTable, a.RefColumns = objectNameFromQn($7), $8
				a.MatchFull = $9
				acts := $10.(*fkActs)
				a.OnDelete, a.OnUpdate, a.OnDeleteSetCols = acts.del, acts.up, acts.delSetCols
				$$ = applyATTail(a, mustATTail($11))
			}
	| EXCLUDE opt_using_method '(' exclude_elem_list ')' opt_include opt_exclude_where at_constr_tail
			{
				a := parser.NewATAction(parser.AlterTableAddExclude)
				d := newExclusionConstraint("", $2, $4.([]excludeElem), $6, nil, nil)
				a.Columns, a.ExclusionOp, a.ExclusionMethod = d.Columns, d.ExclusionOp, d.Method
				a.ExclusionWhere, _ = $7.(parser.Expr)
				$$ = applyATTail(a, mustATTail($8))
			}
	| NOT NULL_P ColId at_constr_tail
			{
				a := parser.NewATAction(parser.AlterTableAddNotNull)
				a.ColumnName = $3
				$$ = applyATTail(a, mustATTail($4))
			}

/* `USING expr` on a column type change. */
/* DETACH PARTITION's tail — CONCURRENTLY is recorded, FINALIZE is not (legacy
   accepts and drops it). */
/* `ALTER TABLE e_star* ...` — the inheritance star, consumed and recorded
   nowhere, exactly as in a FROM clause. */
opt_inh_star:
		/* empty */   { _ = 0 }
	| '*'             { _ = 0 }

trigger_toggle:
		ENABLE_P opt_trigger_mode TRIGGER trigger_target       { _ = 0 }
	| DISABLE_P TRIGGER trigger_target                        { _ = 0 }

opt_trigger_mode:
		/* empty */   { _ = 0 }
	| ALWAYS          { _ = 0 }
	| REPLICA         { _ = 0 }

trigger_target:
		ColId         { _ = 0 }
	| ALL             { _ = 0 }
	| USER            { _ = 0 }

/* Identity tweaks that may follow SET GENERATED, and a bare RESTART. */
/* relrewrite state codes, as pg_rewrite stores them. */
rule_state:
		/* empty */   { $$ = int('O') }
	| ALWAYS          { $$ = int('A') }
	| REPLICA         { $$ = int('R') }

seq_tweaks:
		/* empty */                   { _ = 0 }
	| seq_tweaks SET identity_seq_opt { _ = 0 }
	| seq_tweaks identity_seq_opt     { _ = 0 }
	| seq_tweaks RESTART opt_restart_with { _ = 0 }

opt_restart_with:
		/* empty */          { _ = 0 }
	| WITH signed_iconst     { _ = 0 }
	| signed_iconst          { _ = 0 }

opt_detach_tail:
		/* empty */     { $$ = false }
	| CONCURRENTLY      { $$ = true }
	| FINALIZE          { $$ = false }

opt_at_using:
		/* empty */    { $$ = (parser.Expr)(nil) }
	| USING a_expr     { $$ = $2 }

at_constr_tail:
		/* empty */                        { $$ = (*atConstrTail)(nil) }
	| at_constr_tail DEFERRABLE            { $$ = mergeATTail(mustATTail($1), "deferrable") }
	| at_constr_tail NOT DEFERRABLE        { $$ = mergeATTail(mustATTail($1), "not_deferrable") }
	| at_constr_tail INITIALLY DEFERRED    { $$ = mergeATTail(mustATTail($1), "initially_deferred") }
	| at_constr_tail INITIALLY IMMEDIATE   { $$ = mergeATTail(mustATTail($1), "initially_immediate") }
	| at_constr_tail NOT VALID             { $$ = mergeATTail(mustATTail($1), "not_valid") }
	| at_constr_tail NOT ENFORCED          { $$ = mergeATTail(mustATTail($1), "not_enforced") }
	| at_constr_tail ENFORCED              { $$ = mergeATTail(mustATTail($1), "enforced") }
	| at_constr_tail NO INHERIT            { $$ = mergeATTail(mustATTail($1), "no_inherit") }
	| at_constr_tail INHERIT               { $$ = mergeATTail(mustATTail($1), "inherit") }

opt_COLUMN:
		/* empty */  { _ = 0 }
	| COLUMN         { _ = 0 }

/* create_view_stmt — P5 v0: CREATE [OR REPLACE] [TEMP] VIEW name [(cols)] AS select */
create_view_stmt:
		CREATE opt_or_replace VIEW qualified_name opt_name_list_p opt_view_with AS { yylex.(*lexerState).markSpanStart() } select_bare opt_check_option
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
				co := $10.(*checkOpt)
				cv.CheckOption = co.opt
				if co.pos >= 0 {
					// RawDef stops at WITH: legacy's span excludes the option.
					cv.RawDef = yylex.(*lexerState).spanTextUpTo(co.pos)
				} else {
					cv.RawDef = yylex.(*lexerState).spanTextUpTo(yylex.(*lexerState).fragEnd)
				}
				$$ = cv
			}

/* WITH [CASCADED|LOCAL] CHECK OPTION — legacy records "cascaded" for the
   bare form. */
opt_check_option:
		/* empty */                       { $$ = &checkOpt{pos: -1} }
	| WITH CHECK OPTION                   { $$ = &checkOpt{opt: "cascaded", pos: $<p>1} }
	| WITH CASCADED CHECK OPTION          { $$ = &checkOpt{opt: "cascaded", pos: $<p>1} }
	| WITH LOCAL CHECK OPTION             { $$ = &checkOpt{opt: "local", pos: $<p>1} }

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

/* ============================================================================
   CREATE FUNCTION / PROCEDURE, DROP FUNCTION / PROCEDURE / ROUTINE, CALL —
   gram.y CreateFunctionStmt / RemoveFuncStmt / CallStmt (P5.2).

   The `AS 'body'` form needs no special lexing: the lexer already folds
   `$$...$$` into a plain string token. The `BEGIN ATOMIC ... END` form is a
   raw token scan in legacy (function.go parseFunctionBody) with no grammar at
   all, so it stays on the legacy path — the dispatcher refuses to route it.

   The attribute list is a left-recursive fold of closures over one *fnAttrs
   carrier, the shape already used for the identity/sequence option lists: a
   mid-rule action per attribute would put an empty nonterminal right after
   each keyword and force the decision a token early.
   ========================================================================= */

create_function_stmt:
		CREATE opt_or_replace FUNCTION qualified_name '(' opt_func_args ')' RETURNS fn_return fn_attrs
			{
				a := mustFnAttrs($10)
				if msg, raw, at := fnAttrsCheck(yylex, a, "FUNCTION"); msg != "" {
					yylex.(*lexerState).raise(msg, raw, at)
					return 1
				}
				st := parser.NewCreateFunctionStmt($<p>1, $2, objectNameFromQn($4), $6)
				r := $9.(*fnReturn)
				st.ReturnType, st.ReturnsSet, st.ReturnsTable = r.typ, r.setof, r.table
				if r.table {
					st.Args = tableArgs(st.Args, r.cols)
				}
				$$ = applyFnAttrs(st, a)
			}
	/* gram.y makes RETURNS optional (inferring the type from the OUT
	   arguments); legacy requires it — "expected keyword returns" — so there is
	   no RETURNS-less alternative here. CREATE PROCEDURE never takes one. */
	| CREATE opt_or_replace PROCEDURE qualified_name '(' opt_func_args ')' fn_attrs
			{
				a := mustFnAttrs($8)
				if msg, raw, at := fnAttrsCheck(yylex, a, "PROCEDURE"); msg != "" {
					yylex.(*lexerState).raise(msg, raw, at)
					return 1
				}
				st := parser.NewCreateProcedureStmt($<p>1, $2, objectNameFromQn($4), $6)
				$$ = applyProcAttrs(st, a)
			}

fn_return:
		fn_type
			{ $$ = &fnReturn{typ: colTypeOf($1.(*typeWithArgs))} }
	| SETOF fn_type
			{ $$ = &fnReturn{typ: colTypeOf($2.(*typeWithArgs)), setof: true} }
	/* Legacy records RETURNS TABLE as `SETOF record` plus the columns folded
	   into trailing OUT arguments (function.go), not as a distinct type. */
	| TABLE '(' func_arg_list_p ')'
			{ $$ = &fnReturn{typ: parser.NewColumnType("", "record", nil, false), setof: true, table: true, cols: $3} }

/* An absent list and an empty one are different in the AST: legacy returns nil
   for "no parens at all" (DROP FUNCTION f) and a non-nil empty slice for `()`. */
opt_func_args:
		/* empty */        { $$ = []parser.FunctionArg{} }
	| func_arg_list_p      { $$ = $1 }

func_arg_list_p:
		func_arg                        { $$ = []parser.FunctionArg{$1.(parser.FunctionArg)} }
	| func_arg_list_p ',' func_arg      { $$ = append($1, $3.(parser.FunctionArg)) }

/* [mode] [name] type [ {DEFAULT|=} expr ]. The name is optional AND a mode
   keyword may precede it, so the four combinations are spelled out: hiding
   either behind an optional nonterminal would reduce an empty rule right after
   `(` and decide one token too early. */
func_arg:
		fn_type opt_arg_default
			{ $$ = parser.NewFunctionArg("", parser.FuncArgIn, false, colTypeOf($1.(*typeWithArgs)), $2) }
	| fn_param_name fn_type opt_arg_default
			{ $$ = parser.NewFunctionArg($1, parser.FuncArgIn, false, colTypeOf($2.(*typeWithArgs)), $3) }
	| fn_arg_mode fn_type opt_arg_default
			{ $$ = parser.NewFunctionArg("", parser.FuncArgMode($1), true, colTypeOf($2.(*typeWithArgs)), $3) }
	| fn_arg_mode fn_param_name fn_type opt_arg_default
			{ $$ = parser.NewFunctionArg($2, parser.FuncArgMode($1), true, colTypeOf($3.(*typeWithArgs)), $4) }
	/* `name mode type` (`a OUT int`) as well as `mode name type`: legacy
	   accepts both orderings (function.go parseFunctionArg). */
	| fn_param_name fn_arg_mode fn_type opt_arg_default
			{ $$ = parser.NewFunctionArg($1, parser.FuncArgMode($2), true, colTypeOf($3.(*typeWithArgs)), $4) }

/* gram.y's param_name is type_function_name, NOT ColId: OUT_P and INOUT are
   col_name_keywords, so a ColId here would be reduce/reduce-ambiguous with
   fn_arg_mode on the very first token of an argument. */
fn_param_name:
		IDENT                { $$ = $1 }
	| unreserved_keyword     { $$ = $1 }

/* TRIGGER is a keyword, so it never reaches cast_ident's IDENT; every other
   pseudo-type a routine mentions (void, record, internal, cstring, anyelement,
   language_handler) is an ordinary identifier. Widening cast_ident instead
   would also make `x::trigger` legal, which legacy rejects. */
fn_type:
		col_type_name   { $$ = $1 }
	| TRIGGER           { $$ = &typeWithArgs{ct: castType{name: "trigger"}} }

fn_arg_mode:
		IN_P        { $$ = int(parser.FuncArgIn) }
	| OUT_P         { $$ = int(parser.FuncArgOut) }
	| INOUT         { $$ = int(parser.FuncArgInout) }
	| VARIADIC      { $$ = int(parser.FuncArgVariadic) }

opt_arg_default:
		/* empty */        { $$ = nil }
	/* gram.y also allows `= expr` here, but legacy REJECTS it
	   ("expected ',' or ')' in function arg list"), so this grammar must too. */
	| DEFAULT a_expr       { $$ = $2 }

fn_attrs:
		/* empty */             { $$ = newFnAttrs() }
	| fn_attrs fn_attr          { a := mustFnAttrs($1); $2.(func(*fnAttrs))(a); $$ = a }

fn_attr:
		LANGUAGE fn_lang_name
			{
				s, at := $2, $<p>1
				$$ = func(a *fnAttrs) {
					if a.sawLanguage {
						a.fail("duplicate LANGUAGE clause (got language)", false, at)
						return
					}
					a.language, a.sawLanguage = s, true
				}
			}
	| AS fn_body_list
			{
				s, two, at := $2, twoItemAS(yylex, $<p>2), $<p>1
				$$ = func(a *fnAttrs) {
					if a.hasBody {
						/* Legacy's errAtCur appends the token it stopped on;
						   here that token is the AS keyword itself, so the
						   text is fixed. */
						a.fail("duplicate AS clause (got as)", false, at)
						return
					}
					a.body, a.hasBody, a.twoItemAs = s, true, two
				}
			}
	/* `RETURN expr` (PG14 SQL-standard body). Legacy swallows every remaining
	   token to the end of the statement into the body, so the join runs from
	   the expression's first token to the fragment end rather than to the end
	   of a_expr — which is the same thing whenever RETURN is written last, and
	   faithfully wrong in the same way when it is not. */
	| RETURN a_expr
			{
				p, at := $<p>2, $<p>1
				$$ = func(a *fnAttrs) {
					if a.hasBody {
						a.fail("duplicate function body specified", true, at)
						return
					}
					a.body, a.hasBody, a.isReturnForm = "SELECT "+joinBodyTokens(yylex, p), true, true
				}
			}
	| IMMUTABLE                   { $$ = func(a *fnAttrs) { a.volatility = "i" } }
	| STABLE                      { $$ = func(a *fnAttrs) { a.volatility = "s" } }
	| VOLATILE                    { $$ = func(a *fnAttrs) { a.volatility = "v" } }
	| STRICT_P                      { $$ = func(a *fnAttrs) { a.strict = true } }
	| CALLED ON NULL_P INPUT_P    { $$ = func(a *fnAttrs) { a.strict = false } }
	| RETURNS NULL_P ON NULL_P INPUT_P { $$ = func(a *fnAttrs) { a.strict = true } }
	| LEAKPROOF                   { $$ = func(a *fnAttrs) { a.leakproof = true } }
	| NOT LEAKPROOF               { $$ = func(a *fnAttrs) { a.leakproof = false } }
	| WINDOW                      { $$ = func(a *fnAttrs) { a.window = true } }
	| SECURITY DEFINER            { $$ = func(a *fnAttrs) { a.securityDefiner = true } }
	| SECURITY INVOKER            { $$ = func(a *fnAttrs) { a.securityDefiner = false } }
	| EXTERNAL SECURITY DEFINER   { $$ = func(a *fnAttrs) { a.securityDefiner = true } }
	| EXTERNAL SECURITY INVOKER   { $$ = func(a *fnAttrs) { a.securityDefiner = false } }
	| PARALLEL ColId              { s := parallelCode($2); $$ = func(a *fnAttrs) { a.parallel = s } }
	/* COST/ROWS take a plain numeric, not an integer: `COST 0.5` is legal. */
	| COST fn_number              { s := $2; $$ = func(a *fnAttrs) { a.cost = s } }
	| ROWS fn_number              { s := $2; $$ = func(a *fnAttrs) { a.rows = s } }
	/* SUPPORT is parsed and dropped by legacy (no AST field). */
	| SUPPORT qualified_name      { $$ = func(a *fnAttrs) {} }
	| SET fn_config_name fn_config_value
			{
				n, v := $2, $3
				$$ = func(a *fnAttrs) {
					/* FROM CURRENT / TO DEFAULT record NO config op: goopg has
					   no GUC snapshot to capture (function.go
					   parseFunctionConfigSetClause returns ok=false). */
					if v != fnConfigUnset {
						a.configOps = append(a.configOps, parser.NewFunctionConfigOp(false, false, n, v))
					}
				}
			}
	| RESET ALL                   { $$ = func(a *fnAttrs) { a.configOps = append(a.configOps, parser.NewFunctionConfigOp(false, true, "", "")) } }
	| RESET fn_config_name        { n := $2; $$ = func(a *fnAttrs) { a.configOps = append(a.configOps, parser.NewFunctionConfigOp(true, false, n, "")) } }

fn_number:
		signed_iconst   { $$ = atStatValue($1) }
	| FCONST            { $$ = $1 }
	| '-' FCONST        { $$ = "-" + $2 }

fn_lang_name:
		ColId       { $$ = lowerIdent($1) }
	| SCONST        { $$ = lowerIdent($1) }

/* `AS 'obj', 'link'` is the LANGUAGE C two-item form; legacy keeps only the
   object-file string and discards the link symbol. */
fn_body_list:
		SCONST                  { $$ = $1 }
	| SCONST ',' SCONST         { $$ = $1 }

fn_config_name:
		ColId               { $$ = $1 }
	| ColId '.' ColId       { $$ = $1 + "." + $3 }

/* `= DEFAULT` cannot be its own alternative: set_value_atom already accepts
   DEFAULT, and both reduce at the same point. The keyword is recognised at the
   token instead, which also keeps `= 'default'` (a real value) distinct. */
fn_config_value:
		fn_eq_to fn_set_values      { if isDefaultKeywordAt(yylex, $<p>2) { $$ = fnConfigUnset } else { $$ = $2 } }
	| FROM CURRENT_P                { $$ = fnConfigUnset }

/* set_value_list keeps only its FIRST atom (its callers recover the rest from
   the token stream via lexerState.setValueAtoms, which scans a whole SET
   statement and would find the wrong '=' here), so the attribute list needs its
   own accumulating copy. */
fn_set_values:
		set_value_atom                       { $$ = $1 }
	| fn_set_values ',' set_value_atom       { $$ = $1 + "," + $3 }

fn_eq_to:
		/* empty */   { }
	| '='             { }
	| TO              { }

drop_function_stmt:
		DROP FUNCTION opt_if_exists_drop fn_drop_item fn_drop_extras opt_drop_behavior
			{
				it := $4.(*parser.DropFunctionItem)
				$$ = parser.NewDropFunctionStmt($<p>1, $3, it.Name, it.Args, fnDropBehavior($6), $5)
			}
	| DROP PROCEDURE opt_if_exists_drop fn_drop_item fn_drop_extras opt_drop_behavior
			{
				it := $4.(*parser.DropFunctionItem)
				/* ObjKind is set for ROUTINE only; DROP PROCEDURE leaves it empty
				   (ddl.go:5948 is the sole writer). */
				$$ = parser.NewDropProcedureStmt($<p>1, $3, it.Name, dropProcNames($5), it.Args, fnDropBehavior($6), "")
			}
	| DROP ROUTINE opt_if_exists_drop fn_drop_item fn_drop_extras opt_drop_behavior
			{
				it := $4.(*parser.DropFunctionItem)
				$$ = parser.NewDropProcedureStmt($<p>1, $3, it.Name, dropProcNames($5), it.Args, fnDropBehavior($6), "routine")
			}

fn_drop_item:
		qualified_name
			{ $$ = &parser.DropFunctionItem{Name: objectNameFromQn($1)} }
	| qualified_name '(' opt_func_args ')'
			{ $$ = &parser.DropFunctionItem{Name: objectNameFromQn($1), Args: $3} }

fn_drop_extras:
		/* empty */                        { $$ = []parser.DropFunctionItem(nil) }
	| fn_drop_extras ',' fn_drop_item      { $$ = append($1, *$3.(*parser.DropFunctionItem)) }

call_stmt:
		CALL qualified_name
			{ $$ = parser.NewCallStmt($<p>1, objectNameFromQn($2), nil, nil) }
	| CALL qualified_name '(' opt_call_named_args ')'
			{
				ca := $4.(*namedCallArgs)
				$$ = parser.NewCallStmt($<p>1, objectNameFromQn($2), ca.exprs, ca.names())
			}

opt_call_named_args:
		/* empty */                { $$ = &namedCallArgs{exprs: []parser.Expr{}} }
	| call_named_arg_list          { $$ = $1 }

call_named_arg_list:
		call_named_arg                             { $$ = appendNamedCallArg(nil, $1.(callArg)) }
	| call_named_arg_list ',' call_named_arg       { $$ = appendNamedCallArg($1.(*namedCallArgs), $3.(callArg)) }

/* CALL's named form is `name => value` ONLY — legacy's `:=` handling lives in
   the function-call path, not here (function.go parseCallStatement). */
call_named_arg:
		a_expr                              { $$ = callArg{expr: $1} }
	| a_expr EQUALS_GREATER a_expr          { $$ = callArg{expr: $3, name: exprIdentName($1), named: true} }

/* ============================================================================
   P5.3 — utility statements: transaction control (SAVEPOINT / RELEASE),
   prepared statements (PREPARE / EXECUTE / DEALLOCATE), cursors (DECLARE /
   FETCH / MOVE / CLOSE) and the maintenance commands (ANALYZE / VACUUM /
   REINDEX / CLUSTER / LOCK / CHECKPOINT / DISCARD).

   Together these are the largest remaining unrouted block in the regress
   corpus (fetch 139, analyze 130, declare 80, execute 45, reindex 43, vacuum
   33, savepoint 33, prepare 30, close 29 ... ).

   Legacy is the parity target and is NARROWER than gram.y in several places,
   so the grammar is deliberately narrowed to match rather than widened to
   upstream: DISCARD ALL is REJECTED, a cursor may carry only [NO] SCROLL
   before CURSOR (not BINARY / INSENSITIVE / ASENSITIVE), CLUSTER takes no
   parenthesised option list, and MOVE is parsed-and-discarded as a
   CompatNoopStmt with an empty body.
   ========================================================================= */

savepoint_stmt:
		SAVEPOINT ColId          { $$ = parser.NewSavepointStmt($<p>1, $2) }
	/* SAVEPOINT is unreserved and therefore also a ColId, which is exactly the
	   pinned S/R conflict on RELEASE: shift keeps the keyword reading. */
	| RELEASE SAVEPOINT ColId    { $$ = parser.NewReleaseSavepointStmt($<p>1, $3) }
	| RELEASE ColId              { $$ = parser.NewReleaseSavepointStmt($<p>1, $2) }

checkpoint_stmt:
		CHECKPOINT               { $$ = parser.NewCheckpointStmt($<p>1) }

/* DISCARD ALL is a legacy REJECT ("syntax error at or near \"all\""), so ALL
   is not an alternative here; TEMPORARY normalises to TEMP. */
discard_stmt:
		DISCARD PLANS            { $$ = parser.NewDiscardStmt($<p>1, "PLANS") }
	| DISCARD SEQUENCES          { $$ = parser.NewDiscardStmt($<p>1, "SEQUENCES") }
	| DISCARD TEMP               { $$ = parser.NewDiscardStmt($<p>1, "TEMP") }
	| DISCARD TEMPORARY          { $$ = parser.NewDiscardStmt($<p>1, "TEMP") }

deallocate_stmt:
		DEALLOCATE ColId             { $$ = parser.NewDeallocateStmt($<p>1, $2) }
	| DEALLOCATE PREPARE ColId       { $$ = parser.NewDeallocateStmt($<p>1, $3) }
	| DEALLOCATE ALL                 { $$ = parser.NewDeallocateStmt($<p>1, "") }
	| DEALLOCATE PREPARE ALL         { $$ = parser.NewDeallocateStmt($<p>1, "") }

prepare_stmt:
		PREPARE ColId opt_prep_types AS stmt
			{ $$ = parser.NewPrepareStmt($<p>1, $2, $3, $5) }
	/* PREPARE TRANSACTION 'gid' is a different statement sharing the keyword
	   (parser.go parsePrepare); TRANSACTION is reserved and never a ColId, so
	   the two cannot be confused. */
	| PREPARE TRANSACTION SCONST
			{ $$ = parser.NewPrepareTransactionStmt($<p>1, $3) }

/* The declared parameter types are recorded as their plain names; nil when no
   list was written, which is why this is not an `opt_paren_list` of strings. */
opt_prep_types:
		/* empty */                  { $$ = []string(nil) }
	| '(' prep_type_list ')'         { $$ = $2 }

prep_type_list:
		fn_type                          { $$ = []string{typeNameOf($1)} }
	| prep_type_list ',' fn_type         { $$ = append($1, typeNameOf($3)) }

execute_stmt:
		EXECUTE ColId                        { $$ = parser.NewExecuteStmt($<p>1, $2, nil) }
	| EXECUTE ColId '(' opt_func_call_args ')' { $$ = parser.NewExecuteStmt($<p>1, $2, $4) }

close_stmt:
		CLOSE ColId              { $$ = parser.NewCloseStmt($<p>1, $2) }
	| CLOSE ALL                  { $$ = parser.NewCloseStmt($<p>1, "") }

/* DECLARE name [NO] SCROLL CURSOR [WITH|WITHOUT HOLD] FOR <stmt>. gram.y also
   accepts BINARY / INSENSITIVE / ASENSITIVE here; legacy rejects all three
   ("expected CURSOR"), so they stay rejected. */
declare_stmt:
		DECLARE ColId opt_cursor_scroll CURSOR opt_cursor_hold FOR stmt
			{ $$ = parser.NewDeclareCursorStmt($<p>1, $2, $7) }

opt_cursor_scroll:
		/* empty */   { }
	| SCROLL          { }
	| NO SCROLL       { }

opt_cursor_hold:
		/* empty */       { }
	| WITH HOLD           { }
	| WITHOUT HOLD        { }

/* FETCH's direction and count collapse into (Count, Forward): ALL is -1, a
   bare cursor is 1, and the backward directions only flip Forward — PRIOR and
   LAST are Count=1 backward, exactly as parser.go records them. */
fetch_stmt:
		FETCH fetch_arg          { $$ = fetchStmt($<p>1, $2, false) }
	| MOVE fetch_arg             { $$ = fetchStmt($<p>1, $2, true) }

fetch_arg:
	/* `FETCH c`, `FETCH FROM c` and `FETCH IN c` are spelled out rather than
	   sharing the optional cursor_from used below: an EMPTY nonterminal here
	   would reduce right after FETCH and decide before the direction word is
	   visible — 16 extra shift/reduce conflicts. */
		cursor_ref                                   { $$ = &fetchSpec{count: 1, forward: true, name: $1} }
	| FROM cursor_ref                                { $$ = &fetchSpec{count: 1, forward: true, name: $2} }
	| IN_P cursor_ref                                { $$ = &fetchSpec{count: 1, forward: true, name: $2} }
	| ALL cursor_from cursor_ref                     { $$ = &fetchSpec{count: -1, forward: true, name: $3} }
	| signed_iconst cursor_from cursor_ref           { $$ = &fetchSpec{count: $1, forward: true, name: $3} }
	| NEXT cursor_from cursor_ref                    { $$ = &fetchSpec{count: 1, forward: true, name: $3} }
	| PRIOR cursor_from cursor_ref                   { $$ = &fetchSpec{count: 1, forward: false, name: $3} }
	| FIRST_P cursor_from cursor_ref                 { $$ = &fetchSpec{count: 1, forward: true, name: $3} }
	| LAST_P cursor_from cursor_ref                  { $$ = &fetchSpec{count: 1, forward: false, name: $3} }
	| ABSOLUTE_P signed_iconst cursor_from cursor_ref { $$ = &fetchSpec{count: $2, forward: true, name: $4} }
	| RELATIVE_P signed_iconst cursor_from cursor_ref { $$ = &fetchSpec{count: $2, forward: true, name: $4} }
	| FORWARD fetch_count cursor_from cursor_ref     { $$ = &fetchSpec{count: $2, forward: true, name: $4} }
	| BACKWARD fetch_count cursor_from cursor_ref    { $$ = &fetchSpec{count: $2, forward: false, name: $4} }

fetch_count:
		/* empty */      { $$ = int64(1) }
	| signed_iconst      { $$ = $1 }
	| ALL                { $$ = int64(-1) }

cursor_from:
		/* empty */   { }
	| FROM            { }
	| IN_P            { }

cursor_ref:
		ColId   { $$ = $1 }

analyze_stmt:
		analyze_kw opt_vacuum_opts opt_vacuum_targets
			{
				v := $2.(*parser.VacuumStmt)
				tg := $3.(*vacTargets)
				$$ = parser.NewAnalyzeStmt($<p>1, v.Verbose, v.SkipLocked, tg.names, tg.cols)
			}

analyze_kw:
		ANALYZE    { }
	| ANALYSE      { }

vacuum_stmt:
		VACUUM opt_vacuum_opts opt_vacuum_targets
			{
				// The option list allocates the statement (it is the fold's
				// accumulator), so the position is stamped by rebuilding the
				// carrier here rather than mutating an unexported field.
				v := vacuumAt($<p>1, $2)
				tg := $3.(*vacTargets)
				v.Targets, v.TargetCols = tg.names, tg.cols
				$$ = v
			}

/* The bare-keyword prefix (`VACUUM FULL FREEZE VERBOSE ANALYZE t`) and the
   parenthesised list are two different vocabularies in legacy: only the four
   keyword options exist outside the parens. Sharing one nonterminal would
   accept `VACUUM skip_locked t`, which legacy rejects. */
opt_vacuum_opts:
		vacuum_kw_opts                   { $$ = $1 }
	| '(' vacuum_opt_list ')'            { $$ = $2 }

vacuum_kw_opts:
		/* empty */                      { $$ = parser.NewVacuumStmt(0) }
	| vacuum_kw_opts vacuum_kw_opt       { v := $1.(*parser.VacuumStmt); $2.(func(*parser.VacuumStmt))(v); $$ = v }

vacuum_kw_opt:
		VERBOSE      { $$ = func(v *parser.VacuumStmt) { v.Verbose = true } }
	| analyze_kw     { $$ = func(v *parser.VacuumStmt) { v.Analyze = true } }
	| FULL           { $$ = func(v *parser.VacuumStmt) { v.Full = true } }
	| FREEZE         { $$ = func(v *parser.VacuumStmt) { v.Freeze = true } }

vacuum_opt_list:
		vacuum_opt                          { v := parser.NewVacuumStmt(0); $1.(func(*parser.VacuumStmt))(v); $$ = v }
	| vacuum_opt_list ',' vacuum_opt        { v := $1.(*parser.VacuumStmt); $3.(func(*parser.VacuumStmt))(v); $$ = v }

/* ONE name+value rule, not one alternative per option: `VERBOSE`, `VERBOSE
   true` and `skip_locked` would otherwise be three productions whose first
   symbol reduces at the same point, which was 1584 reduce/reduce. The value is
   optional and its meaning is per-option (ignored for the plain booleans,
   three-valued for index_cleanup, negating for truncate/process_*). */
vacuum_opt:
		vacuum_opt_name opt_opt_value    { $$ = vacuumNamedOpt($1, $2) }

/* ColId already covers the unreserved names (truncate, parallel, skip_locked,
   index_cleanup ...); only the type_func_name keywords need spelling out. */
vacuum_opt_name:
		ColId       { $$ = $1 }
	| VERBOSE       { $$ = "verbose" }
	| analyze_kw    { $$ = "analyze" }
	| FULL          { $$ = "full" }
	| FREEZE        { $$ = "freeze" }

opt_opt_value:
		/* empty */   { $$ = "" }
	| TRUE_P          { $$ = "true" }
	| FALSE_P         { $$ = "false" }
	| ColId           { $$ = $1 }
	| SCONST          { $$ = $1 }
	| signed_iconst   { $$ = strconv.FormatInt($1, 10) }

opt_vacuum_targets:
		/* empty */          { $$ = &vacTargets{} }
	| vacuum_target_list     { $$ = $1 }

vacuum_target_list:
		vacuum_target                            { $$ = appendVacTarget(nil, $1.(*vacTarget)) }
	| vacuum_target_list ',' vacuum_target       { $$ = appendVacTarget($1.(*vacTargets), $3.(*vacTarget)) }

vacuum_target:
		qualified_name                       { $$ = &vacTarget{name: objectNameFromQn($1)} }
	| qualified_name '(' colid_list ')'      { $$ = &vacTarget{name: objectNameFromQn($1), cols: $3} }

/* CONCURRENTLY may sit on EITHER side of the object kind: legacy checks for it
   before the kind switch AND again after it (parser.go:2608 / :2627), so
   `REINDEX CONCURRENTLY INDEX i` and `REINDEX INDEX CONCURRENTLY i` are both
   accepted. IF EXISTS is parsed and discarded, as in legacy. */
reindex_stmt:
		REINDEX opt_reindex_opts opt_concurrently reindex_kind opt_concurrently opt_if_exists_drop qualified_name
			{ $$ = parser.NewReindexStmt($<p>1, $2, $3 || $5, $4, qnText($7)) }

opt_reindex_opts:
		/* empty */                    { $$ = false }
	| '(' reindex_opt_list ')'         { $$ = $2 }

reindex_opt_list:
		reindex_opt                        { $$ = $1 }
	| reindex_opt_list ',' reindex_opt     { $$ = $1 || $3 }

reindex_opt:
		VERBOSE            { $$ = true }
	| TABLESPACE ColId     { $$ = false }

reindex_kind:
		INDEX       { $$ = "INDEX" }
	| TABLE         { $$ = "TABLE" }
	| DATABASE      { $$ = "DATABASE" }
	| SCHEMA        { $$ = "SCHEMA" }
	| SYSTEM_P      { $$ = "SYSTEM" }

/* CLUSTER takes NO parenthesised option list in legacy ("expected identifier
   (got (")" ), only the bare VERBOSE keyword. */
cluster_stmt:
		CLUSTER opt_verbose_kw                                { $$ = parser.NewClusterStmt($<p>1, $2, nil, "") }
	| CLUSTER opt_verbose_kw qualified_name opt_cluster_using
			{ n := objectNameFromQn($3); $$ = parser.NewClusterStmt($<p>1, $2, &n, $4) }

opt_verbose_kw:
		/* empty */   { $$ = false }
	| VERBOSE         { $$ = true }

opt_cluster_using:
		/* empty */     { $$ = "" }
	| USING ColId       { $$ = $2 }

/* LockTableRelation has no ONLY flag, so `LOCK TABLE ONLY t` records the same
   relation an unqualified one does — legacy parses and drops the keyword. */
lock_stmt:
		LOCK_P opt_TABLE_kw lock_rel_list opt_lock_mode opt_nowait
			{ $$ = parser.NewLockTableStmt($<p>1, $3, $4, $5) }

opt_TABLE_kw:
		/* empty */   { }
	| TABLE           { }

lock_rel_list:
		lock_rel                        { $$ = []parser.LockTableRelation{$1} }
	| lock_rel_list ',' lock_rel        { $$ = append($1, $3) }

lock_rel:
		opt_ONLY_kw qualified_name      { n := objectNameFromQn($2); $$ = parser.NewLockTableRelation(n.Schema, n.Name) }

opt_lock_mode:
		/* empty */                     { $$ = "AccessExclusiveLock" }
	| IN_P lock_mode_words MODE
			{
				n, ok := parser.LockModeName($2)
				if !ok {
					yylex.Error("unrecognised lock mode")
					return 1
				}
				$$ = n
			}

lock_mode_words:
		lock_mode_word                        { $$ = []string{$1} }
	| lock_mode_words lock_mode_word          { $$ = append($1, $2) }

/* ACCESS / SHARE / ROW / UPDATE are all ColIds or unreserved words except
   EXCLUSIVE, so the list is spelled from the exact vocabulary of
   parser.go's lockModeNames table. */
lock_mode_word:
		ACCESS      { $$ = "access" }
	| SHARE         { $$ = "share" }
	| ROW           { $$ = "row" }
	| UPDATE        { $$ = "update" }
	| EXCLUSIVE     { $$ = "exclusive" }

opt_nowait:
		/* empty */   { $$ = false }
	| NOWAIT          { $$ = true }

/* ============================================================================
   P5.4 — MERGE (gram.y MergeStmt). 106 unrouted regress fragments, plus the
   four `WITH ... (MERGE ...)` / `PREPARE ... AS MERGE` fragments that were the
   last yacc-side rejects in already-routed classes.

   INTO is optional in legacy (parser.go parseMerge accepts a bare `MERGE t`),
   and both the target and the source are base_table_refs — the same
   nonterminal UPDATE/DELETE use — so a parenthesised sub-select source with an
   alias comes for free.
   ========================================================================= */

merge_stmt:
		MERGE opt_INTO_kw base_table_ref USING base_table_ref ON a_expr merge_when_list opt_returning
			{ $$ = parser.NewMergeStmt($<p>1, $3, $5, $7, $8, $9) }

opt_INTO_kw:
		/* empty */   { }
	| INTO            { }

/* Legacy loops `for p.cur() == WHEN`, so an empty clause list is accepted. */
merge_when_list:
		/* empty */                        { $$ = []*parser.MergeWhenClause(nil) }
	| merge_when_list merge_when_clause    { $$ = append($1, $2) }

merge_when_clause:
		WHEN merge_match opt_merge_and THEN merge_action
			{
				c := $2
				c.Condition = $3
				applyMergeAction(c, $5)
				$$ = c
			}

merge_match:
		MATCHED                      { $$ = parser.NewMergeWhenClause(0, true, false, false) }
	| NOT MATCHED                    { $$ = parser.NewMergeWhenClause(0, false, false, false) }
	| NOT MATCHED BY ColId           { $$ = mergeNotMatchedBy($4) }

opt_merge_and:
		/* empty */   { $$ = nil }
	| AND a_expr      { $$ = $2 }

merge_action:
		UPDATE SET update_set_list        { $$ = &mergeAction{kind: parser.MergeActionUpdate, assigns: $3} }
	| DELETE_P                            { $$ = &mergeAction{kind: parser.MergeActionDelete} }
	| DO NOTHING                          { $$ = &mergeAction{kind: parser.MergeActionDoNothing} }
	/* VALUES_LA, not VALUES: base_yylex substitutes the lookahead variant
	   whenever VALUES is followed by '(' (base_yylex.go), so the plain token
	   never reaches this position. `DEFAULT VALUES` below keeps plain VALUES
	   because no paren follows it. */
	| INSERT merge_ins_cols VALUES_LA '(' expr_list ')'
			{ $$ = &mergeAction{kind: parser.MergeActionInsert, cols: $2, vals: $5} }
	/* DEFAULT VALUES leaves InsertValues nil, which is how the executor tells
	   the two apart. */
	| INSERT merge_ins_cols DEFAULT VALUES
			{ $$ = &mergeAction{kind: parser.MergeActionInsert, cols: $2} }

merge_ins_cols:
		/* empty */             { $$ = []string(nil) }
	| '(' colid_list ')'        { $$ = $2 }

/* ============================================================================
   P5.5 — CREATE / DROP TYPE, CREATE / DROP DOMAIN, CREATE SEQUENCE, DO.

   Legacy parses NONE of these bodies as expressions: it walks raw tokens and
   stores their join (parseCreateType's composite/range scans,
   parseDomainCheckExpr). The grammar therefore does the structural work — so a
   malformed body is still a syntax error — and the ACTION rebuilds the stored
   string from the token stream, the same division of labour CHECK bodies use.
   ========================================================================= */

create_type_stmt:
	/* Shell type: `CREATE TYPE t` records the name and nothing else. */
		CREATE TYPE_P qualified_name
			{ $$ = parser.NewCreateTypeStmt($<p>1, objectNameFromQn($3)) }
	/* Base type: legacy sets ONLY HasOptions and skips the whole list. */
	| CREATE TYPE_P qualified_name '(' type_opt_list ')'
			{
				st := parser.NewCreateTypeStmt($<p>1, objectNameFromQn($3))
				st.HasOptions = true
				$$ = st
			}
	| CREATE TYPE_P qualified_name AS ENUM_P '(' enum_val_list ')'
			{
				st := parser.NewCreateTypeStmt($<p>1, objectNameFromQn($3))
				st.IsEnum, st.EnumValues = true, $7
				$$ = st
			}
	| CREATE TYPE_P qualified_name AS '(' type_field_list ')'
			{
				st := parser.NewCreateTypeStmt($<p>1, objectNameFromQn($3))
				st.IsComposite, st.CompositeFields = true, $6
				$$ = st
			}
	| CREATE TYPE_P qualified_name AS RANGE '(' range_opt_list ')'
			{
				st := parser.NewCreateTypeStmt($<p>1, objectNameFromQn($3))
				st.IsRange = true
				applyRangeOpts(st, $7)
				$$ = st
			}

/* Legacy rejects an EMPTY enum list ("expected string literal in ENUM value
   list"), so this is not an optional list. */
enum_val_list:
		SCONST                       { $$ = []string{$1} }
	| enum_val_list ',' SCONST       { $$ = append($1, $3) }

type_field_list:
		type_field                        { $$ = []parser.TypeField{$1} }
	| type_field_list ',' type_field      { $$ = append($1, $3) }

/* ColType is the RAW token join legacy stores, not a rendered type name:
   `character varying(20)` is kept as "character varying ( 20 )". The join runs
   from the type's first token to the field's terminator, exactly as
   parseCreateType's inner scan does. */
type_field:
		ColId col_type_name opt_field_collate
			{ $$ = parser.NewTypeField(lowerIdent($1), rawTypeSpan(yylex, $<p>2), $3) }

opt_field_collate:
		/* empty */          { $$ = "" }
	| COLLATE qualified_name { $$ = qnLastPart($2) }

/* The base-type option list reaches the AST only as HasOptions, so the values
   are accepted in the shapes gram.y allows and then dropped. */
type_opt_list:
		type_opt                        { }
	| type_opt_list ',' type_opt        { }

/* gram.y's def_elem takes a ColLabel, which upstream widens to EVERY keyword.
   This grammar's ColLabel already reaches type_func_name_keyword (so LIKE and
   ANALYZE are covered — `create type t (..., like = int8)` is 7 regress
   fragments); only the RESERVED words need their own alternative, and DEFAULT
   is the one that shows up as a base-type option name. */
type_opt:
		type_opt_name                       { }
	| type_opt_name '=' type_opt_value      { }

type_opt_name:
		ColLabel     { }
	| DEFAULT        { }

type_opt_value:
		ColId                           { }
	| ColId '(' opt_func_args ')'       { }
	| ColId '.' ColId                   { }
	| SCONST                            { }
	| signed_iconst                     { }
	| FCONST                            { }
	| TRUE_P                            { }
	| FALSE_P                           { }

range_opt_list:
		range_opt                       { $$ = appendRangeOpt(nil, $1.(*kvPair)) }
	| range_opt_list ',' range_opt      { $$ = appendRangeOpt($1, $3.(*kvPair)) }

/* SUBTYPE's value is a raw token join (it may be a multi-word type name); the
   other three are object names of which legacy keeps only the last part. */
range_opt:
		ColId '=' range_opt_value
			{ $$ = &kvPair{key: lowerIdent($1), val: rawTypeSpan(yylex, $<p>3), last: $3} }
	/* COLLATION is a type_func_name keyword, not a ColId, so it needs its own
	   alternative; the other three option names are ordinary identifiers. */
	| COLLATION '=' range_opt_value
			{ $$ = &kvPair{key: "collation", val: rawTypeSpan(yylex, $<p>3), last: $3} }

/* `SUBTYPE = int4[]` is a real spelling, so the value takes an array tail;
   the three name-valued options never carry one and ignore it. */
range_opt_value:
		qualified_name opt_array_tail    { $$ = qnLastPart($1) }

drop_type_stmt:
		DROP TYPE_P opt_if_exists_drop drop_name_list opt_drop_behavior
			{ $$ = parser.NewDropTypeStmt($<p>1, $4, $3, $5 == "cascade") }

drop_domain_stmt:
		DROP DOMAIN_P opt_if_exists_drop drop_name_list opt_drop_behavior
			{ $$ = parser.NewDropDomainStmt($<p>1, $4, $3, $5 == "cascade") }

/* AS is optional (`CREATE DOMAIN d int`), and the base type is stored as a
   NAME plus separate typmod args, not as a ColumnType. */
create_domain_stmt:
		CREATE DOMAIN_P qualified_name opt_AS_kw col_type_name domain_constraints
			{
				tw := $5.(*typeWithArgs)
				ct := colTypeOf(tw)
				nm := ct.Name
				if ct.Schema != "" {
					nm = ct.Schema + "." + ct.Name
				}
				if ct.IsArray {
					nm += "[]"
				}
				st := parser.NewCreateDomainStmt($<p>1, objectNameFromQn($3), nm, ct.Args)
				applyDomainConstraints(st, $6)
				$$ = st
			}

opt_AS_kw:
		/* empty */   { }
	| AS              { }

domain_constraints:
		/* empty */                           { $$ = []any(nil) }
	| domain_constraints domain_constraint    { $$ = append($1, $2) }

domain_constraint:
		NOT NULL_P                { $$ = domainNotNull(true) }
	| NULL_P                      { $$ = domainNotNull(false) }
	| DEFAULT a_expr              { $$ = domainDefault($2) }
	| check_body                  { $$ = domainCheck(yylex, "", $<p>1) }
	| CONSTRAINT ColId check_body { $$ = domainCheck(yylex, $2, $<p>3) }
	/* COLLATE on a domain is parsed and discarded by legacy. */
	| COLLATE qualified_name      { $$ = domainNoop() }

create_sequence_stmt:
		CREATE opt_create_modifier SEQUENCE opt_if_not_exists qualified_name seq_opts
			{
				pfx, _ := $2.(*createPrefix)
				temp, unlogged := false, false
				if pfx != nil {
					temp, unlogged = pfx.temporary, pfx.unlogged
				}
				st := parser.NewCreateSequenceStmt($<p>1, objectNameFromQn($5), temp, unlogged, $4)
				applySeqOpts(st, $6)
				$$ = st
			}

seq_opts:
		/* empty */          { $$ = []any(nil) }
	| seq_opts seq_opt       { $$ = append($1, $2) }

/* NO MINVALUE / NO MAXVALUE / NO CYCLE all leave the field unset — legacy
   consumes the word and records nothing, so CYCLE followed by NO CYCLE keeps
   Cycle=true, which is what the option loop does. */
seq_opt:
		AS ColId                     { $$ = seqDataType($2) }
	| INCREMENT opt_BY_kw signed_iconst { $$ = seqInt("increment", $3) }
	| MINVALUE signed_iconst         { $$ = seqInt("minvalue", $2) }
	| MAXVALUE signed_iconst         { $$ = seqInt("maxvalue", $2) }
	| START opt_WITH_kw signed_iconst { $$ = seqInt("start", $3) }
	| CACHE signed_iconst            { $$ = seqInt("cache", $2) }
	| CYCLE                          { $$ = seqCycle() }
	| NO MINVALUE                    { $$ = seqNoop() }
	| NO MAXVALUE                    { $$ = seqNoop() }
	| NO CYCLE                       { $$ = seqNoop() }
	| OWNED BY seq_owner             { $$ = seqOwnedBy($3) }

opt_BY_kw:
		/* empty */   { }
	| BY              { }

opt_WITH_kw:
		/* empty */   { }
	| WITH            { }

/* OWNED BY NONE records an EMPTY owner, not the word "none". */
seq_owner:
		qualified_name       { $$ = seqOwnerName($1) }

do_stmt:
	/* Legacy accepts ONLY `DO <dollar-quoted body>` — no LANGUAGE clause on
	   either side ("expected dollar-quoted string for DO body"). */
		DO SCONST            { $$ = parser.NewDoStmt($<p>1, "plpgsql", $2) }

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
				var cols []ColumnDef
				var pk []string
				var uqs [][]string
				var uqIncludes [][]string
				var uqNullsNotDistinct, uqDeferrable, uqInitiallyDeferred []bool
				var named []TableConstraintDef
				var exclusions []TableConstraintDef
				var likes []ObjectName
				var bodyOrder []string
				var namedChecks []PartitionCheckConstraint
				var fks []TableForeignKeyDef
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
						cd := NewColumnDefAt(c.namePos, c.typePos, c.name, NewColumnType(c.schema, c.typ, c.args, c.isArray))
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
						applyNotNullOccurrences(yylex, cd, c.nnOccur)
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
							cd.FKNotValid, cd.FKNotEnforced = c.fkNotValid, c.fkNotEnforced
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
							// Legacy ALSO emits a ColumnDef with an EMPTY type
							// for the standalone item — but ONLY when the
							// column has no definition of its own in the same
							// list, where the NOT NULL is just an attribute of
							// an existing column instead.
							if !elemsDefineColumn(elems, e.notNull.col) {
							ncd := NewColumnDef(e.notNull.col, NewColumnType("", "", nil, false))
							// NotNullExplicit stays FALSE: legacy sets it only
							// for a NOT NULL written on the column itself.
							ncd.NotNull = true
							ncd.NotNullNoInherit = e.notNull.noInherit
							ncd.NotNullConstraintName = e.notNull.name
							cols = append(cols, *ncd)
							bodyOrder = append(bodyOrder, e.notNull.col)
							}
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
								namedChecks = append(namedChecks, PartitionCheckConstraint{Name: e.checkName, Expr: e.check, NoInherit: e.checkNoInh, NotEnforced: e.checkNotEnf})
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
				tbl := objectNameFromQn($5)
 	ct := NewCreateTableStmt(0, tbl, cols, pk)
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
				applyCtWith(ct, tail.withKv)
				ct.Inherits = tail.inherits
				ct.PartitionBy = tail.partition
				if tail.partOf.Name != "" {
					ct.PartitionOf = NewPartitionOfClause(tail.partOf, tail.fromVals, tail.toVals, tail.inVals, tail.bDefault)
					SetPartitionOfHashBound(ct.PartitionOf, tail.modulus, tail.remainder, tail.isHash)
					applyPartOfElems(ct.PartitionOf, tail.partOfElems)
				}
				ct.SelectSource = tail.asSelect
				$$ = ct
			}
	| CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name ct_tail_list opt_tablespace
			{
				tbl := objectNameFromQn($5)
				ct := NewCreateTableStmt(0, tbl, nil, nil)
				ct.IfNotExists = $4
				if pfx := $2.(*createPrefix); pfx != nil {
					ct.Temporary = pfx.temporary
					ct.Unlogged = pfx.unlogged
				}
				tail := $6
				ct.Tablespace = $7
				ct.OnCommit = tail.onCommit
				applyCtWith(ct, tail.withKv)
				ct.Inherits = tail.inherits
				ct.PartitionBy = tail.partition
				if tail.partOf.Name != "" {
					ct.PartitionOf = NewPartitionOfClause(tail.partOf, tail.fromVals, tail.toVals, tail.inVals, tail.bDefault)
					SetPartitionOfHashBound(ct.PartitionOf, tail.modulus, tail.remainder, tail.isHash)
					applyPartOfElems(ct.PartitionOf, tail.partOfElems)
				}
				ct.SelectSource = tail.asSelect
				/* This arm exists for the column-list-LESS spellings —
				   PARTITION OF and the tails. A CREATE TABLE with NO column
				   list and NO partition parent is not one of gram.y's three
				   CreateStmt alternatives: `CREATE TABLE t` and
				   `CREATE TABLE t WITH (fillfactor=70)` are syntax errors, and
				   an empty ct_tail_list made them parse. ddl.go says
				   "expected '(' ". */
				if ct.PartitionOf == nil {
					raiseErr(yylex, &SyntaxError{Pos: lastTokPos(yylex), Message: "expected '(' (got end of input)"})
				}
				$$ = ct
			}

	/* Plain CTAS, `CREATE TABLE t [USING am] [WITH (...)] AS query`, lives HERE
	   over ct_tail_list rather than in create_table_stmt_as: its optional
	   USING / WITH prefix and the no-column CREATE's tail start in the same
	   state, and two separate rules for them reduce/reduce on WITH. */
	| CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name ct_tail_list AS ctas_source opt_ctas_with_data
			{
				src := $8.(*ctasSrc)
				ct := NewCreateTableStmt(0, objectNameFromQn($5), nil, nil)
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
	| CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name OF qualified_name opt_of_elements
			{
				ct := NewCreateTableStmt(0, objectNameFromQn($5), nil, nil)
				if pfx := $2.(*createPrefix); pfx != nil {
					ct.Temporary = pfx.temporary
					ct.Unlogged = pfx.unlogged
				}
				ct.IfNotExists = $4
				ot := objectNameFromQn($7)
				ct.OfType = &ot
				applyOfElements(ct, $8)
				$$ = ct
			}

part_bound_spec2:
		FOR VALUES IN_P '(' expr_list ')'
			{
				$$ = &partBound{inVals: $5}
			}
	/* A range bound's elements are NOT plain expressions: bare MINVALUE and
	   MAXVALUE are sentinels with their own node, and both are unreserved
	   words that a_expr would otherwise read as column references. */
	| FOR VALUES FROM '(' expr_list ')' TO '(' expr_list ')'
			{
				$$ = &partBound{from: partBoundValues(yylex, $5), to: partBoundValues(yylex, $9)}
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
				cs := &colSpec{name: $1, namePos: $<p>1, typePos: $<p>2}
				tw := $2.(*typeWithArgs)
				// cast_typename folds the array brackets INTO the name
				// (castType.withArrays); ColumnType keeps them in a separate
				// flag with the ELEMENT name, so they must be split back out.
				// Detecting the suffix without stripping it left `text[]` as a
				// type literally named "text[]" on 36 regress fragments.
				ct := colTypeOf(yylex, tw, $<p>2)
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
				cs.fkNotValid, cs.fkNotEnforced = cc.fkNotValid, cc.fkNotEnforced
				cs.nullsNotDistinct = cc.nullsNotDistinct
				cs.deferrable, cs.initiallyDeferred = cc.deferrable, cc.initiallyDeferred
				cs.genExpr, cs.genAlways, cs.genVirtual = cc.genExpr, cc.genAlways, cc.genVirtual
				cs.identity, cs.identityAlways, cs.identitySeq = cc.identity, cc.identityAlways, cc.identitySeq
				cs.checkName, cs.nnName, cs.uqName = cc.checkName, cc.nnName, cc.uqName
				cs.collation, cs.compression = cc.collation, cc.compression
				cs.nnNoInherit, cs.checkNoInherit = cc.nnNoInherit, cc.checkNoInherit
				cs.checkNotEnforced, cs.storage = cc.checkNotEnforced, cc.storage
				cs.nnOccur = cc.nnOccur
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
				w, _ := $7.(Expr) // opt_exclude_where boxes an untyped nil
				$$ = &tableElem{exclusion: newExclusionConstraint("", $2, $4.([]excludeElem), $6, w, a)}
			}
	| CONSTRAINT ColId EXCLUDE opt_using_method '(' exclude_elem_list ')' opt_include opt_exclude_where opt_constr_attrs
			{
				a, _ := $10.(*constrAttrs)
				w, _ := $9.(Expr)
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
				fk := &TableForeignKeyDef{
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
					fk.NotValid, fk.NotEnforced = a.notValid, a.notEnforced
				}
				$$ = &tableElem{fkDef: fk}
			}
	| CONSTRAINT ColId FOREIGN KEY '(' colid_list ')' REFERENCES qualified_name opt_ref_cols opt_fk_match opt_fk_actions opt_constr_attrs
			{
				fk := &TableForeignKeyDef{
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
					fk.NotValid, fk.NotEnforced = a.notValid, a.notEnforced
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
		/* empty */        { $$ = (Expr)(nil) }
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
					$$ = &typeWithArgs{ct: ct, args: implicitCharLen(yylex, $<p>1, ct), quoted: isQuotedIdentAt(yylex, $<p>1)}
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
				tbl := objectNameFromQn($5)
				ct := NewCreateTableStmt(0, tbl, nil, nil)
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
				sel, _ := $1.(*SelectStmt)
				$$ = &ctasSrc{sel: sel}
			}
	| EXECUTE ColId opt_execute_params
			{
				$$ = &ctasSrc{exec: NewExecuteStmt($<p>1, $2, $3)}
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

/* A typed table's element list takes TABLE CONSTRAINTS as well as the
   `col WITH OPTIONS ...` entries, and may be EMPTY — `CREATE TABLE e OF et ()`
   is legal. Constraints reuse table_element so PRIMARY KEY / UNIQUE / CHECK
   land in the same fields they would on an ordinary CREATE TABLE. */
opt_of_elements:
		/* empty */                    { $$ = []*tableElem(nil) }
	| '(' ')'                          { $$ = []*tableElem{} }
	| '(' of_element_list ')'          { $$ = $2 }

of_element_list:
		of_element                       { $$ = []*tableElem{$1.(*tableElem)} }
	| of_element_list ',' of_element     { $$ = append($1, $3.(*tableElem)) }

of_element:
		of_col_opt        { cd := $1.(ColumnDef); $$ = &tableElem{ofCol: &cd} }
	| table_element       { $$ = $1 }

of_col_opt:
		ColId WITH OPTIONS col_constraints
			{
				cc := $4.(*colConstraints)
				cd := NewColumnDef($1, NewColumnType("", "", nil, false))
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
				$$ = NewDropTableStmt(0, $3, $4, dropBehavior($5))
			}

opt_if_exists_drop:
		/* empty */   { $$ = false }
	| IF_P EXISTS     { $$ = true }

drop_name_list:
		qualified_name                         { $$ = []ObjectName{objectNameFromQn($1)} }
	| drop_name_list ',' qualified_name        { $$ = append($1, objectNameFromQn($3)) }

opt_drop_behavior:
		/* empty */  { $$ = "" }
	| CASCADE       { $$ = "cascade" }
	| RESTRICT      { $$ = "restrict" }

truncate_stmt:
		TRUNCATE opt_TRUNCATE_kw trunc_name_list opt_restart opt_drop_behavior
			{
				tt := $3.(*truncTargets)
				$$ = NewTruncateStmt(0, tt.names, tt.only, dropBehavior($5), $4)
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
				$$ = &truncTargets{names: []ObjectName{objectNameFromQn($1)}, only: []bool{false}}
			}
	| ONLY qualified_name
			{
				$$ = &truncTargets{names: []ObjectName{objectNameFromQn($2)}, only: []bool{true}}
			}

/* A partition may itself be partitioned: `... PARTITION OF p FOR VALUES ...
   PARTITION BY RANGE (c)`. gram.y hangs OptPartitionSpec off the same
   CreateStmt as the bound, and ctTail already carries both fields. */
opt_subpartition_by:
		/* empty */   { $$ = (*PartitionByClause)(nil) }
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
	/* PRESERVE ROWS is the DEFAULT, and legacy records an EMPTY string for it
	   rather than the words. */
	| ON COMMIT PRESERVE ROWS
			{ $$ = &ctTail{} }
	| PARTITION BY ColId '(' part_elem_list ')'
			{
				i := &ctTail{}; i.partition = partitionByFrom($3, $<p>3, $5.([]partKey)); $$ = i
			}
	| PARTITION OF qualified_name part_bound_spec2
			{
				par := objectNameFromQn($3)
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
	| ColId                                { $$ = $1 }                           /* WITH (security_barrier); RESET (n_distinct) */

with_value:
		SCONST   { $$ = $1 }
	| ICONST     { $$ = yylex.(*lexerState).lastText }

	| FCONST     { $$ = yylex.(*lexerState).lastText }

	| TRUE_P     { $$ = "true" }
	| FALSE_P    { $$ = "false" }
	| ON         { $$ = "on" }
	| ColId      { $$ = $1 } /* covers off, heap, ... */
	| '-' with_value { $$ = "-" + $2 } /* n_distinct = -0.5 */
	/* An explicit plus sign is legal (`autovacuum_vacuum_cost_delay = +5`) but
	   is NOT kept: ddl.go's reloption scanner records a sign only for '-', so
	   the stored text is "5". Only '-' is meaningful downstream (the valid
	   floor of log_autovacuum_min_duration is -1). */
	| '+' with_value { $$ = $2 }

/* create_index_stmt / drop_index_stmt — P4.4 (gram.y IndexStmt / DropStmt
   subsets). v0: plain column keys (expressions, DESC/NULLS, opclasses and
   CONCURRENTLY arrive later); ColOrders/ColExprs filled with per-column
   defaults for legacy dump parity. */
create_index_stmt:
		CREATE opt_unique INDEX opt_concurrently opt_if_not_exists opt_index_name ON opt_ONLY_kw qualified_name opt_using_method '(' index_col_list ')' opt_include idx_tail_list opt_index_where
			{
				tbl := objectNameFromQn($9)
				elems := $12.([]indexElem)
				cols := make([]string, len(elems))
				exprs := make([]Expr, len(elems))
				orders := make([]IndexColOrder, len(elems))
				withOpts := ""
				for i, e := range elems {
					cols[i], exprs[i], orders[i] = e.name, e.expr, e.order
					if withOpts == "" {
						withOpts = e.optsOpClass
					}
				}
				ix := NewCreateIndexStmt(0, $2, $5, $6, tbl, $10, cols)
				ix.ColExprs = exprs
				ix.ColOrders = orders
				ix.OpClassWithOptions = withOpts
				ix.Concurrently = $4
				ix.OnOnly = $8
				ix.IncludeColumns = $14
				tl := $15.(*idxTail)
				ix.NullsNotDistinct = tl.nnd
				applyIndexOpts(ix, tl.opts)
				ix.Tablespace = tl.tablespace
				if p, _ := $16.(Expr); p != nil {
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

/* The three tail clauses in ANY ORDER, not the fixed sequence gram.y spells
   out. ddl.go scans them in a loop, so it accepts
   `WITH (...) NULLS NOT DISTINCT` — which upstream's fixed order does not —
   and goopg's own index-recovery DDL is written that way. A flat repeatable
   list matches the scanner and costs no conflict. */
idx_tail_list:
		/* empty */                       { $$ = &idxTail{} }
	| idx_tail_list idx_tail_item         { $$ = mergeIdxTail($1.(*idxTail), $2.(*idxTail)) }

idx_tail_item:
		NULLS_P DISTINCT                  { $$ = &idxTail{} }
	| NULLS_P NOT DISTINCT                { $$ = &idxTail{nnd: true} }
	| WITH '(' str_pair_list ')'          { $$ = &idxTail{opts: indexOptsFrom($3)} }
	| TABLESPACE ColId                    { $$ = &idxTail{tablespace: $2} }

/* opt_index_name — `CREATE INDEX ON t (a)` lets the server pick the name. ON
   is reserved and therefore never a ColId, so the empty case is unambiguous. */
opt_index_name:
		/* empty */        { $$ = "" }
	| ColId                { $$ = $1 }

opt_index_where:
		/* empty */        { $$ = (Expr)(nil) }
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
	| COLLATE collation_name { $$ = $2 }

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
				$$ = NewDropIndexStmt(0, $3, $4, $5, dropBehavior($6))
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
				b := NewBeginStmt($3)
				b.IsolationLevel, b.ReadOnly, b.Deferrable = m.iso, m.ro, m.def
				$$ = b
			}
	| START TRANSACTION begin_pos opt_tx_modes
			{
				m := $4.(*txModes)
				b := NewBeginStmt($3)
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

/* ZERO, like every other transaction-control statement ddl.go builds — and
   lastConsumedPos() here did not even name the leading keyword: these rules
   reduce after their trailing operand, so it pointed at the SCONST. */
tx_commit:
		COMMIT opt_transaction  { $$ = NewCommitStmt(0) }
	| END_P opt_transaction     { $$ = NewCommitStmt(0) }
	| COMMIT PREPARED SCONST    { $$ = NewCommitPreparedStmt(0, $3) }

tx_rollback:
		ROLLBACK opt_transaction { $$ = NewRollbackStmt(0) }
	| ABORT_P opt_transaction    { $$ = NewRollbackStmt(0) }
	| ROLLBACK PREPARED SCONST   { $$ = NewRollbackPreparedStmt(0, $3) }
	| ROLLBACK opt_transaction TO opt_savepoint_kw ColId
			{ $$ = NewRollbackToSavepointStmt(0, $5) }

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
			{ $$ = NewSetConstraintsStmt(0, true, nil, $4) }
	| SET CONSTRAINTS constr_name_list constraints_set_mode
			{ $$ = NewSetConstraintsStmt(0, false, $3, $4) }
	| SET set_scope set_guc_name set_eq_to set_value_list
			{
				// One alternative, not two: `SET x = DEFAULT` differs from
				// `SET x = 'default'` only by token KIND, which the grammar
				// cannot see — a separate DEFAULT alternative would reduce/reduce
				// against the permissive value list below. setValueIsDefault
				// inspects the token instead.
				l := yylex.(*lexerState)
				$$ = NewSetStmt(0, $2, $3, l.setValueAtoms(), l.setValueIsDefault())
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
			{ $$ = roleSetStmt($2, $4, isDefaultKeywordAt(yylex, $<p>4)) }

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
				$$ = NewSetTransactionStmt(0, m.iso, $2)
			}

set_scope:
		/* empty */   { $$ = false }
	| SESSION          { $$ = false }
	| LOCAL            { $$ = true }

set_eq_to:
		'='           { $$ = "=" }
	| TO              { $$ = "to" }

show_stmt:
		SHOW ALL       { $$ = NewShowStmt(0, true, "") }
	| SHOW set_guc_name { $$ = NewShowStmt(0, false, $2) }
	| SHOW TIME ZONE   { $$ = NewShowStmt(0, false, "timezone") }

reset_stmt:
		RESET ALL      { $$ = NewResetStmt(0, true, "") }
	| RESET set_guc_name { $$ = NewResetStmt(0, false, $2) }
	| RESET TIME ZONE    { $$ = NewResetStmt(0, false, "timezone") }
	/* RESET SESSION AUTHORIZATION — gram.y's dedicated VariableResetStmt
	   alternative. AUTHORIZATION is a reserved keyword, so `RESET ColId`
	   cannot reach it; legacy normalises the pair to the GUC's real name. */
	| RESET SESSION AUTHORIZATION
			{ $$ = NewResetStmt(0, false, "session_authorization") }

/* alter_table_stmt — P4.2 v0 (gram.y AlterTableStmt subset): single action
   of ADD COLUMN / ADD PRIMARY KEY / DROP COLUMN / ALTER COLUMN TYPE /
   RENAME TO. Multi-action lists, DROP DEFAULT/NOT NULL, SET forms and
   partition actions arrive in later slices. */
alter_table_stmt:
		ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star alter_action_list
			{
				acts := $7.([]AlterTableAction)
				st := NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.Only = $4
				st.Actions = acts
				$$ = st
			}
	/* ENABLE / DISABLE [ALWAYS|REPLICA] TRIGGER — legacy records only a
	   statement-level flag, with no action and no trigger name. */
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star trigger_toggle
			{
				st := NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.Only = $4
				st.EnableDisableTrigger = true
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star OWNER TO ColId
			{
				st := NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.Only = $4
				st.OwnerTo = $9
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star SET SCHEMA ColId
			{
				st := NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.Only = $4
				st.SetSchema = $9
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star SET LOGGED
			{
				st := NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.SetLogged = "logged"
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star SET UNLOGGED
			{
				st := NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				st.SetLogged = "unlogged"
				$$ = st
			}
	| ALTER TABLE opt_if_exists_drop opt_ONLY_kw qualified_name opt_inh_star SET '(' str_pair_list ')'
			{
				st := NewAlterTableStmt(0, objectNameFromQn($5))
				st.IfExists = $3
				a := NewATActionAt(AlterTableSetReloptions, $<p>7)
				m := map[string]string{}
				for _, kv := range $9 {
					parts := splitKV(kv)
					if len(parts) == 2 {
						m[parts[0]] = parts[1]
					}
				}
				a.With = m
				st.Actions = []AlterTableAction{*a}
				$$ = st
			}

alter_action_list:
		alter_table_action
			{
				$$ = []AlterTableAction{*($1.(*AlterTableAction))}
			}
	| alter_action_list ',' alter_table_action
			{
				$$ = append($$.([]AlterTableAction), *($3.(*AlterTableAction)))
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
				cd := NewColumnDefAt($<p>2, $<p>3, $2, colTypeOf(yylex, ct, $<p>3))
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
				applyNotNullOccurrences(yylex, cd, cc.nnOccur)
				a := NewATActionAt(AlterTableAddColumn, $<p>2)
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
				cd := NewColumnDefAt($<p>3, $<p>4, $3, colTypeOf(yylex, ct, $<p>4))
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
				applyNotNullOccurrences(yylex, cd, cc.nnOccur)
				a := NewATActionAt(AlterTableAddColumn, $<p>2)
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
				cd := NewColumnDefAt($<p>5, $<p>6, $5, colTypeOf(yylex, ct, $<p>6))
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
				applyNotNullOccurrences(yylex, cd, cc.nnOccur)
				a := NewATActionAt(AlterTableAddColumn, $<p>2)
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
				cd := NewColumnDefAt($<p>6, $<p>7, $6, colTypeOf(yylex, ct, $<p>7))
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
				applyNotNullOccurrences(yylex, cd, cc.nnOccur)
				a := NewATActionAt(AlterTableAddColumn, $<p>2)
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
	/* ddl.go anchors an ADD-constraint action at the token right AFTER ADD —
	   the constraint keyword, or CONSTRAINT for the named spelling — so the
	   position is stamped here, where that token is on the stack. */
	| ADD_P at_constraint
			{ $$ = atActionAt($2, $<p>2) }
	| ADD_P CONSTRAINT ColId at_constraint
			{
				a := atActionAt($4, $<p>2)
				a.ConstraintName = $3
				$$ = a
			}
	| DROP opt_COLUMN ColId opt_drop_behavior
			{
				a := NewATActionAt(AlterTableDropColumn, $<p>3)
				a.ColumnName = $3 // CASCADE / RESTRICT: parsed and dropped, as legacy
				$$ = a
			}
	| DROP opt_COLUMN IF_P EXISTS ColId opt_drop_behavior
			{
				a := NewATActionAt(AlterTableDropColumn, $<p>5)
				a.ColumnName = $5
				a.IfExists = true
				$$ = a
			}
	| ALTER opt_COLUMN ColId TYPE_P col_type_name opt_at_using
			{
				ct := $5.(*typeWithArgs)
				a := NewATActionAt(AlterTableAlterColumnType, $<p>3)
				a.ColumnName = $3
				a.NewType = colTypeOf(yylex, ct, $<p>5)
				a.UsingExpr, _ = $6.(Expr)
				$$ = a
			}
	| RENAME TO ColId
			{
				a := NewATActionAt(AlterTableRenameTable, $<p>3)
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
				a := NewATActionAt(AlterTableDropConstraint, $<p>4)
				a.ConstraintName = $4
				a.IfExists = $3
				a.Restrict = $5 != "cascade"
				$$ = a
			}
	| ALTER opt_COLUMN ColId SET DEFAULT a_expr
			{
				a := NewATAction(AlterTableSetDefault)
				a.ColumnName = $3
				a.DefaultExpr = $6
				$$ = a
			}
	| ALTER opt_COLUMN ColId DROP DEFAULT
			{
				a := NewATAction(AlterTableDropDefault)
				a.ColumnName = $3
				$$ = a
			}
	| ALTER opt_COLUMN ColId SET NOT NULL_P
			{
				a := NewATAction(AlterTableSetNotNull)
				a.ColumnName = $3
				$$ = a
			}
	| ALTER opt_COLUMN ColId DROP NOT NULL_P
			{
				a := NewATAction(AlterTableDropNotNull)
				a.ColumnName = $3
				$$ = a
			}
	| RENAME opt_COLUMN ColId TO ColId
			{
				a := NewATActionAt(AlterTableRenameColumn, $<p>3)
				a.OldColumnName = $3
				a.NewName = $5
				$$ = a
			}
	| VALIDATE CONSTRAINT ColId
			{
				// ConstraintName, not OldConstraintName (legacy ddl.go:9960).
				a := NewATActionAt(AlterTableValidateConstraint, $<p>3)
				a.ConstraintName = $3
				$$ = a
			}
	| REPLICA IDENTITY_P FULL
			{
				a := NewATActionAt(AlterTableReplicaIdentity, $<p>1)
				a.ReplicaIdentityMode = "f"
				$$ = a
			}
	| REPLICA IDENTITY_P NOTHING
			{
				a := NewATActionAt(AlterTableReplicaIdentity, $<p>1)
				a.ReplicaIdentityMode = "n"
				$$ = a
			}
	| REPLICA IDENTITY_P DEFAULT
			{
				a := NewATActionAt(AlterTableReplicaIdentity, $<p>1)
				a.ReplicaIdentityMode = "d"
				$$ = a
			}
	| REPLICA IDENTITY_P USING INDEX ColId
			{
				a := NewATActionAt(AlterTableReplicaIdentity, $<p>1)
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
				a := NewATAction(AlterTableSetStatistics)
				a.ColumnName = $3
				a.CheckExpr = atStatValue($6) // legacy stores the number as TEXT
				$$ = a
			}
	| ALTER opt_COLUMN ColId SET STORAGE ColId
			{
				a := NewATAction(AlterTableSetStorage)
				a.ColumnName, a.StorageType = $3, $6
				$$ = a
			}
	| ALTER opt_COLUMN ColId SET COMPRESSION ColId
			{
				a := NewATAction(AlterTableSetCompression)
				a.ColumnName, a.CompressionType = $3, $6
				$$ = a
			}
	/* `SET COMPRESSION default` — legacy records an EMPTY method for it. */
	| ALTER opt_COLUMN ColId SET COMPRESSION DEFAULT
			{
				a := NewATAction(AlterTableSetCompression)
				a.ColumnName = $3
				$$ = a
			}
	| ALTER opt_COLUMN ColId SET '(' str_pair_list ')'
			{
				a := NewATAction(AlterTableAlterColumnSet)
				a.ColumnName, a.SetOptions = $3, $6
				$$ = a
			}
	| ALTER opt_COLUMN ColId RESET '(' str_pair_list ')'
			{
				a := NewATAction(AlterTableAlterColumnReset)
				a.ColumnName, a.SetOptions = $3, $6
				$$ = a
			}
	| ALTER opt_COLUMN ColId DROP EXPRESSION opt_if_exists_drop
			{ $$ = NewATAction(AlterTableNoOp) }
	/* SET EXPRESSION / SET DATA TYPE / the identity-sequence tweaks are all
	   NoOp in legacy — note SET DATA TYPE differs from plain TYPE, which does
	   record AlterColumnType. Reproduced as legacy has it. */
	| ALTER opt_COLUMN ColId SET EXPRESSION AS '(' a_expr ')'
			{ $$ = NewATAction(AlterTableNoOp) }
	| ALTER opt_COLUMN ColId SET DATA_P TYPE_P col_type_name opt_at_using
			{ $$ = NewATAction(AlterTableNoOp) }
	| ALTER opt_COLUMN ColId RESTART opt_restart_with
			{ $$ = NewATAction(AlterTableNoOp) }
	| ALTER opt_COLUMN ColId SET identity_seq_opt
			{ $$ = NewATAction(AlterTableNoOp) }
	| ALTER opt_COLUMN ColId SET GENERATED ALWAYS seq_tweaks
			{ $$ = NewATAction(AlterTableNoOp) }
	| ALTER opt_COLUMN ColId SET GENERATED BY DEFAULT seq_tweaks
			{ $$ = NewATAction(AlterTableNoOp) }
	| ALTER opt_COLUMN ColId DROP IDENTITY_P opt_if_exists_drop
			{ $$ = NewATAction(AlterTableNoOp) }
	| ALTER opt_COLUMN ColId ADD_P GENERATED ALWAYS AS IDENTITY_P opt_identity_seq_opts
			{ $$ = NewATAction(AlterTableNoOp) }
	| ALTER opt_COLUMN ColId ADD_P GENERATED BY DEFAULT AS IDENTITY_P opt_identity_seq_opts
			{ $$ = NewATAction(AlterTableNoOp) }
	/* ALTER CONSTRAINT name [NOT] DEFERRABLE [INITIALLY ...] — legacy records
	   the three flags plus a marker that a deferrability clause was written. */
	| ALTER CONSTRAINT ColId at_constr_tail
			{
				a := NewATActionAt(AlterTableAlterConstraint, $<p>3)
				a.ConstraintName = $3
				t := mustATTail($4)
				/* gram.y:2672-2676 rejects CAS_NOT_VALID inside
				   ConstraintAttributeSpec when the spec belongs to ALTER
				   CONSTRAINT — a PARSE-time 0A000, not an executor check. */
				if t.notValid {
					raiseErr(yylex, &SyntaxError{
						Pos: t.notValidPos, Raw: true, Code: "0A000",
						Message: "constraints cannot be altered to be NOT VALID",
					})
				}
				$$ = applyAlterConstraint(a, t)
			}
	/* Table-level SET / RESET / CLUSTER. RESET's option names ride in With,
	   the same map SET fills — ddl.go:10051 builds both kinds through one
	   `AlterTableAction{pos: pos, Kind: kind, With: opts}`. Dropping them
	   made `RESET (parallel_workers)` a no-op that left the reloption in
	   pg_class. */
	| RESET '(' str_pair_list ')'
			{
				a := NewATActionAt(AlterTableResetReloptions, $<p>1)
				a.With = strPairMap($3)
				$$ = a
			}
	| SET ACCESS METHOD ColId
			{
				a := NewATActionAt(AlterTableSetAccessMethod, $<p>1)
				a.AccessMethodName = $4
				$$ = a
			}
	| SET TABLESPACE ColId
			{
				a := NewATActionAt(AlterTableSetTablespace, $<p>3)
				a.TablespaceName = $3
				$$ = a
			}
	| SET WITHOUT OIDS
			{ $$ = NewATAction(AlterTableNoOp) }
	| SET WITHOUT CLUSTER
			{ $$ = NewATActionAt(AlterTableSetWithoutCluster, $<p>1) }
	| CLUSTER ON ColId
			{
				a := NewATActionAt(AlterTableClusterOn, $<p>1)
				a.ClusterIndexName = $3
				$$ = a
			}
	| RENAME CONSTRAINT ColId TO ColId
			{
				a := NewATActionAt(AlterTableRenameConstraint, $<p>3)
				a.OldConstraintName, a.NewName = $3, $5
				$$ = a
			}
	| INHERIT qualified_name
			{
				a := NewATAction(AlterTableInherit)
				a.InheritParent = objectNameFromQn($2)
				$$ = a
			}
	| NO INHERIT qualified_name
			{
				a := NewATAction(AlterTableNoInherit)
				a.InheritParent = objectNameFromQn($3)
				$$ = a
			}
	| OF qualified_name
			{
				a := NewATAction(AlterTableAddOf)
				a.OfType = objectNameFromQn($2)
				$$ = a
			}
	| NOT OF
			{ $$ = NewATAction(AlterTableDropOf) }
	| ENABLE_P rule_state RULE ColId
			{
				a := NewATAction(AlterTableEnableDisableRule)
				a.RuleName, a.RuleEnabledState = $4, byte($2)
				$$ = a
			}
	| DISABLE_P RULE ColId
			{
				a := NewATAction(AlterTableEnableDisableRule)
				a.RuleName, a.RuleEnabledState = $3, byte('D')
				$$ = a
			}
	| ENABLE_P ROW LEVEL SECURITY
			{ $$ = NewATAction(AlterTableEnableRowSecurity) }
	| DISABLE_P ROW LEVEL SECURITY
			{ $$ = NewATAction(AlterTableDisableRowSecurity) }
	| FORCE ROW LEVEL SECURITY
			{ $$ = NewATAction(AlterTableForceRowSecurity) }
	| NO FORCE ROW LEVEL SECURITY
			{ $$ = NewATAction(AlterTableNoForceRowSecurity) }
	| ATTACH PARTITION qualified_name part_bound_spec2
			{
				b := $4.(*partBound)
				a := NewATAttachPartition(0, objectNameFromQn($3), b.from, b.to, b.inVals, b.isDefault)
				SetPartitionOfHashBound(a.AttachPartitionOf, b.modulus, b.remainder, b.isHash)
				$$ = a
			}
	| DETACH PARTITION qualified_name opt_detach_tail
			{
				a := NewATDetachPartition(0, objectNameFromQn($3))
				a.DetachConcurrently = $4
				$$ = a
			}

/* One table constraint as ALTER TABLE ADD takes it. Every alternative ends in
   at_constr_tail, the FLAT trailing-word list (see atConstrTail). */
at_constraint:
		PRIMARY KEY pk_cols opt_include at_constr_tail
			{
				a := NewATActionAt(AlterTableAddPrimaryKey, $<p>1)
				a.Columns, a.IncludeColumns = $3, $4
				$$ = applyATTail(a, mustATTail($5))
			}
	| PRIMARY KEY USING INDEX ColId at_constr_tail
			{
				a := NewATActionAt(AlterTableAddPrimaryKey, $<p>1)
				a.UsingIndexName = $5
				$$ = applyATTail(a, mustATTail($6))
			}
	| UNIQUE uq_cols opt_include at_constr_tail
			{
				a := NewATActionAt(AlterTableAddUnique, $<p>1)
				a.Columns, a.IncludeColumns = $2, $3
				$$ = applyATTail(a, mustATTail($4))
			}
	| UNIQUE USING INDEX ColId at_constr_tail
			{
				a := NewATActionAt(AlterTableAddUnique, $<p>1)
				a.UsingIndexName = $4
				$$ = applyATTail(a, mustATTail($5))
			}
	| check_body at_constr_tail
			{
				a := NewATActionAt(AlterTableAddCheck, $<p>1)
				a.CheckExpr = $1
				$$ = applyATCheckTail(a, mustATTail($2))
			}
	| FOREIGN KEY '(' colid_list ')' REFERENCES qualified_name opt_ref_cols opt_fk_match opt_fk_actions at_constr_tail
			{
				a := NewATActionAt(AlterTableAddForeignKey, $<p>1)
				a.Columns = $4
				a.RefTable, a.RefColumns = objectNameFromQn($7), $8
				a.MatchFull = $9
				acts := $10.(*fkActs)
				a.OnDelete, a.OnUpdate, a.OnDeleteSetCols = acts.del, acts.up, acts.delSetCols
				$$ = applyATTail(a, mustATTail($11))
			}
	| EXCLUDE opt_using_method '(' exclude_elem_list ')' opt_include opt_exclude_where at_constr_tail
			{
				a := NewATActionAt(AlterTableAddExclude, $<p>1)
				d := newExclusionConstraint("", $2, $4.([]excludeElem), $6, nil, nil)
				a.Columns, a.ExclusionOp, a.ExclusionMethod = d.Columns, d.ExclusionOp, d.Method
				a.ExclusionWhere, _ = $7.(Expr)
				$$ = applyATTail(a, mustATTail($8))
			}
	| NOT NULL_P ColId at_constr_tail
			{
				a := NewATActionAt(AlterTableAddNotNull, $<p>1)
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
		/* empty */    { $$ = (Expr)(nil) }
	| USING a_expr     { $$ = $2 }

at_constr_tail:
		/* empty */                        { $$ = (*atConstrTail)(nil) }
	| at_constr_tail DEFERRABLE            { $$ = mergeATTail(mustATTail($1), "deferrable") }
	| at_constr_tail NOT DEFERRABLE        { $$ = mergeATTail(mustATTail($1), "not_deferrable") }
	| at_constr_tail INITIALLY DEFERRED    { $$ = mergeATTail(mustATTail($1), "initially_deferred") }
	| at_constr_tail INITIALLY IMMEDIATE   { $$ = mergeATTail(mustATTail($1), "initially_immediate") }
	/* NOT VALID is LEGAL here — `ALTER TABLE t ADD CHECK (...) NOT VALID`
	   shares this tail. Only ALTER CONSTRAINT rejects it, and that check
	   therefore lives in that arm, not in this shared rule. */
	| at_constr_tail NOT VALID             { $$ = mergeATTailAt(mustATTail($1), "not_valid", $<p>2) }
	| at_constr_tail NOT ENFORCED          { $$ = mergeATTail(mustATTail($1), "not_enforced") }
	| at_constr_tail ENFORCED              { $$ = mergeATTail(mustATTail($1), "enforced") }
	| at_constr_tail NO INHERIT            { $$ = mergeATTail(mustATTail($1), "no_inherit") }
	| at_constr_tail INHERIT               { $$ = mergeATTail(mustATTail($1), "inherit") }

opt_COLUMN:
		/* empty */  { _ = 0 }
	| COLUMN         { _ = 0 }

/* create_view_stmt — P5 v0: CREATE [OR REPLACE] [TEMP] VIEW name [(cols)] AS select */
/* Two alternatives, not `CREATE opt_or_replace opt_create_modifier VIEW`: with
   both optional nonterminals in front, a TEMP after CREATE had to choose
   between reducing an empty opt_or_replace (view path) and shifting into
   opt_create_modifier (table path), and shift won — which killed
   `CREATE TEMP VIEW` outright. Sharing the modifier position with
   create_table_stmt removes the choice. */
create_view_stmt:
		CREATE opt_create_modifier VIEW qualified_name opt_name_list_p opt_view_with AS { yylex.(*lexerState).markSpanStart() } select_bare opt_check_option
			{ $$ = buildView(yylex, false, $2, $4, $5, $6, $9, $10) }
	| CREATE OR REPLACE opt_create_modifier VIEW qualified_name opt_name_list_p opt_view_with AS { yylex.(*lexerState).markSpanStart() } select_bare opt_check_option
			{ $$ = buildView(yylex, true, $4, $6, $7, $8, $11, $12) }

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
				$$ = NewDropViewStmt(0, $3, $4, dropBehavior($5))
			}

/* create_matview_stmt — P5 v0: CREATE MATERIALIZED VIEW [IF NOT EXISTS] qn
   [(aliases)] AS select [WITH [NO] DATA]. USING/WITH (opts)/TABLESPACE
   deferred (legacy fallback covers them via the modifier-fallback rule). */
create_matview_stmt:
		CREATE MATERIALIZED VIEW opt_if_not_exists qualified_name opt_name_list_p opt_table_am AS { yylex.(*lexerState).markSpanStart() } select_bare opt_with_data
			{
				v := objectNameFromQn($5)
				sel := $10.(*SelectStmt)
				cv := NewCreateMatViewStmt(v, $6, sel)
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
				st := NewRefreshMatViewStmt(0, objectNameFromQn($5))
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
				$$ = NewDropCompatStmt(0, "materialized view", $4, $5, dropBehavior($6))
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
				st := NewCreateFunctionStmt($<p>1, $2, objectNameFromQn($4), $6)
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
				st := NewCreateProcedureStmt($<p>1, $2, objectNameFromQn($4), $6)
				$$ = applyProcAttrs(st, a)
			}

fn_return:
		fn_type
			{ $$ = &fnReturn{typ: colTypeOf(yylex, $1.(*typeWithArgs), $<p>1)} }
	| SETOF fn_type
			{ $$ = &fnReturn{typ: colTypeOf(yylex, $2.(*typeWithArgs), $<p>2), setof: true} }
	/* Legacy records RETURNS TABLE as `SETOF record` plus the columns folded
	   into trailing OUT arguments (function.go), not as a distinct type. */
	| TABLE '(' func_arg_list_p ')'
			{ $$ = &fnReturn{typ: NewColumnType("", "record", nil, false), setof: true, table: true, cols: $3} }

/* An absent list and an empty one are different in the AST: legacy returns nil
   for "no parens at all" (DROP FUNCTION f) and a non-nil empty slice for `()`. */
opt_func_args:
		/* empty */        { $$ = []FunctionArg{} }
	| func_arg_list_p      { $$ = $1 }

func_arg_list_p:
		func_arg                        { $$ = []FunctionArg{$1.(FunctionArg)} }
	| func_arg_list_p ',' func_arg      { $$ = append($1, $3.(FunctionArg)) }

/* [mode] [name] type [ {DEFAULT|=} expr ]. The name is optional AND a mode
   keyword may precede it, so the four combinations are spelled out: hiding
   either behind an optional nonterminal would reduce an empty rule right after
   `(` and decide one token too early. */
func_arg:
		fn_type opt_arg_default
			{ $$ = NewFunctionArgAt($<p>1, "", FuncArgIn, false, colTypeOf(yylex, $1.(*typeWithArgs), $<p>1), $2) }
	| fn_param_name fn_type opt_arg_default
			{ $$ = NewFunctionArgAt($<p>1, $1, FuncArgIn, false, colTypeOf(yylex, $2.(*typeWithArgs), $<p>2), $3) }
	| fn_arg_mode fn_type opt_arg_default
			{ $$ = NewFunctionArgAt($<p>1, "", FuncArgMode($1), true, colTypeOf(yylex, $2.(*typeWithArgs), $<p>2), $3) }
	| fn_arg_mode fn_param_name fn_type opt_arg_default
			{ $$ = NewFunctionArgAt($<p>1, $2, FuncArgMode($1), true, colTypeOf(yylex, $3.(*typeWithArgs), $<p>3), $4) }
	/* `name mode type` (`a OUT int`) as well as `mode name type`: legacy
	   accepts both orderings (function.go parseFunctionArg). */
	| fn_param_name fn_arg_mode fn_type opt_arg_default
			{ $$ = NewFunctionArgAt($<p>1, $1, FuncArgMode($2), true, colTypeOf(yylex, $3.(*typeWithArgs), $<p>3), $4) }

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
		IN_P        { $$ = int(FuncArgIn) }
	| OUT_P         { $$ = int(FuncArgOut) }
	| INOUT         { $$ = int(FuncArgInout) }
	| VARIADIC      { $$ = int(FuncArgVariadic) }

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
						a.configOps = append(a.configOps, NewFunctionConfigOp(false, false, n, v))
					}
				}
			}
	| RESET ALL                   { $$ = func(a *fnAttrs) { a.configOps = append(a.configOps, NewFunctionConfigOp(false, true, "", "")) } }
	| RESET fn_config_name        { n := $2; $$ = func(a *fnAttrs) { a.configOps = append(a.configOps, NewFunctionConfigOp(true, false, n, "")) } }

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
				it := $4.(*DropFunctionItem)
				$$ = NewDropFunctionStmt($<p>1, $3, it.Name, it.Args, fnDropBehavior($6), $5)
			}
	| DROP PROCEDURE opt_if_exists_drop fn_drop_item fn_drop_extras opt_drop_behavior
			{
				it := $4.(*DropFunctionItem)
				/* ObjKind is set for ROUTINE only; DROP PROCEDURE leaves it empty
				   (ddl.go:5948 is the sole writer). */
				$$ = NewDropProcedureStmt($<p>1, $3, it.Name, dropProcNames($5), it.Args, fnDropBehavior($6), "")
			}
	| DROP ROUTINE opt_if_exists_drop fn_drop_item fn_drop_extras opt_drop_behavior
			{
				it := $4.(*DropFunctionItem)
				$$ = NewDropProcedureStmt($<p>1, $3, it.Name, dropProcNames($5), it.Args, fnDropBehavior($6), "routine")
			}

fn_drop_item:
		qualified_name
			{ $$ = &DropFunctionItem{Name: objectNameFromQn($1)} }
	| qualified_name '(' opt_func_args ')'
			{ $$ = &DropFunctionItem{Name: objectNameFromQn($1), Args: $3} }

fn_drop_extras:
		/* empty */                        { $$ = []DropFunctionItem(nil) }
	| fn_drop_extras ',' fn_drop_item      { $$ = append($1, *$3.(*DropFunctionItem)) }

call_stmt:
		CALL qualified_name
			{ $$ = NewCallStmt($<p>1, objectNameFromQn($2), nil, nil) }
	| CALL qualified_name '(' opt_call_named_args ')'
			{
				ca := $4.(*namedCallArgs)
				$$ = NewCallStmt($<p>1, objectNameFromQn($2), ca.exprs, ca.names())
			}

opt_call_named_args:
		/* empty */                { $$ = &namedCallArgs{exprs: []Expr{}} }
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
		SAVEPOINT ColId          { $$ = NewSavepointStmt($<p>1, $2) }
	/* SAVEPOINT is unreserved and therefore also a ColId, which is exactly the
	   pinned S/R conflict on RELEASE: shift keeps the keyword reading. */
	| RELEASE SAVEPOINT ColId    { $$ = NewReleaseSavepointStmt($<p>1, $3) }
	| RELEASE ColId              { $$ = NewReleaseSavepointStmt($<p>1, $2) }

checkpoint_stmt:
		CHECKPOINT               { $$ = NewCheckpointStmt($<p>1) }

/* DISCARD ALL is a legacy REJECT ("syntax error at or near \"all\""), so ALL
   is not an alternative here; TEMPORARY normalises to TEMP. */
discard_stmt:
		DISCARD PLANS            { $$ = NewDiscardStmt($<p>1, "PLANS") }
	| DISCARD SEQUENCES          { $$ = NewDiscardStmt($<p>1, "SEQUENCES") }
	| DISCARD TEMP               { $$ = NewDiscardStmt($<p>1, "TEMP") }
	| DISCARD TEMPORARY          { $$ = NewDiscardStmt($<p>1, "TEMP") }

deallocate_stmt:
		DEALLOCATE ColId             { $$ = NewDeallocateStmt($<p>1, $2) }
	| DEALLOCATE PREPARE ColId       { $$ = NewDeallocateStmt($<p>1, $3) }
	| DEALLOCATE ALL                 { $$ = NewDeallocateStmt($<p>1, "") }
	| DEALLOCATE PREPARE ALL         { $$ = NewDeallocateStmt($<p>1, "") }

prepare_stmt:
		PREPARE ColId opt_prep_types AS stmt
			{ $$ = NewPrepareStmt($<p>1, $2, $3, $5) }
	/* PREPARE TRANSACTION 'gid' is a different statement sharing the keyword
	   (parser.go parsePrepare); TRANSACTION is reserved and never a ColId, so
	   the two cannot be confused. */
	| PREPARE TRANSACTION SCONST
			{ $$ = NewPrepareTransactionStmt($<p>1, $3) }

/* The declared parameter types are recorded as their plain names; nil when no
   list was written, which is why this is not an `opt_paren_list` of strings. */
opt_prep_types:
		/* empty */                  { $$ = []string(nil) }
	| '(' prep_type_list ')'         { $$ = $2 }

prep_type_list:
		fn_type                          { $$ = []string{typeNameOf($1)} }
	| prep_type_list ',' fn_type         { $$ = append($1, typeNameOf($3)) }

execute_stmt:
		EXECUTE ColId                        { $$ = NewExecuteStmt($<p>1, $2, nil) }
	| EXECUTE ColId '(' opt_func_call_args ')' { $$ = NewExecuteStmt($<p>1, $2, $4) }

close_stmt:
		CLOSE ColId              { $$ = NewCloseStmt($<p>1, $2) }
	| CLOSE ALL                  { $$ = NewCloseStmt($<p>1, "") }

/* DECLARE name [NO] SCROLL CURSOR [WITH|WITHOUT HOLD] FOR <stmt>. gram.y also
   accepts BINARY / INSENSITIVE / ASENSITIVE here; legacy rejects all three
   ("expected CURSOR"), so they stay rejected. */
/* The body is SelectStmt, not `stmt`: legacy's DECLARE calls parseSelect, and
   routing it through `stmt` would run intoWrap over it — turning
   `DECLARE c CURSOR FOR SELECT 1 INTO t` into a CREATE TABLE instead of the
   error legacy raises. */
declare_stmt:
		DECLARE ColId opt_cursor_scroll CURSOR opt_cursor_hold FOR SelectStmt
			{
				sel, _ := $7.(*SelectStmt)
				checkStrayInto(yylex, sel, "SELECT ... INTO is not allowed here", false)
				$$ = NewDeclareCursorStmt($<p>1, $2, $7)
			}

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
				v := $2.(*VacuumStmt)
				tg := $3.(*vacTargets)
				$$ = NewAnalyzeStmt($<p>1, v.Verbose, v.SkipLocked, tg.names, tg.cols)
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
		/* empty */                      { $$ = NewVacuumStmt(0) }
	| vacuum_kw_opts vacuum_kw_opt       { v := $1.(*VacuumStmt); $2.(func(*VacuumStmt))(v); $$ = v }

vacuum_kw_opt:
		VERBOSE      { $$ = func(v *VacuumStmt) { v.Verbose = true } }
	| analyze_kw     { $$ = func(v *VacuumStmt) { v.Analyze = true } }
	| FULL           { $$ = func(v *VacuumStmt) { v.Full = true } }
	| FREEZE         { $$ = func(v *VacuumStmt) { v.Freeze = true } }

vacuum_opt_list:
		vacuum_opt                          { v := NewVacuumStmt(0); $1.(func(*VacuumStmt))(v); $$ = v }
	| vacuum_opt_list ',' vacuum_opt        { v := $1.(*VacuumStmt); $3.(func(*VacuumStmt))(v); $$ = v }

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
			{ $$ = NewReindexStmt($<p>1, $2, $3 || $5, $4, qnText($7)) }

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
		CLUSTER opt_verbose_kw                                { $$ = NewClusterStmt($<p>1, $2, nil, "") }
	| CLUSTER opt_verbose_kw qualified_name opt_cluster_using
			{ n := objectNameFromQn($3); $$ = NewClusterStmt($<p>1, $2, &n, $4) }

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
			{ $$ = NewLockTableStmt($<p>1, $3, $4, $5) }

opt_TABLE_kw:
		/* empty */   { }
	| TABLE           { }

lock_rel_list:
		lock_rel                        { $$ = []LockTableRelation{$1} }
	| lock_rel_list ',' lock_rel        { $$ = append($1, $3) }

lock_rel:
		opt_ONLY_kw qualified_name      { n := objectNameFromQn($2); $$ = NewLockTableRelation(n.Schema, n.Name) }

opt_lock_mode:
		/* empty */                     { $$ = "AccessExclusiveLock" }
	| IN_P lock_mode_words MODE
			{
				n, ok := LockModeName($2)
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
			{ $$ = NewMergeStmt($<p>1, $3, $5, $7, $8, $9) }

opt_INTO_kw:
		/* empty */   { }
	| INTO            { }

/* Legacy loops `for p.cur() == WHEN`, so an empty clause list is accepted. */
merge_when_list:
		/* empty */                        { $$ = []*MergeWhenClause(nil) }
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
		MATCHED                      { $$ = NewMergeWhenClause(0, true, false, false) }
	| NOT MATCHED                    { $$ = NewMergeWhenClause(0, false, false, false) }
	| NOT MATCHED BY ColId           { $$ = mergeNotMatchedBy($4) }

opt_merge_and:
		/* empty */   { $$ = nil }
	| AND a_expr      { $$ = $2 }

merge_action:
		UPDATE SET update_set_list        { $$ = &mergeAction{kind: MergeActionUpdate, assigns: $3} }
	| DELETE_P                            { $$ = &mergeAction{kind: MergeActionDelete} }
	| DO NOTHING                          { $$ = &mergeAction{kind: MergeActionDoNothing} }
	/* VALUES_LA, not VALUES: base_yylex substitutes the lookahead variant
	   whenever VALUES is followed by '(' (base_yylex.go), so the plain token
	   never reaches this position. `DEFAULT VALUES` below keeps plain VALUES
	   because no paren follows it. */
	| INSERT merge_ins_cols VALUES_LA '(' expr_list ')'
			{ $$ = &mergeAction{kind: MergeActionInsert, cols: $2, vals: $5} }
	/* DEFAULT VALUES leaves InsertValues nil, which is how the executor tells
	   the two apart. */
	| INSERT merge_ins_cols DEFAULT VALUES
			{ $$ = &mergeAction{kind: MergeActionInsert, cols: $2} }

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
			{ $$ = NewCreateTypeStmt($<p>1, objectNameFromQn($3)) }
	/* Base type: legacy sets ONLY HasOptions and skips the whole list. */
	| CREATE TYPE_P qualified_name '(' type_opt_list ')'
			{
				st := NewCreateTypeStmt($<p>1, objectNameFromQn($3))
				st.HasOptions = true
				$$ = st
			}
	| CREATE TYPE_P qualified_name AS ENUM_P '(' enum_val_list ')'
			{
				st := NewCreateTypeStmt($<p>1, objectNameFromQn($3))
				st.IsEnum, st.EnumValues = true, $7
				$$ = st
			}
	| CREATE TYPE_P qualified_name AS '(' type_field_list ')'
			{
				st := NewCreateTypeStmt($<p>1, objectNameFromQn($3))
				st.IsComposite, st.CompositeFields = true, $6
				$$ = st
			}
	| CREATE TYPE_P qualified_name AS RANGE '(' range_opt_list ')'
			{
				st := NewCreateTypeStmt($<p>1, objectNameFromQn($3))
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
		type_field                        { $$ = []TypeField{$1} }
	| type_field_list ',' type_field      { $$ = append($1, $3) }

/* ColType is the RAW token join legacy stores, not a rendered type name:
   `character varying(20)` is kept as "character varying ( 20 )". The join runs
   from the type's first token to the field's terminator, exactly as
   parseCreateType's inner scan does. */
type_field:
		ColId col_type_name opt_field_collate
			{ $$ = NewTypeField(lowerIdent($1), rawTypeSpan(yylex, $<p>2), $3) }

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
			{ $$ = NewDropTypeStmt($<p>1, $4, $3, $5 == "cascade") }

drop_domain_stmt:
		DROP DOMAIN_P opt_if_exists_drop drop_name_list opt_drop_behavior
			{ $$ = NewDropDomainStmt($<p>1, $4, $3, $5 == "cascade") }

/* AS is optional (`CREATE DOMAIN d int`), and the base type is stored as a
   NAME plus separate typmod args, not as a ColumnType. */
create_domain_stmt:
		CREATE DOMAIN_P qualified_name opt_AS_kw col_type_name domain_constraints
			{
				tw := $5.(*typeWithArgs)
				ct := colTypeOf(yylex, tw, $<p>5)
				nm := ct.Name
				if ct.Schema != "" {
					nm = ct.Schema + "." + ct.Name
				}
				if ct.IsArray {
					nm += "[]"
				}
				st := NewCreateDomainStmt($<p>1, objectNameFromQn($3), nm, ct.Args)
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
				st := NewCreateSequenceStmt($<p>1, objectNameFromQn($5), temp, unlogged, $4)
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
		DO SCONST            { $$ = NewDoStmt($<p>1, "plpgsql", $2) }

/* ============================================================================
   P5.6 — CREATE / DROP TRIGGER, COMMENT ON, ALTER FUNCTION / PROCEDURE /
   ROUTINE. 42 + 18 unrouted regress fragments plus COMMENT ON.

   Almost every word in a trigger definition is an ORDINARY IDENTIFIER in
   goopg's lexer — BEFORE, AFTER, INSTEAD, EACH, ROW, STATEMENT, REFERENCING,
   OLD, NEW are all acceptIdentKeyword calls in ddl.go — so they arrive as
   ColIds and the rules discriminate on their text rather than on distinct
   terminals. The trigger function's arguments are NOT expressions either:
   legacy keeps each literal's raw text and re-renders integers.
   ========================================================================= */

create_trigger_stmt:
		CREATE TRIGGER ColId trig_timing trig_events ON qualified_name
		opt_trig_referencing opt_trig_foreach opt_trig_when
		EXECUTE trig_func '(' opt_trig_args ')'
			{ $$ = buildTrigger($<p>1, $3, objectNameFromQn($7), false, $4, $5, nil, $8, $9, $10, objectNameFromQn($12), $14) }
	/* CONSTRAINT triggers additionally take the [NOT] DEFERRABLE trailer, and
	   ONLY they do: parseCreateTriggerTail reads it under `if isConstraint`. */
	| CREATE CONSTRAINT TRIGGER ColId trig_timing trig_events ON qualified_name
		opt_constr_defer opt_trig_referencing opt_trig_foreach opt_trig_when
		EXECUTE trig_func '(' opt_trig_args ')'
			{ $$ = buildTrigger($<p>1, $4, objectNameFromQn($8), true, $5, $6, $9, $10, $11, $12, objectNameFromQn($14), $16) }

trig_timing:
		ColId              { $$ = trigTiming($1) }
	/* INSTEAD OF — OF is a reserved keyword and can never be a ColId. */
	| ColId OF             { $$ = trigTiming($1) }

trig_events:
		trig_event                        { $$ = appendTrigEvent(nil, $1.(*trigEvent)) }
	| trig_events OR trig_event           { $$ = appendTrigEvent($1, $3.(*trigEvent)) }

trig_event:
		INSERT               { $$ = &trigEvent{name: "insert"} }
	| DELETE_P               { $$ = &trigEvent{name: "delete"} }
	| TRUNCATE               { $$ = &trigEvent{name: "truncate"} }
	| UPDATE                 { $$ = &trigEvent{name: "update"} }
	| UPDATE OF colid_list   { $$ = &trigEvent{name: "update", cols: $3} }

opt_constr_defer:
		/* empty */                        { $$ = (*constrDefer)(nil) }
	| constr_defer_words                   { $$ = $1 }

constr_defer_words:
		DEFERRABLE opt_initially           { $$ = &constrDefer{deferrable: true, initDeferred: $2} }
	| NOT DEFERRABLE opt_initially         { $$ = &constrDefer{deferrable: false, initDeferred: $3} }
	| INITIALLY ColId                      { $$ = &constrDefer{initDeferred: initiallyDeferred($2)} }

opt_initially:
		/* empty */       { $$ = false }
	| INITIALLY ColId     { $$ = initiallyDeferred($2) }

/* REFERENCING OLD|NEW TABLE [AS] name [ ... ] — OLD and NEW are plain
   identifiers here, so the pair is one ColId followed by TABLE. */
opt_trig_referencing:
		/* empty */                       { $$ = []any(nil) }
	| REFERENCING trig_transitions        { $$ = $2 }

trig_transitions:
		trig_transition                   { $$ = []any{$1} }
	| trig_transitions trig_transition    { $$ = append(asAnySlice($1), $2) }

/* OLD and NEW have their OWN terminals even though they are unreserved (and so
   also ColIds). Writing this as `ColId TABLE ...` made the list ambiguous with
   the EXECUTE that follows it — EXECUTE is unreserved too, so it could start
   another transition — and shift won, which would have eaten the EXECUTE. */
trig_transition:
		OLD TABLE opt_AS_kw ColId       { $$ = trigTransition("old", $4) }
	| NEW TABLE opt_AS_kw ColId         { $$ = trigTransition("new", $4) }

/* Spelled out rather than `FOR opt_EACH_kw ColId`: an empty nonterminal after
   FOR reduces before EACH is visible. */
opt_trig_foreach:
		/* empty */             { $$ = (*trigForEach)(nil) }
	| FOR ColId                 { $$ = trigForEachOf($2) }
	| FOR EACH ColId            { $$ = trigForEachOf($3) }

opt_trig_when:
		/* empty */             { $$ = (Expr)(nil) }
	| WHEN '(' a_expr ')'       { $$ = $3 }

/* EXECUTE [FUNCTION|PROCEDURE] name — the optional keyword is spelled into
   three alternatives so nothing reduces empty right after EXECUTE. */
trig_func:
		qualified_name             { $$ = $1 }
	| FUNCTION qualified_name      { $$ = $2 }
	| PROCEDURE qualified_name     { $$ = $2 }

/* Legacy's argument scan keeps string, numeric and identifier literals and
   SKIPS anything else, so the list is deliberately loose. */
opt_trig_args:
		/* empty */                  { $$ = []string(nil) }
	| trig_arg_list                  { $$ = $1 }

trig_arg_list:
		trig_arg                     { $$ = []string{$1} }
	| trig_arg_list ',' trig_arg     { $$ = append($1, $3) }

trig_arg:
		SCONST          { $$ = $1 }
	| ICONST            { $$ = CanonicalTriggerIntArg(strconv.FormatInt(int64($1), 10)) }
	| FCONST            { $$ = $1 }
	| ColId             { $$ = $1 }

drop_trigger_stmt:
		DROP TRIGGER opt_if_exists_drop ColId ON qualified_name opt_drop_behavior
			{ $$ = NewDropTriggerStmt($<p>1, $4, objectNameFromQn($6), $3) }

/* ---------------------------------------------------------------------------
   COMMENT ON <kind> <name> IS { 'text' | NULL }. The object-kind vocabulary is
   exactly parser.go parseCommentOnTail's switch; an unlisted kind falls
   through there to a different handler, so this grammar must not accept one.
   --------------------------------------------------------------------------- */

comment_stmt:
		COMMENT ON comment_target IS comment_text
			{
				cs := $3.(*CommentOnStmt)
				cs.Description = $5
				$$ = commentAt($<p>1, cs)
			}

comment_text:
		SCONST      { $$ = $1 }
	| NULL_P        { $$ = "" }

comment_target:
		TABLE qualified_name          { $$ = NewCommentOnStmt(0, "table", objectNameFromQn($2), "") }
	| INDEX qualified_name            { $$ = NewCommentOnStmt(0, "index", objectNameFromQn($2), "") }
	| VIEW qualified_name             { $$ = NewCommentOnStmt(0, "view", objectNameFromQn($2), "") }
	| SEQUENCE qualified_name         { $$ = NewCommentOnStmt(0, "sequence", objectNameFromQn($2), "") }
	| TYPE_P qualified_name           { $$ = NewCommentOnStmt(0, "type", objectNameFromQn($2), "") }
	| DOMAIN_P qualified_name         { $$ = NewCommentOnStmt(0, "domain", objectNameFromQn($2), "") }
	| SCHEMA qualified_name           { $$ = NewCommentOnStmt(0, "schema", objectNameFromQn($2), "") }
	| EXTENSION qualified_name        { $$ = NewCommentOnStmt(0, "extension", objectNameFromQn($2), "") }
	| COLLATION qualified_name        { $$ = NewCommentOnStmt(0, "collation", objectNameFromQn($2), "") }
	| SERVER qualified_name           { $$ = NewCommentOnStmt(0, "server", objectNameFromQn($2), "") }
	| STATISTICS qualified_name       { $$ = NewCommentOnStmt(0, "statistics", objectNameFromQn($2), "") }
	| MATERIALIZED VIEW qualified_name { $$ = NewCommentOnStmt(0, "materialized view", objectNameFromQn($3), "") }
	| ACCESS METHOD qualified_name    { $$ = NewCommentOnStmt(0, "access method", objectNameFromQn($3), "") }
	| FOREIGN TABLE qualified_name    { $$ = NewCommentOnStmt(0, "foreign table", objectNameFromQn($3), "") }
	| FOREIGN DATA_P WRAPPER qualified_name { $$ = NewCommentOnStmt(0, "foreign data wrapper", objectNameFromQn($4), "") }
	/* COLUMN's name is split at the LAST dot: `COMMENT ON COLUMN t.c` records
	   ObjName{Name:"t"} and SubName "c", while `s.t.c` keeps the schema. */
	| COLUMN qualified_name           { $$ = commentColumn($2) }
	/* These five name the object by a bare name plus its table. */
	| CONSTRAINT ColId ON qualified_name
			{ $$ = commentConstraint($2, false, objectNameFromQn($4)) }
	| CONSTRAINT ColId ON DOMAIN_P qualified_name
			{ $$ = commentConstraint($2, true, objectNameFromQn($5)) }
	| TRIGGER ColId ON qualified_name { $$ = NewCommentOnStmt(0, "trigger", objectNameFromQn($4), $2) }
	| POLICY ColId ON qualified_name  { $$ = NewCommentOnStmt(0, "policy", objectNameFromQn($4), $2) }
	| RULE ColId ON qualified_name    { $$ = NewCommentOnStmt(0, "rule", objectNameFromQn($4), $2) }
	/* The cast's type names are stored as WRITTEN, not as the normalised
	   name: legacy's parseCastTypeName joins the raw tokens, so
	   `character varying` stays that and does not become varchar. */
	| CAST '(' fn_type AS fn_type ')' { $$ = commentCast(rawTypeSpan(yylex, $<p>3), rawTypeSpan(yylex, $<p>5)) }
	| FUNCTION qualified_name opt_comment_args
			{
				cs := NewCommentOnStmt(0, "function", objectNameFromQn($2), "")
				cs.Args = $3
				$$ = cs
			}

/* An ABSENT arg list is nil, an empty `()` is a non-nil empty slice — the same
   distinction DROP FUNCTION draws. */
opt_comment_args:
		/* empty */                  { $$ = []FunctionArg(nil) }
	| '(' opt_func_args ')'          { $$ = $2 }

/* ---------------------------------------------------------------------------
   ALTER FUNCTION / PROCEDURE / ROUTINE. The attribute list overlaps CREATE's
   (common_func_opt_item) but stores POINTERS, so an unwritten attribute stays
   distinguishable from one written with its default value.
   --------------------------------------------------------------------------- */

alter_function_stmt:
		ALTER alter_routine_kind qualified_name opt_comment_args alter_fn_actions
			{
				kind := $2
				st := NewAlterFunctionStmt($<p>1, objectNameFromQn($3), $4, kind == "procedure", kind == "routine")
				applyAlterFnActions(st, $5)
				$$ = st
			}

alter_routine_kind:
		FUNCTION      { $$ = "function" }
	| PROCEDURE       { $$ = "procedure" }
	| ROUTINE         { $$ = "routine" }

alter_fn_actions:
		/* empty */                          { $$ = []any(nil) }
	| alter_fn_actions alter_fn_action       { $$ = append($1, $2) }

alter_fn_action:
		IMMUTABLE                    { $$ = alterFnVolatile("i") }
	| STABLE                         { $$ = alterFnVolatile("s") }
	| VOLATILE                       { $$ = alterFnVolatile("v") }
	| STRICT_P                       { $$ = alterFnStrict(true) }
	| CALLED ON NULL_P INPUT_P       { $$ = alterFnStrict(false) }
	| RETURNS NULL_P ON NULL_P INPUT_P { $$ = alterFnStrict(true) }
	| LEAKPROOF                      { $$ = alterFnLeakproof(true) }
	| NOT LEAKPROOF                  { $$ = alterFnLeakproof(false) }
	| SECURITY DEFINER               { $$ = alterFnSecurity(true) }
	| SECURITY INVOKER               { $$ = alterFnSecurity(false) }
	/* EXTERNAL SECURITY ... records NOTHING on an ALTER: legacy's attribute
	   loop only recognises the bare `security` word and sends the EXTERNAL
	   spelling through consumeFunctionAttribute, which drops it. CREATE keeps
	   both spellings — the two paths genuinely disagree. */
	| EXTERNAL SECURITY DEFINER      { $$ = alterFnNoop() }
	| EXTERNAL SECURITY INVOKER      { $$ = alterFnNoop() }
	/* PARALLEL / COST / ROWS / SUPPORT / WINDOW are consumed and DISCARDED:
	   AlterFunctionStmt has no field for any of them. */
	| PARALLEL ColId                 { $$ = alterFnNoop() }
	| COST fn_number                 { $$ = alterFnNoop() }
	| ROWS fn_number                 { $$ = alterFnNoop() }
	| SUPPORT qualified_name         { $$ = alterFnNoop() }
	| WINDOW                         { $$ = alterFnNoop() }
	| OWNER TO alter_fn_owner        { $$ = alterFnOwner($3) }
	| RENAME TO ColId                { $$ = alterFnRename($3) }
	/* `SET SCHEMA x` is NOT a separate alternative: SCHEMA is an unreserved
	   keyword and therefore also a config name, so spelling it out was 394
	   shift/reduce conflicts — one per token that can start a ColId. The two
	   forms are told apart in the action instead, which is what legacy does
	   (ddl.go peeks for the word "schema" after SET). */
	| SET fn_config_name fn_config_value
			{
				n, v := $2, $3
				if eqFold(n, "schema") {
					$$ = alterFnSchema(v)
				} else {
					$$ = alterFnConfig(NewFunctionConfigOp(false, false, n, v), v != fnConfigUnset)
				}
			}
	| RESET ALL                      { $$ = alterFnConfig(NewFunctionConfigOp(false, true, "", ""), true) }
	| RESET fn_config_name           { $$ = alterFnConfig(NewFunctionConfigOp(true, false, $2, ""), true) }

/* CURRENT_USER / SESSION_USER / CURRENT_ROLE all collapse to the sentinel
   "current_user", and so does an unparsable name (ddl.go's else branch). */
alter_fn_owner:
		ColId          { $$ = alterFnOwnerName($1) }
	| CURRENT_USER     { $$ = "current_user" }
	| SESSION_USER     { $$ = "current_user" }
	| CURRENT_ROLE     { $$ = "current_user" }

/* ============================================================================
   P5.7 — the DROP family. Every remaining DROP class in legacy is a plain
   `DROP <kind> [IF EXISTS] <names> [CASCADE|RESTRICT]` that produces a
   DropCompatStmt tagged with the kind, plus five that have their own node.
   None of them is a skip-to-semicolon compat form, so all of them get a real
   grammar here.
   ========================================================================= */

drop_misc_stmt:
		DROP drop_compat_kind opt_if_exists_drop drop_name_list opt_drop_behavior
			{ $$ = NewDropCompatStmt($<p>1, $2, $3, $4, dropBehavior($5)) }
	/* AGGREGATE and OPERATOR carry a signature; the names list is single. */
	/* An aggregate keeps only its FIRST argument type (the AST comment on
	   DropCompatStmt.ArgTypes says so, and parseDropAggregate reads one). */
	| DROP AGGREGATE opt_if_exists_drop qualified_name '(' drop_arg_types ')' opt_drop_behavior
			{ $$ = dropWithArgs($<p>1, "aggregate", $3, $4, firstArg($6), dropBehavior($8)) }
	| DROP OPERATOR opt_if_exists_drop any_operator_name '(' drop_arg_types ')' opt_drop_behavior
			{ $$ = dropWithArgs($<p>1, "operator", $3, $4, $6, dropBehavior($8)) }
	| DROP OPERATOR opclass_or_family opt_if_exists_drop qualified_name USING ColId opt_drop_behavior
			{
				st := NewDropCompatStmt($<p>1, $3, $4, []ObjectName{objectNameFromQn($5)}, dropBehavior($8))
				SetDropCompatExtras(st, nil, $7, nil, "", "")
				$$ = st
			}
	| DROP CAST opt_if_exists_drop '(' fn_type AS fn_type ')' opt_drop_behavior
			{
				st := NewDropCompatStmt($<p>1, "cast", $3, nil, dropBehavior($9))
				SetDropCompatExtras(st, nil, "", []string{typeNameOf($5), typeNameOf($7)}, "", "")
				$$ = st
			}
	| DROP TRANSFORM opt_if_exists_drop FOR fn_type LANGUAGE ColId opt_drop_behavior
			{
				st := NewDropCompatStmt($<p>1, "transform", $3, nil, dropBehavior($8))
				SetDropCompatExtras(st, nil, "", nil, typeNameOf($5), lowerIdent($7))
				$$ = st
			}
	/* The five kinds with their own AST node. RULE and POLICY name an object
	   ON a table; the other three take a bare name. */
	| DROP RULE opt_if_exists_drop ColId ON qualified_name opt_drop_behavior
			{ $$ = NewDropRuleStmt($<p>1, $4, objectNameFromQn($6), $3) }
	| DROP POLICY opt_if_exists_drop ColId ON qualified_name opt_drop_behavior
			{ $$ = NewDropPolicyStmt($<p>1, $4, objectNameFromQn($6), $3) }
	| DROP PUBLICATION opt_if_exists_drop ColId opt_drop_behavior
			{ $$ = NewDropPublicationStmt($<p>1, $4, $3) }
	| DROP SUBSCRIPTION opt_if_exists_drop ColId opt_drop_behavior
			{ $$ = NewDropSubscriptionStmt($<p>1, $4, $3) }
	| DROP TABLESPACE opt_if_exists_drop ColId opt_drop_behavior
			{ $$ = NewDropTablespaceStmt($<p>1, $4, $3) }

opclass_or_family:
		CLASS      { $$ = "operator class" }
	| FAMILY       { $$ = "operator family" }

/* The kind words that reach DropCompatStmt verbatim. Multi-word kinds are
   spelled out because their AST string joins them with a single space. */
drop_compat_kind:
		SEQUENCE               { $$ = "sequence" }
	| SCHEMA                   { $$ = "schema" }
	| EXTENSION                { $$ = "extension" }
	| STATISTICS               { $$ = "statistics" }
	| COLLATION                { $$ = "collation" }
	| SERVER                   { $$ = "server" }
	| CONVERSION_P             { $$ = "conversion" }
	| LANGUAGE                 { $$ = "language" }
	| EVENT TRIGGER            { $$ = "event trigger" }
	| ACCESS METHOD            { $$ = "access method" }
	| FOREIGN TABLE            { $$ = "foreign table" }
	/* Legacy spells this kind with a HYPHEN, unlike every other one. */
	| FOREIGN DATA_P WRAPPER   { $$ = "foreign-data wrapper" }
	| TEXT_P SEARCH ColId      { $$ = "text search " + lowerIdent($3) }

/* An operator name is not an identifier, and it is not ONE token either: the
   lexer splits `===` into three separate `=` tokens (scan.l's {self} set is
   per-character), so legacy's parseOperatorRefName joins the run back up. The
   run rule is reachable only after DROP OPERATOR, where nothing else can
   follow, so it costs no conflict in expression context. */
any_operator_name:
		op_run                 { $$ = qname{parts: []string{$1}} }
	| ColId '.' op_run         { $$ = qname{parts: []string{$1, $3}} }

op_run:
		op_char                { $$ = $1 }
	| op_run op_char           { $$ = $1 + $2 }

/* scan.l's {self} characters arrive as char terminals, everything else as Op,
   and the handful of two-character operators as their own named terminals. */
op_char:
		Op                 { $$ = $1 }
	| '+'                  { $$ = "+" }
	| '-'                  { $$ = "-" }
	| '*'                  { $$ = "*" }
	| '/'                  { $$ = "/" }
	| '%'                  { $$ = "%" }
	| '^'                  { $$ = "^" }
	| '<'                  { $$ = "<" }
	| '>'                  { $$ = ">" }
	| '='                  { $$ = "=" }
	/* These carry their ORIGINAL text: `!=` and `<>` are the same terminal but
	   an operator NAME keeps the spelling it was written with, so `!====`
	   must not come back as `<>===`. */
	| LESS_EQUALS          { $$ = $1 }
	| GREATER_EQUALS       { $$ = $1 }
	| NOT_EQUALS           { $$ = $1 }

/* NONE is the "no operand" spelling for a prefix operator and stores "". */
drop_arg_types:
		drop_arg_type                        { $$ = []string{$1} }
	| drop_arg_types ',' drop_arg_type       { $$ = append($1, $3) }

drop_arg_type:
		fn_type      { $$ = typeNameOf($1) }
	/* NONE is stored as the WORD, not as an empty slot: legacy reads it with
	   the same identifier path every other argument type takes. */
	| NONE           { $$ = "none" }
	/* `DROP AGGREGATE a(*)` — the any-signature spelling. */
	| '*'            { $$ = "*" }

/* ============================================================================
   P5.9 — DROP DATABASE, CREATE EXTENSION, ALTER SCHEMA, CREATE POLICY.

   These four have clean, fully-specified ASTs. The rest of the remaining DDL
   splits into two other groups, neither of which belongs here: the classes
   goopg parses ABOVE Parse (role DDL, GRANT/REVOKE — Parse
   REJECTS `CREATE ROLE r` outright), and the parse-and-ignore compat classes
   whose legacy handler is a token walk ending in parseSkipToSemicolon, which a
   grammar cannot reproduce without accepting arbitrary token soup.
   ========================================================================= */

drop_database_stmt:
		DROP DATABASE opt_if_exists_drop drop_name_list opt_drop_behavior
			{ $$ = NewDropCompatStmt($<p>1, "database", $3, $4, dropBehavior($5)) }

/* The extension NAME and every option value take a string literal as well as
   an identifier, and legacy stores the RAW token value either way. */
create_extension_stmt:
		CREATE EXTENSION opt_if_not_exists ext_name opt_WITH_kw ext_opts
			{
				st := NewCreateExtensionStmt($<p>1, $4, $3)
				applyExtOpts(st, $6)
				$$ = st
			}

ext_name:
		ColId       { $$ = $1 }
	| SCONST        { $$ = $1 }

ext_opts:
		/* empty */          { $$ = []any(nil) }
	| ext_opts ext_opt       { $$ = append($1, $2) }

ext_opt:
		SCHEMA ext_name      { $$ = extSchema($2) }
	| VERSION_P ext_name     { $$ = extVersion($2) }
	| CASCADE                { $$ = extCascade() }

/* Real PostgreSQL has exactly two ALTER SCHEMA forms (schemacmds.c's
   RenameSchema / AlterSchemaOwner) and so does legacy. */
alter_schema_stmt:
		ALTER SCHEMA ColId RENAME TO ColId
			{ $$ = NewAlterSchemaStmt($<p>1, $3, "rename", $6, "") }
	| ALTER SCHEMA ColId OWNER TO alter_fn_owner
			{ $$ = NewAlterSchemaStmt($<p>1, $3, "owner", "", $6) }

/* CREATE ACCESS METHOD — gram.y:5991 CreateAmStmt, verbatim:
   `CREATE ACCESS METHOD name TYPE_P am_type HANDLER handler_name`.

   goopg never invokes a user-defined access method; this only round-trips the
   DDL for pg_dump (pg_am virtual view). ddl.go reads TYPE and HANDLER with
   acceptIdentKeyword, but both are real kwlist keywords and therefore
   terminals here — which is also what makes the two error cases (a missing
   TYPE, an am_type that is neither INDEX nor TABLE) reject without an
   explicit check. */
create_access_method_stmt:
		CREATE ACCESS METHOD ColId TYPE_P am_type HANDLER qualified_name
			{ $$ = NewCreateAccessMethodStmt($<p>1, $4, $6, objectNameFromQn($8)) }

am_type:
		INDEX     { $$ = "i" }
	| TABLE       { $$ = "t" }

/* ============================================================================
   CREATE / ALTER EVENT TRIGGER (gram.y CreateEventTrigStmt / AlterEventTrigStmt).

   EVENT is a real keyword token but the event NAME (ddl_command_start) and
   the ALTER action words (DISABLE/ENABLE/RENAME/OWNER) are ordinary
   identifiers in ddl.go — acceptIdentKeyword, not acceptKeyword — so they are
   ColIds here rather than terminals.
   ========================================================================= */

/* EXECUTE takes FUNCTION or PROCEDURE interchangeably, and ddl.go accepts
   NEITHER as well (`_ = p.acceptKeyword(...) || ...`). Spelled as three arms:
   an optional-keyword nonterminal has to reduce its empty alternative right
   after EXECUTE, one token before FUNCTION/PROCEDURE can decide it. */
create_event_trigger_stmt:
		CREATE EVENT TRIGGER ColId ON ColId evtrig_when EXECUTE qualified_name '(' ')'
			{ $$ = NewCreateEventTriggerStmt($<p>1, $4, $6, $7, objectNameFromQn($9)) }
	| CREATE EVENT TRIGGER ColId ON ColId evtrig_when EXECUTE FUNCTION qualified_name '(' ')'
			{ $$ = NewCreateEventTriggerStmt($<p>1, $4, $6, $7, objectNameFromQn($10)) }
	| CREATE EVENT TRIGGER ColId ON ColId evtrig_when EXECUTE PROCEDURE qualified_name '(' ')'
			{ $$ = NewCreateEventTriggerStmt($<p>1, $4, $6, $7, objectNameFromQn($10)) }

/* WHEN <var> IN ('a','b') [AND <var> IN (...)]. The filter list is flattened
   to "var\x1fv1\x1fv2" entries so one []string carries both halves;
   NewCreateEventTriggerStmt splits them back out and applies ddl.go's rule
   that only a filter variable named "tag" contributes to Tags. */
evtrig_when:
		/* empty */                { $$ = []string(nil) }
	| WHEN evtrig_filter_list      { $$ = $2 }

evtrig_filter_list:
		ColId IN_P '(' evtrig_sconst_list ')'
			{ $$ = []string{evtrigFilter($1, $4)} }
	| evtrig_filter_list AND ColId IN_P '(' evtrig_sconst_list ')'
			{ $$ = append($1, evtrigFilter($3, $6)) }

evtrig_sconst_list:
		SCONST                             { $$ = []string{$1} }
	| evtrig_sconst_list ',' SCONST        { $$ = append($1, $3) }

/* DISABLE / ENABLE [REPLICA|ALWAYS] — ddl.go reads these with
   acceptIdentKeyword, but they are all real kwlist unreserved keywords, so
   here they are TERMINALS. That matters for the error cases: a ColId pair
   would have matched `DISABLE ALWAYS` and `ENABLE BOGUS` and produced an
   AlterEventTriggerStmt with an empty Action, where ddl.go leaves the extra
   word unconsumed and it surfaces as a trailing-token syntax error. Exact
   terminals reproduce that for free. */
alter_event_trigger_stmt:
		ALTER EVENT TRIGGER ColId DISABLE_P
			{ $$ = NewAlterEventTriggerStmt($<p>1, $4, "disable", "", "") }
	| ALTER EVENT TRIGGER ColId ENABLE_P
			{ $$ = NewAlterEventTriggerStmt($<p>1, $4, "enable", "", "") }
	| ALTER EVENT TRIGGER ColId ENABLE_P REPLICA
			{ $$ = NewAlterEventTriggerStmt($<p>1, $4, "enable_replica", "", "") }
	| ALTER EVENT TRIGGER ColId ENABLE_P ALWAYS
			{ $$ = NewAlterEventTriggerStmt($<p>1, $4, "enable_always", "", "") }
	| ALTER EVENT TRIGGER ColId RENAME TO ColId
			{ $$ = NewAlterEventTriggerStmt($<p>1, $4, "rename", $7, "") }
	| ALTER EVENT TRIGGER ColId OWNER TO alter_fn_owner
			{ $$ = NewAlterEventTriggerStmt($<p>1, $4, "owner", "", $7) }

create_policy_stmt:
		CREATE POLICY ColId ON qualified_name opt_policy_as opt_policy_for
		opt_policy_to opt_policy_using opt_policy_check
			{
				st := NewCreatePolicyStmt($<p>1, $3, objectNameFromQn($5))
				if $6 != "" {
					st.Permissive = $6 == "permissive"
				}
				if $7 != "" {
					st.Command = $7
				}
				st.Roles = $8
				st.Using, st.WithCheck = $9, $10
				$$ = st
			}

opt_policy_as:
		/* empty */   { $$ = "" }
	| AS ColId        { $$ = lowerIdent($2) }

opt_policy_for:
		/* empty */   { $$ = "" }
	| FOR ALL         { $$ = "all" }
	| FOR SELECT      { $$ = "select" }
	| FOR INSERT      { $$ = "insert" }
	| FOR UPDATE      { $$ = "update" }
	| FOR DELETE_P    { $$ = "delete" }

opt_policy_to:
		/* empty */          { $$ = []string(nil) }
	| TO policy_role_list    { $$ = $2 }

policy_role_list:
		ColId                          { $$ = []string{$1} }
	| policy_role_list ',' ColId       { $$ = append($1, $3) }

opt_policy_using:
		/* empty */                 { $$ = (Expr)(nil) }
	| USING '(' a_expr ')'          { $$ = $3 }

opt_policy_check:
		/* empty */                 { $$ = (Expr)(nil) }
	| WITH CHECK '(' a_expr ')'     { $$ = $4 }

/* ============================================================================
   P5.10 — ALTER SEQUENCE / TYPE / DOMAIN.

   Each has its own AST node with an ACTION string or a set of option fields;
   none of them is a skip-to-semicolon compat form. ALTER DOMAIN's VALIDATE
   CONSTRAINT is the one exception — legacy answers it with a CompatNoopStmt —
   so it is left out of the grammar and vetoed at the dispatcher.
   ========================================================================= */

alter_sequence_stmt:
		ALTER SEQUENCE opt_if_exists_drop qualified_name alter_seq_opts
			{
				$$ = buildAlterSequence($<p>1, objectNameFromQn($4), $3, $5)
			}

alter_seq_opts:
		/* empty */                        { $$ = []any(nil) }
	| alter_seq_opts alter_seq_opt         { $$ = append($1, $2) }

/* Unlike CREATE SEQUENCE, the NO forms are RECORDED here: a sequence already
   has values, so `NO MINVALUE` means "reset to the type default", which is a
   different statement from "leave unchanged". */
alter_seq_opt:
		AS ColId                              { $$ = altSeqDataType($2) }
	| INCREMENT opt_BY_kw signed_iconst       { $$ = altSeqInt("increment", $3) }
	| MINVALUE signed_iconst                  { $$ = altSeqInt("minvalue", $2) }
	| MAXVALUE signed_iconst                  { $$ = altSeqInt("maxvalue", $2) }
	| START opt_WITH_kw signed_iconst         { $$ = altSeqInt("start", $3) }
	| RESTART                                 { $$ = altSeqRestart() }
	| RESTART opt_WITH_kw signed_iconst       { $$ = altSeqInt("restart", $3) }
	| CACHE signed_iconst                     { $$ = altSeqInt("cache", $2) }
	| CYCLE                                   { $$ = altSeqFlag("cycle") }
	| NO MINVALUE                             { $$ = altSeqFlag("nominvalue") }
	| NO MAXVALUE                             { $$ = altSeqFlag("nomaxvalue") }
	| NO CYCLE                                { $$ = altSeqFlag("nocycle") }
	| SET LOGGED                              { $$ = altSeqLogged("logged") }
	| SET UNLOGGED                            { $$ = altSeqLogged("unlogged") }
	| OWNED BY seq_owner                      { $$ = altSeqOwnedBy($3) }
	/* RENAME / OWNER TO / SET SCHEMA are RELATION operations, not sequence
	   options: ddl.go routes all three to an AlterTableStmt carrying
	   TagOverride "ALTER SEQUENCE" (a sequence IS a relation, and the three
	   actions are shared with tables), and AlterSequenceStmt has no field for
	   any of them. They are alternatives HERE rather than separate statement
	   arms so SET keeps a single decision point — SET SCHEMA as its own arm
	   was reduce/reduce against SET LOGGED. */
	| RENAME TO ColId                         { $$ = altSeqRelOp("rename", $3) }
	| OWNER TO alter_fn_owner                 { $$ = altSeqRelOp("owner", $3) }
	| SET SCHEMA ColId                        { $$ = altSeqRelOp("schema", $3) }

alter_type_stmt:
		ALTER TYPE_P qualified_name alter_type_action
			{
				st := NewAlterTypeStmt($<p>1, objectNameFromQn($3))
				$4.(alterTypeOp)(st)
				$$ = st
			}

alter_type_action:
	/* The enum labels are string literals, not identifiers. */
		ADD_P VALUE_P opt_if_not_exists SCONST opt_enum_pos
			{ $$ = altTypeAddValue($3, $4, $5.(*enumPos)) }
	| RENAME VALUE_P SCONST TO SCONST      { $$ = altTypeRenameValue($3, $5) }
	| RENAME ATTRIBUTE attr_name TO attr_name opt_drop_behavior
			{ $$ = altTypeRenameAttr($3, $5) }
	| RENAME TO ColId                      { $$ = altTypeRenameTo($3) }
	| OWNER TO alter_fn_owner              { $$ = altTypeOwner($3) }
	/* The attribute subcommands are a COMMA-SEPARATED list, and AttrCmds[0] is
	   mirrored into the legacy scalar fields (the executor reads those when
	   there is at most one). The attribute type is stored as the RAW token
	   join, like a composite field's: `numeric(3,1)` comes back as
	   "numeric ( 3 , 1 )". */
	| attr_cmd_list                        { $$ = altTypeAttrCmds($1) }

attr_cmd_list:
		attr_cmd                       { $$ = []any{$1} }
	| attr_cmd_list ',' attr_cmd       { $$ = append($1, $3) }

attr_cmd:
		ADD_P ATTRIBUTE attr_name fn_type opt_field_collate opt_drop_behavior
			{ $$ = NewAlterTypeAttrCmd("add", lowerIdent($3), rawTypeSpan(yylex, $<p>4), $5, false) }
	| DROP ATTRIBUTE opt_if_exists_drop attr_name opt_drop_behavior
			{ $$ = NewAlterTypeAttrCmd("drop", lowerIdent($4), "", "", $3) }
	| ALTER ATTRIBUTE attr_name opt_set_data TYPE_P fn_type opt_field_collate opt_drop_behavior
			{ $$ = NewAlterTypeAttrCmd("alter", lowerIdent($3), rawTypeSpan(yylex, $<p>6), $7, false) }

/* Legacy reads an attribute name with parseIdent, which takes any TokenIdent —
   and goopg's lexer emits ONLY as one, so `ADD ATTRIBUTE only int` is legal
   there while a plain ColId would refuse it. */
attr_name:
		ColId      { $$ = $1 }
	| ONLY         { $$ = "only" }

opt_set_data:
		/* empty */   { }
	| SET DATA_P      { }

opt_enum_pos:
		/* empty */        { $$ = &enumPos{} }
	| BEFORE SCONST        { $$ = &enumPos{before: $2} }
	| AFTER SCONST         { $$ = &enumPos{after: $2} }

alter_domain_stmt:
		ALTER DOMAIN_P qualified_name alter_domain_action
			{
				st := $4.(alterDomainOp)(qnLastPart($3))
				$$ = alterDomainAt($<p>1, st)
			}

alter_domain_action:
		SET NOT NULL_P                        { $$ = altDomAction("setnotnull") }
	| DROP NOT NULL_P                         { $$ = altDomAction("dropnotnull") }
	| SET DEFAULT a_expr                      { e := $3; $$ = altDomDefault(e) }
	| DROP DEFAULT                            { $$ = altDomAction("dropdefault") }
	/* NOT VALID rides the domain constraint too (gram.y AlterDomainStmt's
	   ConstraintAttributeSpec); legacy parses and drops it. */
	| ADD_P check_body opt_constr_attrs               { $$ = altDomAddCheck(yylex, "", $<p>2) }
	| ADD_P CONSTRAINT ColId check_body opt_constr_attrs { $$ = altDomAddCheck(yylex, $3, $<p>4) }
	/* The drop behaviour is parsed and DROPPED — AlterDomainStmt has no field
	   for it, and ddl.go consumes the word the same way. */
	| DROP CONSTRAINT opt_if_exists_drop ColId opt_drop_behavior { $$ = altDomDropConstraint($3, $4) }
	| RENAME CONSTRAINT ColId TO ColId        { $$ = altDomRenameConstraint($3, $5) }
	| RENAME TO ColId                         { $$ = altDomRenameTo($3) }
	| OWNER TO alter_fn_owner                 { $$ = altDomOwner($3) }

/* ============================================================================
   P5.11 — COPY (gram.y CopyStmt).

   Two shapes: a relation (with an optional column list) copied FROM or TO an
   endpoint, and a parenthesised query copied TO one. The options come either
   in the modern parenthesised list or in the pre-9.0 bare trail, which admits
   a FIXED vocabulary — legacy's parseCopyLegacyTrail STOPS at any other word
   rather than erroring, so the trail is a list of known items, not a generic
   name/value list.
   ========================================================================= */

copy_stmt:
		COPY qualified_name opt_copy_cols copy_dir copy_endpoint opt_copy_opts
			{
				st := NewCopyStmt($<p>1)
				st.Table, st.Columns = objectNameFromQn($2), $3
				st.Direction = CopyDirection($4)
				ep := $5.(*copyEndpoint)
				/* The endpoint is only legal in ONE direction, and the check
				   needs both — so it lives here, not in copy_endpoint, which
				   reduces before the direction is on the stack. copy.go
				   raises the same two messages. */
				checkCopyEndpointDir(yylex, st.Direction, ep, $<p>5)
				st.Endpoint, st.Filename = ep.kind, ep.name
				st.Options = $6
				$$ = st
			}
	/* A query source is TO-only; legacy answers a FROM with a bare syntax
	   error rather than an explanatory one. */
	| COPY '(' copy_inner ')' TO copy_endpoint opt_copy_opts
			{
				st := NewCopyStmt($<p>1)
				st.Direction = CopyTo
				if sel, ok := $3.(*SelectStmt); ok {
					st.Query = sel
				} else {
					st.QueryDML = $3
				}
				ep := $6.(*copyEndpoint)
				st.Endpoint, st.Filename = ep.kind, ep.name
				st.Options = $7
				$$ = st
			}

copy_inner:
		select_bare      { $$ = $1 }
	| insert_stmt        { $$ = $1 }
	| update_stmt        { $$ = $1 }
	| delete_stmt        { $$ = $1 }

opt_copy_cols:
		/* empty */             { $$ = []string(nil) }
	| '(' colid_list ')'        { $$ = $2 }

copy_dir:
		FROM      { $$ = int(CopyFrom) }
	| TO          { $$ = int(CopyTo) }

/* STDIN / STDOUT / PROGRAM arrive as ordinary identifiers (legacy compares
   t.Value), so they are ColIds rather than terminals; which of them is legal
   depends on the direction, and legacy raises its own message for a mismatch. */
copy_endpoint:
		ColId              { $$ = copyEndpointWord(yylex, $1, $<p>1) }
	| ColId SCONST         { $$ = copyEndpointProgram(yylex, $1, $2, $<p>1) }
	| SCONST               { $$ = &copyEndpoint{kind: CopyEndpointFile, name: $1} }

/* WITH is optional before BOTH option forms: legacy consumes it and then
   checks for '(' , so `WITH DELIMITER ','` is the bare trail with a WITH in
   front of it. */
opt_copy_opts:
		/* empty */                        { $$ = []CopyOption(nil) }
	| WITH '(' copy_opt_list ')'           { $$ = $3 }
	| '(' copy_opt_list ')'                { $$ = $2 }
	| WITH copy_trail_list                 { $$ = $2 }
	| copy_trail_list                      { $$ = $1 }

copy_opt_list:
		copy_opt                           { $$ = []CopyOption{$1} }
	| copy_opt_list ',' copy_opt           { $$ = append($1, $3) }

copy_opt:
		copy_opt_name                          { $$ = NewCopyOption($<p>1, $1) }
	| copy_opt_name '*'                        { $$ = CopyOptionStar(NewCopyOption($<p>1, $1)) }
	| copy_opt_name '(' colid_list ')'         { $$ = CopyOptionCols(NewCopyOption($<p>1, $1), $3) }
	| copy_opt_name copy_opt_value             { $$ = CopyOptionValue(NewCopyOption($<p>1, $1), $2) }

/* Both the NAME and the VALUE take ANY keyword: legacy's parseCopyOption
   accepts a bare TokenIdent-or-TokenKeyword for each, which is how
   `NULL ''` and `FREEZE true` parse. as_col_label is ColLabel plus the
   reserved words. The name is stored RAW, exactly as written. */
copy_opt_name:
		as_col_label      { $$ = $1 }

copy_opt_value:
		as_col_label           { $$ = $1 }
	| SCONST                   { $$ = $1 }
	| ICONST                   { $$ = strconv.FormatInt(int64($1), 10) }

/* The pre-9.0 bare trail. Its vocabulary is closed: parseCopyLegacyTrail
   returns as soon as it sees a word outside this set. */
copy_trail_list:
		copy_trail_item                    { $$ = $1 }
	| copy_trail_list copy_trail_item      { $$ = append($1, $2...) }

copy_trail_item:
		BINARY            { $$ = []CopyOption{NewCopyOption($<p>1, "binary")} }
	| copy_trail_flag     { $$ = []CopyOption{NewCopyOption($<p>1, $1)} }
	| copy_trail_str SCONST
			{ $$ = []CopyOption{CopyOptionValue(NewCopyOption($<p>1, $1), $2)} }
	| FORCE QUOTE '*'
			{ $$ = []CopyOption{CopyOptionStar(NewCopyOption($<p>1, "force_quote"))} }
	| FORCE QUOTE colid_list
			{ $$ = []CopyOption{CopyOptionCols(NewCopyOption($<p>1, "force_quote"), $3)} }
	| FORCE NOT NULL_P colid_list
			{ $$ = []CopyOption{CopyOptionCols(NewCopyOption($<p>1, "force_not_null"), $4)} }
	| FORCE NULL_P colid_list
			{ $$ = []CopyOption{CopyOptionCols(NewCopyOption($<p>1, "force_null"), $3)} }

copy_trail_flag:
		CSV        { $$ = "csv" }
	| HEADER_P     { $$ = "header" }
	| FREEZE       { $$ = "freeze" }

copy_trail_str:
		DELIMITER    { $$ = "delimiter" }
	| NULL_P         { $$ = "null" }
	| QUOTE          { $$ = "quote" }
	| ESCAPE         { $$ = "escape" }
	| ENCODING       { $$ = "encoding" }

/* ============================================================================
   P5.12 — ALTER INDEX. Legacy builds an AlterTableStmt for it (index and table
   share the ALTER machinery) and overrides the CommandComplete tag for exactly
   two of the six forms. Anything else falls through to a CompatNoopStmt built
   by a skip-to-semicolon scan, so the dispatcher gates on the action word.
   ========================================================================= */

alter_index_stmt:
	/* The column may be a NUMBER (an expression index's position). Legacy
	   records the statistics target as TEXT, not as an integer. */
		ALTER INDEX qualified_name ALTER opt_COLUMN alter_idx_col SET STATISTICS signed_iconst
			{
				st := NewAlterIndexStmt($<p>1, objectNameFromQn($3), "")
				a := NewATActionAt(AlterTableSetStatistics, $<p>1)
				a.ColumnName, a.CheckExpr = $6, atStatValue($9)
				st.Actions = append(st.Actions, *a)
				$$ = st
			}
	/* The per-column option list is CONSUMED AND DISCARDED here, unlike ALTER
	   TABLE's identical spelling which records it in SetOptions. */
	| ALTER INDEX qualified_name ALTER opt_COLUMN alter_idx_col SET '(' str_pair_list ')'
			{
				st := NewAlterIndexStmt($<p>1, objectNameFromQn($3), "")
				a := NewATActionAt(AlterTableAlterColumnSet, $<p>1)
				a.ColumnName = $6
				st.Actions = append(st.Actions, *a)
				$$ = st
			}
	| ALTER INDEX qualified_name SET TABLESPACE ColId
			{
				st := NewAlterIndexStmt($<p>1, objectNameFromQn($3), "ALTER INDEX")
				a := NewATActionAt(AlterTableSetTablespace, $<p>3)
				a.TablespaceName = $6
				st.Actions = append(st.Actions, *a)
				$$ = st
			}
	| ALTER INDEX qualified_name SET '(' str_pair_list ')'
			{
				st := NewAlterIndexStmt($<p>1, objectNameFromQn($3), "")
				a := NewATActionAt(AlterIndexSetReloptions, $<p>1)
				a.With = strPairMap($6)
				st.Actions = append(st.Actions, *a)
				$$ = st
			}
	/* ConstraintName carries the PARENT index name here — legacy reuses the
	   field rather than adding one. */
	| ALTER INDEX qualified_name ATTACH PARTITION qualified_name
			{
				parent := objectNameFromQn($3)
				st := NewAlterIndexStmt($<p>1, parent, "")
				a := NewATActionAt(AlterIndexAttachPartition, $<p>1)
				a.ConstraintName, a.ChildIndexName = parent.Name, objectNameFromQn($6).Name
				st.Actions = append(st.Actions, *a)
				$$ = st
			}
	| ALTER INDEX qualified_name RENAME TO ColId
			{
				st := NewAlterIndexStmt($<p>1, objectNameFromQn($3), "ALTER INDEX")
				a := NewATActionAt(AlterTableRenameTable, $<p>3)
				a.NewName = $6
				st.Actions = append(st.Actions, *a)
				$$ = st
			}

alter_idx_col:
		ColId       { $$ = $1 }
	| ICONST        { $$ = strconv.FormatInt(int64($1), 10) }

/* ============================================================================
   P5.13 — ALTER VIEW. Like ALTER INDEX it produces an AlterTableStmt, but it
   tags EVERY form "ALTER VIEW" and it supports IF EXISTS. Anything outside the
   seven forms below falls through in legacy to a skip-to-semicolon no-op, so
   the dispatcher gates on the action word.
   ========================================================================= */

alter_view_stmt:
		ALTER VIEW opt_if_exists_drop qualified_name alter_view_action
			{
				st := NewAlterIndexStmt($<p>1, objectNameFromQn($4), "ALTER VIEW")
				st.IfExists = $3
				$5.(alterViewOp)(st)
				$$ = st
			}

alter_view_action:
		RENAME TO ColId                       { $$ = altViewRenameTo($3) }
	| RENAME opt_COLUMN ColId TO ColId        { $$ = altViewRenameCol($3, $5) }
	| OWNER TO alter_fn_owner                 { $$ = altViewOwner($3) }
	| SET SCHEMA ColId                        { $$ = altViewSetSchema($3) }
	| SET '(' str_pair_list ')'               { $$ = altViewReloptions($3, false) }
	| RESET '(' str_pair_list ')'             { $$ = altViewReloptions($3, true) }
	| ALTER opt_COLUMN ColId SET DEFAULT a_expr { $$ = altViewSetDefault($3, $6) }
	| ALTER opt_COLUMN ColId DROP DEFAULT     { $$ = altViewDropDefault($3) }

/* ============================================================================
   P5.14 — LISTEN / NOTIFY / UNLISTEN, plus DROP LANGUAGE and the one
   ALTER MATERIALIZED VIEW form that has a real AST.
   ========================================================================= */

listen_stmt:
		LISTEN ColId              { $$ = NewListenStmt($<p>1, $2) }

notify_stmt:
		NOTIFY ColId              { $$ = NewNotifyStmt($<p>1, $2, "", false) }
	| NOTIFY ColId ',' SCONST     { $$ = NewNotifyStmt($<p>1, $2, $4, true) }

unlisten_stmt:
		UNLISTEN ColId            { $$ = NewUnlistenStmt($<p>1, $2, false) }
	| UNLISTEN '*'                { $$ = NewUnlistenStmt($<p>1, "", true) }

/* Only SET SCHEMA has a real AST; legacy answers every other ALTER
   MATERIALIZED VIEW form with a CompatNoopStmt, so the dispatcher gates on it. */
alter_matview_stmt:
		ALTER MATERIALIZED VIEW qualified_name SET SCHEMA ColId
			{
				st := NewAlterIndexStmt($<p>1, objectNameFromQn($4), "ALTER MATERIALIZED VIEW")
				st.SetSchema = $7
				$$ = st
			}

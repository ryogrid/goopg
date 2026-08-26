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
				var uqIncludes [][]string
				var uqNullsNotDistinct, uqDeferrable, uqInitiallyDeferred []bool
				var named []parser.TableConstraintDef
				var namedChecks []parser.PartitionCheckConstraint
				var fks []parser.TableForeignKeyDef
				var pkIncl []string
				var pkDeferrable, pkInitiallyDeferred bool
				var checks []string
				var checkNoInherit, checkNotEnforced []bool
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
						if e.check != "" {
							if e.checkName != "" {
								// CONSTRAINT c CHECK (...) -> TableNamedChecks,
								// and legacy does NOT touch the anonymous
								// parallel slices for it (ddl.go:4191-4238).
								namedChecks = append(namedChecks, parser.PartitionCheckConstraint{Name: e.checkName, Expr: e.check})
							} else {
								checks = append(checks, e.check)
								checkNoInherit = append(checkNoInherit, false)
								checkNotEnforced = append(checkNotEnforced, false)
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
				for _, c := range cols {
					ct.BodyOrder = append(ct.BodyOrder, c.Name)
				}
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
				ct.TableNamedChecks = namedChecks
				ct.TableForeignKeys = fks
				ct.PrimaryKeyInclude = pkIncl
				ct.PrimaryKeyDeferrable = pkDeferrable
				ct.PrimaryKeyInitiallyDeferred = pkInitiallyDeferred
				ct.TableChecks = checks
				ct.TableCheckNoInherit = checkNoInherit
				ct.TableCheckNotEnforced = checkNotEnforced
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
				if tail.partOf.Name != "" {
					ct.PartitionOf = parser.NewPartitionOfClause(tail.partOf, tail.fromVals, tail.toVals, tail.inVals, tail.bDefault)
				}
				ct.SelectSource = tail.asSelect
				$$ = ct
			}
	| CREATE opt_create_modifier TABLE opt_if_not_exists qualified_name opt_ct_tail_noas
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
				}
				ct.SelectSource = tail.asSelect
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
	| CONSTRAINT ColId CHECK '(' { yylex.(*lexerState).markSpanStart() } a_expr ')'
			{ $$ = &tableElem{check: yylex.(*lexerState).spanTextCloseParen(), checkName: $2} }
	/* Anonymous table-level CHECK and FOREIGN KEY (gram.y TableConstraint).
	   CHECK and FOREIGN are reserved keywords, so they cannot start the
	   `ColId col_type_name …` column alternative and the element-start decision
	   stays on distinct terminals. Only the plain forms are ported: MATCH FULL,
	   [NOT] DEFERRABLE [INITIALLY …], NOT VALID, [NOT] ENFORCED, ON DELETE SET
	   (cols) and NO INHERIT still fall to legacy — see TODO.md P4.1. */
	| CHECK '(' { yylex.(*lexerState).markSpanStart() } a_expr ')'
			{ $$ = &tableElem{check: yylex.(*lexerState).spanTextCloseParen()} }
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
				$$ = i
			}
opt_ct_tail_noas:
		/* empty */     { $$ = &ctTail{} }
	| WITH '(' str_pair_list ')'
			{ i := &ctTail{}; i.withKv = $3; $$ = i }
	| INHERITS '(' drop_name_list ')'
			{ i := &ctTail{}; i.inherits = $3; $$ = i }
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
				$$ = i
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
		CREATE opt_unique INDEX opt_concurrently opt_if_not_exists ColId ON qualified_name opt_using_method '(' index_col_list ')' opt_include
			{
				nm := $8.parts
				tbl := parser.ObjectName{Name: nm[len(nm)-1]}
				if len(nm) > 1 {
					tbl.Schema = nm[len(nm)-2]
				}
				ix := parser.NewCreateIndexStmt(0, $2, $5, $6, tbl, $9, $11)
				ix.Concurrently = $4
				ix.IncludeColumns = $13
				$$ = ix
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
		SET set_scope set_guc_name set_eq_to set_value_list
			{
				// One alternative, not two: `SET x = DEFAULT` differs from
				// `SET x = 'default'` only by token KIND, which the grammar
				// cannot see — a separate DEFAULT alternative would reduce/reduce
				// against the permissive value list below. setValueIsDefault
				// inspects the token instead.
				l := yylex.(*lexerState)
				$$ = parser.NewSetStmt(0, $2, $3, l.setValueAtoms(), l.setValueIsDefault())
			}

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
				a := parser.NewATAction(parser.AlterTableAddColumn)
				a.Column = *cd
				$$ = a
			}
	| ADD_P PRIMARY KEY pk_cols
			{
				a := parser.NewATAction(parser.AlterTableAddPrimaryKey)
				a.Columns = $4
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
	| ATTACH PARTITION qualified_name FOR VALUES IN_P '(' expr_list ')'
			{
				$$ = parser.NewATAttachPartition(0, objectNameFromQn($3), nil, nil, $8, false)
			}
	| ATTACH PARTITION qualified_name FOR VALUES FROM '(' expr_list ')' TO '(' expr_list ')'
			{
				$$ = parser.NewATAttachPartition(0, objectNameFromQn($3), $8, $12, nil, false)
			}
	| ATTACH PARTITION qualified_name DEFAULT
			{
				$$ = parser.NewATAttachPartition(0, objectNameFromQn($3), nil, nil, nil, true)
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
		CREATE opt_or_replace VIEW qualified_name opt_name_list_p AS { yylex.(*lexerState).markSpanStart() } SelectStmt
			{
				nm := $4.parts
				v := parser.ObjectName{Name: nm[len(nm)-1]}
				if len(nm) > 1 {
					v.Schema = nm[len(nm)-2]
				}
				sel := $8.(*parser.SelectStmt)
				cv := parser.NewCreateViewStmt(v, $5, sel)
				cv.OrReplace = $2
				cv.RawDef = yylex.(*lexerState).spanTextUpTo(yylex.(*lexerState).fragEnd)
				$$ = cv
			}

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
		CREATE MATERIALIZED VIEW opt_if_not_exists qualified_name opt_name_list_p AS { yylex.(*lexerState).markSpanStart() } SelectStmt opt_with_data
			{
				nm := $5.parts
				v := parser.ObjectName{Name: nm[len(nm)-1]}
				if len(nm) > 1 {
					v.Schema = nm[len(nm)-2]
				}
				sel := $9.(*parser.SelectStmt)
				cv := parser.NewCreateMatViewStmt(v, $6, sel)
				cv.IfNotExists = $4
				cv.RawDef = yylex.(*lexerState).spanTextUpTo(yylex.(*lexerState).spanEnd())
				cv.WithNoData = $10
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

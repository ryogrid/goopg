package pgnodes

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// view_bool_null_test.go — M0123-S4 sub-slice 2 gate: BoolExpr (AND/OR/NOT) and
// NullTest (IS [NOT] NULL) in a VIEW WHERE-qual must serialize to the exact
// pg_rewrite.ev_action PostgreSQL 18.3 stores for the same DDL. Sub-slice-1
// wired these nodes only into the scalar (column-DEFAULT) resolver; this slice
// routes the query-scoped resolver/rebuild (resolver_query.go / rebuild_query.go)
// through the recursion-injectable *With builders so a multi-condition view qual
// over base-relation columns becomes canonical ev_action (was SQL-text fallback).
//
// Both goldens were captured live from PG18.3 against the same bench_log table
// the S3 goldens use (relid 16384, client int4 / src text):
//
//	CREATE TABLE bench_log(client int, src text);
//	CREATE VIEW v3 AS SELECT client, src FROM bench_log
//	  WHERE src IS NOT NULL AND client > 0;          -- AND over NullTest + OpExpr
//	CREATE VIEW v4 AS SELECT client, src FROM bench_log
//	  WHERE NOT (client > 0) OR src IS NULL;          -- OR over NOT(OpExpr) + NullTest

// goldenViewV3 is v3's ev_action: the WHERE qual is a two-arg AND BOOLEXPR whose
// first arg is a NOT-NULL NULLTEST (nulltesttype 1) over src and whose second is
// the `client > 0` OPEXPR (opno 521), exactly the flattening the scalar path
// already reproduces — now over view columns.
const goldenViewV3 = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {BOOLEXPR :boolop and :args ({NULLTEST :arg {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :nulltesttype 1 :argisrow false :location -1} {OPEXPR :opno 521 :opfuncid 147 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}) :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname client :ressortgroupref 0 :resorigtbl 16384 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname src :ressortgroupref 0 :resorigtbl 16384 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

// goldenViewV4 is v4's ev_action: an OR BOOLEXPR whose first arg is a single-arg
// NOT BOOLEXPR wrapping the `client > 0` OPEXPR and whose second is an IS-NULL
// NULLTEST (nulltesttype 0) over src. This exercises the NOT and OR builders and
// a nested BoolExpr-inside-BoolExpr (the NOT stays nested, not flattened).
const goldenViewV4 = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {BOOLEXPR :boolop or :args ({BOOLEXPR :boolop not :args ({OPEXPR :opno 521 :opfuncid 147 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}) :location -1} {NULLTEST :arg {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :nulltesttype 0 :argisrow false :location -1}) :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname client :ressortgroupref 0 :resorigtbl 16384 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname src :ressortgroupref 0 :resorigtbl 16384 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

// goldenViewV5 is v5's ev_action (M0123-S4 sub-slice 6): the WHERE qual is a
// BOOLEANTEST (booltesttype 0 = IS TRUE) over the `client > 0` OPEXPR — the
// query-scoped analogue of the scalar sub-slice-5 goldens, now with the operand
// resolved as a base-relation column. Captured live from PG18.3:
//
//	CREATE VIEW v5 AS SELECT client, src FROM bench_log WHERE (client > 0) IS TRUE;
const goldenViewV5 = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {BOOLEANTEST :arg {OPEXPR :opno 521 :opfuncid 147 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1} :booltesttype 0 :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname client :ressortgroupref 0 :resorigtbl 16384 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname src :ressortgroupref 0 :resorigtbl 16384 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

// goldenViewV6 is v6's ev_action (M0123-S4 sub-slice 6): a BOOLEANTEST with
// booltesttype 3 (IS NOT FALSE) over the same OPEXPR — exercises a non-zero
// booltesttype ordinal in the view path. Captured live from PG18.3:
//
//	CREATE VIEW v6 AS SELECT client, src FROM bench_log WHERE (client > 0) IS NOT FALSE;
const goldenViewV6 = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {BOOLEANTEST :arg {OPEXPR :opno 521 :opfuncid 147 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1} :booltesttype 3 :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname client :ressortgroupref 0 :resorigtbl 16384 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname src :ressortgroupref 0 :resorigtbl 16384 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

var viewBoolNullCases = []struct {
	name, sql, golden string
}{
	{"v3_and_nulltest", "SELECT client, src FROM bench_log WHERE src IS NOT NULL AND client > 0", goldenViewV3},
	{"v4_or_not", "SELECT client, src FROM bench_log WHERE NOT (client > 0) OR src IS NULL", goldenViewV4},
	{"v5_booleantest_istrue", "SELECT client, src FROM bench_log WHERE (client > 0) IS TRUE", goldenViewV5},
	{"v6_booleantest_isnotfalse", "SELECT client, src FROM bench_log WHERE (client > 0) IS NOT FALSE", goldenViewV6},
}

// TestResolveViewQueryBoolNull is the forward gate: a multi-condition view qual
// with BoolExpr/NullTest over base-relation columns must resolve to bytes
// byte-identical to PG18.3's ev_action. Before this slice the query resolver
// rejected AND/OR/NOT/IS-NULL and the writer fell back to SQL text.
func TestResolveViewQueryBoolNull(t *testing.T) {
	for _, tc := range viewBoolNullCases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := ResolveViewQuery(parseSelect(t, tc.sql), benchLogResolver{})
			if err != nil {
				t.Fatalf("ResolveViewQuery: %v", err)
			}
			if got := OutRuleAction([]Node{q}); got != tc.golden {
				t.Fatalf("ev_action mismatch:\n got=%s\nwant=%s", got, tc.golden)
			}
		})
	}
}

// TestResolveViewQueryBoolNullRoundTrip closes the codec loop: Out -> Read ->
// Out is stable, proving the reader accepts the bool/null query shape and
// re-emits it identically.
func TestResolveViewQueryBoolNullRoundTrip(t *testing.T) {
	for _, tc := range viewBoolNullCases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := ResolveViewQuery(parseSelect(t, tc.sql), benchLogResolver{})
			if err != nil {
				t.Fatalf("ResolveViewQuery: %v", err)
			}
			first := OutRuleAction([]Node{q})
			nodes, err := ReadRuleAction(first)
			if err != nil {
				t.Fatalf("ReadRuleAction: %v", err)
			}
			if got := OutRuleAction(nodes); got != first {
				t.Fatalf("re-Out mismatch:\n first=%s\n reOut=%s", first, got)
			}
		})
	}
}

// TestRebuildViewQueryBoolNull is the reload-inverse fixed point: resolve ->
// RebuildViewQuery -> re-resolve reproduces the exact golden. This proves the
// query-scoped rebuild (rebuild_query.go) turns the BoolExpr/NullTest IR back
// into an AST that re-resolves to the identical canonical bytes — the reload
// path re-derives the same view without a live catalog lookup.
func TestRebuildViewQueryBoolNull(t *testing.T) {
	for _, tc := range viewBoolNullCases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := ResolveViewQuery(parseSelect(t, tc.sql), benchLogResolver{})
			if err != nil {
				t.Fatalf("ResolveViewQuery: %v", err)
			}
			sel, err := RebuildViewQuery(q)
			if err != nil {
				t.Fatalf("RebuildViewQuery: %v", err)
			}
			q2, err := ResolveViewQuery(sel, benchLogResolver{})
			if err != nil {
				t.Fatalf("re-ResolveViewQuery: %v", err)
			}
			if got := OutRuleAction([]Node{q2}); got != tc.golden {
				t.Fatalf("rebuild round-trip ev_action mismatch:\n got=%s\nwant=%s", got, tc.golden)
			}
		})
	}
}

// TestViewQueryBoolNullStructure inspects the resolved IR and rebuilt AST so a
// byte-identical but structurally wrong resolution can't slip through: v3's qual
// is a two-arg AND BoolExpr [NullTest(IsNotNull), OpExpr] and its rebuilt WHERE
// is `src IS NOT NULL AND client > 0`; v4's qual is an OR BoolExpr whose first
// arg is a NOT BoolExpr (kept nested, not flattened into the OR).
func TestViewQueryBoolNullStructure(t *testing.T) {
	// v3: AND over NullTest + OpExpr.
	q, err := ResolveViewQuery(parseSelect(t, viewBoolNullCases[0].sql), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery v3: %v", err)
	}
	fe := q.Jointree.(*FromExpr)
	be, ok := fe.Quals.(*BoolExpr)
	if !ok || be.Boolop != BoolExprAnd || len(be.Args) != 2 {
		t.Fatalf("v3 qual = %#v, want 2-arg AND BoolExpr", fe.Quals)
	}
	nt, ok := be.Args[0].(*NullTest)
	if !ok || nt.NullTestType != IsNotNull {
		t.Errorf("v3 arg0 = %#v, want IS NOT NULL NullTest", be.Args[0])
	}
	if _, ok := be.Args[1].(*OpExpr); !ok {
		t.Errorf("v3 arg1 = %T, want *OpExpr", be.Args[1])
	}
	sel, err := RebuildViewQuery(q)
	if err != nil {
		t.Fatalf("RebuildViewQuery v3: %v", err)
	}
	bo, ok := sel.Where.(*parser.BinaryOp)
	if !ok || bo.Op != parser.OpAnd {
		t.Fatalf("v3 rebuilt WHERE = %#v, want AND BinaryOp", sel.Where)
	}
	if isn, ok := bo.Left.(*parser.IsNullExpr); !ok || !isn.Negated {
		t.Errorf("v3 rebuilt WHERE left = %#v, want IS NOT NULL", bo.Left)
	}

	// v4: OR whose first arg is a NOT BoolExpr (a nested BoolExpr, not flattened).
	q4, err := ResolveViewQuery(parseSelect(t, viewBoolNullCases[1].sql), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery v4: %v", err)
	}
	be4 := q4.Jointree.(*FromExpr).Quals.(*BoolExpr)
	if be4.Boolop != BoolExprOr || len(be4.Args) != 2 {
		t.Fatalf("v4 qual = %#v, want 2-arg OR BoolExpr", be4)
	}
	notBe, ok := be4.Args[0].(*BoolExpr)
	if !ok || notBe.Boolop != BoolExprNot || len(notBe.Args) != 1 {
		t.Errorf("v4 arg0 = %#v, want single-arg NOT BoolExpr", be4.Args[0])
	}
}

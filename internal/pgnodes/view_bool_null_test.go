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

// goldenViewV7 is v7's ev_action (M0123-S4 sub-slice 8): the WHERE qual is a
// searched CASEEXPR (casetype 16 = bool) with one CASEWHEN (`client > 0` OPEXPR
// -> true) and an ELSE (false) — the query-scoped analogue of the scalar
// sub-slice-7 goldens, now with the WHEN condition resolved as a base-relation
// column Var. Captured live from PG18.3:
//
//	CREATE VIEW v7 AS SELECT client, src FROM bench_log
//	  WHERE CASE WHEN client > 0 THEN true ELSE false END;
const goldenViewV7 = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {CASEEXPR :casetype 16 :casecollid 0 :arg <> :args ({CASEWHEN :expr {OPEXPR :opno 521 :opfuncid 147 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :location -1}) :defresult {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]} :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname client :ressortgroupref 0 :resorigtbl 16384 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname src :ressortgroupref 0 :resorigtbl 16384 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

// goldenViewV8 is v8's ev_action (M0123-S4 sub-slice 8): a searched CASEEXPR with
// TWO CASEWHENs (NULLTEST-then-false, OPEXPR-then-true) and an OMITTED ELSE — PG
// synthesizes a typed NULL Const defresult (constisnull true, constvalue <>),
// exactly the newNullConst path the scalar resolver reproduces, now with column
// Vars inside both WHEN conditions. Captured live from PG18.3:
//
//	CREATE VIEW v8 AS SELECT client, src FROM bench_log
//	  WHERE CASE WHEN src IS NULL THEN false WHEN client > 0 THEN true END;
const goldenViewV8 = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {CASEEXPR :casetype 16 :casecollid 0 :arg <> :args ({CASEWHEN :expr {NULLTEST :arg {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :nulltesttype 0 :argisrow false :location -1} :result {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]} :location -1} {CASEWHEN :expr {OPEXPR :opno 521 :opfuncid 147 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :location -1}) :defresult {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull true :location -1 :constvalue <>} :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname client :ressortgroupref 0 :resorigtbl 16384 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname src :ressortgroupref 0 :resorigtbl 16384 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

// goldenViewV9 is v9's ev_action (M0123-S4 sub-slice 10): the WHERE qual is a
// DISTINCTEXPR (`client IS DISTINCT FROM 5`) with opno 96 (int4eq) — the
// query-scoped analogue of the scalar sub-slice-9 goldens, now with the left
// operand resolved as a base-relation column Var and the right a CONST. Captured
// live from PG18.3:
//
//	CREATE VIEW v9 AS SELECT client, src FROM bench_log WHERE client IS DISTINCT FROM 5;
const goldenViewV9 = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {DISTINCTEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname client :ressortgroupref 0 :resorigtbl 16384 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname src :ressortgroupref 0 :resorigtbl 16384 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

// goldenViewV10 is v10's ev_action (M0123-S4 sub-slice 10): the NOT form
// (`client IS NOT DISTINCT FROM 5`) — a single-arg NOT BOOLEXPR wrapping the
// same DISTINCTEXPR, exactly the wrapper resolveDistinctFromWith emits. Exercises
// the NOT-over-DISTINCTEXPR view path (the BoolExpr recursion re-enters the
// DISTINCTEXPR arm). Captured live from PG18.3:
//
//	CREATE VIEW v10 AS SELECT client, src FROM bench_log WHERE client IS NOT DISTINCT FROM 5;
const goldenViewV10 = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {BOOLEXPR :boolop not :args ({DISTINCTEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1}) :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname client :ressortgroupref 0 :resorigtbl 16384 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname src :ressortgroupref 0 :resorigtbl 16384 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

// goldenViewV11 is v11's ev_action (M0123-S4 sub-slice 11): `client IS DISTINCT
// FROM NULL` does NOT become a DISTINCTEXPR — PG's transformAExprDistinct rewrites
// an undecorated-NULL operand via make_nulltest_from_distinct into a NULLTEST
// (nulltesttype 1 = IS NOT NULL) over the other operand (the client Var), with NO
// NOT wrapper and argisrow false. pg_get_viewdef renders `client IS NOT NULL`.
// Captured live from PG18.3 (bench_log relid 16384):
//
//	CREATE VIEW v11 AS SELECT client, src FROM bench_log WHERE client IS DISTINCT FROM NULL;
const goldenViewV11 = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {NULLTEST :arg {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :nulltesttype 1 :argisrow false :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname client :ressortgroupref 0 :resorigtbl 16384 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname src :ressortgroupref 0 :resorigtbl 16384 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

// goldenViewV12 is v12's ev_action (M0123-S4 sub-slice 11): the NOT form
// `client IS NOT DISTINCT FROM NULL` — make_nulltest_from_distinct folds the
// negation into the NULLTEST (nulltesttype 0 = IS NULL) rather than wrapping a
// DISTINCTEXPR in a NOT, so there is NO NOT BOOLEXPR here (contrast v10). Captured
// live from PG18.3:
//
//	CREATE VIEW v12 AS SELECT client, src FROM bench_log WHERE client IS NOT DISTINCT FROM NULL;
const goldenViewV12 = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {NULLTEST :arg {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :nulltesttype 0 :argisrow false :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname client :ressortgroupref 0 :resorigtbl 16384 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname src :ressortgroupref 0 :resorigtbl 16384 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

// goldenViewV13 is v13's ev_action (M0123-S4 sub-slice 12): a SIMPLE-form
// CASEEXPR whose operand is the base-relation column `client` (so :arg is a VAR,
// not <>), one CASEWHEN whose expr is the OPEXPR `CaseTestExpr = 5`, and an
// explicit ELSE. This is the query-scoped analogue of the scalar sub-slice-12
// goldens and exercises the Var-operand path (CaseTestExpr typed from the Var's
// vartypmod/varcollid) plus the deparse inverse (only the OpExpr RHS surfaces as
// the WHEN value). Captured live from PG18.3 (relid rewritten 16394→16384 to
// match the shared bench_log fixture):
//
//	CREATE VIEW v13 AS SELECT client, src FROM bench_log
//	  WHERE CASE client WHEN 5 THEN true ELSE false END;
const goldenViewV13 = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {CASEEXPR :casetype 16 :casecollid 0 :arg {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 23 :typeMod -1 :collation 0} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :location -1}) :defresult {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]} :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname client :ressortgroupref 0 :resorigtbl 16384 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname src :ressortgroupref 0 :resorigtbl 16384 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

var viewBoolNullCases = []struct {
	name, sql, golden string
}{
	{"v3_and_nulltest", "SELECT client, src FROM bench_log WHERE src IS NOT NULL AND client > 0", goldenViewV3},
	{"v4_or_not", "SELECT client, src FROM bench_log WHERE NOT (client > 0) OR src IS NULL", goldenViewV4},
	{"v5_booleantest_istrue", "SELECT client, src FROM bench_log WHERE (client > 0) IS TRUE", goldenViewV5},
	{"v6_booleantest_isnotfalse", "SELECT client, src FROM bench_log WHERE (client > 0) IS NOT FALSE", goldenViewV6},
	{"v7_caseexpr_else", "SELECT client, src FROM bench_log WHERE CASE WHEN client > 0 THEN true ELSE false END", goldenViewV7},
	{"v8_caseexpr_no_else", "SELECT client, src FROM bench_log WHERE CASE WHEN src IS NULL THEN false WHEN client > 0 THEN true END", goldenViewV8},
	{"v9_distinctexpr", "SELECT client, src FROM bench_log WHERE client IS DISTINCT FROM 5", goldenViewV9},
	{"v10_distinctexpr_not", "SELECT client, src FROM bench_log WHERE client IS NOT DISTINCT FROM 5", goldenViewV10},
	{"v11_distinct_from_null", "SELECT client, src FROM bench_log WHERE client IS DISTINCT FROM NULL", goldenViewV11},
	{"v12_not_distinct_from_null", "SELECT client, src FROM bench_log WHERE client IS NOT DISTINCT FROM NULL", goldenViewV12},
	{"v13_simple_case_var_operand", "SELECT client, src FROM bench_log WHERE CASE client WHEN 5 THEN true ELSE false END", goldenViewV13},
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

	// v7: a searched CASEEXPR (casetype bool) with one CASEWHEN and an ELSE — the
	// ELSE is a real (non-null) Const, so rebuild must emit an explicit ELSE.
	q7, err := ResolveViewQuery(parseSelect(t, viewBoolNullCases[4].sql), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery v7: %v", err)
	}
	ce7, ok := q7.Jointree.(*FromExpr).Quals.(*CaseExpr)
	if !ok || ce7.Casetype != OidBool || len(ce7.Args) != 1 {
		t.Fatalf("v7 qual = %#v, want 1-arm CASEEXPR of bool", q7.Jointree.(*FromExpr).Quals)
	}
	if cw, ok := ce7.Args[0].(*CaseWhen); !ok {
		t.Errorf("v7 arm0 = %T, want *CaseWhen", ce7.Args[0])
	} else if _, ok := cw.Expr.(*OpExpr); !ok {
		t.Errorf("v7 arm0 cond = %T, want *OpExpr (client > 0)", cw.Expr)
	}
	if def, ok := ce7.Defresult.(*Const); !ok || def.ConstIsNull {
		t.Errorf("v7 defresult = %#v, want non-null Const (explicit ELSE)", ce7.Defresult)
	}
	sel7, err := RebuildViewQuery(q7)
	if err != nil {
		t.Fatalf("RebuildViewQuery v7: %v", err)
	}
	if ce, ok := sel7.Where.(*parser.CaseExpr); !ok || ce.Else == nil || len(ce.Whens) != 1 {
		t.Errorf("v7 rebuilt WHERE = %#v, want CASE with explicit ELSE + 1 WHEN", sel7.Where)
	}

	// v8: two CASEWHENs and an OMITTED ELSE — the resolver synthesizes a typed
	// NULL Const defresult, and rebuild must map that back to no ELSE (fixed point).
	q8, err := ResolveViewQuery(parseSelect(t, viewBoolNullCases[5].sql), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery v8: %v", err)
	}
	ce8, ok := q8.Jointree.(*FromExpr).Quals.(*CaseExpr)
	if !ok || len(ce8.Args) != 2 {
		t.Fatalf("v8 qual = %#v, want 2-arm CASEEXPR", q8.Jointree.(*FromExpr).Quals)
	}
	if def, ok := ce8.Defresult.(*Const); !ok || !def.ConstIsNull {
		t.Errorf("v8 defresult = %#v, want synthesized NULL Const (omitted ELSE)", ce8.Defresult)
	}
	sel8, err := RebuildViewQuery(q8)
	if err != nil {
		t.Fatalf("RebuildViewQuery v8: %v", err)
	}
	if ce, ok := sel8.Where.(*parser.CaseExpr); !ok || ce.Else != nil || len(ce.Whens) != 2 {
		t.Errorf("v8 rebuilt WHERE = %#v, want CASE with no ELSE + 2 WHENs", sel8.Where)
	}

	// v9: a bare DISTINCTEXPR (client IS DISTINCT FROM 5) — its first arg is the
	// column Var, the second the CONST; rebuild round-trips to a non-negated
	// IsDistinctFromExpr.
	q9, err := ResolveViewQuery(parseSelect(t, viewBoolNullCases[6].sql), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery v9: %v", err)
	}
	de9, ok := q9.Jointree.(*FromExpr).Quals.(*DistinctExpr)
	if !ok || len(de9.Args) != 2 {
		t.Fatalf("v9 qual = %#v, want 2-arg DISTINCTEXPR", q9.Jointree.(*FromExpr).Quals)
	}
	if _, ok := de9.Args[0].(*Var); !ok {
		t.Errorf("v9 arg0 = %T, want *Var (client column)", de9.Args[0])
	}
	sel9, err := RebuildViewQuery(q9)
	if err != nil {
		t.Fatalf("RebuildViewQuery v9: %v", err)
	}
	if idf, ok := sel9.Where.(*parser.IsDistinctFromExpr); !ok || idf.Negated {
		t.Errorf("v9 rebuilt WHERE = %#v, want non-negated IsDistinctFromExpr", sel9.Where)
	}

	// v10: the NOT form (client IS NOT DISTINCT FROM 5) — a single-arg NOT BoolExpr
	// wrapping the DISTINCTEXPR (kept nested, not collapsed), and rebuild emits the
	// NOT spelling `NOT (client IS DISTINCT FROM 5)` which re-resolves to the same IR.
	q10, err := ResolveViewQuery(parseSelect(t, viewBoolNullCases[7].sql), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery v10: %v", err)
	}
	be10, ok := q10.Jointree.(*FromExpr).Quals.(*BoolExpr)
	if !ok || be10.Boolop != BoolExprNot || len(be10.Args) != 1 {
		t.Fatalf("v10 qual = %#v, want single-arg NOT BoolExpr", q10.Jointree.(*FromExpr).Quals)
	}
	if _, ok := be10.Args[0].(*DistinctExpr); !ok {
		t.Errorf("v10 NOT arg = %T, want *DistinctExpr", be10.Args[0])
	}

	// v11: `client IS DISTINCT FROM NULL` is NOT a DISTINCTEXPR — the undecorated
	// NULL operand makes PG rewrite it to a NULLTEST (IS NOT NULL) over the client
	// Var (make_nulltest_from_distinct), with no `=` operator lookup. Rebuild maps
	// it back to `IS NOT NULL` (the stable fixed point, matching pg_get_viewdef),
	// NOT to the original IS DISTINCT FROM spelling.
	q11, err := ResolveViewQuery(parseSelect(t, viewBoolNullCases[8].sql), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery v11: %v", err)
	}
	nt11, ok := q11.Jointree.(*FromExpr).Quals.(*NullTest)
	if !ok || nt11.NullTestType != IsNotNull || nt11.ArgIsRow {
		t.Fatalf("v11 qual = %#v, want IS-NOT-NULL NullTest (argisrow false)", q11.Jointree.(*FromExpr).Quals)
	}
	if _, ok := nt11.Arg.(*Var); !ok {
		t.Errorf("v11 NullTest arg = %T, want *Var (client column)", nt11.Arg)
	}
	sel11, err := RebuildViewQuery(q11)
	if err != nil {
		t.Fatalf("RebuildViewQuery v11: %v", err)
	}
	if isn, ok := sel11.Where.(*parser.IsNullExpr); !ok || !isn.Negated {
		t.Errorf("v11 rebuilt WHERE = %#v, want IS NOT NULL (IsNullExpr Negated)", sel11.Where)
	}

	// v12: the NOT form `client IS NOT DISTINCT FROM NULL` folds the negation into
	// the NULLTEST (IS NULL) — there is NO NOT BoolExpr wrapper (contrast v10's
	// DISTINCTEXPR-under-NOT). Rebuild round-trips to `IS NULL`.
	q12, err := ResolveViewQuery(parseSelect(t, viewBoolNullCases[9].sql), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery v12: %v", err)
	}
	nt12, ok := q12.Jointree.(*FromExpr).Quals.(*NullTest)
	if !ok || nt12.NullTestType != IsNull || nt12.ArgIsRow {
		t.Fatalf("v12 qual = %#v, want IS-NULL NullTest (argisrow false, no NOT wrapper)", q12.Jointree.(*FromExpr).Quals)
	}
	sel12, err := RebuildViewQuery(q12)
	if err != nil {
		t.Fatalf("RebuildViewQuery v12: %v", err)
	}
	if isn, ok := sel12.Where.(*parser.IsNullExpr); !ok || isn.Negated {
		t.Errorf("v12 rebuilt WHERE = %#v, want IS NULL (IsNullExpr not negated)", sel12.Where)
	}

	// v13: a SIMPLE-form CASEEXPR whose Arg is the `client` Var (not nil) and whose
	// single arm's condition is the OPEXPR `CaseTestExpr = 5`. Rebuild must restore
	// the Operand and emit only the OpExpr RHS as the WHEN value, re-resolving to
	// the same simple-form IR (the pg_get_viewdef fixed point).
	q13, err := ResolveViewQuery(parseSelect(t, viewBoolNullCases[10].sql), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery v13: %v", err)
	}
	ce13, ok := q13.Jointree.(*FromExpr).Quals.(*CaseExpr)
	if !ok || ce13.Casetype != OidBool || len(ce13.Args) != 1 {
		t.Fatalf("v13 qual = %#v, want 1-arm CASEEXPR of bool", q13.Jointree.(*FromExpr).Quals)
	}
	if _, ok := ce13.Arg.(*Var); !ok {
		t.Errorf("v13 CASE arg = %T, want *Var (client operand)", ce13.Arg)
	}
	cw13, ok := ce13.Args[0].(*CaseWhen)
	if !ok {
		t.Fatalf("v13 arm0 = %T, want *CaseWhen", ce13.Args[0])
	}
	op13, ok := cw13.Expr.(*OpExpr)
	if !ok || len(op13.Args) != 2 {
		t.Fatalf("v13 arm0 cond = %#v, want 2-arg OpExpr (placeholder = 5)", cw13.Expr)
	}
	if _, ok := op13.Args[0].(*CaseTestExpr); !ok {
		t.Errorf("v13 arm0 cond left = %T, want *CaseTestExpr placeholder", op13.Args[0])
	}
	sel13, err := RebuildViewQuery(q13)
	if err != nil {
		t.Fatalf("RebuildViewQuery v13: %v", err)
	}
	ce, ok := sel13.Where.(*parser.CaseExpr)
	if !ok || ce.Operand == nil || len(ce.Whens) != 1 || ce.Else == nil {
		t.Errorf("v13 rebuilt WHERE = %#v, want simple CASE (operand set, 1 WHEN, explicit ELSE)", sel13.Where)
	}
}

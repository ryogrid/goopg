package pgnodes

import "testing"

// These goldens are the exact pg_rewrite.ev_action bytes PostgreSQL 18.3 stored
// for the two views below, captured live from a local PG18.3 instance:
//
//	CREATE TABLE bench_log(client int, src text);      -- relid 16384
//	CREATE VIEW v  AS SELECT client, src FROM bench_log WHERE client > 0;
//	CREATE VIEW v2 AS SELECT upper(src) AS us FROM bench_log;
//	SELECT ev_action FROM pg_rewrite WHERE ev_class = 'v'::regclass;
//
// They exercise: a WHERE qual (OpExpr int4 '>' with a Var and a Const), a plain
// Var target list, a computed FuncExpr target (upper), the RTE_RELATION range
// table with its Alias/colnames, the RTEPermissionInfo with a selectedCols
// Bitmapset, and an empty (nil) qual. Out(Read(golden)) must equal the golden
// byte-for-byte — that is the whole point of the canonical serializer.
const goldenViewV = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {OPEXPR :opno 521 :opfuncid 147 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname client :ressortgroupref 0 :resorigtbl 16384 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname src :ressortgroupref 0 :resorigtbl 16384 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

const goldenViewV2 = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname bench_log :colnames ("client" "src")} :rtekind 0 :relid 16384 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16384 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 9) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals <>} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {FUNCEXPR :funcid 871 :funcresulttype 25 :funcretset false :funcvariadic false :funcformat 0 :funccollid 100 :inputcollid 100 :args ({VAR :varno 1 :varattno 2 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1}) :location -1} :resno 1 :resname us :ressortgroupref 0 :resorigtbl 0 :resorigcol 0 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

func TestRuleActionRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name   string
		golden string
	}{
		{"v_with_where", goldenViewV},
		{"v2_funcexpr_no_where", goldenViewV2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes, err := ReadRuleAction(tc.golden)
			if err != nil {
				t.Fatalf("ReadRuleAction: %v", err)
			}
			if len(nodes) != 1 {
				t.Fatalf("ev_action node count = %d, want 1", len(nodes))
			}
			if _, ok := nodes[0].(*Query); !ok {
				t.Fatalf("ev_action[0] type = %T, want *Query", nodes[0])
			}
			got := OutRuleAction(nodes)
			if got != tc.golden {
				t.Fatalf("round-trip mismatch:\n got=%s\nwant=%s", got, tc.golden)
			}
		})
	}
}

// TestRuleActionStructure spot-checks the decoded IR so a byte-identical but
// semantically wrong decode can't slip through.
func TestRuleActionStructure(t *testing.T) {
	nodes, err := ReadRuleAction(goldenViewV)
	if err != nil {
		t.Fatalf("ReadRuleAction: %v", err)
	}
	q := nodes[0].(*Query)
	if q.CommandType != 1 {
		t.Errorf("CommandType = %d, want 1 (CMD_SELECT)", q.CommandType)
	}
	if len(q.Rtable) != 1 {
		t.Fatalf("Rtable len = %d, want 1", len(q.Rtable))
	}
	rte := q.Rtable[0].(*RangeTblEntry)
	if rte.Relid != 16384 {
		t.Errorf("RTE.Relid = %d, want 16384", rte.Relid)
	}
	if rte.Relkind != 'r' {
		t.Errorf("RTE.Relkind = %q, want 'r'", rte.Relkind)
	}
	eref := rte.Eref.(*Alias)
	if eref.Aliasname != "bench_log" {
		t.Errorf("eref.Aliasname = %q, want bench_log", eref.Aliasname)
	}
	if len(eref.Colnames) != 2 || eref.Colnames[0] != "client" || eref.Colnames[1] != "src" {
		t.Errorf("eref.Colnames = %v, want [client src]", eref.Colnames)
	}
	pi := q.RtePermInfos[0].(*RTEPermissionInfo)
	if len(pi.SelectedCols) != 2 || pi.SelectedCols[0] != 8 || pi.SelectedCols[1] != 9 {
		t.Errorf("selectedCols = %v, want [8 9]", pi.SelectedCols)
	}
	// The WHERE qual is an OpExpr (int4 '>') with a Var and a Const arg.
	fe := q.Jointree.(*FromExpr)
	op, ok := fe.Quals.(*OpExpr)
	if !ok {
		t.Fatalf("quals type = %T, want *OpExpr", fe.Quals)
	}
	if op.Opno != 521 {
		t.Errorf("quals OpExpr.Opno = %d, want 521 (int4 '>')", op.Opno)
	}
	if len(q.TargetList) != 2 {
		t.Errorf("TargetList len = %d, want 2", len(q.TargetList))
	}
}

// TestRuleActionShapeGate confirms the Read path rejects a query shape outside
// the supported subset (here: hasAggs true), giving callers their fall-back
// signal instead of silently mis-decoding.
func TestRuleActionShapeGate(t *testing.T) {
	// Minimal QUERY with hasAggs flipped to true.
	bad := `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs true`
	if _, err := ReadRuleAction(bad); err == nil {
		t.Fatalf("expected error for hasAggs=true query, got nil")
	}
}

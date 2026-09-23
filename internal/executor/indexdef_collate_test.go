package executor

import (
	"strings"
	"testing"
)

// TestIndexdefCollateExpr pins the COLLATE deparse (get_rule_expr
// T_CollateExpr: `(arg COLLATE name)`, name quote_identifier'd) in
// pg_get_indexdef. The node had no arm, so the renderer fell back to %v and
// upstream create_index's predicate index printed a Go struct
// (`WHERE (c1::text > &{105 0xc000… C})`) — wrong, and different on every
// run. Statements are upstream create_index.sql's.
func TestIndexdefCollateExpr(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE concur_exprs_tab (c1 int, c2 boolean)")
	runSQL(t, ctx, `CREATE UNIQUE INDEX concur_exprs_index_expr ON concur_exprs_tab ((c1::text COLLATE "C"))`)
	runSQL(t, ctx, `CREATE UNIQUE INDEX concur_exprs_index_pred ON concur_exprs_tab (c1) WHERE (c1::text > 500000000::text COLLATE "C")`)
	runSQL(t, ctx, `CREATE UNIQUE INDEX concur_exprs_index_pred_2 ON concur_exprs_tab ((1 / c1)) WHERE ('-H') >= (c2::TEXT) COLLATE "C"`)
	for _, idx := range []string{"concur_exprs_index_expr", "concur_exprs_index_pred", "concur_exprs_index_pred_2"} {
		rows := runQuery(t, ctx, "SELECT pg_get_indexdef('"+idx+"'::regclass)")
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows", idx, len(rows))
		}
		def := rows[0][0].StringValue()
		t.Logf("%s: %s", idx, def)
		if strings.Contains(def, "&{") || strings.Contains(def, "0x") {
			t.Errorf("%s: Go value leaked into the definition: %s", idx, def)
		}
		if !strings.Contains(def, `COLLATE "C"`) {
			t.Errorf("%s: definition lost its COLLATE \"C\": %s", idx, def)
		}
	}
}

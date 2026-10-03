package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCTASHonoursTempAndUnlogged pins M0146-0039a against PG 18.3's
// create_ctas_internal, which hands the INTO clause's relpersistence to
// DefineRelation: CREATE TEMP TABLE ... AS (and its pg_temp-qualified form)
// makes a temporary relation, CREATE UNLOGGED TABLE ... AS an unlogged one.
// goopg used to create both as permanent, so a "temp" CTAS table outlived its
// session and the server.
func TestCTASHonoursTempAndUnlogged(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, sql := range []string{
		"CREATE TEMP TABLE ctas_t AS SELECT 1 AS x",
		"CREATE TABLE pg_temp.ctas_pt AS SELECT 2 AS y",
		"CREATE UNLOGGED TABLE ctas_u AS SELECT 3 AS z",
		"CREATE TABLE ctas_p AS SELECT 4 AS w",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	for _, c := range []struct {
		name           string
		temp, unlogged bool
	}{
		{"ctas_t", true, false},
		{"ctas_pt", true, false},
		{"ctas_u", false, true},
		{"ctas_p", false, false},
	} {
		tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: c.name})
		if !ok {
			t.Fatalf("%s not in catalog", c.name)
		}
		if tbl.Temp != c.temp || tbl.Unlogged != c.unlogged {
			t.Errorf("%s: Temp=%v Unlogged=%v, want Temp=%v Unlogged=%v",
				c.name, tbl.Temp, tbl.Unlogged, c.temp, c.unlogged)
		}
	}
	rows := runSQL(t, ctx, "SELECT x FROM ctas_t")
	if len(rows) != 1 || datumTestString(rows[0][0]) != "1" {
		t.Errorf("SELECT x FROM ctas_t = %v, want one row 1", rows)
	}
}

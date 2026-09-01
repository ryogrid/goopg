package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestLimitZeroWithTiesReturnsNoRows is the review/260831-2 EO1-3 guard for
// the `limitOp.Next` sibling in operators.go. Its twin
// TestLimitZeroWithTiesReturnsNoRowsFast covers `limitOpNext` (opnode.go),
// which this Build+Run path never reaches. Both siblings entered their WITH TIES window as soon as
// `emitted >= limitCount`, which for a count of ZERO is true before any row
// has been emitted — so `tieKeyVals` was still nil and the tie comparison
// indexed an empty Row, panicking with "index out of range [0] with length 0".
// PG 18.3 simply returns no rows:
//
//	select i from generate_series(1,5) i order by i fetch first 0 rows with ties;
//	 (0 rows)
func TestLimitZeroWithTiesReturnsNoRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE z (a int)"); err != nil {
		t.Fatal(err)
	}
	// All-equal keys: every row would tie with a boundary row if one existed.
	for i := 0; i < 3; i++ {
		if err := runDDL(t, ctx, "INSERT INTO z VALUES (1)"); err != nil {
			t.Fatal(err)
		}
	}

	for _, sql := range []string{
		"SELECT a FROM z ORDER BY a FETCH FIRST 0 ROWS WITH TIES",
		"SELECT a FROM z ORDER BY a LIMIT 0",
	} {
		rows, err := runQueryErr(t, ctx, sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		if len(rows) != 0 {
			t.Errorf("%s returned %d rows, want 0", sql, len(rows))
		}
	}
}

// TestLimitZeroWithTiesReturnsNoRowsFast is the EO2-5 half of the guard: the
// same queries driven through BuildFast/RunFast, the path the live server
// takes (BuildFastIterator), so the `limitOpNext` (opnode.go) sibling of the
// fix is actually executed. Build+Run in the test above never enters it.
func TestLimitZeroWithTiesReturnsNoRowsFast(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE zf (a int)"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := runDDL(t, ctx, "INSERT INTO zf VALUES (1)"); err != nil {
			t.Fatal(err)
		}
	}

	for _, sql := range []string{
		"SELECT a FROM zf ORDER BY a FETCH FIRST 0 ROWS WITH TIES",
		"SELECT a FROM zf ORDER BY a LIMIT 0",
	} {
		ctx.CommandCounterIncrement()
		ctx.CmdID = ctx.GetCurrentCommandId(true)
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("%s: parse: %v", sql, err)
		}
		plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
		if err != nil {
			t.Fatalf("%s: plan: %v", sql, err)
		}
		tree, rootIdx, err := BuildFast(plan)
		if err != nil {
			t.Fatalf("%s: BuildFast: %v", sql, err)
		}
		rows, err := RunFast(tree, rootIdx, ctx)
		if err != nil {
			t.Fatalf("%s: RunFast: %v", sql, err)
		}
		if len(rows) != 0 {
			t.Errorf("%s returned %d rows, want 0", sql, len(rows))
		}
	}
}

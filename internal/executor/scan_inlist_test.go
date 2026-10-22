package executor

import (
	"strings"
	"testing"
)

// seqScans lists the seq scans in a built tree.
func seqScans(op Operator) []*seqScanOp {
	var out []*seqScanOp
	var walk func(Operator)
	walk = func(cur Operator) {
		switch o := cur.(type) {
		case *instrumentedOp:
			walk(o.inner)
		case *seqScanOp:
			out = append(out, o)
		case *aggregateOp:
			walk(o.child)
		case *projectOp:
			walk(o.child)
		case *sortOp:
			walk(o.child)
		case *joinOp:
			walk(o.left)
			walk(o.right)
		}
	}
	walk(op)
	return out
}

// TestScanQualValueListIN pins TPC-H Q12's `l_shipmode = ANY
// ('{MAIL,SHIP}'::bpchar[])` shape on both halves of its fix:
//   - a value-list IN is judged EARLY on the deformed prefix (PlanScanQual);
//     it used to force the late path, which decodes and clones every row
//     before the qual rejects it;
//   - a literal list is evaluated once per statement (inListItems) instead
//     of once per row.
//
// Each IN query is checked against the same predicate spelled as ORs, a
// different evaluator, with the early path on and off. The lists cover
// bpchar blank-insensitivity, typed date literals, a NULL item under NOT IN,
// a non-equality ANY, and a column item, which must not be cached.
func TestScanQualValueListIN(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, "CREATE TABLE ql (a int, b int, mode char(10), d date, s text, pad text)")
	runSQL(t, ctx, "INSERT INTO ql SELECT x % 17, x % 5, (ARRAY['MAIL','SHIP','AIR','RAIL','TRUCK'])[1 + x % 5], DATE '2020-01-01' + x % 40, repeat('s', x % 50), repeat('p', 200) FROM generate_series(1, 6000) x")
	runSQL(t, ctx, "ANALYZE ql")
	for _, c := range []struct {
		in, or string
		early  bool
	}{
		{"mode = ANY ('{MAIL,SHIP}'::bpchar[])", "mode = 'MAIL' OR mode = 'SHIP'", true},
		{"mode IN ('MAIL', 'SHIP ')", "mode = 'MAIL' OR mode = 'SHIP'", true},
		{"d IN (DATE '2020-01-03', DATE '2020-02-01')", "d = DATE '2020-01-03' OR d = DATE '2020-02-01'", true},
		{"a < ANY ('{3,5}'::int[])", "a < 3 OR a < 5", true},
		{"a NOT IN (1, 2)", "a <> 1 AND a <> 2", true},
		{"a NOT IN (1, NULL)", "false", true},
		{"a IN (1, b)", "a = 1 OR a = b", true},
	} {
		inQ := "SELECT a, b, mode, d FROM ql WHERE " + c.in
		orQ := "SELECT a, b, mode, d FROM ql WHERE " + c.or
		plan, err := testPlanDeform(t, ctx, inQ)
		if err != nil {
			t.Fatalf("%s: %v", inQ, err)
		}
		scans := seqScans(mustBuildDeform(t, plan))
		if len(scans) != 1 || scans[0].prefilterSet != c.early {
			t.Fatalf("%s: want one seq scan with prefilter %v, got %d scans", inQ, c.early, len(scans))
		}
		want := sortedRowValues(t, ctx, orQ)
		for _, on := range []bool{true, false} {
			old := scanPrefilterOn.Load()
			scanPrefilterOn.Store(on)
			got := sortedRowValues(t, ctx, inQ)
			scanPrefilterOn.Store(old)
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("%s (early path %v): %d rows, the OR spelling gives %d", c.in, on, len(got), len(want))
			}
		}
		if c.or != "false" && len(want) == 0 {
			t.Fatalf("%s: no rows", c.or)
		}
	}
}
